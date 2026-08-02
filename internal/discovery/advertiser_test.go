package discovery

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/mdns"
)

// fakeServer stands in for *mdns.Server in tests — Advertiser only ever
// calls Shutdown on the server it started, so that's all this needs to
// implement. shutdowns counts calls so tests can assert exactly one
// shutdown per stop, not zero and not a double-shutdown panic-in-disguise.
type fakeServer struct {
	mu          sync.Mutex
	shutdowns   int
	shutdownErr error
}

func (f *fakeServer) Shutdown() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shutdowns++
	return f.shutdownErr
}

func (f *fakeServer) shutdownCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shutdowns
}

// newTestAdvertiser wires an Advertiser with a fake mDNS server constructor
// so tests never touch a real UDP multicast socket, and returns the
// constructor's call count alongside it.
func newTestAdvertiser(t *testing.T, primary bool) (*Advertiser, *int, func(bool)) {
	t.Helper()
	settings := openTestSettings(t)
	role := primary
	var mu sync.Mutex
	setRole := func(p bool) {
		mu.Lock()
		defer mu.Unlock()
		role = p
	}
	isPrimary := func(context.Context) bool {
		mu.Lock()
		defer mu.Unlock()
		return role
	}
	a := NewAdvertiser(settings, isPrimary, 8080)
	starts := 0
	a.newServer = func(zone mdns.Zone) (mdnsServer, error) {
		starts++
		return &fakeServer{}, nil
	}
	return a, &starts, setRole
}

func TestAdvertiser_TickStartsWhenPrimary(t *testing.T) {
	a, starts, _ := newTestAdvertiser(t, true)
	a.tick(context.Background())

	if *starts != 1 {
		t.Fatalf("expected the mDNS server to be started once, got %d starts", *starts)
	}
	if a.server == nil {
		t.Fatal("expected Advertiser to be actively advertising after a primary tick")
	}
}

func TestAdvertiser_TickDoesNothingWhenReplica(t *testing.T) {
	a, starts, _ := newTestAdvertiser(t, false)
	a.tick(context.Background())

	if *starts != 0 {
		t.Fatalf("expected no mDNS server started for a replica, got %d starts", *starts)
	}
	if a.server != nil {
		t.Fatal("expected Advertiser to stay inactive for a replica")
	}
}

func TestAdvertiser_StopsAdvertisingWhenRoleFlipsToReplica(t *testing.T) {
	a, starts, setRole := newTestAdvertiser(t, true)
	a.tick(context.Background()) // becomes primary, starts advertising
	if *starts != 1 || a.server == nil {
		t.Fatalf("setup: expected advertising to have started, starts=%d server=%v", *starts, a.server)
	}
	fake := a.server.(*fakeServer)

	setRole(false)
	a.tick(context.Background())

	if fake.shutdownCount() != 1 {
		t.Fatalf("expected exactly one Shutdown call when role flips to replica, got %d", fake.shutdownCount())
	}
	if a.server != nil {
		t.Fatal("expected Advertiser to be inactive after the role flip")
	}
}

func TestAdvertiser_StartsAdvertisingWhenRoleFlipsToPrimary(t *testing.T) {
	a, starts, setRole := newTestAdvertiser(t, false)
	a.tick(context.Background()) // replica, stays inactive
	if *starts != 0 {
		t.Fatalf("setup: expected no advertising yet, starts=%d", *starts)
	}

	setRole(true)
	a.tick(context.Background())

	if *starts != 1 {
		t.Fatalf("expected advertising to start once the role flips to primary, got %d starts", *starts)
	}
	if a.server == nil {
		t.Fatal("expected Advertiser to be actively advertising after the role flip")
	}
}

func TestAdvertiser_RepeatedTicksInSameRoleDoNotRestart(t *testing.T) {
	a, starts, _ := newTestAdvertiser(t, true)
	a.tick(context.Background())
	a.tick(context.Background())
	a.tick(context.Background())

	if *starts != 1 {
		t.Fatalf("expected exactly one start across repeated primary ticks, got %d", *starts)
	}
}

func TestAdvertiser_StartFailureLeavesItRetryableNextTick(t *testing.T) {
	a, starts, _ := newTestAdvertiser(t, true)
	a.newServer = func(zone mdns.Zone) (mdnsServer, error) {
		*starts++
		return nil, errors.New("boom: no multicast interface")
	}

	a.tick(context.Background())
	if a.server != nil {
		t.Fatal("expected no server recorded after a failed start")
	}

	// A later tick (still primary) must retry, not remain permanently
	// wedged because of one failed attempt.
	a.newServer = func(zone mdns.Zone) (mdnsServer, error) {
		*starts++
		return &fakeServer{}, nil
	}
	a.tick(context.Background())
	if a.server == nil {
		t.Fatal("expected the next tick to retry and succeed")
	}
	if *starts != 2 {
		t.Fatalf("expected two start attempts (one failed, one succeeded), got %d", *starts)
	}
}

// TestAdvertiser_TXTRecordCarriesNoSecrets is ADR-0033 §8's outbound
// guard made concrete: the TXT record is broadcast, unauthenticated, to
// the whole LAN — it must carry only name/id/protocol-version, never
// anything resembling a pairing commitment (a 64-char hex sha256) or an
// enrolment/bearer token (this codebase's tokens are 32-byte-random hex,
// see sync_api.go's enrolTokens.issue / hashBearer).
func TestAdvertiser_TXTRecordCarriesNoSecrets(t *testing.T) {
	txt := txtRecord("Task Runner", "till-abc123")

	joined := strings.Join(txt, "|")
	if strings.Contains(joined, "commitment") || strings.Contains(joined, "token") || strings.Contains(joined, "secret") || strings.Contains(joined, "bearer") {
		t.Fatalf("TXT record must never name a secret-shaped field, got %v", txt)
	}

	for _, field := range txt {
		k, v, ok := strings.Cut(field, "=")
		if !ok {
			t.Fatalf("expected every TXT field to be key=value, got %q", field)
		}
		if k != "v" && k != "name" && k != "id" {
			t.Fatalf("unexpected TXT key %q — only v/name/id are allowed by design", k)
		}
		// A 64-hex-char value is exactly the shape of this codebase's
		// sha256 pairing commitment (pairing_api.go's commitmentMatches) —
		// guard against it leaking in here even accidentally.
		if len(v) == 64 && isHex(v) {
			t.Fatalf("TXT field %q looks like a sha256 commitment/hash, not identity data", field)
		}
		// A 64-hex-char bearer token (32 random bytes, hex-encoded) is the
		// same length as the commitment check above, already covered; also
		// reject anything that merely LOOKS like a long random hex blob in
		// an unexpected field.
	}
}

// TestAdvertiser_StartJoinsWaitGroupAndShutsDownOnCancel exercises the real
// Start loop (not just tick directly): it must register on wg before
// returning (so app.Run's drainBackgroundServices actually waits for it),
// and it must shut down any active mDNS server when ctx is cancelled rather
// than leaking it.
func TestAdvertiser_StartJoinsWaitGroupAndShutsDownOnCancel(t *testing.T) {
	a, _, _ := newTestAdvertiser(t, true)
	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	a.Start(ctx, &wg)

	// Start's first tick runs synchronously-ish inside the goroutine; give
	// it a moment to have started advertising before we assert on it.
	// Every read of a.server must hold a.mu — Start's goroutine writes it
	// under that lock, so polling it unsynchronized is a genuine data race
	// (caught by `go test -race`), not merely untidy.
	currentServer := func() mdnsServer {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.server
	}
	deadline := time.Now().Add(2 * time.Second)
	for currentServer() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	srv, _ := currentServer().(*fakeServer)
	if srv == nil {
		t.Fatal("expected Advertiser to be advertising shortly after Start")
	}

	cancel()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected Start's goroutine to exit (and release wg) promptly after ctx cancellation")
	}

	if srv.shutdownCount() != 1 {
		t.Fatalf("expected the active mDNS server to be shut down on ctx cancellation, got %d shutdowns", srv.shutdownCount())
	}
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return len(s) > 0
}

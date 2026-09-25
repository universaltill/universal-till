package discovery

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

const (
	testPrimaryID = "11111111-1111-4111-8111-111111111111"
	testTillID    = "22222222-2222-4222-8222-222222222222"
	testBearer    = "bearer-secret"
	deadURL       = "http://192.0.2.1:37673" // TEST-NET-1: never answers
)

func testBearerHash() string {
	sum := sha256.Sum256([]byte(testBearer))
	return hex.EncodeToString(sum[:])
}

// fakePrimary answers POST /api/sync/primary-proof the way a real main till
// does, holding bearerHash for testTillID and claiming primaryID. Counts the
// requests it gets and records any Authorization header it is sent — a
// re-discovery must never send the bearer before the proof checks out.
type fakePrimary struct {
	srv        *httptest.Server
	hits       atomic.Int32
	sawBearer  atomic.Bool
	primaryID  string
	bearerHash string
}

func newFakePrimary(t *testing.T, primaryID, bearerHash string) *fakePrimary {
	t.Helper()
	f := &fakePrimary{primaryID: primaryID, bearerHash: bearerHash}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		if r.Header.Get("Authorization") != "" {
			f.sawBearer.Store(true)
		}
		if r.URL.Path != ProofPath || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var req ProofRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TillID != testTillID {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": ProofResponse{
			PrimaryTillID: f.primaryID,
			Proof:         PrimaryProof(f.bearerHash, f.primaryID, req.TillID, req.Nonce, r.Host),
		}, "error": nil})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// openFastFileSettings is a settings table in a real file (so every pooled
// connection sees the same data, unlike ":memory:") without paying for the
// full migration run openFileBackedTestSettings does.
func openFastFileSettings(t *testing.T) *data.SettingsRepo {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if _, err := sqlDB.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	return data.NewSettingsRepo(sqlDB)
}

type watchFixture struct {
	w        *PrimaryWatch
	settings *data.SettingsRepo
	browses  atomic.Int32
	now      time.Time
}

func newWatchFixture(t *testing.T, candidates ...Candidate) *watchFixture {
	t.Helper()
	settings := openFastFileSettings(t)
	ctx := context.Background()
	for k, v := range map[string]string{
		"sync.primary_url":      deadURL,
		"sync.bearer":           testBearer,
		"sync.till_id":          testTillID,
		PrimaryTillIDSettingKey: testPrimaryID,
		"sync.last_contact_at":  "2026-09-16T23:33:27Z",
	} {
		if err := settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	f := &watchFixture{settings: settings, now: time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)}
	f.w = NewPrimaryWatch(settings, nil)
	f.w.now = func() time.Time { return f.now }
	f.w.browse = func(context.Context, time.Duration) ([]Candidate, error) {
		f.browses.Add(1)
		return candidates, nil
	}
	return f
}

func (f *watchFixture) get(t *testing.T, key string) string {
	t.Helper()
	v, _, err := f.settings.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// The owner's replica (ut-docs#2722): the main till moved from :37673 to
// :34029. Browsing finds it advertising the SAME till id and it proves it
// holds this replica's pairing → primary_url is updated, bearer untouched.
func TestPrimaryWatch_RelinksOnIdentityMatchAndProof(t *testing.T) {
	primary := newFakePrimary(t, testPrimaryID, testBearerHash())
	f := newWatchFixture(t, Candidate{Name: "Shop", TillID: testPrimaryID, BaseURL: primary.srv.URL})
	ctx := context.Background()

	var out FailureOutcome
	for i := 0; i < UnreachableThreshold && !out.Relinked; i++ {
		out = f.w.ContactFailed(ctx)
	}
	if !out.Relinked || out.NewURL != primary.srv.URL {
		t.Fatalf("outcome = %+v, want relinked to %s", out, primary.srv.URL)
	}
	if got := f.get(t, "sync.primary_url"); got != primary.srv.URL {
		t.Fatalf("sync.primary_url = %q, want %q", got, primary.srv.URL)
	}
	if got := f.get(t, "sync.bearer"); got != testBearer {
		t.Fatalf("bearer changed to %q — re-discovery must keep the existing pairing", got)
	}
	if primary.sawBearer.Load() {
		t.Fatal("the bearer was sent to a candidate during re-discovery")
	}
	if _, unreachable := f.w.Unreachable(ctx); unreachable {
		t.Fatal("still reported unreachable right after a successful re-link")
	}
}

// Security first: a device advertising a DIFFERENT till id is never tried,
// never switched to — even when it is the only thing on the network.
func TestPrimaryWatch_NeverSwitchesOnIdentityMismatch(t *testing.T) {
	other := newFakePrimary(t, "33333333-3333-4333-8333-333333333333", testBearerHash())
	f := newWatchFixture(t, Candidate{Name: "Shop", TillID: other.primaryID, BaseURL: other.srv.URL})
	ctx := context.Background()
	for i := 0; i < UnreachableThreshold+2; i++ {
		if out := f.w.ContactFailed(ctx); out.Relinked {
			t.Fatalf("relinked to a mismatched till: %+v", out)
		}
	}
	if got := f.get(t, "sync.primary_url"); got != deadURL {
		t.Fatalf("sync.primary_url = %q, want it unchanged (%q)", got, deadURL)
	}
	if other.hits.Load() != 0 {
		t.Fatalf("a mismatched candidate was contacted %d times", other.hits.Load())
	}
}

// A spoofer can copy the till id off the mDNS beacon (it is public), but it
// cannot produce the proof without this replica's pairing record.
func TestPrimaryWatch_NeverSwitchesWhenProofFails(t *testing.T) {
	spoof := newFakePrimary(t, testPrimaryID, "not-the-real-bearer-hash")
	f := newWatchFixture(t, Candidate{Name: "Shop", TillID: testPrimaryID, BaseURL: spoof.srv.URL})
	ctx := context.Background()
	for i := 0; i < UnreachableThreshold; i++ {
		if out := f.w.ContactFailed(ctx); out.Relinked {
			t.Fatalf("relinked to a device that failed the proof: %+v", out)
		}
	}
	if got := f.get(t, "sync.primary_url"); got != deadURL {
		t.Fatalf("sync.primary_url = %q, want unchanged", got)
	}
	if spoof.sawBearer.Load() {
		t.Fatal("the bearer was sent to an unproven device")
	}
}

func TestPrimaryWatch_NoBrowseBeforeThreshold(t *testing.T) {
	f := newWatchFixture(t)
	// A fresh contact: the till was reachable a moment ago, so a single
	// failed tick is not "unreachable" yet.
	if err := f.settings.Set(context.Background(), "sync.last_contact_at", f.now.Add(-30*time.Second).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < UnreachableThreshold-1; i++ {
		if out := f.w.ContactFailed(context.Background()); out.Warn {
			t.Fatalf("warned after %d failures, want only at %d", i+1, UnreachableThreshold)
		}
	}
	if n := f.browses.Load(); n != 0 {
		t.Fatalf("browsed %d times before the threshold", n)
	}
	if _, unreachable := f.w.Unreachable(context.Background()); unreachable {
		t.Fatal("unreachable before the threshold")
	}
}

// On launch: the main till was already unreachable before this process
// started (last contact days ago), so the very first failed contact counts
// — no need to wait out N more ticks.
func TestPrimaryWatch_StaleLastContactBrowsesOnFirstFailure(t *testing.T) {
	f := newWatchFixture(t)
	if out := f.w.ContactFailed(context.Background()); !out.Warn {
		t.Fatalf("outcome = %+v, want a warn on the first failure after a long-stale last contact", out)
	}
	if n := f.browses.Load(); n != 1 {
		t.Fatalf("browses = %d, want 1", n)
	}
}

func TestPrimaryWatch_WarnsOnceThenStaysQuiet(t *testing.T) {
	f := newWatchFixture(t)
	warns := 0
	for i := 0; i < 10; i++ {
		if f.w.ContactFailed(context.Background()).Warn {
			warns++
		}
	}
	if warns != 1 {
		t.Fatalf("warned %d times over 10 failures, want exactly once", warns)
	}
	f.w.ContactOK(context.Background())
	for i := 0; i < UnreachableThreshold; i++ {
		f.w.ContactFailed(context.Background())
	}
	// A recovery re-arms the warning for the NEXT outage. (last_contact_at
	// is still stale in this fixture, so it fires on the first failure.)
	if !f.w.warned {
		t.Fatal("a new outage after recovery did not warn again")
	}
}

func TestPrimaryWatch_BrowseIsRateLimited(t *testing.T) {
	f := newWatchFixture(t)
	for i := 0; i < 20; i++ {
		f.w.ContactFailed(context.Background())
	}
	if n := f.browses.Load(); n != 1 {
		t.Fatalf("browses = %d over 20 quick failures, want 1 (rate-limited)", n)
	}
	f.now = f.now.Add(MinBrowseInterval)
	f.w.ContactFailed(context.Background())
	if n := f.browses.Load(); n != 2 {
		t.Fatalf("browses = %d after the interval passed, want 2", n)
	}
}

func TestPrimaryWatch_UnreachableSinceIsLastContact(t *testing.T) {
	f := newWatchFixture(t)
	for i := 0; i < UnreachableThreshold; i++ {
		f.w.ContactFailed(context.Background())
	}
	since, unreachable := f.w.Unreachable(context.Background())
	if !unreachable || since != "2026-09-16T23:33:27Z" {
		t.Fatalf("Unreachable = %q, %v; want the last contact time, true", since, unreachable)
	}
	f.w.ContactOK(context.Background())
	if _, unreachable := f.w.Unreachable(context.Background()); unreachable {
		t.Fatal("still unreachable after a successful contact")
	}
}

// Replicas paired before ut-docs#2722 never stored their main till's id.
// The first successful contact learns it — through the same proof, so a
// wrong URL can't plant a wrong identity.
func TestPrimaryWatch_ContactOKLearnsPrimaryIDOnce(t *testing.T) {
	primary := newFakePrimary(t, testPrimaryID, testBearerHash())
	f := newWatchFixture(t)
	ctx := context.Background()
	_ = f.settings.Set(ctx, PrimaryTillIDSettingKey, "")
	_ = f.settings.Set(ctx, "sync.primary_url", primary.srv.URL)

	f.w.ContactOK(ctx)
	f.w.ContactOK(ctx)
	if got := f.get(t, PrimaryTillIDSettingKey); got != testPrimaryID {
		t.Fatalf("%s = %q, want %q", PrimaryTillIDSettingKey, got, testPrimaryID)
	}
	if n := primary.hits.Load(); n != 1 {
		t.Fatalf("proof requests = %d, want 1 (learned once, not every tick)", n)
	}
}

// A pre-#2722 replica that is ALREADY stranded has no stored id. Its
// lan_discovery.till_id — which on a replica is the main till's own id,
// copied in by the join snapshot and every admin pull — is used as a hint.
func TestPrimaryWatch_LegacyReplicaUsesDiscoveryIDHint(t *testing.T) {
	primary := newFakePrimary(t, testPrimaryID, testBearerHash())
	f := newWatchFixture(t, Candidate{Name: "Shop", TillID: testPrimaryID, BaseURL: primary.srv.URL})
	ctx := context.Background()
	_ = f.settings.Set(ctx, PrimaryTillIDSettingKey, "")
	_ = f.settings.Set(ctx, TillIDSettingKey, testPrimaryID)

	out := f.w.ContactFailed(ctx)
	if !out.Relinked {
		t.Fatalf("outcome = %+v, want relinked", out)
	}
	if got := f.get(t, PrimaryTillIDSettingKey); got != testPrimaryID {
		t.Fatalf("%s not stored after a proven re-link: %q", PrimaryTillIDSettingKey, got)
	}
}

func TestPrimaryProof_BindsEveryInput(t *testing.T) {
	base := PrimaryProof("h", "p", "t", "n", "192.0.2.5:8080")
	for _, other := range []string{
		PrimaryProof("h2", "p", "t", "n", "192.0.2.5:8080"),
		PrimaryProof("h", "p2", "t", "n", "192.0.2.5:8080"),
		PrimaryProof("h", "p", "t2", "n", "192.0.2.5:8080"),
		PrimaryProof("h", "p", "t", "n2", "192.0.2.5:8080"),
		PrimaryProof("h", "p", "t", "n", "192.0.2.6:8080"),
		PrimaryProof("h", "p", "t", "n", "192.0.2.5:8081"),
	} {
		if other == base {
			t.Fatal("PrimaryProof ignores one of its inputs")
		}
	}
}

// Review finding (ut-docs#2722): the till id on the mDNS beacon is public,
// so a LAN device can advertise it, take the replica's challenge, RELAY it
// to the real main till and hand back that genuine proof. The proof binds
// the host:port the replica actually dialled, so the relayed answer (made
// for the real main till's own address) does not verify for the relay's
// address — and primary_url never points at the relay, whose next pull
// would otherwise receive the bearer.
func TestPrimaryWatch_RelayedProofDoesNotSwitch(t *testing.T) {
	real := newFakePrimary(t, testPrimaryID, testBearerHash())
	var relayed atomic.Int32
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Forward the challenge verbatim to the real main till (the Go
		// client sets Host to the real till's address) and pass its
		// answer straight back.
		resp, err := http.Post(real.srv.URL+r.URL.Path, "application/json", r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		relayed.Add(1)
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(relay.Close)

	f := newWatchFixture(t, Candidate{Name: "Shop", TillID: testPrimaryID, BaseURL: relay.URL})
	ctx := context.Background()
	for i := 0; i < UnreachableThreshold; i++ {
		if out := f.w.ContactFailed(ctx); out.Relinked {
			t.Fatalf("relinked to a relay of the real main till's proof: %+v", out)
		}
	}
	if relayed.Load() == 0 {
		t.Fatal("the relay never forwarded a challenge — the test proves nothing")
	}
	if got := f.get(t, "sync.primary_url"); got != deadURL {
		t.Fatalf("sync.primary_url = %q, want unchanged (%q)", got, deadURL)
	}
}

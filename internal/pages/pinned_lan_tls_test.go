package pages

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/lantls"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Pinned LAN TLS, replica side (ADR-0114 §7, ut-docs#4092), over real
// sockets: a main till serving the sync API and the link on a same-port
// TLS + plain listener, and a replica whose peer clients are the shared
// lantls transport, as internal/app wires them.

type tlsSyncMain struct {
	f          *syncLinkFixture
	base       string
	cert       *lantls.Cert
	tls, plain atomic.Int32 // /api/sync/* requests by transport
}

func newTLSSyncMain(t *testing.T) *tlsSyncMain {
	t.Helper()
	m := &tlsSyncMain{f: newSyncLinkFixture(t)}
	m.base, m.cert = serveTLSTill(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/sync/") {
			if r.TLS != nil {
				m.tls.Add(1)
			} else {
				m.plain.Add(1)
			}
		}
		m.f.mux.ServeHTTP(w, r)
	}))
	return m
}

// pinnedReplica pairs a replica to m holding pin ("" = unpinned) and points
// the shared peer transport at its settings for the rest of the test.
func pinnedReplica(t *testing.T, m *tlsSyncMain, pin string) (*common.Deps, string) {
	t.Helper()
	tillID := m.f.enrol(t, "Till 2", "token-abc")
	r := linkReplica(t, m.base, tillID, fastLinkClientOptions())
	if err := r.Settings.Set(t.Context(), discovery.PrimaryCertPinSettingKey, pin); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lantls.SetPeerSource(func(ctx context.Context) (string, string, error) {
		primary, _, _ := r.Settings.Get(ctx, "sync.primary_url")
		p, _, _ := r.Settings.Get(ctx, discovery.PrimaryCertPinSettingKey)
		return primary, p, nil
	}))
	return r, tillID
}

// startLink runs the replica's link client until the test ends.
func startLink(t *testing.T, r *common.Deps) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	StartSyncLinkClient(ctx, r, &wg)
	t.Cleanup(func() { cancel(); wg.Wait() })
}

func TestPinnedReplica_AllSyncTrafficIsPinnedTLS(t *testing.T) {
	m := newTLSSyncMain(t)
	r, tillID := pinnedReplica(t, m, m.cert.Pin())
	ctx := t.Context()

	syncPullTick(ctx, r, lantls.NewPeerClient(5*time.Second), func(context.Context) {})
	if settingOf(t, r, "sync.pull_version") == "" {
		t.Fatal("pull over pinned TLS did not apply a bundle")
	}
	syncPushTick(ctx, r, lantls.NewPeerClient(5*time.Second))

	startLink(t, r)
	if !waitFor(t, 3*time.Second, func() bool {
		rep, ok := m.f.dp.Link.Report(tillID)
		return r.LinkClient.Linked() && ok && rep.TLSPinned
	}) {
		t.Fatalf("pinned replica not linked with tls_pinned reported (linked=%v)", r.LinkClient.Linked())
	}
	if m.plain.Load() != 0 {
		t.Fatalf("main till got %d plain-HTTP /api/sync/* request(s) from a pinned replica", m.plain.Load())
	}
	if m.tls.Load() < 3 { // pull, probe ping, link upgrade at least
		t.Fatalf("main till got only %d TLS request(s)", m.tls.Load())
	}
}

// A main till presenting another certificate gets no request at all —
// neither over TLS nor as a plain retry — and the replica treats it as
// unreachable (re-proof is ut-docs#2737).
func TestPinnedReplica_AnotherCertificateGetsNothing(t *testing.T) {
	m := newTLSSyncMain(t)
	r, _ := pinnedReplica(t, m, strings.Repeat("0", 64))

	syncPullTick(t.Context(), r, lantls.NewPeerClient(5*time.Second), func(context.Context) {})
	if settingOf(t, r, "sync.pull_version") != "" {
		t.Fatal("pull applied a bundle from a main till whose certificate does not match the pin")
	}
	startLink(t, r)
	time.Sleep(300 * time.Millisecond) // several fast-backoff dial attempts
	if r.LinkClient.Linked() {
		t.Fatal("linked to a main till whose certificate does not match the pin")
	}
	if n := m.plain.Load() + m.tls.Load(); n != 0 {
		t.Fatalf("main till served %d /api/sync/* request(s) despite the pin mismatch", n)
	}
}

// A replica with no pin (paired before TLS, or to a main till without it)
// keeps plain HTTP and reports tls_pinned false.
func TestPinnedReplica_UnpinnedReplicaStaysPlain(t *testing.T) {
	m := newTLSSyncMain(t)
	r, tillID := pinnedReplica(t, m, "")

	syncPullTick(t.Context(), r, lantls.NewPeerClient(5*time.Second), func(context.Context) {})
	if settingOf(t, r, "sync.pull_version") == "" {
		t.Fatal("unpinned pull did not apply a bundle")
	}
	startLink(t, r)
	if !waitFor(t, 3*time.Second, func() bool {
		rep, ok := m.f.dp.Link.Report(tillID)
		return ok && !rep.TLSPinned
	}) {
		t.Fatal("no tls_pinned=false report from an unpinned replica")
	}
	if m.tls.Load() != 0 || m.plain.Load() == 0 {
		t.Fatalf("unpinned replica sent tls=%d plain=%d; want plain only", m.tls.Load(), m.plain.Load())
	}
}

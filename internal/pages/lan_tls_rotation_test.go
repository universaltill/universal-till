package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/lantls"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// LAN TLS key rotation (ADR-0114 §7, ut-docs#4409): the main till
// announces its next pin in the link's hello, a pinned replica holds it
// and confirms it in its report, and the main till switches at its next
// start once every pinned till has confirmed.

// ageRotationState moves a timestamp in the TLS dir's rotation.json back
// by d, so the next LoadOrCreate sees a key (or a pending next key) that
// old — the on-disk state is the only clock a load reads.
func ageRotationState(t *testing.T, dir, field string, d time.Duration) {
	t.Helper()
	path := filepath.Join(dir, "rotation.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	ts, ok := st[field].(string)
	if !ok {
		t.Fatalf("rotation.json has no %s: %s", field, b)
	}
	at, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		t.Fatal(err)
	}
	st[field] = at.Add(-d).Format(time.RFC3339Nano)
	if b, err = json.Marshal(st); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// pendingRotationCert is a main till's LAN key that is RotateEvery old, so
// loading it now creates the next key: Pin is served, NextPin pending.
func pendingRotationCert(t *testing.T) (dir string, c *lantls.Cert) {
	t.Helper()
	dir = t.TempDir()
	if _, err := lantls.LoadOrCreate(dir); err != nil {
		t.Fatal(err)
	}
	ageRotationState(t, dir, "key_created_at", lantls.RotateEvery)
	c, err := lantls.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.NextPin() == "" {
		t.Fatal("no rotation pending")
	}
	return dir, c
}

func TestConfirmNextPin_ReadyOnceEveryPinnedTillConfirmed(t *testing.T) {
	dp := newSyncPairingGateTestDeps(t)
	tills := data.NewTillsRepo(dp.Db)
	ctx := t.Context()
	var ids []string
	for _, n := range []string{"Till A", "Till B", "Till C"} {
		id, err := tills.InsertTill(ctx, n, hashBearer(n))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	a, b, c := ids[0], ids[1], ids[2]
	dir, cert := pendingRotationCert(t)
	old, next := cert.Pin(), cert.NextPin()
	// Till C reported it doesn't pin: a switch can't break it.
	reportOf := func(id string) (fleetlink.Report, bool) {
		if id == c {
			return fleetlink.Report{TLSPinned: false}, true
		}
		return fleetlink.Report{}, false
	}
	served := func() string {
		t.Helper()
		got, err := lantls.LoadOrCreate(dir)
		if err != nil {
			t.Fatal(err)
		}
		return got.Pin()
	}

	confirmNextPin(cert, tills, reportOf, a, fleetlink.Report{TLSPinned: true, NextPin: strings.Repeat("0", 64)})
	confirmNextPin(cert, tills, reportOf, a, fleetlink.Report{TLSPinned: true, NextPin: next})
	if served() != old {
		t.Fatal("switched with Till B not yet confirmed")
	}
	confirmNextPin(cert, tills, reportOf, b, fleetlink.Report{TLSPinned: true, NextPin: next})
	if got := served(); got != next {
		t.Fatalf("served pin after every pinned till confirmed = %s; want the next pin %s", got, next)
	}
}

// A nil certificate (TLS off) or no rotation pending is a no-op.
func TestConfirmNextPin_NoRotationIsANoOp(t *testing.T) {
	dp := newSyncPairingGateTestDeps(t)
	tills := data.NewTillsRepo(dp.Db)
	none := func(string) (fleetlink.Report, bool) { return fleetlink.Report{}, false }
	confirmNextPin(nil, tills, none, "till-x", fleetlink.Report{NextPin: strings.Repeat("a", 64)})
	c, err := lantls.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	confirmNextPin(c, tills, none, "till-x", fleetlink.Report{NextPin: strings.Repeat("a", 64)})
}

// pinSource points the shared peer transport at r's own settings, as
// internal/app wires it.
func pinSource(t *testing.T, r *common.Deps) {
	t.Helper()
	t.Cleanup(lantls.SetPeerSource(func(ctx context.Context) (lantls.PeerTarget, error) {
		var pt lantls.PeerTarget
		pt.PrimaryURL, _, _ = r.Settings.Get(ctx, "sync.primary_url")
		pt.Pin, _, _ = r.Settings.Get(ctx, discovery.PrimaryCertPinSettingKey)
		pt.NextPin, _, _ = r.Settings.Get(ctx, discovery.PrimaryCertNextPinSettingKey)
		return pt, nil
	}))
}

// The pins are compared with the pin verified on the link's own connection
// (verified), never the hello's self-reported tls_pin: a hello that came
// over plain HTTP (verified "") changes nothing, whatever pin is held by
// the time it is handled.
func TestApplyMainHelloPins(t *testing.T) {
	held, next, other := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	ptr := func(s string) *string { return &s }
	cases := []struct {
		name              string
		pin, heldNext     string // replica's settings before
		verified          string // pin verified on the link's connection
		hello             fleetlink.Hello
		wantPin, wantNext string
	}{
		{"pinned link announces a next pin: held", held, "", held, fleetlink.Hello{TLSPin: ptr(held), NextPin: next}, held, next},
		{"plain link with a pin held meanwhile: ignored", held, "", "", fleetlink.Hello{TLSPin: ptr(held), NextPin: next}, held, ""},
		{"unpinned replica never takes a next pin", "", "", held, fleetlink.Hello{TLSPin: ptr(held), NextPin: next}, "", ""},
		{"connection verified under another pin: ignored", held, "", other, fleetlink.Hello{TLSPin: ptr(held), NextPin: next}, held, ""},
		{"self-reported tls_pin is not trusted", held, "", other, fleetlink.Hello{TLSPin: ptr(held), NextPin: next}, held, ""},
		{"no tls_pin (older main till) over a pinned link", held, "", held, fleetlink.Hello{NextPin: next}, held, next},
		{"malformed next pin: ignored", held, "", held, fleetlink.Hello{TLSPin: ptr(held), NextPin: strings.ToUpper(next)}, held, ""},
		{"short next pin: ignored", held, "", held, fleetlink.Hello{TLSPin: ptr(held), NextPin: "abcd"}, held, ""},
		{"next pin equal to the held one: ignored", held, "", held, fleetlink.Hello{TLSPin: ptr(held), NextPin: held}, held, ""},
		{"connection verified under the held next pin: promoted", held, next, next, fleetlink.Hello{TLSPin: ptr(held)}, next, ""},
		{"rotation abandoned: stale next pin cleared", held, next, held, fleetlink.Hello{TLSPin: ptr(held)}, held, ""},
		{"a new next pin replaces the held one", held, other, held, fleetlink.Hello{TLSPin: ptr(held), NextPin: next}, held, next},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newPullTestReplica(t, "http://127.0.0.1:1")
			ctx := t.Context()
			_ = r.Settings.Set(ctx, discovery.PrimaryCertPinSettingKey, tc.pin)
			_ = r.Settings.Set(ctx, discovery.PrimaryCertNextPinSettingKey, tc.heldNext)
			pinSource(t, r)
			applyMainHelloPins(ctx, r, tc.hello, tc.verified)
			if got := settingOf(t, r, discovery.PrimaryCertPinSettingKey); got != tc.wantPin {
				t.Errorf("pin = %q; want %q", got, tc.wantPin)
			}
			if got := settingOf(t, r, discovery.PrimaryCertNextPinSettingKey); got != tc.wantNext {
				t.Errorf("next pin = %q; want %q", got, tc.wantNext)
			}
		})
	}
}

// Review blocker (ut-docs#4409): an unpinned replica links over plain
// HTTP; its first hello's ContactOK backfill learns the main till's pin
// over a separate TLS connection. The hello itself travelled unpinned, so
// its next_pin — rewritable by anyone on path — must not be taken.
func TestRotation_PlainLinkHelloNeverSetsANextPinAfterBackfill(t *testing.T) {
	_, cert := pendingRotationCert(t)
	m := newRotatingTLSSyncMain(t, cert)
	registerPrimaryProof(m.f.mux, m.f.dp)
	r, tillID := pinnedReplica(t, m, "")
	r.PrimaryWatch = discovery.NewPrimaryWatch(r.Settings, nil)

	startLink(t, r)
	if !waitFor(t, 3*time.Second, func() bool {
		rep, ok := m.f.dp.Link.Report(tillID)
		return ok && rep.TLSPinned && settingOf(t, r, discovery.PrimaryCertPinSettingKey) == cert.Pin()
	}) {
		t.Fatalf("backfill did not pin during the first hello (pin=%q)", settingOf(t, r, discovery.PrimaryCertPinSettingKey))
	}
	if got := settingOf(t, r, discovery.PrimaryCertNextPinSettingKey); got != "" {
		t.Fatalf("next pin %q taken from a hello that came over plain HTTP", got)
	}
	if rep, _ := m.f.dp.Link.Report(tillID); rep.NextPin != "" {
		t.Fatalf("report confirms next pin %q learned over plain HTTP", rep.NextPin)
	}
}

// newRotatingTLSSyncMain is newTLSSyncMain serving cert, which the hub
// announces as lantls.Serving() does in production.
func newRotatingTLSSyncMain(t *testing.T, cert *lantls.Cert) *tlsSyncMain {
	t.Helper()
	m := &tlsSyncMain{f: newSyncLinkFixture(t)}
	m.base = serveTLSTillWithCert(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/sync/") {
			if r.TLS != nil {
				m.tls.Add(1)
			} else {
				m.plain.Add(1)
			}
		}
		m.f.mux.ServeHTTP(w, r)
	}), cert)
	m.cert = cert
	t.Cleanup(lantls.SetServing(cert))
	return m
}

// End to end over real sockets: the pinned replica learns the next pin
// from the hello, reports it, and the main till's rotation becomes ready
// — its next start serves the new key.
func TestRotation_PinnedReplicaLearnsAndConfirmsTheNextPin(t *testing.T) {
	dir, cert := pendingRotationCert(t)
	m := newRotatingTLSSyncMain(t, cert)
	r, tillID := pinnedReplica(t, m, cert.Pin())
	pinSource(t, r)

	startLink(t, r)
	if !waitFor(t, 3*time.Second, func() bool {
		rep, ok := m.f.dp.Link.Report(tillID)
		return ok && rep.NextPin == cert.NextPin()
	}) {
		t.Fatalf("no report confirming the next pin (held next = %q)", settingOf(t, r, discovery.PrimaryCertNextPinSettingKey))
	}
	if got := settingOf(t, r, discovery.PrimaryCertNextPinSettingKey); got != cert.NextPin() {
		t.Fatalf("replica's next pin = %q; want %q", got, cert.NextPin())
	}
	if got := settingOf(t, r, discovery.PrimaryCertPinSettingKey); got != cert.Pin() {
		t.Fatalf("replica's pin changed to %q before the switch", got)
	}
	// OnReport runs before the report is stored, so the confirmation is
	// already on disk.
	again, err := lantls.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.Pin() != cert.NextPin() {
		t.Fatalf("main till's next start serves %s; want the confirmed next pin %s", again.Pin(), cert.NextPin())
	}
	if m.plain.Load() != 0 {
		t.Fatalf("main till got %d plain-HTTP request(s)", m.plain.Load())
	}
}

// After the switch, a replica holding the next pin links with no re-proof
// (none is wired here: a mismatch would never link) and promotes it.
func TestRotation_ReplicaPromotesTheNextPinAfterTheSwitch(t *testing.T) {
	dir, pending := pendingRotationCert(t)
	old, next := pending.Pin(), pending.NextPin()
	ageRotationState(t, dir, "next_created_at", lantls.RotationWindow)
	switched, err := lantls.LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if switched.Pin() != next {
		t.Fatalf("switched pin = %s; want %s", switched.Pin(), next)
	}
	m := newRotatingTLSSyncMain(t, switched)
	r, _ := pinnedReplica(t, m, old)
	if err := r.Settings.Set(t.Context(), discovery.PrimaryCertNextPinSettingKey, next); err != nil {
		t.Fatal(err)
	}
	pinSource(t, r)

	startLink(t, r)
	if !waitFor(t, 3*time.Second, func() bool {
		return r.LinkClient.Linked() && settingOf(t, r, discovery.PrimaryCertPinSettingKey) == next
	}) {
		t.Fatalf("not linked with the next pin promoted (linked=%v pin=%q)", r.LinkClient.Linked(), settingOf(t, r, discovery.PrimaryCertPinSettingKey))
	}
	if got := settingOf(t, r, discovery.PrimaryCertNextPinSettingKey); got != "" {
		t.Fatalf("next pin = %q after promotion; want it cleared (the retired key is no longer trusted)", got)
	}
	if m.plain.Load() != 0 {
		t.Fatalf("main till got %d plain-HTTP request(s)", m.plain.Load())
	}
}

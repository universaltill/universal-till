package pages

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/lantls"
)

// Pinned LAN TLS at pairing (ADR-0114 §7, ut-docs#4091), end to end over
// real sockets: the main till serves on a same-port TLS listener wired the
// way internal/server wires it.

// serveTLSTill serves h on a same-port TLS + plain listener with a fresh LAN
// key and returns its plain base URL (what discovery hands a joining till).
func serveTLSTill(t *testing.T, h http.Handler) (string, *lantls.Cert) {
	t.Helper()
	cert, err := lantls.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h, ConnContext: lantls.ConnContext(cert)}
	go func() { _ = srv.Serve(lantls.Listen(inner, cert.TLSConfig())) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "http://" + inner.Addr().String(), cert
}

// tlsPairingPrimary is a main till serving the pairing backend over TLS.
func tlsPairingPrimary(t *testing.T) (pmux *http.ServeMux, base string, cert *lantls.Cert, primaryTillID string, primary *data.PairingRepo) {
	t.Helper()
	d, _ := newSyncDepsWithPath(t, "primary.db")
	if err := d.Settings.Set(t.Context(), "store.name", "Corner Shop"); err != nil {
		t.Fatal(err)
	}
	pmux = http.NewServeMux()
	ptokens := registerSyncAPI(pmux, d)
	registerPairingAPI(pmux, d, auth.NewService(d.Db), ptokens)
	base, cert = serveTLSTill(t, pmux)
	id, err := discovery.TillID(t.Context(), data.NewSettingsRepo(d.Db))
	if err != nil {
		t.Fatal(err)
	}
	return pmux, base, cert, id, data.NewPairingRepo(d.Db)
}

// managerCodes is what the main till's approval card shows.
func managerCodes(t *testing.T, pmux http.Handler) []struct{ ID, Code string } {
	t.Helper()
	rec := doGet(t, pmux, "/api/sync/pair-requests")
	var out struct {
		Data struct {
			Pending []struct {
				ID   string `json:"id"`
				Code string `json:"verification_code"`
			} `json:"pending"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("pair-requests: %v (%s)", err, rec.Body.String())
	}
	res := make([]struct{ ID, Code string }, 0, len(out.Data.Pending))
	for _, p := range out.Data.Pending {
		res = append(res, struct{ ID, Code string }{p.ID, p.Code})
	}
	return res
}

func TestPairing_TLSMainTillBindsThePinAndStagesIt(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	pmux, base, cert, primaryTillID, pending := tlsPairingPrimary(t)

	replica, replicaPath := newSyncDepsWithPath(t, "replica.db")
	rsvc := auth.NewService(replica.Db)
	replica.AuthSvc = rsvc
	rmux := http.NewServeMux()
	registerPairingJoinAPI(rmux, replica)
	rh := auth.Middleware(rmux, rsvc)

	rec := doPostForm(t, rh, "/api/setup/pair-start", url.Values{
		"base_url": {base}, "till_id": {primaryTillID}, "name": {"Bar Till"},
	})
	rows, err := pending.ListPending(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatalf("pending rows %+v, err %v", rows, err)
	}
	if rows[0].ServedPin != cert.Pin() {
		t.Fatalf("main till stored served pin %q, want its own %q (request went over TLS)", rows[0].ServedPin, cert.Pin())
	}
	pinned := derivedVerificationCode(rows[0].Commitment, primaryTillID, cert.Pin())
	if legacy := derivedVerificationCode(rows[0].Commitment, primaryTillID, ""); legacy == pinned {
		t.Skip("pinned and legacy codes collide for this commitment (1 in 10^6) — nothing to tell apart")
	}
	if !strings.Contains(rec.Body.String(), pinned) {
		t.Fatalf("joining till's screen lacks the pinned code %s: %s", pinned, rec.Body.String())
	}
	codes := managerCodes(t, pmux)
	if len(codes) != 1 || codes[0].Code != pinned {
		t.Fatalf("manager's card shows %+v, want the pinned code %s", codes, pinned)
	}

	// Approve; the next poll (pinned to the same certificate) completes the
	// join and stages the pin for the restarted till.
	arec := doPostForm(t, pmux, "/api/sync/pair-requests/"+codes[0].ID+"/approve", nil)
	if arec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", arec.Code, arec.Body.String())
	}
	rec = doGet(t, rh, "/api/setup/pair-status")
	if !strings.Contains(rec.Body.String(), "Corner Shop") || !appdb.PendingRestore(replicaPath) {
		t.Fatalf("join did not complete over the pinned link: %s", rec.Body.String())
	}
	raw, err := os.ReadFile(appdb.ReplicaIdentityPath(replicaPath))
	if err != nil {
		t.Fatal(err)
	}
	var id appdb.ReplicaIdentity
	if err := json.Unmarshal(raw, &id); err != nil {
		t.Fatal(err)
	}
	if id.PrimaryCertPin != cert.Pin() {
		t.Fatalf("staged pin %q, want %q", id.PrimaryCertPin, cert.Pin())
	}
	// Sync keeps dialling the plain URL until slice 3/3 switches it.
	if id.PrimaryURL != base {
		t.Fatalf("staged primary_url %q, want %q", id.PrimaryURL, base)
	}
}

// A device in the middle presents its own certificate and relays to the
// real main till over TLS. The main till binds the pin IT served to the
// relay, the joining till the relay's pin: the two screens show different
// codes, which is what the manager's compare catches.
func TestPairing_MITMCertificateChangesTheCode(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	pmux, base, cert, primaryTillID, pending := tlsPairingPrimary(t)

	target, _ := lantls.HTTPSBase(base)
	targetURL, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	relay := httputil.NewSingleHostReverseProxy(targetURL)
	relay.Transport = lantls.NewPinnedClient(0, "").Transport
	relayBase, relayCert := serveTLSTill(t, relay)

	replica, _ := newSyncDepsWithPath(t, "replica.db")
	rsvc := auth.NewService(replica.Db)
	replica.AuthSvc = rsvc
	rmux := http.NewServeMux()
	registerPairingJoinAPI(rmux, replica)
	rec := doPostForm(t, auth.Middleware(rmux, rsvc), "/api/setup/pair-start", url.Values{
		"base_url": {relayBase}, "till_id": {primaryTillID}, "name": {"Bar Till"},
	})

	rows, err := pending.ListPending(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatalf("relay did not reach the main till: rows %+v err %v body %s", rows, err, rec.Body.String())
	}
	joinerCode := derivedVerificationCode(rows[0].Commitment, primaryTillID, relayCert.Pin())
	if !strings.Contains(rec.Body.String(), joinerCode) {
		t.Fatalf("joining till's code is not over the pin it saw: %s", rec.Body.String())
	}
	codes := managerCodes(t, pmux)
	if len(codes) != 1 {
		t.Fatalf("manager's card: %+v", codes)
	}
	if want := derivedVerificationCode(rows[0].Commitment, primaryTillID, cert.Pin()); codes[0].Code != want {
		t.Fatalf("manager's code %s, want the code over the main till's own pin %s", codes[0].Code, want)
	}
	if codes[0].Code == joinerCode {
		t.Fatal("a MITM certificate left both screens showing the same code")
	}
}

// An existing replica learns the pin from the REAL proof handler over TLS
// (ContactOK's once-per-process backfill).
func TestPrimaryProof_TLSReplicaLearnsThePin(t *testing.T) {
	dp := newMigratedSyncDeps(t, "primary.db")
	tillID, err := data.NewTillsRepo(dp.Db).InsertTill(t.Context(), "Back office", hashBearer("token-abc"))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerPrimaryProof(mux, dp)
	base, cert := serveTLSTill(t, mux)

	rs := data.NewSettingsRepo(newMigratedSyncDeps(t, "replica.db").Db)
	for k, v := range map[string]string{
		"sync.primary_url": base, "sync.bearer": "token-abc", "sync.till_id": tillID,
	} {
		if err := rs.Set(t.Context(), k, v); err != nil {
			t.Fatal(err)
		}
	}
	discovery.NewPrimaryWatch(rs, nil).ContactOK(t.Context())
	got, _, _ := rs.Get(t.Context(), discovery.PrimaryCertPinSettingKey)
	if got != cert.Pin() {
		t.Fatalf("replica pinned %q, want the main till's %q", got, cert.Pin())
	}
}

// The status poll carries request_secret in its URL, so once the pair
// request learned a pin, a different certificate at that address never
// sees a single request byte: the poll stays "waiting", nothing is joined.
func TestPairStatus_PollRefusesAnotherCertificate(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	d, _ := newSyncDepsWithPath(t, "primary.db")
	pmux := http.NewServeMux()
	registerPairingAPI(pmux, d, auth.NewService(d.Db), registerSyncAPI(pmux, d))
	primaryTillID, err := discovery.TillID(t.Context(), data.NewSettingsRepo(d.Db))
	if err != nil {
		t.Fatal(err)
	}

	// The real main till, on a listener this test can close and re-bind.
	realCert, err := lantls.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	realSrv := &http.Server{Handler: pmux, ConnContext: lantls.ConnContext(realCert)}
	go func() { _ = realSrv.Serve(lantls.Listen(ln, realCert.TLSConfig())) }()

	replica, replicaPath := newSyncDepsWithPath(t, "replica.db")
	rsvc := auth.NewService(replica.Db)
	replica.AuthSvc = rsvc
	rmux := http.NewServeMux()
	registerPairingJoinAPI(rmux, replica)
	rh := auth.Middleware(rmux, rsvc)
	doPostForm(t, rh, "/api/setup/pair-start", url.Values{
		"base_url": {"http://" + addr}, "till_id": {primaryTillID}, "name": {"Bar Till"},
	})
	codes := managerCodes(t, pmux)
	if len(codes) != 1 {
		t.Fatalf("pending: %+v", codes)
	}
	if rec := doPostForm(t, pmux, "/api/sync/pair-requests/"+codes[0].ID+"/approve", nil); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d", rec.Code)
	}

	// Something else takes the address with its own certificate.
	_ = realSrv.Close()
	fakeCert, err := lantls.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ln2, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("could not re-bind %s: %v", addr, err)
	}
	var sawRequest atomic.Bool
	fake := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRequest.Store(true)
		pmux.ServeHTTP(w, r) // would hand over the approved token
	})}
	go func() { _ = fake.Serve(lantls.Listen(ln2, fakeCert.TLSConfig())) }()
	t.Cleanup(func() { _ = fake.Close() })

	rec := doGet(t, rh, "/api/setup/pair-status")
	if appdb.PendingRestore(replicaPath) {
		t.Fatalf("joined through a certificate that isn't the pinned one: %s", rec.Body.String())
	}
	if sawRequest.Load() {
		t.Fatal("the unpinned server received the poll (and its request_secret)")
	}
}

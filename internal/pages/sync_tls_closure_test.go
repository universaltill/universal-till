package pages

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/lantls"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ADR-0114 §7, ut-docs#4411: once this main till serves TLS and every paired
// till has pinned, plain-HTTP peer sync closes entirely — the unauthenticated
// handshake routes (enroll, pair-request, pair-request poll, primary-proof)
// refuse plain HTTP too, not only pinned bearers. Satellites are TLS-only
// from enrolment, and a code join tries TLS first.

// closureMain is a main till with the sync, pairing and primary-proof
// surfaces mounted, and its tokens store.
type closureMain struct {
	mux    *http.ServeMux
	d      *common.Deps
	tokens *enrolTokens
	tills  *data.TillsRepo
}

func newClosureMain(t *testing.T) *closureMain {
	t.Helper()
	t.Setenv("UT_AUTH", "off")
	d, _ := newSyncDepsWithPath(t, "primary.db")
	mux := http.NewServeMux()
	tokens := registerSyncAPI(mux, d)
	registerPairingAPI(mux, d, auth.NewService(d.Db), tokens)
	registerPrimaryProof(mux, d)
	return &closureMain{mux: mux, d: d, tokens: tokens, tills: data.NewTillsRepo(d.Db)}
}

// serveLANTLS makes lantls.Serving() non-nil for the test, as the server
// does when the LAN port serves TLS.
func serveLANTLS(t *testing.T) {
	t.Helper()
	cert, err := lantls.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lantls.SetServing(cert))
}

// addTill enrols a till directly; pinned marks it as having used TLS.
func (m *closureMain) addTill(t *testing.T, name, bearer string, pinned bool) string {
	t.Helper()
	id, err := m.tills.InsertTill(t.Context(), name, hashBearer(bearer))
	if err != nil {
		t.Fatal(err)
	}
	if pinned {
		if err := m.tills.MarkTLSPinned(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// overTLS marks a recorder request as arriving over the LAN port's TLS.
func overTLS(req *http.Request) *http.Request {
	req.TLS = &tls.ConnectionState{}
	return req
}

func (m *closureMain) serve(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	m.mux.ServeHTTP(rec, req)
	return rec
}

func enrollReq(token, name, role string, viaTLS bool) *http.Request {
	body, _ := json.Marshal(map[string]string{"token": token, "name": name, "role": role})
	req := httptest.NewRequest(http.MethodPost, "/api/sync/enroll", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	if viaTLS {
		overTLS(req)
	}
	return req
}

func pairReq(name, commitment, remote string, viaTLS bool) *http.Request {
	body, _ := json.Marshal(map[string]string{"device_name": name, "commitment": commitment})
	req := httptest.NewRequest(http.MethodPost, "/api/sync/pair-request", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remote
	if viaTLS {
		overTLS(req)
	}
	return req
}

// proofReq is a primary-proof challenge for tillID, addressed to the socket
// it arrives on (hostIsThisTill).
func proofReq(tillID string, viaTLS bool) *http.Request {
	body, _ := json.Marshal(map[string]string{"till_id": tillID, "nonce": strings.Repeat("ab", 32)})
	req := httptest.NewRequest(http.MethodPost, "/api/sync/primary-proof", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Host = "127.0.0.1:8080"
	req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey,
		&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080}))
	if viaTLS {
		overTLS(req)
	}
	return req
}

func pingReq(bearer string, viaTLS bool) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/sync/ping", nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	if viaTLS {
		overTLS(req)
	}
	return req
}

func TestPlainSyncClosure_ClosedRefusesPlainHandshakesWithoutBurningTheToken(t *testing.T) {
	m := newClosureMain(t)
	serveLANTLS(t)
	tillID := m.addTill(t, "Till 2", "bearer-2", true)
	tok := m.tokens.issue()

	// Plain enroll: refused before the one-time token is spent.
	if rec := m.serve(enrollReq(tok, "Till 3", "additional", false)); rec.Code != http.StatusForbidden {
		t.Fatalf("plain enroll once closed = %d %s, want 403", rec.Code, rec.Body)
	}
	// Plain pair-request: refused, and nothing is queued.
	if rec := m.serve(pairReq("Bar till", commitOf("s-plain"), "10.0.0.40:1", false)); rec.Code != http.StatusForbidden {
		t.Fatalf("plain pair-request once closed = %d %s, want 403", rec.Code, rec.Body)
	}
	if rows, _ := data.NewPairingRepo(m.d.Db).ListPending(t.Context()); len(rows) != 0 {
		t.Fatalf("a refused plain pair-request queued %+v", rows)
	}
	// Plain primary-proof: refused.
	if rec := m.serve(proofReq(tillID, false)); rec.Code != http.StatusForbidden {
		t.Fatalf("plain primary-proof once closed = %d %s, want 403", rec.Code, rec.Body)
	}

	// The same handshakes over TLS still work.
	if rec := m.serve(proofReq(tillID, true)); rec.Code != http.StatusOK {
		t.Fatalf("TLS primary-proof = %d %s, want 200", rec.Code, rec.Body)
	}
	secret := "s-tls"
	rec := m.serve(pairReq("Bar till", commitOf(secret), "10.0.0.41:1", true))
	id := pairRequestID(t, rec)
	if arec := doPostForm(t, m.mux, "/api/sync/pair-requests/"+id+"/approve", nil); arec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", arec.Code, arec.Body)
	}
	poll := httptest.NewRequest(http.MethodGet, "/api/sync/pair-requests/"+id+"?request_secret="+secret, nil)
	poll.RemoteAddr = "10.0.0.42:1"
	if rec := m.serve(poll); rec.Code != http.StatusForbidden {
		t.Fatalf("plain pair-request poll once closed = %d %s, want 403", rec.Code, rec.Body)
	}
	poll = httptest.NewRequest(http.MethodGet, "/api/sync/pair-requests/"+id+"?request_secret="+secret, nil)
	poll.RemoteAddr = "10.0.0.42:1"
	if rec := m.serve(overTLS(poll)); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"token"`) {
		t.Fatalf("TLS pair-request poll = %d %s, want 200 with the token", rec.Code, rec.Body)
	}
	// The token the refused plain enroll presented is still live over TLS.
	if rec := m.serve(enrollReq(tok, "Till 3", "additional", true)); rec.Code != http.StatusOK {
		t.Fatalf("TLS enroll with the token a refused plain enroll presented = %d %s, want 200", rec.Code, rec.Body)
	}
}

// Ping and the browser's own session routes are never closed: the closure
// covers only the unauthenticated peer handshakes. A plain ping with a
// pinned bearer is still syncTill's 401, not the closure's 403.
func TestPlainSyncClosure_PingAndBrowserRoutesStayOpen(t *testing.T) {
	m := newClosureMain(t)
	serveLANTLS(t)
	m.addTill(t, "Till 2", "bearer-2", true)

	if rec := m.serve(pingReq("bearer-2", true)); rec.Code != http.StatusOK {
		t.Fatalf("TLS ping = %d, want 200", rec.Code)
	}
	if rec := m.serve(pingReq("bearer-2", false)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("plain ping with a pinned bearer = %d, want syncTill's 401 (not the closure's 403)", rec.Code)
	}
	// The local browser mints pairing codes over plain HTTP.
	rec := doPostForm(t, m.mux, "/api/sync/enroll-token", url.Values{"url": {"http://192.168.1.10:8080"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "pairing-short-code") {
		t.Fatalf("plain enroll-token once closed = %d %s, want 200", rec.Code, rec.Body)
	}
	if rec := doGet(t, m.mux, "/api/sync/pair-requests"); rec.Code != http.StatusOK {
		t.Fatalf("plain pair-requests list once closed = %d %s, want 200", rec.Code, rec.Body)
	}
}

func TestPlainSyncClosure_StaysOpenUntilEveryTillPinned(t *testing.T) {
	cases := []struct {
		name    string
		serving bool
		tills   []bool // pinned flag per enrolled till
	}{
		{"one till unpinned", true, []bool{true, false}},
		{"zero tills", true, nil},
		{"TLS not serving", false, []bool{true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newClosureMain(t)
			if tc.serving {
				serveLANTLS(t)
			} else {
				t.Cleanup(lantls.SetServing(nil))
			}
			var firstID string
			for i, pinned := range tc.tills {
				id := m.addTill(t, "Till "+string(rune('2'+i)), "bearer-"+string(rune('a'+i)), pinned)
				if firstID == "" {
					firstID = id
				}
			}
			if rec := m.serve(pairReq("Bar till", commitOf("s1"), "10.0.0.50:1", false)); rec.Code != http.StatusOK {
				t.Fatalf("plain pair-request = %d %s, want 200 (still open)", rec.Code, rec.Body)
			}
			if firstID != "" {
				if rec := m.serve(proofReq(firstID, false)); rec.Code != http.StatusOK {
					t.Fatalf("plain primary-proof = %d %s, want 200 (still open)", rec.Code, rec.Body)
				}
			}
			if rec := m.serve(enrollReq(m.tokens.issue(), "New till", "additional", false)); rec.Code != http.StatusOK {
				t.Fatalf("plain enroll = %d %s, want 200 (still open)", rec.Code, rec.Body)
			}
		})
	}
}

// Satellites are TLS-only from enrolment (ADR-0114 §7): a plain satellite
// enrolment is refused before the token is spent, whether or not the fleet
// has closed plain HTTP; one over TLS is pinned at once.
func TestEnroll_SatelliteIsTLSOnlyFromEnrolment(t *testing.T) {
	m := newClosureMain(t)
	tok := m.tokens.issue()

	if rec := m.serve(enrollReq(tok, "Kiosk", "satellite", false)); rec.Code != http.StatusForbidden {
		t.Fatalf("plain satellite enroll = %d %s, want 403", rec.Code, rec.Body)
	}
	rec := m.serve(enrollReq(tok, "Kiosk", "satellite", true))
	if rec.Code != http.StatusOK {
		t.Fatalf("TLS satellite enroll with the same token = %d %s, want 200", rec.Code, rec.Body)
	}
	var out struct {
		Data struct {
			Bearer string `json:"bearer"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Data.Bearer == "" {
		t.Fatalf("enroll body %s: %v", rec.Body, err)
	}
	row, ok, err := m.tills.TillByBearerHashAt(t.Context(), hashBearer(out.Data.Bearer), time.Now())
	if err != nil || !ok || !row.TLSPinned {
		t.Fatalf("satellite after TLS enrolment: pinned=%v ok=%v err=%v, want pinned", row.TLSPinned, ok, err)
	}
	if rec := m.serve(pingReq(out.Data.Bearer, false)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("plain ping with a fresh satellite's bearer = %d, want 401", rec.Code)
	}

	// An additional till enrolled over TLS is not pre-pinned (its first TLS
	// bearer request pins it, as before).
	rec = m.serve(enrollReq(m.tokens.issue(), "Till 3", "additional", true))
	if rec.Code != http.StatusOK {
		t.Fatalf("TLS additional enroll = %d %s", rec.Code, rec.Body)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if row, _, _ := m.tills.TillByBearerHashAt(t.Context(), hashBearer(out.Data.Bearer), time.Now()); row.TLSPinned {
		t.Fatal("an additional till enrolled over TLS must not be pre-pinned")
	}
}

// The manager's approval card can make a till a satellite; that enrolment is
// TLS-only too, even if the joining till itself asked for "additional".
func TestEnroll_ApprovedSatelliteRefusedOnPlain(t *testing.T) {
	m := newClosureMain(t)
	secret := "s-sat"
	id := pairRequestID(t, m.serve(pairReq("Order station", commitOf(secret), "10.0.0.60:1", true)))
	if rec := doPostForm(t, m.mux, "/api/sync/pair-requests/"+id+"/approve", url.Values{"role": {"satellite"}}); rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	row, _, err := data.NewPairingRepo(m.d.Db).GetByID(t.Context(), id)
	if err != nil || row.Token == "" {
		t.Fatalf("approved row %+v err %v", row, err)
	}
	if rec := m.serve(enrollReq(row.Token, "Order station", "additional", false)); rec.Code != http.StatusForbidden {
		t.Fatalf("plain enroll of an approved satellite = %d %s, want 403", rec.Code, rec.Body)
	}
	if rec := m.serve(enrollReq(row.Token, "Order station", "additional", true)); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"role":"satellite"`) {
		t.Fatalf("TLS enroll of an approved satellite = %d %s, want 200 as satellite", rec.Code, rec.Body)
	}
}

// A code join (QR / long code / short code) sends the enrolment over TLS on
// the main till's same port first, learning the pin, and stages it — so it
// still works against a main till that has closed plain HTTP, and the new
// till dials pinned from the start.
func TestSyncJoin_CodeJoinTriesTLSFirstAndStagesThePin(t *testing.T) {
	m := newClosureMain(t)
	if err := m.d.Settings.Set(t.Context(), "store.name", "Corner Shop"); err != nil {
		t.Fatal(err)
	}
	h, plain := countPlainSync(m.mux)
	base, cert := serveTLSTill(t, h)
	t.Cleanup(lantls.SetServing(cert))
	m.addTill(t, "Till 2", "bearer-2", true) // the fleet has closed plain HTTP

	code := encodeEnrollCode(base, m.tokens.issue())
	replica, replicaPath := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerSyncAPI(rmux, replica)
	rec := doPostForm(t, rmux, "/api/sync/join", url.Values{"code": {code}, "name": {"Till 3"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Corner Shop") {
		t.Fatalf("code join against a closed TLS main till = %d %s, want 200", rec.Code, rec.Body)
	}
	if n := plain.Load(); n != 0 {
		t.Fatalf("main till got %d plain-HTTP /api/sync/* request(s) during a TLS code join", n)
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
		t.Fatalf("staged pin %q, want the main till's %q", id.PrimaryCertPin, cert.Pin())
	}
	if id.PrimaryURL != base {
		t.Fatalf("staged primary_url %q, want the plain LAN address %q", id.PrimaryURL, base)
	}
	// The snapshot download over TLS pinned the new till on the main till.
	row, ok, err := m.tills.TillByBearerHashAt(t.Context(), hashBearer(id.Bearer), time.Now())
	if err != nil || !ok || !row.TLSPinned {
		t.Fatalf("joined till on the main till: pinned=%v ok=%v err=%v, want pinned", row.TLSPinned, ok, err)
	}
}

// Against a main till that serves plain HTTP only, the code join falls back
// to plain with no pin, exactly as before.
func TestSyncJoin_CodeJoinFallsBackToPlainWithoutAPin(t *testing.T) {
	m := newClosureMain(t)
	srv := httptest.NewServer(m.mux)
	t.Cleanup(srv.Close)
	code := encodeEnrollCode(srv.URL, m.tokens.issue())
	replica, replicaPath := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerSyncAPI(rmux, replica)
	if rec := doPostForm(t, rmux, "/api/sync/join", url.Values{"code": {code}, "name": {"Till 3"}}); rec.Code != http.StatusOK {
		t.Fatalf("plain code join = %d %s, want 200", rec.Code, rec.Body)
	}
	raw, err := os.ReadFile(appdb.ReplicaIdentityPath(replicaPath))
	if err != nil {
		t.Fatal(err)
	}
	var id appdb.ReplicaIdentity
	if err := json.Unmarshal(raw, &id); err != nil {
		t.Fatal(err)
	}
	if id.PrimaryCertPin != "" || id.PrimaryURL != srv.URL {
		t.Fatalf("plain fallback staged pin %q url %q, want no pin and %q", id.PrimaryCertPin, id.PrimaryURL, srv.URL)
	}
}

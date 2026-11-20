package pages

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/universaltill/universal-till/internal/discovery"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/lantls"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#6538 R1a (ADR-0198 §6.2/6.3, design-batch2 §2/§3/§6): a phone's
// card reader identity. Request over TLS only, manager approval with PIN, role
// fixed by the request, the bearer handed over once, revoke at once.

type readerRigT struct {
	mux    *http.ServeMux
	d      *common.Deps
	tokens *enrolTokens
	cert   *lantls.Cert
	polls  int
}

// newReaderRig is a main till with the sync, pairing, reader-management and
// link surfaces mounted and one reader-capable payment method configured.
// UT_AUTH=off: the manager PIN gate is bypassed, as in the other pairing
// tests; the manager-only tests below turn it back on.
func newReaderRig(t *testing.T, authOff bool) *readerRigT {
	t.Helper()
	if authOff {
		t.Setenv("UT_AUTH", "off")
	}
	resetBearerFailures(t)
	d, _ := newSyncDepsWithPath(t, "primary.db")
	if err := d.Settings.Set(t.Context(), settingPhoneReaderPairing, "on"); err != nil {
		t.Fatal(err)
	}
	d.AuthSvc = auth.NewService(d.Db)
	seedReaderPlugin(t, d, "p.pay", "pay", true)
	cert, err := lantls.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lantls.SetServing(cert))
	mux := http.NewServeMux()
	tokens := registerSyncAPI(mux, d)
	registerPairingAPI(mux, d, d.AuthSvc, tokens)
	registerPendingPairingsUI(mux, d)
	registerSyncLink(mux, d)
	registerSyncBearerRotate(mux, d)
	registerPrimaryProof(mux, d)
	registerReaderManagement(mux, d)
	return &readerRigT{mux: mux, d: d, tokens: tokens, cert: cert}
}

// asTLS marks a request as arriving over the LAN port's TLS with this till's
// pin stamped on the connection, as lantls.ConnContext does in the server.
func (g *readerRigT) asTLS(req *http.Request) *http.Request {
	req.TLS = &tls.ConnectionState{}
	ctx := lantls.ConnContext(g.cert)(req.Context(), &tls.Conn{})
	return req.WithContext(ctx)
}

func (g *readerRigT) serve(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	g.mux.ServeHTTP(rec, req)
	return rec
}

func readerPairBody(name, commitment, role string) string {
	m := map[string]string{"device_name": name, "commitment": commitment}
	if role != "-" {
		m["role"] = role
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func (g *readerRigT) request(t *testing.T, name, secret, role string, viaTLS bool, remote string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/pair-request", strings.NewReader(readerPairBody(name, commitOf(secret), role)))
	req.Header.Set("Content-Type", "application/json")
	if remote != "" {
		req.RemoteAddr = remote
	}
	if viaTLS {
		req = g.asTLS(req)
	}
	return g.serve(req)
}

func idOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Data.ID == "" {
		t.Fatalf("no request id in %d %s", rec.Code, rec.Body)
	}
	return out.Data.ID
}

func (g *readerRigT) approve(id, form string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/sync/pair-requests/"+id+"/approve", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return g.serve(req)
}

type readerHandover struct {
	ReaderID string `json:"reader_id"`
	Bearer   string `json:"bearer"`
	Pin      string `json:"pin"`
}

func (g *readerRigT) poll(t *testing.T, id, secret string, viaTLS bool) (*httptest.ResponseRecorder, readerHandover) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/sync/pair-requests/"+id, nil)
	req.Header.Set(fleetlink.HeaderPairRequestSecret, secret)
	g.polls++
	req.RemoteAddr = fmt.Sprintf("192.0.2.%d:7000", 100+g.polls) // each poll its own source: the per-source limiter is not under test here
	if viaTLS {
		req = g.asTLS(req)
	}
	rec := g.serve(req)
	var out struct {
		Data readerHandover `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out.Data
}

// enrolReader runs the whole pairing and returns the reader id and its bearer.
func (g *readerRigT) enrolReader(t *testing.T, name string) (readerID, bearer string) {
	t.Helper()
	secret := "secret-" + name
	rec := g.request(t, name, secret, "card_reader", true, "192.0.2.50:4000")
	if rec.Code != http.StatusOK {
		t.Fatalf("reader pair request = %d %s", rec.Code, rec.Body)
	}
	id := idOf(t, rec)
	if a := g.approve(id, ""); a.Code != http.StatusOK {
		t.Fatalf("approve = %d %s", a.Code, a.Body)
	}
	pr, h := g.poll(t, id, secret, true)
	if pr.Code != http.StatusOK || h.ReaderID == "" || h.Bearer == "" {
		t.Fatalf("poll = %d %s", pr.Code, pr.Body)
	}
	return h.ReaderID, h.Bearer
}

func TestPairRequestCardReaderRefusedOnPlainHTTP(t *testing.T) {
	g := newReaderRig(t, true)
	rec := g.request(t, "Phone", "s1", "card_reader", false, "")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "tls_required") {
		t.Fatalf("plain-HTTP reader request = %d %q, want 400 tls_required", rec.Code, rec.Body)
	}
	if list, _ := data.NewPairingRepo(g.d.Db).ListPending(t.Context()); len(list) != 0 {
		t.Fatalf("a refused request stored %d pending rows", len(list))
	}
	// Over TLS the same request is accepted and stored as a reader request.
	rec = g.request(t, "Phone", "s1", "card_reader", true, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("TLS reader request = %d %s", rec.Code, rec.Body)
	}
	list, _ := data.NewPairingRepo(g.d.Db).ListPending(t.Context())
	if len(list) != 1 || list[0].RequestedRole != data.TillRoleCardReader {
		t.Fatalf("pending = %+v, want one card_reader request", list)
	}
}

func TestPairRequestCardReaderRefusedOnAFollowingTill(t *testing.T) {
	g := newReaderRig(t, true)
	if err := g.d.Settings.Set(t.Context(), "sync.primary_url", "https://main.example"); err != nil {
		t.Fatal(err)
	}
	rec := g.request(t, "Phone", "s1", "card_reader", true, "")
	if rec.Code == http.StatusOK {
		t.Fatalf("an additional till took a reader request: %s (ADR-0198 O-3: readers pair to the main/standalone till only)", rec.Body)
	}
}

func TestQRAndShortCodeNeverEnrolReader(t *testing.T) {
	g := newReaderRig(t, true)
	long := g.tokens.issue()
	if rec := g.serve(g.asTLS(enrollReq(long, "Phone", "card_reader", true))); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "reader_needs_request") {
		t.Fatalf("QR-token enrol as card_reader = %d %q, want 400 reader_needs_request", rec.Code, rec.Body)
	}
	// The refusal happens before the token is spent: a till can still use it.
	if rec := g.serve(g.asTLS(enrollReq(long, "Till 2", "additional", true))); rec.Code != http.StatusOK {
		t.Fatalf("the unspent QR token must still enrol a till: %d %s", rec.Code, rec.Body)
	}
	_, code := g.tokens.issueWithShort()
	if rec := g.serve(g.asTLS(enrollReq(code, "Phone", "card_reader", true))); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "reader_needs_request") {
		t.Fatalf("short-code enrol as card_reader = %d %q, want 400 reader_needs_request", rec.Code, rec.Body)
	}
	if rs, _ := data.NewCardReaderRepo(g.d.Db).ListActive(t.Context()); len(rs) != 0 {
		t.Fatalf("a QR/short-code enrolment created %d reader rows", len(rs))
	}
}

func TestReaderPairRequestRateLimited(t *testing.T) {
	g := newReaderRig(t, true)
	got429 := false
	for i := 0; i < 8; i++ {
		rec := g.request(t, "Phone", "s"+string(rune('a'+i)), "card_reader", true, "192.0.2.77:5555")
		if rec.Code == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatal("reader pair requests from one source were never rate limited")
	}
}

func TestEmptyRoleRequestIsAnAdditionalTillNeverAReader(t *testing.T) {
	g := newReaderRig(t, true)
	rec := g.request(t, "Till Two", "s1", "-", true, "") // role omitted
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy request = %d %s", rec.Code, rec.Body)
	}
	list, _ := data.NewPairingRepo(g.d.Db).ListPending(t.Context())
	if len(list) != 1 || list[0].RequestedRole != data.TillRoleAdditional {
		t.Fatalf("pending = %+v, want one additional-till request", list)
	}
	if rec := g.request(t, "Odd", "s2", "Card_Reader ", true, "192.0.2.9:1"); rec.Code != http.StatusBadRequest {
		t.Fatalf("a near-miss role spelling = %d, want 400 (role must be exact)", rec.Code)
	}
}

func TestApproveRefusesRoleChangeForReaderRequestsBothWays(t *testing.T) {
	g := newReaderRig(t, true)
	id := idOf(t, g.request(t, "Phone", "s1", "card_reader", true, ""))
	for _, role := range []string{"additional", "satellite"} {
		if rec := g.approve(id, "role="+role); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "role_mismatch") {
			t.Fatalf("reader request approved as %s = %d %q, want 409 role_mismatch", role, rec.Code, rec.Body)
		}
	}
	tid := idOf(t, g.request(t, "Till", "s2", "additional", true, "192.0.2.9:1"))
	if rec := g.approve(tid, "role=card_reader"); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "role_mismatch") {
		t.Fatalf("till request approved as card_reader = %d %q, want 409 role_mismatch", rec.Code, rec.Body)
	}
}

func TestReaderNeverEnrolledAsAdditional(t *testing.T) {
	g := newReaderRig(t, true)
	id := idOf(t, g.request(t, "Phone", "s1", "card_reader", true, ""))
	if rec := g.approve(id, ""); rec.Code != http.StatusOK {
		t.Fatalf("approve = %d %s", rec.Code, rec.Body)
	}
	// No enrolment token exists for the request: nothing to spend at /enroll.
	row, _, _ := data.NewPairingRepo(g.d.Db).GetByID(t.Context(), id)
	if row.Token != "" {
		t.Fatalf("a reader approval minted an enrolment token %q that /api/sync/enroll could redeem as a till", row.Token)
	}
	if tills, _ := data.NewTillsRepo(g.d.Db).ListTills(t.Context()); len(tills) != 0 {
		t.Fatalf("the reader request created %d till rows", len(tills))
	}
}

func TestApproveCreatesReaderWithBearerShownOnce(t *testing.T) {
	g := newReaderRig(t, true)
	secret := "s-once"
	id := idOf(t, g.request(t, "Counter phone", secret, "card_reader", true, ""))
	// Unapproved: nothing to hand over.
	if rec, _ := g.poll(t, id, secret, true); rec.Code != http.StatusNotFound {
		t.Fatalf("poll before approval = %d, want 404", rec.Code)
	}
	if rec := g.approve(id, ""); rec.Code != http.StatusOK {
		t.Fatalf("approve = %d %s", rec.Code, rec.Body)
	}
	// Wrong secret: 404, and the approval is not consumed.
	if rec, _ := g.poll(t, id, "not-the-secret", true); rec.Code != http.StatusNotFound {
		t.Fatalf("poll with wrong secret = %d, want 404", rec.Code)
	}
	// Plain HTTP: never over the wire in the clear.
	if rec, _ := g.poll(t, id, secret, false); rec.Code == http.StatusOK {
		t.Fatalf("the bearer left over plain HTTP: %s", rec.Body)
	}
	rec, h := g.poll(t, id, secret, true)
	if rec.Code != http.StatusOK || h.ReaderID == "" || h.Bearer == "" || h.Pin != g.cert.Pin() {
		t.Fatalf("handover = %d %s (pin %q want this till's pin)", rec.Code, rec.Body, h.Pin)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("the bearer response must be Cache-Control: no-store")
	}
	// Replay of the approval: the same secret never yields a second bearer.
	if rec, _ := g.poll(t, id, secret, true); rec.Code != http.StatusNotFound {
		t.Fatalf("replayed poll = %d, want 404 (the bearer is shown once)", rec.Code)
	}
	cr, err := data.NewCardReaderRepo(g.d.Db).Get(t.Context(), h.ReaderID)
	if err != nil {
		t.Fatal(err)
	}
	if cr.Kind != data.CardReaderKindPhone || cr.Source != data.CardReaderSourcePaired || cr.Name != "Counter phone" || cr.MethodKey != "pay" {
		t.Fatalf("reader row = %+v", cr)
	}
	// Only the hash is stored.
	var n int
	if err := g.d.Db.QueryRow(`SELECT COUNT(*) FROM card_reader_credentials WHERE bearer_hash = ?`, h.Bearer).Scan(&n); err != nil || n != 0 {
		t.Fatalf("plaintext bearer found in the table (n=%d err=%v)", n, err)
	}
	if err := g.d.Db.QueryRow(`SELECT COUNT(*) FROM card_reader_credentials WHERE bearer_hash = ?`, hashBearer(h.Bearer)).Scan(&n); err != nil || n != 1 {
		t.Fatalf("hashed bearer rows = %d err=%v, want 1", n, err)
	}
}

func TestPollWithAnotherPinDoesNotHandOverTheBearer(t *testing.T) {
	g := newReaderRig(t, true)
	secret := "s-pin"
	id := idOf(t, g.request(t, "Phone", secret, "card_reader", true, ""))
	g.approve(id, "")
	// A poll arriving on a connection that served a different pin than the
	// request saw (a relay or a rotated key): refused, bearer not released.
	other, err := lantls.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/sync/pair-requests/"+id, nil)
	req.Header.Set(fleetlink.HeaderPairRequestSecret, secret)
	req.TLS = &tls.ConnectionState{}
	req = req.WithContext(lantls.ConnContext(other)(req.Context(), &tls.Conn{}))
	if rec := g.serve(req); rec.Code == http.StatusOK {
		t.Fatalf("bearer handed over on a connection with another pin: %s", rec.Body)
	}
	if rec, _ := g.poll(t, id, secret, true); rec.Code != http.StatusOK {
		t.Fatalf("the right pin must still collect it: %d", rec.Code)
	}
}

func TestRogueBearerRefusedOnLink(t *testing.T) {
	g := newReaderRig(t, true)
	req := g.asTLS(httptest.NewRequest(http.MethodGet, "/api/sync/link", nil))
	req.Header.Set("Authorization", "Bearer never-enrolled")
	if rec := g.serve(req); rec.Code != http.StatusUnauthorized {
		t.Fatalf("rogue bearer on /api/sync/link = %d, want 401", rec.Code)
	}
}

func readerReq(g *readerRigT, method, path, bearer string, viaTLS bool) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.RemoteAddr = "192.0.2.50:4000"
	if viaTLS {
		req = g.asTLS(req)
	}
	return req
}

func TestEnrolledReaderPassesAuthOnTheAllowedRoutes(t *testing.T) {
	g := newReaderRig(t, true)
	_, bearer := g.enrolReader(t, "Phone")
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/sync/link"},
		{http.MethodPost, "/api/sync/bearer/rotate"},
		{http.MethodPost, "/api/sync/enroll/withdraw"},
	} {
		if rec := g.serve(readerReq(g, tc.method, tc.path, bearer, true)); rec.Code == http.StatusUnauthorized {
			t.Errorf("%s %s = 401 for an enrolled reader, want past auth", tc.method, tc.path)
		}
	}
	// The reader link needs TLS: its bearer over plain HTTP is refused.
	if rec := g.serve(readerReq(g, http.MethodGet, "/api/sync/link", bearer, false)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("reader bearer over plain HTTP = %d, want 401", rec.Code)
	}
}

func TestReaderBearerRotation(t *testing.T) {
	g := newReaderRig(t, true)
	_, bearer := g.enrolReader(t, "Phone")
	rec := g.serve(readerReq(g, http.MethodPost, "/api/sync/bearer/rotate", bearer, true))
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate = %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Data struct {
			Bearer string `json:"bearer"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Data.Bearer == "" || out.Data.Bearer == bearer {
		t.Fatalf("rotate returned %q", out.Data.Bearer)
	}
	if rec := g.serve(readerReq(g, http.MethodPost, "/api/sync/enroll/withdraw", out.Data.Bearer, true)); rec.Code == http.StatusUnauthorized {
		t.Fatal("the rotated bearer must authenticate")
	}
}

func TestRevokedReaderBearerRefusedOnAllowList(t *testing.T) {
	g := newReaderRig(t, true)
	id, bearer := g.enrolReader(t, "Phone")
	if _, err := data.NewCardReaderRepo(g.d.Db).SoftRemove(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/sync/link"},
		{http.MethodPost, "/api/sync/bearer/rotate"},
		{http.MethodPost, "/api/sync/enroll/withdraw"},
	} {
		if rec := g.serve(readerReq(g, tc.method, tc.path, bearer, true)); rec.Code != http.StatusUnauthorized {
			t.Errorf("revoked reader on %s %s = %d, want 401", tc.method, tc.path, rec.Code)
		}
	}
}

func TestForeignPinBearerRefused(t *testing.T) {
	// A bearer presented on a connection that served a pin other than the one
	// recorded at enrolment (a stolen phone moved to another main, or a
	// replayed bearer through a relay) is refused.
	g := newReaderRig(t, true)
	_, bearer := g.enrolReader(t, "Phone")
	other, err := lantls.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	req := readerReq(g, http.MethodGet, "/api/sync/link", bearer, false)
	req.TLS = &tls.ConnectionState{}
	req = req.WithContext(lantls.ConnContext(other)(req.Context(), &tls.Conn{}))
	if rec := g.serve(req); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bearer on a foreign-pin connection = %d, want 401", rec.Code)
	}
}

func TestReaderCannotEnrolAsTillOrSatelliteByRole(t *testing.T) {
	// Role escalation: a reader bearer never authenticates a till-only route.
	g := newReaderRig(t, true)
	_, bearer := g.enrolReader(t, "Phone")
	// (The whole /api/sync surface is walked by TestReaderBearerRefusedOnEverySyncRoute.)
	for _, p := range []string{"/api/sync/snapshot", "/api/sync/ping"} {
		if rec := g.serve(readerReq(g, http.MethodGet, p, bearer, true)); rec.Code != http.StatusUnauthorized {
			t.Errorf("reader bearer on %s = %d, want 401", p, rec.Code)
		}
	}
}

func TestReaderNotInTillRosterOrSnapshot(t *testing.T) {
	g := newReaderRig(t, true)
	id, _ := g.enrolReader(t, "Phone")
	// The tills table (roster, admin bundle) has no row for it.
	tills, _ := data.NewTillsRepo(g.d.Db).ListTills(t.Context())
	for _, tl := range tills {
		if tl.ID == id {
			t.Fatal("the reader appears in the tills roster")
		}
	}
	// A joining till's snapshot carries neither the credential hash nor the
	// paired reader row.
	enrolTill := g.tokens.issue()
	rec := g.serve(g.asTLS(enrollReq(enrolTill, "Till 2", "additional", true)))
	if rec.Code != http.StatusOK {
		t.Fatalf("enrol a till = %d %s", rec.Code, rec.Body)
	}
	var tout struct {
		Data struct {
			Bearer string `json:"bearer"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &tout)
	if tout.Data.Bearer == "" {
		t.Fatalf("no till bearer in %s", rec.Body)
	}
	snap := g.serve(readerReq(g, http.MethodGet, "/api/sync/snapshot", tout.Data.Bearer, true))
	if snap.Code != http.StatusOK {
		t.Fatalf("snapshot = %d", snap.Code)
	}
	// The table exists in the copy (schema); its rows must not.
	copyPath := filepath.Join(t.TempDir(), "snap.db")
	if err := os.WriteFile(copyPath, snap.Body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	cdb, err := sql.Open("sqlite", "file:"+copyPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer cdb.Close()
	var n int
	if err := cdb.QueryRow(`SELECT COUNT(*) FROM card_reader_credentials`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("snapshot carries %d credential rows (err %v), want 0", n, err)
	}
	if err := cdb.QueryRow(`SELECT COUNT(*) FROM card_readers WHERE source = 'paired'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("snapshot carries %d paired reader rows (err %v), want 0", n, err)
	}
}

func TestSyncedAdminBundleCannotWriteReaders(t *testing.T) {
	g := newReaderRig(t, true)
	if err := g.d.Settings.Set(t.Context(), "sync.primary_url", "https://main.example"); err != nil {
		t.Fatal(err)
	}
	repo := data.NewSyncAdminRepo(g.d.Db)
	bundle := data.AdminBundle{Tables: map[string][]map[string]any{
		"card_readers":            {{"id": "x1", "name": "Injected", "method_key": "pay", "plugin_id": "p.pay", "reader_ref": "rd_inj", "kind": "phone"}},
		"card_reader_credentials": {{"reader_id": "x1", "bearer_hash": "abc"}},
	}}
	// An unknown-table bundle may error or be ignored; it must write nothing.
	_, _ = repo.ApplyAdminWithResult(t.Context(), bundle)
	var n int
	_ = g.d.Db.QueryRow(`SELECT COUNT(*) FROM card_readers WHERE id = 'x1'`).Scan(&n)
	var m int
	_ = g.d.Db.QueryRow(`SELECT COUNT(*) FROM card_reader_credentials WHERE reader_id = 'x1'`).Scan(&m)
	if n != 0 || m != 0 {
		t.Fatalf("a synced admin bundle wrote reader rows (readers %d, credentials %d)", n, m)
	}
}

// --- manager-only routes ---

func TestReaderPairingRoutesManagerOnly(t *testing.T) {
	g := newReaderRig(t, false)
	cases := []struct{ method, path, body string }{
		{http.MethodGet, "/ui/tills/reader-requests", ""},
		{http.MethodGet, "/ui/tills/readers", ""},
		{http.MethodPost, "/ui/tills/readers/some-id/rename", "name=Hacked"},
		{http.MethodPost, "/ui/tills/readers/some-id/revoke", "manager_pin=0000"},
		{http.MethodGet, "/api/sync/pair-requests", ""},
		{http.MethodPost, "/api/sync/pair-requests/some-id/approve", ""},
		{http.MethodPost, "/api/sync/pair-requests/some-id/deny", ""},
	}
	for _, tc := range cases {
		var body *strings.Reader
		if tc.body != "" {
			body = strings.NewReader(tc.body)
		} else {
			body = strings.NewReader("")
		}
		req := auth.WithUser(httptest.NewRequest(tc.method, tc.path, body), auth.User{ID: "u1", Role: "cashier"})
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if rec := g.serve(req); rec.Code != http.StatusForbidden {
			t.Errorf("cashier %s %s = %d, want 403", tc.method, tc.path, rec.Code)
		}
	}
	// No session at all is not a manager either.
	if rec := g.serve(httptest.NewRequest(http.MethodGet, "/ui/tills/readers", nil)); rec.Code != http.StatusForbidden {
		t.Errorf("no session /ui/tills/readers = %d, want 403", rec.Code)
	}
}

func TestReaderListShowsPairedPhonesToAManagerOnly(t *testing.T) {
	g := newReaderRig(t, false)
	t.Setenv("UT_AUTH", "off")
	_, _ = g.enrolReader(t, "Counter phone")
	t.Setenv("UT_AUTH", "")
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/ui/tills/readers", nil), auth.User{ID: "m1", Role: "manager"})
	rec := g.serve(req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Counter phone") {
		t.Fatalf("manager reader list = %d %q", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "bearer") {
		t.Fatal("the reader list must not mention bearer material")
	}
}

func TestRevokeReaderNeedsPinRemovesRowAndClosesLink(t *testing.T) {
	g := newReaderRig(t, true)
	id, bearer := g.enrolReader(t, "Phone")
	var closed []string
	old := disconnectReaderLink
	disconnectReaderLink = func(_ *common.Deps, readerID string) { closed = append(closed, readerID) }
	t.Cleanup(func() { disconnectReaderLink = old })

	rec := g.serve(newFormReq(http.MethodPost, "/ui/tills/readers/"+id+"/revoke", url.Values{}))
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d %s", rec.Code, rec.Body)
	}
	if len(closed) != 1 || closed[0] != id {
		t.Fatalf("link close calls = %v, want [%s] (revoke closes the link at once)", closed, id)
	}
	if rec := g.serve(readerReq(g, http.MethodPost, "/api/sync/enroll/withdraw", bearer, true)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked bearer = %d, want 401", rec.Code)
	}
}

func TestRevokeReaderWithoutPinRefusedWhenAuthIsOn(t *testing.T) {
	g := newReaderRig(t, true)
	id, bearer := g.enrolReader(t, "Phone")
	t.Setenv("UT_AUTH", "")
	req := auth.WithUser(newFormReq(http.MethodPost, "/ui/tills/readers/"+id+"/revoke", url.Values{}), auth.User{ID: "m1", Role: "manager"})
	if rec := g.serve(req); rec.Code != http.StatusForbidden {
		t.Fatalf("revoke without a manager PIN = %d, want 403", rec.Code)
	}
	if rec := g.serve(readerReq(g, http.MethodPost, "/api/sync/enroll/withdraw", bearer, true)); rec.Code == http.StatusUnauthorized {
		t.Fatal("a refused revoke must not have revoked the reader")
	}
}

func TestRenameReaderRefusesDuplicateName(t *testing.T) {
	g := newReaderRig(t, true)
	a, _ := g.enrolReader(t, "Phone A")
	_, _ = g.enrolReader(t, "Phone B")
	rec := g.serve(newFormReq(http.MethodPost, "/ui/tills/readers/"+a+"/rename", url.Values{"name": {"Phone B"}}))
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate rename = %d, want 409", rec.Code)
	}
	rec = g.serve(newFormReq(http.MethodPost, "/ui/tills/readers/"+a+"/rename", url.Values{"name": {"Back phone"}}))
	if rec.Code != http.StatusOK {
		t.Fatalf("rename = %d %s", rec.Code, rec.Body)
	}
}

func TestReaderRequestsFragmentSaysCardReaderInWordsAndNoRoleChoice(t *testing.T) {
	g := newReaderRig(t, true)
	g.request(t, "Counter phone", "s1", "card_reader", true, "")
	rec := g.serve(httptest.NewRequest(http.MethodGet, "/ui/tills/reader-requests", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Counter phone") {
		t.Fatalf("reader requests = %d %q", rec.Code, body)
	}
	if strings.Contains(body, `name="role"`) {
		t.Fatal("a reader request card must offer no role choice (the role is fixed by the request)")
	}
	if !strings.Contains(body, "no shop data") {
		t.Fatal("the card must say in words that a card reader gets no shop data")
	}
	// The till request list never shows reader requests.
	till := g.serve(httptest.NewRequest(http.MethodGet, "/ui/tills/pending-pairings", nil))
	if strings.Contains(till.Body.String(), "Counter phone") {
		t.Fatal("a reader request appeared in the till approval list")
	}
}

func TestReaderRequestsSectionNeedsTLS(t *testing.T) {
	g := newReaderRig(t, true)
	restore := lantls.SetServing(nil)
	t.Cleanup(restore)
	rec := g.serve(httptest.NewRequest(http.MethodGet, "/ui/tills/reader-requests", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Turn on HTTPS first") {
		t.Fatalf("plain-HTTP main must say readers need a secure connection: %d %q", rec.Code, rec.Body)
	}
}

// --- bearer failure limiter ---

func TestBearerFailureLimiterLocksOutPerIP(t *testing.T) {
	g := newReaderRig(t, true)
	tills := data.NewTillsRepo(g.d.Db)
	if _, err := tills.InsertTill(t.Context(), "Till 2", hashBearer("good-till-bearer")); err != nil {
		t.Fatal(err)
	}
	_, readerBearer := g.enrolReader(t, "Phone")
	now := time.Now()
	syncBearerFailures = newBearerFailureLimiter(5, time.Minute, time.Minute, func() time.Time { return now })
	try := func(bearer, remote string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/sync/ping", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.RemoteAddr = remote
		return g.serve(req)
	}
	// Mixed failures: till-table and reader-table lookups share one counter.
	for i := 0; i < 3; i++ {
		try("wrong", "198.51.100.7:1")
	}
	for i := 0; i < 2; i++ {
		rq := readerReq(g, http.MethodPost, "/api/sync/enroll/withdraw", "wrong-reader", true)
		rq.RemoteAddr = "198.51.100.7:2"
		g.serve(rq)
	}
	rec := try("good-till-bearer", "198.51.100.7:3")
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("locked-out IP with a valid bearer = %d Retry-After %q, want 401 + Retry-After", rec.Code, rec.Header().Get("Retry-After"))
	}
	// Another IP is untouched.
	if rec := try("good-till-bearer", "198.51.100.8:1"); rec.Code != http.StatusOK {
		t.Fatalf("other IP = %d, want 200", rec.Code)
	}
	// The lock lifts after its duration.
	now = now.Add(2 * time.Minute)
	if rec := try("good-till-bearer", "198.51.100.7:4"); rec.Code != http.StatusOK {
		t.Fatalf("after the lockout = %d, want 200", rec.Code)
	}
	_ = readerBearer
}

func TestBearerFailureLimiterIgnoresLoopbackAndEmptyBearer(t *testing.T) {
	now := time.Now()
	l := newBearerFailureLimiter(2, time.Minute, time.Minute, func() time.Time { return now })
	for i := 0; i < 10; i++ {
		l.fail("127.0.0.1")
		l.fail("::1")
		l.fail("")
	}
	if l.locked("127.0.0.1") || l.locked("::1") {
		t.Fatal("loopback must never be locked out (the main till calls itself)")
	}
}

// resetBearerFailures gives the test a fresh per-IP failure limiter (a
// process global) and restores a fresh one afterwards. t may be nil.
func resetBearerFailures(t testing.TB) {
	if t != nil {
		t.Cleanup(func() { syncBearerFailures = newDefaultBearerFailureLimiter() })
	}
	syncBearerFailures = newDefaultBearerFailureLimiter()
}

func newFormReq(method, path string, form url.Values) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func TestPhoneReaderPairingShipsDark(t *testing.T) {
	g := newReaderRig(t, true)
	if err := g.d.Settings.Set(t.Context(), settingPhoneReaderPairing, ""); err != nil {
		t.Fatal(err)
	}
	if rec := g.request(t, "Phone", "s1", "card_reader", true, ""); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "reader_pairing_off") {
		t.Fatalf("reader request with the feature off = %d %q, want 403 reader_pairing_off", rec.Code, rec.Body)
	}
	for _, p := range []string{"/ui/tills/reader-requests", "/ui/tills/readers"} {
		if rec := g.serve(httptest.NewRequest(http.MethodGet, p, nil)); rec.Code != http.StatusNoContent {
			t.Errorf("%s with the feature off = %d, want 204 (nothing shown)", p, rec.Code)
		}
	}
	// Till pairing is untouched by the flag.
	if rec := g.request(t, "Till 2", "s2", "additional", true, "192.0.2.9:1"); rec.Code != http.StatusOK {
		t.Fatalf("till pair request with the reader feature off = %d", rec.Code)
	}
}

// --- F1 (tester-batch): the phone's approval poll is not the pair request ---

// pollFrom polls from one fixed source, as a real phone does.
func (g *readerRigT) pollFrom(id, secret, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/sync/pair-requests/"+id, nil)
	req.Header.Set(fleetlink.HeaderPairRequestSecret, secret)
	req.RemoteAddr = remote
	return g.serve(g.asTLS(req))
}

func TestPhoneApprovalPollEvery2sForTenMinutesIsNeverRefused(t *testing.T) {
	g := newReaderRig(t, true)
	now := time.Now()
	pairPollNow = func() time.Time { return now }
	t.Cleanup(func() { pairPollNow = testPollClock })
	const phone = "192.0.2.60:7000"
	secret := "s-poll"
	id := idOf(t, g.request(t, "Phone", secret, "card_reader", true, phone))
	for i := 0; i < 300; i++ { // 10 minutes at the phone's 2 s cadence
		if i == 299 {
			if rec := g.approve(id, ""); rec.Code != http.StatusOK {
				t.Fatalf("approve = %d", rec.Code)
			}
		}
		rec := g.pollFrom(id, secret, phone)
		want := http.StatusNotFound
		if i == 299 {
			want = http.StatusOK
		}
		if rec.Code != want {
			t.Fatalf("poll %d (t+%ds) = %d, want %d: the phone's poll must never be limited at its own cadence", i, 2*i, rec.Code, want)
		}
		now = now.Add(2 * time.Second)
	}
	// The polls did not eat the pair-request budget of this source.
	if rec := g.request(t, "Phone 2", "s-poll-2", "card_reader", true, phone); rec.Code != http.StatusOK {
		t.Fatalf("pair request after 300 polls = %d, want 200", rec.Code)
	}
}

func TestPollFasterThanTheGapIs429WithRetryAfterAndRequestsStayStrict(t *testing.T) {
	g := newReaderRig(t, true)
	now := time.Now()
	pairPollNow = func() time.Time { return now }
	t.Cleanup(func() { pairPollNow = testPollClock })
	const phone = "192.0.2.61:7000"
	id := idOf(t, g.request(t, "Phone", "s-fast", "card_reader", true, phone))
	if rec := g.pollFrom(id, "s-fast", phone); rec.Code != http.StatusNotFound {
		t.Fatalf("first poll = %d", rec.Code)
	}
	rec := g.pollFrom(id, "s-fast", phone)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "2" {
		t.Fatalf("immediate re-poll = %d Retry-After %q, want 429 + 2", rec.Code, rec.Header().Get("Retry-After"))
	}
	now = now.Add(2 * time.Second)
	if rec := g.pollFrom(id, "s-fast", phone); rec.Code != http.StatusNotFound {
		t.Fatalf("poll after the gap = %d", rec.Code)
	}
	// The request side stays strict: the 6th request in a minute from one source is refused.
	got429 := false
	for i := 0; i < 8; i++ {
		if g.request(t, "P", fmt.Sprintf("sx%d", i), "card_reader", true, phone).Code == http.StatusTooManyRequests {
			got429 = true
		}
	}
	if !got429 {
		t.Fatal("pair requests lost their strict limit")
	}
}

func TestPollPerSourceCapStopsIdEnumerationAcrossIds(t *testing.T) {
	g := newReaderRig(t, true)
	now := time.Now()
	pairPollNow = func() time.Time { return now }
	t.Cleanup(func() { pairPollNow = testPollClock })
	refused := 0
	for i := 0; i < pairPollSourceMax+30; i++ {
		if g.pollFrom(fmt.Sprintf("guess-%d", i), "x", "192.0.2.62:1").Code == http.StatusTooManyRequests {
			refused++
		}
	}
	if refused == 0 {
		t.Fatal("one source could poll unboundedly many distinct ids in a minute")
	}
}

// F3 (tester-batch): two simultaneous polls of one approved pairing make ONE
// reader and ONE bearer; the loser gets the same 404 as any replay.
func TestConcurrentDoublePollOfAnApprovedPairingMakesOneReader(t *testing.T) {
	g := newReaderRig(t, true)
	secret := "s-race"
	id := idOf(t, g.request(t, "Race phone", secret, "card_reader", true, ""))
	if rec := g.approve(id, ""); rec.Code != http.StatusOK {
		t.Fatalf("approve = %d", rec.Code)
	}
	const n = 8
	codes := make([]int, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			codes[i] = g.pollFrom(id, secret, fmt.Sprintf("192.0.2.%d:7000", 150+i)).Code
		}(i)
	}
	close(start)
	wg.Wait()
	ok := 0
	for _, c := range codes {
		if c == http.StatusOK {
			ok++
		}
	}
	var readers, creds int
	if err := g.d.Db.QueryRow(`SELECT COUNT(*) FROM card_readers`).Scan(&readers); err != nil {
		t.Fatal(err)
	}
	if err := g.d.Db.QueryRow(`SELECT COUNT(*) FROM card_reader_credentials`).Scan(&creds); err != nil {
		t.Fatal(err)
	}
	if ok != 1 || readers != 1 || creds != 1 {
		t.Fatalf("codes %v: %d handovers, %d readers, %d credentials; want exactly 1 of each", codes, ok, readers, creds)
	}
}

// --- review R-1/A-6: the phone wipes its credentials on 401, so 401 means
// exactly "this bearer is invalid or revoked" and nothing else ---

var readerBearerRoutes = []struct{ method, path string }{
	{http.MethodGet, "/api/sync/link"},
	{http.MethodPost, "/api/sync/bearer/rotate"},
	{http.MethodPost, "/api/sync/enroll/withdraw"},
}

func TestReaderLockoutIs429WithRetryAfterNever401(t *testing.T) {
	g := newReaderRig(t, true)
	_, bearer := g.enrolReader(t, "Phone")
	now := time.Now()
	syncBearerFailures = newBearerFailureLimiter(3, time.Minute, time.Minute, func() time.Time { return now })
	for i := 0; i < 3; i++ {
		rq := readerReq(g, http.MethodGet, "/api/sync/link", "wrong", true)
		rq.RemoteAddr = "198.51.100.20:1"
		if rec := g.serve(rq); rec.Code != http.StatusUnauthorized {
			t.Fatalf("a wrong bearer = %d, want 401", rec.Code)
		}
	}
	for _, rt := range readerBearerRoutes {
		rq := readerReq(g, rt.method, rt.path, bearer, true) // the VALID bearer
		rq.RemoteAddr = "198.51.100.20:2"
		rec := g.serve(rq)
		if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
			t.Errorf("%s %s from a locked-out source = %d Retry-After %q, want 429 + Retry-After (a 401 makes the phone forget its pairing)", rt.method, rt.path, rec.Code, rec.Header().Get("Retry-After"))
		}
	}
}

func TestReaderLookupErrorIs503AndIsNotCountedAsABearerFailure(t *testing.T) {
	g := newReaderRig(t, true)
	_, bearer := g.enrolReader(t, "Phone")
	if _, err := g.d.Db.Exec(`ALTER TABLE card_reader_credentials RENAME TO card_reader_credentials_gone`); err != nil {
		t.Fatal(err)
	}
	for _, rt := range readerBearerRoutes {
		for i := 0; i < 25; i++ { // more than the lockout cap
			rq := readerReq(g, rt.method, rt.path, bearer, true)
			rq.RemoteAddr = "198.51.100.21:1"
			rec := g.serve(rq)
			if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
				t.Fatalf("%s %s with the credential lookup failing = %d Retry-After %q, want 503 + Retry-After", rt.method, rt.path, rec.Code, rec.Header().Get("Retry-After"))
			}
		}
	}
	if d := syncBearerFailures.retryAfter("198.51.100.21"); d != 0 {
		t.Fatalf("a database error locked the source out for %v; only a wrong bearer counts", d)
	}
}

func TestOnlyAnInvalidOrRevokedReaderBearerIs401(t *testing.T) {
	g := newReaderRig(t, true)
	id, bearer := g.enrolReader(t, "Phone")
	for _, rt := range readerBearerRoutes {
		if rec := g.serve(readerReq(g, rt.method, rt.path, "never-issued", true)); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s unknown bearer = %d, want 401", rt.method, rt.path, rec.Code)
		}
	}
	if _, err := g.d.Db.Exec(`UPDATE card_reader_credentials SET revoked_at = datetime('now') WHERE reader_id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if rec := g.serve(readerReq(g, http.MethodGet, "/api/sync/link", bearer, true)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked bearer = %d, want 401", rec.Code)
	}
}

// F5/F6 (tester-batch): the reader sections are their own cards, with no
// bullet, a visible label on each field, Deny and Approve in their own row,
// and 56 px targets.
func TestReaderSectionsLayout(t *testing.T) {
	g := newReaderRig(t, true)
	g.request(t, "Counter phone", "s-lay", "card_reader", true, "")
	g.enrolReader(t, "Second phone")
	req := g.serve(httptest.NewRequest(http.MethodGet, "/ui/tills/reader-requests", nil)).Body.String()
	lst := g.serve(httptest.NewRequest(http.MethodGet, "/ui/tills/readers", nil)).Body.String()
	for name, body := range map[string]string{"requests": req, "readers": lst} {
		if !strings.Contains(body, `class="card"`) {
			t.Errorf("%s fragment is not its own card", name)
		}
		if strings.Contains(body, `placeholder=`) {
			t.Errorf("%s fragment labels a field by placeholder only", name)
		}
		if strings.Count(body, "<label") == 0 {
			t.Errorf("%s fragment has no visible field label", name)
		}
	}
	if !strings.Contains(req, `class="reader-actions"`) {
		t.Error("Deny and Approve must sit in their own spaced row")
	}
	if strings.Contains(lst, `<li class="card"`) {
		t.Error("reader rows must not be nested cards")
	}
	css, err := os.ReadFile(repoFile("web", "public", "app.css"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{".reader-list { list-style: none", "#card-readers-list input, #reader-requests input { min-block-size: 56px", "#card-readers-list .btn, #reader-requests .btn { min-block-size: 56px", ".reader-actions { display: flex; flex-wrap: wrap; gap: .75rem"} {
		if !strings.Contains(string(css), want) {
			t.Errorf("app.css lacks %q", want)
		}
	}
	page, err := os.ReadFile(repoFile("web", "ui", "pages", "tills.html"))
	if err != nil {
		t.Fatal(err)
	}
	pc := string(page)
	if i, j := strings.Index(pc, `id="tills-pairing-card"`), strings.Index(pc, "/ui/tills/reader-requests"); j < i || strings.Index(pc[i:j], "\n  </div>") < 0 {
		t.Error("the reader sections must sit after, not inside, the pairing card")
	}
}

// repoFile is a path under the repository root, independent of the test's cwd.
func repoFile(parts ...string) string {
	_, here, _, _ := runtime.Caller(0)
	return filepath.Join(append([]string{filepath.Dir(here), "..", ".."}, parts...)...)
}

// --- review R-2/A-7 (ADR-0198 §6.2 "as for tills"): a reader survives a main
// TLS key change by re-proof, without clearing app data ---

func (g *readerRigT) readerProof(t *testing.T, readerID, nonce string, viaTLS bool) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"till_id":"` + readerID + `","nonce":"` + nonce + `"}`
	req := httptest.NewRequest(http.MethodPost, discovery.ProofPath, strings.NewReader(body))
	req.RemoteAddr = "192.0.2.50:5555"
	req.Host = proofLocalAddr
	la, err := net.ResolveTCPAddr("tcp", proofLocalAddr)
	if err != nil {
		t.Fatal(err)
	}
	req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, la))
	if viaTLS {
		req = g.asTLS(req)
	}
	return g.serve(req)
}

func TestReaderSurvivesAMainKeyChangeByReProof(t *testing.T) {
	g := newReaderRig(t, true)
	readerID, bearer := g.enrolReader(t, "Phone")
	pinA := g.cert.Pin()
	// The main's LAN key changes (rotation, reinstall of the same data).
	newCert, err := lantls.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lantls.SetServing(newCert))
	g.cert = newCert
	pinB := newCert.Pin()
	if pinA == pinB {
		t.Fatal("test needs two keys")
	}
	creds := data.NewCardReaderCredentialRepo(g.d.Db)
	if rec := g.serve(readerReq(g, http.MethodGet, "/api/sync/link", bearer, true)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a reader recorded under the old pin must not pass before it re-proved: %d", rec.Code)
	}
	// The phone's handshake with its old pin failed; it re-finds the main and asks for proof.
	rec := g.readerProof(t, readerID, testNonce, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("reader proof = %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Data discovery.ProofResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	// The phone verifies with the hash of ITS bearer, the id it asked for, the host it dialled and the pin it saw.
	want := discovery.PrimaryProof(discovery.HashBearer(bearer), out.Data.PrimaryTillID, readerID, testNonce, proofLocalAddr, pinB)
	if out.Data.Proof != want {
		t.Fatalf("proof %q does not verify with the reader's bearer and the new pin (want %q)", out.Data.Proof, want)
	}
	if bad := discovery.PrimaryProof(discovery.HashBearer("another-bearer"), out.Data.PrimaryTillID, readerID, testNonce, proofLocalAddr, pinB); out.Data.Proof == bad {
		t.Fatal("proof must be keyed by the reader's own bearer")
	}
	// Now the phone dials with the new pin and its bearer: accepted, and the new pin is recorded.
	if rec := g.serve(readerReq(g, http.MethodGet, "/api/sync/link", bearer, true)); rec.Code == http.StatusUnauthorized {
		t.Fatalf("a re-proved reader on the new pin = 401")
	}
	c, ok, err := creds.ByBearerHashAt(t.Context(), hashBearer(bearer), time.Now())
	if err != nil || !ok {
		t.Fatalf("credential lookup: %v %v", ok, err)
	}
	if c.PinSPKI != pinB || c.NextPin != "" {
		t.Fatalf("after the first authenticated link pin=%q next=%q, want pin %q and no next pin", c.PinSPKI, c.NextPin, pinB)
	}
}

func TestReaderProofRefusals(t *testing.T) {
	g := newReaderRig(t, true)
	readerID, bearer := g.enrolReader(t, "Phone")
	if rec := g.readerProof(t, readerID, testNonce, false); rec.Code != http.StatusNotFound {
		t.Errorf("reader proof over plain HTTP = %d, want 404 (a reader never talks plain HTTP)", rec.Code)
	}
	if rec := g.readerProof(t, "rd_unknown", testNonce, true); rec.Code != http.StatusNotFound {
		t.Errorf("proof for an unknown reader = %d, want 404", rec.Code)
	}
	if rec := g.readerProof(t, readerID, "short", true); rec.Code != http.StatusBadRequest {
		t.Errorf("proof with a bad nonce = %d, want 400", rec.Code)
	}
	// A proof asked by anyone only ever records the main's own served pin.
	other, _ := lantls.LoadOrCreate(t.TempDir())
	req := httptest.NewRequest(http.MethodPost, discovery.ProofPath, strings.NewReader(`{"till_id":"`+readerID+`","nonce":"`+testNonce+`"}`))
	req.Host = proofLocalAddr
	la, _ := net.ResolveTCPAddr("tcp", proofLocalAddr)
	req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, la))
	req.TLS = &tls.ConnectionState{}
	req = req.WithContext(lantls.ConnContext(other)(req.Context(), &tls.Conn{}))
	g.serve(req)
	c, _, _ := data.NewCardReaderCredentialRepo(g.d.Db).ByBearerHashAt(t.Context(), hashBearer(bearer), time.Now())
	if c.NextPin != other.Pin() {
		t.Fatalf("next pin = %q, want the pin the main served (%q) and nothing else", c.NextPin, other.Pin())
	}
	// A revoked reader gets no proof.
	if err := data.NewCardReaderCredentialRepo(g.d.Db).Revoke(t.Context(), readerID); err != nil {
		t.Fatal(err)
	}
	if rec := g.readerProof(t, readerID, testNonce, true); rec.Code != http.StatusNotFound {
		t.Errorf("proof for a revoked reader = %d, want 404", rec.Code)
	}
}

// The Kotlin side reads the same fixture (internal/fleetlink/testdata/reader_proof.json).
func TestReaderProofFixtureMatchesTheGoProof(t *testing.T) {
	b, err := os.ReadFile(repoFile("internal", "fleetlink", "testdata", "reader_proof.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Bearer   string `json:"bearer"`
		HostPort string `json:"host_port"`
		PinSeen  string `json:"pin_seen"`
		Request  struct {
			TillID string `json:"till_id"`
			Nonce  string `json:"nonce"`
		} `json:"request"`
		Response struct {
			Data struct {
				PrimaryTillID string `json:"primary_till_id"`
				Proof         string `json:"proof"`
			} `json:"data"`
		} `json:"response"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	got := discovery.PrimaryProof(discovery.HashBearer(f.Bearer), f.Response.Data.PrimaryTillID, f.Request.TillID, f.Request.Nonce, f.HostPort, f.PinSeen)
	if got != f.Response.Data.Proof {
		t.Fatalf("fixture proof %q, Go computes %q", f.Response.Data.Proof, got)
	}
}

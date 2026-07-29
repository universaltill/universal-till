package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

// --- pure logic ---

func TestEnrolTokens_IssueConsumeIsOneTime(t *testing.T) {
	e := &enrolTokens{tokens: map[string]time.Time{}}
	tok := e.issue()
	if tok == "" {
		t.Fatal("expected a non-empty token")
	}
	if !e.consume(tok) {
		t.Fatal("expected the freshly issued token to be valid")
	}
	// One-time: consuming again must fail.
	if e.consume(tok) {
		t.Fatal("expected a second consume of the same token to fail")
	}
	if e.consume("never-issued") {
		t.Fatal("expected an unknown token to fail")
	}
}

func TestEnrolTokens_ExpiredTokenRejected(t *testing.T) {
	e := &enrolTokens{tokens: map[string]time.Time{}}
	tok := "expired-token"
	e.tokens[tok] = time.Now().Add(-time.Second) // already expired
	if e.consume(tok) {
		t.Fatal("expected an expired token to be rejected")
	}
}

func TestWithinLast(t *testing.T) {
	fresh := time.Now().Add(-30 * time.Second).UTC().Format(time.RFC3339)
	stale := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	if !withinLast(fresh, 90*time.Second) {
		t.Fatal("expected a 30s-old timestamp to be within a 90s window")
	}
	if withinLast(stale, 90*time.Second) {
		t.Fatal("expected a 5-minute-old timestamp to be outside a 90s window")
	}
	if withinLast("", 90*time.Second) {
		t.Fatal("expected an empty/unparseable timestamp to report not-fresh, not panic")
	}
	if withinLast("not-a-timestamp", 90*time.Second) {
		t.Fatal("expected an unparseable timestamp to report not-fresh")
	}
}

// --- HTTP-level: registerSyncAPI ---

func newSyncAPITestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	cfg := &config.Config{
		Theme:   "default",
		Locales: config.Locales{Currency: "GBP", TaxRate: 20},
		Marketplace: config.MarketplaceConfig{
			EndpointURL: "http://localhost:8081",
		},
	}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(db),
	}
	mux := http.NewServeMux()
	registerSyncAPI(mux, dp)
	return mux, dp
}

// isManagerOrAuthOff (used by several of these handlers) checks the
// session role or UT_AUTH=off; set UT_AUTH=off for these tests so the
// manager-gated endpoints are reachable without standing up a full
// session, matching how the rest of this package's HTTP tests work.
func TestSyncEnrollTokenAndEnroll_FullPairingFlow(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newSyncAPITestDeps(t)

	// Issue an enrolment token (manager action on the primary).
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/enroll-token", strings.NewReader("url=http://192.168.1.10:8080"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 issuing a token, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// The QR payload (json with token+url) is embedded as plain text too.
	start := strings.Index(body, `{"token"`)
	if start == -1 {
		start = strings.Index(body, `{"url"`)
	}
	if start == -1 {
		t.Fatalf("expected the enrolment payload embedded in the response, got %s", body)
	}
	end := strings.Index(body[start:], "</code>")
	if end == -1 {
		t.Fatalf("expected a closing </code> after the payload, got %s", body)
	}
	payload := body[start : start+end]
	var parsed struct{ URL, Token string }
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		t.Fatalf("expected valid JSON payload, got %q: %v", payload, err)
	}
	if parsed.Token == "" || parsed.URL != "http://192.168.1.10:8080" {
		t.Fatalf("unexpected payload: %+v", parsed)
	}

	// Consume the token to enrol a new replica.
	enrollBody, _ := json.Marshal(map[string]string{"token": parsed.Token, "name": "Till 2"})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/sync/enroll", strings.NewReader(string(enrollBody)))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 enrolling, got %d: %s", rec.Code, rec.Body.String())
	}
	var enrollResp struct {
		Data struct {
			TillID string `json:"till_id"`
			Bearer string `json:"bearer"`
			TillNo int    `json:"till_no"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &enrollResp); err != nil {
		t.Fatal(err)
	}
	if enrollResp.Data.TillID == "" || enrollResp.Data.Bearer == "" {
		t.Fatalf("expected a till id and bearer, got %+v", enrollResp.Data)
	}
	if enrollResp.Data.TillNo != 2 {
		t.Fatalf("expected the first enrolled replica to be till_no 2 (primary is 1), got %d", enrollResp.Data.TillNo)
	}

	// The SAME token cannot be reused (one-time).
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/sync/enroll", strings.NewReader(string(enrollBody)))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 reusing an already-consumed token, got %d: %s", rec.Code, rec.Body.String())
	}

	// The new bearer authenticates /api/sync/ping.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/sync/ping", nil)
	req.Header.Set("Authorization", "Bearer "+enrollResp.Data.Bearer)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 pinging with the new bearer, got %d: %s", rec.Code, rec.Body.String())
	}

	// A garbage bearer is rejected.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/sync/ping", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with a wrong bearer, got %d", rec.Code)
	}

	// Revoking removes it — the same bearer stops working.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/sync/tills/"+enrollResp.Data.TillID+"/revoke", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 revoking, got %d: %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/sync/ping", nil)
	req.Header.Set("Authorization", "Bearer "+enrollResp.Data.Bearer)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 pinging with a revoked bearer, got %d", rec.Code)
	}
}

func TestSyncEnroll_RejectsInvalidOrExpiredToken(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newSyncAPITestDeps(t)

	body, _ := json.Marshal(map[string]string{"token": "never-issued", "name": "Till X"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/enroll", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for an unknown token, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSyncEnrollToken_RequiresManager(t *testing.T) {
	// UT_AUTH intentionally left unset/on: getSessionUserID/role checks
	// apply, and no session is present, so this must be forbidden.
	mux, _ := newSyncAPITestDeps(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/enroll-token", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager session, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSyncPromote_RequiresConfirmationAndActualReplicaState(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newSyncAPITestDeps(t)

	// Missing/wrong confirmation text.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/promote", strings.NewReader("confirm=nope"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without the exact confirmation phrase, got %d: %s", rec.Code, rec.Body.String())
	}

	// Correct confirmation, but this till isn't a replica (no sync.primary_url
	// set) — promoting a primary that's already a primary is nonsensical.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/sync/promote", strings.NewReader("confirm=PROMOTE"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 promoting a till that isn't a replica, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSyncSnapshot_RequiresAuth(t *testing.T) {
	mux, _ := newSyncAPITestDeps(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sync/snapshot", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without a bearer, got %d", rec.Code)
	}
}

func TestSyncJoin_RejectsGarbageCode(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newSyncAPITestDeps(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/join", strings.NewReader("code=not-json&name=Till+2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 for an unparseable join code, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- joinPrimary: the replica-side "join a shop via QR" flow ---

// newSyncDepsWithPath builds a Deps backed by a real on-disk SQLite file
// (so db.Snapshot's VACUUM INTO and the staged restore/identity files have a
// real DBPath to work against) using the simplified seedForPages schema. It
// returns the Deps and the DB path so tests can inspect the staged
// restore-pending.db / replica-identity.json that a join writes.
func newSyncDepsWithPath(t *testing.T, name string) (*common.Deps, string) {
	t.Helper()
	chdirRoot(t)
	path := filepath.Join(t.TempDir(), name)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		t.Fatalf("busy_timeout: %v", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("foreign_keys: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	cfg := &config.Config{
		Theme:   "default",
		DBPath:  path,
		Locales: config.Locales{Currency: "GBP", TaxRate: 20},
		Marketplace: config.MarketplaceConfig{
			EndpointURL: "http://localhost:8081",
		},
	}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(db),
	}
	return dp, path
}

// issueEnrolCode drives the primary's POST /api/sync/enroll-token and pulls
// the embedded QR payload back out — this is exactly the JSON blob a replica
// would scan/paste as its join code, and it carries a live one-time token in
// the primary's in-memory store.
func issueEnrolCode(t *testing.T, mux http.Handler, primaryURL string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/enroll-token",
		strings.NewReader("url="+primaryURL))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("issue enrol token: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	start := strings.Index(body, `{"url"`)
	if start == -1 {
		start = strings.Index(body, `{"token"`)
	}
	end := strings.Index(body[start:], "</code>")
	if start == -1 || end == -1 {
		t.Fatalf("could not find the enrol payload in %q", body)
	}
	return body[start : start+end]
}

func TestSyncJoin_FullFlow_StagesRestoreAndIdentity(t *testing.T) {
	t.Setenv("UT_AUTH", "off")

	// Primary: a real till serving the enrol + snapshot surface over HTTP.
	primary, _ := newSyncDepsWithPath(t, "primary.db")
	if err := primary.Settings.Set(t.Context(), "store.name", "Corner Shop"); err != nil {
		t.Fatalf("set store name: %v", err)
	}
	pmux := http.NewServeMux()
	registerSyncAPI(pmux, primary)
	srv := httptest.NewServer(pmux)
	t.Cleanup(srv.Close)

	// The join code the manager would paste on the replica: a live one-time
	// token bound to THIS primary's URL.
	code := issueEnrolCode(t, pmux, srv.URL)

	// Replica: independent DB + DBPath; drive its POST /api/sync/join.
	replica, replicaPath := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerSyncAPI(rmux, replica)

	rec := httptest.NewRecorder()
	form := "code=" + url.QueryEscape(code) + "&name=Till+2"
	req := httptest.NewRequest(http.MethodPost, "/api/sync/join", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rmux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on a successful join, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Corner Shop") {
		t.Fatalf("expected the primary's shop name echoed back on join, got %q", rec.Body.String())
	}

	// The snapshot must have been downloaded and staged as the pending
	// restore that applies on the next restart.
	if !appdb.PendingRestore(replicaPath) {
		t.Fatalf("expected a staged restore-pending.db after a successful join")
	}
	// And the replica identity (its own bearer/prefix/name) staged alongside.
	idRaw, err := os.ReadFile(appdb.ReplicaIdentityPath(replicaPath))
	if err != nil {
		t.Fatalf("expected a staged replica-identity.json: %v", err)
	}
	var id appdb.ReplicaIdentity
	if err := json.Unmarshal(idRaw, &id); err != nil {
		t.Fatalf("parse staged identity: %v", err)
	}
	if id.PrimaryURL != srv.URL {
		t.Fatalf("expected staged primary_url %q, got %q", srv.URL, id.PrimaryURL)
	}
	if id.Bearer == "" || id.TillID == "" {
		t.Fatalf("expected a staged bearer + till id, got %+v", id)
	}
	if id.TillName != "Till 2" {
		t.Fatalf("expected the joining till's name staged, got %q", id.TillName)
	}
	// Primary is till 1; the first replica numbers from 2 (receipt prefixes).
	if id.ReceiptPrefix != "T2-" {
		t.Fatalf("expected receipt prefix T2- for the first replica, got %q", id.ReceiptPrefix)
	}

	// The join was audited on the replica.
	var n int
	if err := replica.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'joined_primary'`).Scan(&n); err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected one joined_primary audit row, got %d", n)
	}
}

func TestSyncJoin_PrimaryUnreachable(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	replica, replicaPath := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerSyncAPI(rmux, replica)

	// A well-formed code pointing at a port nothing is listening on.
	code := `{"url":"http://127.0.0.1:1","token":"whatever"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/join",
		strings.NewReader("code="+url.QueryEscape(code)+"&name=Till+2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rmux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 when the primary is unreachable, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "cannot reach") {
		t.Fatalf("expected a 'cannot reach the primary' message, got %q", rec.Body.String())
	}
	if appdb.PendingRestore(replicaPath) {
		t.Fatalf("a failed join must not stage a restore")
	}
}

func TestSyncJoin_PrimaryRejectsEnrolment(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	primary, _ := newSyncDepsWithPath(t, "primary.db")
	pmux := http.NewServeMux()
	registerSyncAPI(pmux, primary)
	srv := httptest.NewServer(pmux)
	t.Cleanup(srv.Close)

	replica, replicaPath := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerSyncAPI(rmux, replica)

	// A code with a token the primary never issued: enrol returns 403.
	code := `{"url":"` + srv.URL + `","token":"never-issued-token"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/join",
		strings.NewReader("code="+url.QueryEscape(code)+"&name=Till+2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rmux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 when the primary rejects the token, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "refused the enrolment") {
		t.Fatalf("expected an enrolment-refused message, got %q", rec.Body.String())
	}
	if appdb.PendingRestore(replicaPath) {
		t.Fatalf("a refused enrolment must not stage a restore")
	}
}

func TestSyncJoin_SnapshotDownloadFails(t *testing.T) {
	t.Setenv("UT_AUTH", "off")

	// A stub primary that enrols fine but 500s on the snapshot fetch — the
	// replica must surface the failure and stage NOTHING (offline-first: no
	// half-applied restore).
	stub := http.NewServeMux()
	stub.HandleFunc("POST /api/sync/enroll", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"till_id":   "till-stub",
				"bearer":    "stub-bearer",
				"shop_name": "Stub Shop",
				"till_no":   2,
			},
			"error": nil,
		})
	})
	stub.HandleFunc("GET /api/sync/snapshot", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "snapshot boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)

	replica, replicaPath := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerSyncAPI(rmux, replica)

	code := `{"url":"` + srv.URL + `","token":"tok"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/join",
		strings.NewReader("code="+url.QueryEscape(code)+"&name=Till+2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rmux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 when the snapshot download fails, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "snapshot download failed") {
		t.Fatalf("expected a snapshot-download-failed message, got %q", rec.Body.String())
	}
	if appdb.PendingRestore(replicaPath) {
		t.Fatalf("a failed snapshot download must not leave a staged restore")
	}
	if _, err := os.Stat(appdb.ReplicaIdentityPath(replicaPath)); err == nil {
		t.Fatalf("a failed snapshot download must not stage a replica identity")
	}
}

func TestTillsPage_RequiresManager(t *testing.T) {
	mux, _ := newSyncAPITestDeps(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tills", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected a redirect for a non-manager, got %d", rec.Code)
	}
}

func TestTillsPage_ListsEnrolledTills(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	ctx := context.Background()
	tillsRepo := data.NewTillsRepo(dp.Db)
	if _, err := tillsRepo.InsertTill(ctx, "Front Till", hashBearer("tok-abc")); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tills", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Front Till") {
		t.Fatalf("expected the enrolled till listed, got body without it")
	}
}

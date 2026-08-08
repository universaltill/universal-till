package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
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
	"github.com/universaltill/universal-till/internal/httpx"
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
	// The manual "code" in the <code> element must be the OPAQUE code, not
	// raw JSON leaking {"url":…,"token":…} to whoever reads the screen (#7).
	marker := `<code style="user-select:all">`
	start := strings.Index(body, marker)
	if start == -1 {
		t.Fatalf("expected a <code> element with the enrolment code, got %s", body)
	}
	start += len(marker)
	end := strings.Index(body[start:], "</code>")
	if end == -1 {
		t.Fatalf("expected a closing </code>, got %s", body)
	}
	code := body[start : start+end]
	for _, bad := range []string{"{", "\"token\"", "\"url\""} {
		if strings.Contains(code, bad) {
			t.Fatalf("rendered code still leaks %q: %s", bad, code)
		}
	}
	// It must decode back to the primary URL + a token.
	url, token, err := decodeEnrollCode(code)
	if err != nil || url != "http://192.168.1.10:8080" || token == "" {
		t.Fatalf("rendered code did not decode: url=%q token=%q err=%v", url, token, err)
	}

	// Consume the token to enrol a new replica.
	enrollBody, _ := json.Marshal(map[string]string{"token": token, "name": "Till 2"})
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

// ut-docs#426: the snapshot handed to a joining replica is a full-DB copy,
// and its tills table used to carry EVERY enrolled till's real bearer_hash
// (the sync-auth secret) — the joining till held every sibling's secret from
// the moment of join. The served snapshot must have bearer_hash NULLed for
// every row, while the primary's live DB keeps the real values.
func TestSyncSnapshot_RedactsOtherTillsBearerHash(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	primary, primaryPath := newSyncDepsWithPath(t, "primary.db")
	pmux := http.NewServeMux()
	registerSyncAPI(pmux, primary)

	// Two enrolled tills with real (non-NULL) bearer hashes: the caller
	// itself plus a sibling whose secret is the one at stake.
	ctx := context.Background()
	tillsRepo := data.NewTillsRepo(primary.Db)
	callerBearer := "tok-caller"
	if _, err := tillsRepo.InsertTill(ctx, "Caller Till", hashBearer(callerBearer)); err != nil {
		t.Fatal(err)
	}
	if _, err := tillsRepo.InsertTill(ctx, "Sibling Till", hashBearer("tok-sibling")); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sync/snapshot", nil)
	req.Header.Set("Authorization", "Bearer "+callerBearer)
	pmux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 fetching the snapshot, got %d: %s", rec.Code, rec.Body.String())
	}

	// What the replica receives, opened as SQLite.
	served := filepath.Join(t.TempDir(), "served-snapshot.db")
	if err := os.WriteFile(served, rec.Body.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	sdb, err := sql.Open("sqlite", served)
	if err != nil {
		t.Fatalf("open served snapshot: %v", err)
	}
	defer sdb.Close()
	var total, withHash int
	if err := sdb.QueryRow(`SELECT COUNT(*) FROM tills`).Scan(&total); err != nil {
		t.Fatalf("count tills in served snapshot: %v", err)
	}
	if err := sdb.QueryRow(`SELECT COUNT(*) FROM tills WHERE bearer_hash IS NOT NULL`).Scan(&withHash); err != nil {
		t.Fatalf("count bearer_hash in served snapshot: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected the till roster (2 rows) in the served snapshot, got %d", total)
	}
	if withHash != 0 {
		t.Errorf("served snapshot leaks %d real bearer_hash value(s) — a joining replica would hold every sibling till's sync secret (ut-docs#426)", withHash)
	}

	// The primary's live DB must be untouched — both real hashes intact.
	var live int
	if err := primary.Db.QueryRow(`SELECT COUNT(*) FROM tills WHERE bearer_hash IS NOT NULL`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 2 {
		t.Errorf("primary's live tills table mutated: %d rows with bearer_hash, want 2", live)
	}
	_ = primaryPath
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
	marker := `<code style="user-select:all">`
	start := strings.Index(body, marker)
	if start == -1 {
		t.Fatalf("could not find the enrol code in %q", body)
	}
	start += len(marker)
	end := strings.Index(body[start:], "</code>")
	if end == -1 {
		t.Fatalf("could not find closing </code> in %q", body)
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
	// ut-docs#426: the staged DB the replica will boot from must NOT carry
	// any till's real bearer_hash — the snapshot is redacted before serving.
	stagedPath := filepath.Join(filepath.Dir(replicaPath), "restore-pending.db")
	staged, err := sql.Open("sqlite", stagedPath)
	if err != nil {
		t.Fatalf("open staged restore: %v", err)
	}
	defer staged.Close()
	var leaked int
	if err := staged.QueryRow(`SELECT COUNT(*) FROM tills WHERE bearer_hash IS NOT NULL`).Scan(&leaked); err != nil {
		t.Fatalf("count bearer_hash in staged restore: %v", err)
	}
	if leaked != 0 {
		t.Errorf("staged restore leaks %d real bearer_hash value(s) to the joining replica (ut-docs#426)", leaked)
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

// TestFriendlyJoinError_TranslatesEachKind pins the ut-docs#36 fix: every
// joinPrimary/completeJoin failure kind renders through httpx.T instead of
// leaking its Go-side locale key or English wording, and a kind carrying a
// dynamic detail (a URL, a wrapped network error) interpolates it into the
// locale string's %s placeholder — the same fmt.Sprintf(httpx.T(...), v)
// convention catalog.error.barcode_conflict already uses.
func TestFriendlyJoinError_TranslatesEachKind(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		key    string
		detail string // "" if the locale string has no placeholder
	}{
		{"bad code", &joinError{kind: joinErrBadCode}, "tills.join_error.bad_code", ""},
		{"request failed", &joinError{kind: joinErrRequestFailed, detail: "boom"}, "tills.join_error.request_failed", "boom"},
		{"unreachable", &joinError{kind: joinErrUnreachable, detail: "dial tcp: connect refused"}, "tills.join_error.unreachable", "dial tcp: connect refused"},
		{"not a till", &joinError{kind: joinErrNotATill, detail: "http://10.0.0.5:8080"}, "tills.join_error.not_a_till", "http://10.0.0.5:8080"},
		{"refused", &joinError{kind: joinErrRefused}, "tills.join_error.refused", ""},
		{"snapshot failed", &joinError{kind: joinErrSnapshotFailed, detail: "500 Internal Server Error"}, "tills.join_error.snapshot_failed", "500 Internal Server Error"},
		{"stage snapshot failed", &joinError{kind: joinErrStageSnapshotFailed, detail: "boom"}, "tills.join_error.stage_snapshot_failed", "boom"},
		{"stage identity failed", &joinError{kind: joinErrStageIdentityFailed, detail: "boom"}, "tills.join_error.stage_identity_failed", "boom"},
	}
	for _, locale := range []string{"en", "ar", "fa", "tr"} {
		for _, tt := range tests {
			t.Run(locale+"/"+tt.name, func(t *testing.T) {
				want := httpx.T(locale, tt.key)
				if tt.detail != "" {
					want = fmt.Sprintf(want, tt.detail)
				}
				if want == tt.key {
					t.Fatalf("locale key %q has no translation for %q (httpx.T fell back to the key itself)", tt.key, locale)
				}
				if got := friendlyJoinError(locale, tt.err); got != want {
					t.Errorf("friendlyJoinError(%q, %v) = %q, want %q", locale, tt.err, got, want)
				}
			})
		}
	}
}

// TestFriendlyJoinError_FallsBackForUnclassifiedErrors covers the defensive
// branch: a plain (non-*joinError) error is shown as its own Error() text
// rather than panicking or silently returning an empty string.
func TestFriendlyJoinError_FallsBackForUnclassifiedErrors(t *testing.T) {
	err := fmt.Errorf("some other failure")
	if got := friendlyJoinError("fa", err); got != "some other failure" {
		t.Errorf("expected the raw error text as a fallback, got %q", got)
	}
}

// TestSyncJoin_ErrorsAreLocalized drives the real HTTP handler end-to-end
// with a non-English locale and proves the ut-docs#36 bug is actually
// fixed: the raw English error must not leak, and the operator's own
// locale's translation must appear instead.
func TestSyncJoin_ErrorsAreLocalized(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	replica, replicaPath := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerSyncAPI(rmux, replica)

	// A well-formed code pointing at a port nothing is listening on —
	// same "unreachable" case as TestSyncJoin_PrimaryUnreachable, but
	// requested in Farsi.
	code := `{"url":"http://127.0.0.1:1","token":"whatever"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/join?lang=fa",
		strings.NewReader("code="+url.QueryEscape(code)+"&name=Till+2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rmux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 when the primary is unreachable, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "cannot reach") {
		t.Fatalf("a fa-locale till must not show the English error text, got %q", body)
	}
	wantPrefix := strings.SplitN(httpx.T("fa", "tills.join_error.unreachable"), "%s", 2)[0]
	if !strings.Contains(body, wantPrefix) {
		t.Fatalf("expected the fa translation %q to appear, got %q", wantPrefix, body)
	}
	if appdb.PendingRestore(replicaPath) {
		t.Fatalf("a failed join must not stage a restore")
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

// GET /tills shows the primary till itself in the Enrolled list (ut-docs#396)
// when this device IS the primary (SyncPrimaryURL empty) — the common single
// -till-shop case, which previously showed nothing at all in that card.
func TestTillsPage_ShowsPrimaryTillWhenThisDeviceIsThePrimary(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	if err := dp.Settings.Set(context.Background(), "till.name", "Front Counter"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tills", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Front Counter") {
		t.Fatalf("expected the primary's own name in the Enrolled list, got body without it: %s", rec.Body.String())
	}
}

// ut-docs#405: on a replica, till.name is NOT a fabrication — it's synced
// down verbatim from the primary (till.name isn't in
// PerTillSettingPrefixes), so it genuinely IS the primary's own name. The
// row must render, but tagged "(the primary)" rather than "(this till)" —
// the replica is not that till.
func TestTillsPage_ShowsPrimaryRowTaggedAsPrimaryWhenViewingFromAReplica(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	ctx := context.Background()
	if err := dp.Settings.Set(ctx, "till.name", "Front Counter"); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://192.168.1.10:8080"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tills", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Front Counter") {
		t.Fatalf("expected a replica to show the synced primary name, got body without it: %s", body)
	}
	if !strings.Contains(body, "the primary") {
		t.Fatalf("expected the primary row tagged as \"the primary\" (not \"this till\") on a replica, got: %s", body)
	}
}

// ut-docs#405: the shop's till roster now syncs to every replica
// (adminTables), so a replica's own row appears in .Tills too (the
// primary enrols it there at join time) — it must be tagged "(this
// till)" by matching sync.till_id, same idea TestTillsPage_
// ShowsPrimaryRowTaggedAsPrimaryWhenViewingFromAReplica applies to the
// primary's own row.
func TestTillsPage_TagsThisTillWithinSyncedRosterOnAReplica(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	ctx := context.Background()
	tillsRepo := data.NewTillsRepo(dp.Db)
	selfID, err := tillsRepo.InsertTill(ctx, "Back Counter", hashBearer("tok-self"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tillsRepo.InsertTill(ctx, "Till 3", hashBearer("tok-sibling")); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://192.168.1.10:8080"); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, "sync.till_id", selfID); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tills", nil)
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "Back Counter") || !strings.Contains(body, "Till 3") {
		t.Fatalf("expected both the shop's tills listed on the replica, got: %s", body)
	}
	if !strings.Contains(body, "Back Counter <span class=\"muted\">(this till)</span>") {
		t.Fatalf("expected the replica's own row tagged (this till), got: %s", body)
	}
	if strings.Contains(body, "Till 3 <span class=\"muted\">(this till)</span>") {
		t.Fatalf("a sibling till must not be tagged (this till): %s", body)
	}
}

// ut-docs#405: revoke is primary-authoritative — a replica's own local
// DELETE would look like it worked (row disappears, HX-Refresh) but
// silently reverts on the next admin-bundle pull, since the shop-wide
// roster only really changes on the primary. The button must not even
// render on a replica.
func TestTillsPage_HidesRevokeButtonOnAReplica(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	ctx := context.Background()
	tillsRepo := data.NewTillsRepo(dp.Db)
	if _, err := tillsRepo.InsertTill(ctx, "Till 3", hashBearer("tok-sibling")); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://192.168.1.10:8080"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tills", nil)
	mux.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "/revoke") {
		t.Fatalf("a replica must not offer a revoke action: %s", rec.Body.String())
	}
}

// Backend enforcement independent of the UI hiding the button above —
// never trust the template alone for a primary-authoritative write.
func TestRevokeTill_RejectedOnAReplica(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	ctx := context.Background()
	tillsRepo := data.NewTillsRepo(dp.Db)
	id, err := tillsRepo.InsertTill(ctx, "Till 3", hashBearer("tok-sibling"))
	if err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://192.168.1.10:8080"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/tills/"+id+"/revoke", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 rejecting a revoke attempted on a replica, got %d: %s", rec.Code, rec.Body.String())
	}
	list, err := tillsRepo.ListTills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("expected the till to survive a rejected revoke, got %d rows", len(list))
	}
}

// ut-docs#405 (independent review finding): enrolling is the other half of
// the revoke guard — a code minted on a replica would enrol the new till
// against this replica's own local, non-authoritative tills table instead
// of the shop's real primary, and it would silently lose access on the
// next admin-bundle pull. The QR/code card must not even render there.
func TestTillsPage_HidesAddTillCardOnAReplica(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	ctx := context.Background()
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://192.168.1.10:8080"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tills", nil)
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, "/api/sync/enroll-token") {
		t.Fatalf("a replica must not offer to enrol a new till against itself: %s", body)
	}
	// The "join another shop" / LAN-discovery cards are a different flow
	// (this device joining someone ELSE, not someone joining this device)
	// and must still render.
	if !strings.Contains(body, "/api/sync/join") {
		t.Fatalf("expected the join-an-existing-shop form to still render on a replica: %s", body)
	}
}

// Backend enforcement independent of the UI hiding the card above.
func TestEnrolTokenAndEnroll_RejectedOnAReplica(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	ctx := context.Background()
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://192.168.1.10:8080"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/enroll-token", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 rejecting enroll-token issued on a replica, got %d: %s", rec.Code, rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/sync/enroll", strings.NewReader(`{"token":"whatever","name":"New Till"}`))
	req2.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("expected 409 rejecting enroll attempted on a replica, got %d: %s", rec2.Code, rec2.Body.String())
	}

	tillsRepo := data.NewTillsRepo(dp.Db)
	list, err := tillsRepo.ListTills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("expected no till to have been enrolled against the replica, got %d rows", len(list))
	}
}

// --- pairing code is opaque, not raw JSON (issue #7) ---

func TestEnrollCode_RoundTripOpaque(t *testing.T) {
	code := encodeEnrollCode("http://192.168.1.10:8080", "tok-abc123")
	// The manual code the manager reads/pastes must NOT leak the JSON
	// internals a customer/staffer shouldn't see or mistype.
	for _, bad := range []string{"{", "}", "\"", "token", "http", "url"} {
		if strings.Contains(code, bad) {
			t.Fatalf("enroll code leaks %q: %s", bad, code)
		}
	}
	url, tok, err := decodeEnrollCode(code)
	if err != nil || url != "http://192.168.1.10:8080" || tok != "tok-abc123" {
		t.Fatalf("round-trip failed: url=%q tok=%q err=%v", url, tok, err)
	}
}

func TestDecodeEnrollCode_LegacyRawJSON(t *testing.T) {
	// A QR/paste from a not-yet-upgraded primary is raw JSON — still accept it.
	url, tok, err := decodeEnrollCode(`{"url":"http://10.0.0.9:8080","token":"legacy"}`)
	if err != nil || url != "http://10.0.0.9:8080" || tok != "legacy" {
		t.Fatalf("legacy JSON decode failed: url=%q tok=%q err=%v", url, tok, err)
	}
}

func TestDecodeEnrollCode_Rejects(t *testing.T) {
	for _, bad := range []string{"", "   ", "not-a-code", "eyJ4IjoxfQ", `{"url":"","token":""}`, `{"url":"x"}`} {
		if _, _, err := decodeEnrollCode(bad); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}

// ut-docs#362. A till's kiosk browser runs against http://127.0.0.1:8080, so
// the pairing code minted from the main till's OWN touchscreen used to embed
// 127.0.0.1 — and the joining till then dialled itself. It failed in about a
// millisecond with "primary refused the enrolment (code used or expired?)",
// which blames the code, so the shop owner regenerates it and fails
// identically, forever. Reported from the field exactly that way.
func TestAdvertisableHostNeverReturnsLoopback(t *testing.T) {
	// Whatever this machine's real LAN address is, it must not be loopback —
	// that is the whole point. Skip only if the box genuinely has no network.
	lan, err := lanIPv4()
	if err != nil {
		t.Skipf("no non-loopback interface available: %v", err)
	}
	if net.ParseIP(lan) == nil || net.ParseIP(lan).IsLoopback() {
		t.Fatalf("lanIPv4() = %q, want a real non-loopback IPv4", lan)
	}

	for _, host := range []string{
		"127.0.0.1:8080", // the kiosk browser's own URL — the reported case
		"localhost:8080",
		"[::1]:8080",
		"127.0.0.1",
	} {
		got, err := advertisableHost(host)
		if err != nil {
			t.Fatalf("advertisableHost(%q): %v", host, err)
		}
		h, _, splitErr := net.SplitHostPort(got)
		if splitErr != nil {
			h = got
		}
		ip := net.ParseIP(h)
		if ip == nil {
			t.Fatalf("advertisableHost(%q) = %q, not an IP", host, got)
		}
		if ip.IsLoopback() {
			t.Errorf("advertisableHost(%q) = %q — a pairing code carrying this "+
				"tells the joining till to dial ITSELF (ut-docs#362)", host, got)
		}
		// The port must survive, or the code points at the wrong port.
		if strings.Contains(host, ":8080") && !strings.HasSuffix(got, ":8080") {
			t.Errorf("advertisableHost(%q) = %q, lost the port", host, got)
		}
	}
}

// A host a peer can already dial must be passed through untouched — rewriting
// it would break the normal case (a manager pairing from their phone/laptop,
// where r.Host is already the till's LAN address or a hostname).
func TestAdvertisableHostLeavesReachableHostsAlone(t *testing.T) {
	for _, host := range []string{"192.168.1.167:8080", "till-1.local:8080", "10.0.0.5"} {
		got, err := advertisableHost(host)
		if err != nil {
			t.Fatalf("advertisableHost(%q): %v", host, err)
		}
		if got != host {
			t.Errorf("advertisableHost(%q) = %q, want it unchanged", host, got)
		}
	}
}

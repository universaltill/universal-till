package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
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

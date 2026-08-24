package pages

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

func newPairingAPITestDeps(t *testing.T) (*http.ServeMux, *common.Deps, *auth.Service) {
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
	svc := auth.NewService(db)
	dp.AuthSvc = svc
	mux := http.NewServeMux()
	tokens := registerSyncAPI(mux, dp)
	registerPairingAPI(mux, dp, svc, tokens)
	return mux, dp, svc
}

func commitOf(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func postPairRequest(t *testing.T, mux *http.ServeMux, deviceName, commitment, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"device_name": deviceName, "commitment": commitment})
	req := httptest.NewRequest(http.MethodPost, "/api/sync/pair-request", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestPairRequest_CreatesRowAndReturnsID(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, _ := newPairingAPITestDeps(t)

	rec := postPairRequest(t, mux, "Kitchen Till", commitOf("secret-1"), "10.0.0.5:1234")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.ID == "" {
		t.Fatal("expected a non-empty pending-request id")
	}
}

func TestPairRequest_RateLimitedBurstFromOneSource(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, _ := newPairingAPITestDeps(t)

	var last *httptest.ResponseRecorder
	got429 := false
	for i := 0; i < 20; i++ {
		last = postPairRequest(t, mux, "Spammer", commitOf("secret-spam"), "10.0.0.9:9999")
		if last.Code == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatal("expected a rapid burst from one source to eventually get 429 Too Many Requests")
	}

	// A different source is unaffected by that source's cap.
	rec := postPairRequest(t, mux, "Other Till", commitOf("secret-other"), "10.0.0.10:1111")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected a different source to be unaffected, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestListPairRequests_RequiresManager(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, _, _ := newPairingAPITestDeps(t)

	req := httptest.NewRequest(http.MethodGet, "/api/sync/pair-requests", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager session, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/sync/pair-requests", nil)
	req = auth.WithUser(req, auth.User{ID: "cashier-1", Role: "cashier"})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a cashier session, got %d", rec.Code)
	}
}

func TestListPairRequests_ManagerSeesVerificationCode(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, _ := newPairingAPITestDeps(t)

	postPairRequest(t, mux, "Kitchen Till", commitOf("secret-2"), "10.0.0.6:1234")

	req := httptest.NewRequest(http.MethodGet, "/api/sync/pair-requests", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			Pending []struct {
				ID               string `json:"id"`
				DeviceName       string `json:"device_name"`
				VerificationCode string `json:"verification_code"`
			} `json:"pending"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data.Pending) != 1 || out.Data.Pending[0].DeviceName != "Kitchen Till" {
		t.Fatalf("expected the pending request listed, got %+v", out.Data.Pending)
	}
	code := out.Data.Pending[0].VerificationCode
	if len(code) != 6 {
		t.Fatalf("expected a 6-digit verification code, got %q", code)
	}
	// ADR-0033 §4: "6-digit verification code" — DECIMAL digits, like a
	// TOTP code, not the first 6 hex characters of the hash. The replica
	// side must derive the identical value independently; a hex code
	// would never match a digit-only implementation on the other end.
	for _, c := range code {
		if c < '0' || c > '9' {
			t.Fatalf("expected all-decimal-digit verification code, got %q", code)
		}
	}
}

// TestListPairRequests_VerificationCodeUsesDiscoveryTillID is the actual
// point of ut-docs#264's "close the loop" step: the verification code this
// endpoint returns must be derived from discovery.TillID — the SAME
// settings-backed id the mDNS Advertiser publishes in its TXT record — not
// the unrelated marketplace device id. A replica that reads a primary's id
// off mDNS (discovery.Browse) and independently computes
// derivedVerificationCode(commitment, thatID) must land on the exact code
// this endpoint shows the manager.
func TestListPairRequests_VerificationCodeUsesDiscoveryTillID(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)

	commitment := commitOf("secret-close-the-loop")
	postPairRequest(t, mux, "Replica Till", commitment, "10.0.0.7:1234")

	req := httptest.NewRequest(http.MethodGet, "/api/sync/pair-requests", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			Pending []struct {
				VerificationCode string `json:"verification_code"`
			} `json:"pending"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data.Pending) != 1 {
		t.Fatalf("expected one pending request, got %+v", out.Data.Pending)
	}

	// Independently compute the id the SAME way discovery.Browse would hand
	// a replica (a fresh discovery.TillID call against this primary's own
	// settings — get-or-create is idempotent, so this is exactly the value
	// already persisted, not a new one).
	wantTillID, err := discovery.TillID(req.Context(), data.NewSettingsRepo(dp.Db))
	if err != nil {
		t.Fatalf("discovery.TillID: %v", err)
	}
	wantCode := derivedVerificationCode(commitment, wantTillID)

	if got := out.Data.Pending[0].VerificationCode; got != wantCode {
		t.Fatalf("verification_code = %q, want %q (derived from discovery.TillID %q) — "+
			"the primary and a replica computing this independently off mDNS must agree",
			got, wantCode, wantTillID)
	}
}

func TestDerivedVerificationCode_IsAlwaysSixDecimalDigits(t *testing.T) {
	// Run enough distinct inputs that a hex-leaking implementation
	// (a-f characters) would almost certainly show up.
	for i := 0; i < 200; i++ {
		code := derivedVerificationCode(commitOf("commit-"+string(rune('a'+i%26))), "primary-"+string(rune('a'+i%26)))
		if len(code) != 6 {
			t.Fatalf("expected 6 characters, got %q", code)
		}
		for _, c := range code {
			if c < '0' || c > '9' {
				t.Fatalf("expected only decimal digits, got %q from inputs at i=%d", code, i)
			}
		}
	}
}

// TestPairingFlow_ApproveDeliversTokenOnlyToSecretHolder is the
// end-to-end path: request -> manager approve -> replica retrieves its
// token ONLY by presenting the correct request_secret, and that token
// works against the real /api/sync/enroll (proves it shares the same
// enrolTokens store the QR flow uses).
func TestPairingFlow_ApproveDeliversTokenOnlyToSecretHolder(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, _ := newPairingAPITestDeps(t)

	secret := "replica-secret-xyz"
	rec := postPairRequest(t, mux, "Bar Till", commitOf(secret), "10.0.0.7:1234")
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id := created.Data.ID

	// Approve (manager; UT_AUTH=off bypasses the PIN check itself, same
	// as the refund flow's authOff branch).
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/pair-requests/"+id+"/approve", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 approving, got %d: %s", rec.Code, rec.Body.String())
	}

	// Wrong secret gets nothing.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/sync/pair-requests/"+id+"?request_secret=wrong-secret", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 with a wrong secret, got %d: %s", rec.Code, rec.Body.String())
	}

	// Absent secret gets nothing.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/sync/pair-requests/"+id, nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 with no secret, got %d: %s", rec.Code, rec.Body.String())
	}

	// The correct secret retrieves the token.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/sync/pair-requests/"+id+"?request_secret="+secret, nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with the correct secret, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.Token == "" {
		t.Fatal("expected a non-empty token")
	}

	// That token is a real, working enrolment token against the existing
	// /api/sync/enroll endpoint — not a lookalike.
	enrollBody, _ := json.Marshal(map[string]string{"token": out.Data.Token, "name": "Bar Till"})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/sync/enroll", strings.NewReader(string(enrollBody)))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the approved token to enrol successfully, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPairRequest_ApproveRequiresManagerPINWhenAuthEnabled(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, _, _ := newPairingAPITestDeps(t)

	rec := postPairRequest(t, mux, "Bar Till", commitOf("secret-3"), "10.0.0.8:1234")
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/pair-requests/"+created.Data.ID+"/approve", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 approving without a manager PIN, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestApprovePairRequest_SecondApproveReturns409AndFirstTokenStillWorks
// covers a concurrent-approve race (e.g. a manager double-clicking, or
// two overlapping requests): the second approve must not silently mint
// and orphan a second live token, and must report a conflict, not a
// server error.
func TestApprovePairRequest_SecondApproveReturns409AndFirstTokenStillWorks(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, _ := newPairingAPITestDeps(t)

	secret := "race-secret"
	rec := postPairRequest(t, mux, "Race Till", commitOf(secret), "10.0.0.12:1234")
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	approve := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/sync/pair-requests/"+created.Data.ID+"/approve", nil)
		mux.ServeHTTP(rec, req)
		return rec
	}
	first := approve()
	if first.Code != http.StatusOK {
		t.Fatalf("expected 200 on the first approve, got %d: %s", first.Code, first.Body.String())
	}
	second := approve()
	if second.Code != http.StatusConflict {
		t.Fatalf("expected 409 on the second approve, got %d: %s", second.Code, second.Body.String())
	}

	// The row must still carry the FIRST approve's token, retrievable
	// with the original secret — the second approve's minted token (if
	// any) must have been burned, not left live and orphaned.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sync/pair-requests/"+created.Data.ID+"?request_secret="+secret, nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 retrieving after the race, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestApprovePairRequest_RecordsAuditWithPINOwnerAsActor covers finding
// #9 from independent review: approving a pairing hands a new device the
// entire shop database (users/pin_hash included) — strictly more
// sensitive than a refund, which DOES get an audited actor via the same
// AuthorizeManager pattern this mirrors.
func TestApprovePairRequest_RecordsAuditWithPINOwnerAsActor(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)

	rec := postPairRequest(t, mux, "Audited Till", commitOf("audit-secret"), "10.0.0.13:1234")
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/pair-requests/"+created.Data.ID+"/approve", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 approving, got %d: %s", rec.Code, rec.Body.String())
	}

	var action, entityType, entityID string
	err := dp.Db.QueryRow(`SELECT action, entity_type, entity_id FROM audit_log WHERE entity_id = ?`, created.Data.ID).
		Scan(&action, &entityType, &entityID)
	if err != nil {
		t.Fatalf("expected an audit_log row for the approval: %v", err)
	}
	if action != "pairing_approved" || entityType != "till_pairing" || entityID != created.Data.ID {
		t.Fatalf("unexpected audit row: action=%q entity_type=%q entity_id=%q", action, entityType, entityID)
	}
}

func TestDenyPairRequest_RemovesRowAndSubsequentPollFails(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, _ := newPairingAPITestDeps(t)

	secret := "denied-secret"
	rec := postPairRequest(t, mux, "Patio Till", commitOf(secret), "10.0.0.11:1234")
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/pair-requests/"+created.Data.ID+"/deny", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 denying, got %d: %s", rec.Code, rec.Body.String())
	}

	// The replica's poll (even with the right secret) now finds nothing.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/sync/pair-requests/"+created.Data.ID+"?request_secret="+secret, nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 polling a denied request, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestPairingFlow_AgainstRealMigratedSchema runs the same request-approve-
// retrieve path against a database built from the REAL migrations
// (internal/db.Open), not the hand-rolled seedForPages fixture — the
// fixture's pending_pairings CREATE TABLE was typed by hand to mirror
// migration 027, and this pipeline has previously shipped tests that
// silently passed against a drifted hand-rolled schema. This test would
// fail if that fixture ever falls out of sync with the real migration.
func TestPairingFlow_AgainstRealMigratedSchema(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dbPath := filepath.Join(t.TempDir(), "unitill-pos.db")
	d, err := appdb.Open(dbPath)
	if err != nil {
		t.Fatalf("open real migrated db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	dp := &common.Deps{
		Cfg: &config.Config{Marketplace: config.MarketplaceConfig{EndpointURL: "http://localhost:8081"}},
		Db:  d.DB,
	}
	svc := auth.NewService(d.DB)
	tokens := &enrolTokens{tokens: map[string]time.Time{}}
	mux := http.NewServeMux()
	registerPairingAPI(mux, dp, svc, tokens)

	secret := "real-schema-secret"
	rec := postPairRequest(t, mux, "Real Schema Till", commitOf(secret), "10.0.0.20:1234")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 creating against the real schema, got %d: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/pair-requests/"+created.Data.ID+"/approve", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 approving against the real schema, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/sync/pair-requests/"+created.Data.ID+"?request_secret="+secret, nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 retrieving against the real schema, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Data.Token == "" {
		t.Fatal("expected a non-empty token from the real-schema flow")
	}
}

// --- ut-docs#946 (924 increment 4): raw err.Error() leaks now route through
// common.LogAndLocalizedError. Each test below forces a REAL failure (a
// dropped table or a read-only connection, never a mock/stub repo) at one
// specific call site and asserts the localized "pairings.error.server" copy
// appears while the raw SQL/Go error text does not.

func TestPairRequest_CreateFailureIsLocalized(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	if _, err := dp.Db.Exec(`DROP TABLE pending_pairings`); err != nil {
		t.Fatalf("drop pending_pairings: %v", err)
	}

	rec := postPairRequest(t, mux, "Kitchen Till", commitOf("create-fail"), "10.0.0.30:1234")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	want := httpx.T("en", "pairings.error.server")
	if !strings.Contains(body, want) {
		t.Fatalf("expected the localized message %q, got %q", want, body)
	}
	if strings.Contains(body, "no such table") || strings.Contains(body, "create pending pairing") {
		t.Fatalf("raw SQL error leaked into the response: %q", body)
	}
}

func TestListPairRequests_ListFailureIsLocalized(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	if _, err := dp.Db.Exec(`DROP TABLE pending_pairings`); err != nil {
		t.Fatalf("drop pending_pairings: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sync/pair-requests", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	want := httpx.T("en", "pairings.error.server")
	if !strings.Contains(body, want) {
		t.Fatalf("expected the localized message %q, got %q", want, body)
	}
	if strings.Contains(body, "no such table") || strings.Contains(body, "expire pending pairings") ||
		strings.Contains(body, "list pending pairings") {
		t.Fatalf("raw SQL error leaked into the response: %q", body)
	}
}

// discovery.TillID reads/creates the sync.till_id setting AFTER
// repo.ListPending has already succeeded — dropping only the settings
// table (leaving pending_pairings intact) isolates this specific call site
// (line 177) from ListPending's own (line 172).
func TestListPairRequests_TillIDFailureIsLocalized(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	if _, err := dp.Db.Exec(`DROP TABLE settings`); err != nil {
		t.Fatalf("drop settings: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/sync/pair-requests", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	want := httpx.T("en", "pairings.error.server")
	if !strings.Contains(body, want) {
		t.Fatalf("expected the localized message %q, got %q", want, body)
	}
	if strings.Contains(body, "no such table") || strings.Contains(body, "get setting") {
		t.Fatalf("raw SQL error leaked into the response: %q", body)
	}
}

func TestApprovePairRequest_GetByIDFailureIsLocalized(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)

	rec := postPairRequest(t, mux, "Kitchen Till", commitOf("approve-getbyid-fail"), "10.0.0.31:1234")
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	if _, err := dp.Db.Exec(`DROP TABLE pending_pairings`); err != nil {
		t.Fatalf("drop pending_pairings: %v", err)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/pair-requests/"+created.Data.ID+"/approve", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	want := httpx.T("en", "pairings.error.server")
	if !strings.Contains(body, want) {
		t.Fatalf("expected the localized message %q, got %q", want, body)
	}
	if strings.Contains(body, "no such table") || strings.Contains(body, "pending pairing by id") {
		t.Fatalf("raw SQL error leaked into the response: %q", body)
	}
}

// repo.Approve's UPDATE fails on a read-only connection while the earlier
// repo.GetByID SELECT (line 216) still succeeds — isolates the Approve call
// site (line 230) from the GetByID one (line 217) covered above.
func TestApprovePairRequest_ApproveWriteFailureIsLocalized(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)

	rec := postPairRequest(t, mux, "Kitchen Till", commitOf("approve-write-fail"), "10.0.0.32:1234")
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	if _, err := dp.Db.Exec(`PRAGMA query_only = ON`); err != nil {
		t.Fatalf("set query_only: %v", err)
	}
	t.Cleanup(func() { _, _ = dp.Db.Exec(`PRAGMA query_only = OFF`) })

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/pair-requests/"+created.Data.ID+"/approve", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	want := httpx.T("en", "pairings.error.server")
	if !strings.Contains(body, want) {
		t.Fatalf("expected the localized message %q, got %q", want, body)
	}
	if strings.Contains(body, "readonly database") || strings.Contains(body, "approve pending pairing") {
		t.Fatalf("raw SQL error leaked into the response: %q", body)
	}
}

func TestDenyPairRequest_FailureIsLocalized(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)

	rec := postPairRequest(t, mux, "Kitchen Till", commitOf("deny-fail"), "10.0.0.33:1234")
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	if _, err := dp.Db.Exec(`DROP TABLE pending_pairings`); err != nil {
		t.Fatalf("drop pending_pairings: %v", err)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/pair-requests/"+created.Data.ID+"/deny", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	want := httpx.T("en", "pairings.error.server")
	if !strings.Contains(body, want) {
		t.Fatalf("expected the localized message %q, got %q", want, body)
	}
	if strings.Contains(body, "no such table") || strings.Contains(body, "deny pending pairing") {
		t.Fatalf("raw SQL error leaked into the response: %q", body)
	}
}

func TestRetrievePairRequest_GetByIDFailureIsLocalized(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)

	secret := "retrieve-fail-secret"
	rec := postPairRequest(t, mux, "Kitchen Till", commitOf(secret), "10.0.0.34:1234")
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/pair-requests/"+created.Data.ID+"/approve", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 approving, got %d: %s", rec.Code, rec.Body.String())
	}

	if _, err := dp.Db.Exec(`DROP TABLE pending_pairings`); err != nil {
		t.Fatalf("drop pending_pairings: %v", err)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/sync/pair-requests/"+created.Data.ID+"?request_secret="+secret, nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	want := httpx.T("en", "pairings.error.server")
	if !strings.Contains(body, want) {
		t.Fatalf("expected the localized message %q, got %q", want, body)
	}
	if strings.Contains(body, "no such table") || strings.Contains(body, "pending pairing by id") {
		t.Fatalf("raw SQL error leaked into the response: %q", body)
	}
}

package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// fakeTSECloud serves the two ADR-0053 till-facing endpoints ut-cloud PR #55
// landed — kickoff and single-use credential fetch — and records what the
// till sent. Deliberately does NOT serve /v1/stores/register (same reasoning
// as newFakeMarketplace: serving it would mutate the enroll package's
// process globals across tests).
type fakeTSECloud struct {
	mu             sync.Mutex
	provisionReqs  []map[string]any
	credentialReqs int

	provisionStatus  int    // 0 => 200 OK
	provisionErrCode string // error envelope code for a non-200
	credentialStatus int    // 0 => 200 OK
	credential       map[string]any

	server *httptest.Server
}

func newFakeTSECloud(t *testing.T) *fakeTSECloud {
	t.Helper()
	f := &fakeTSECloud{credential: map[string]any{"api_key": "op-key-1", "tss_id": "tss-9"}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/stores/fiscal/tse/provision", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.provisionReqs = append(f.provisionReqs, body)
		status, code := f.provisionStatus, f.provisionErrCode
		f.mu.Unlock()
		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": nil, "error": map[string]any{"code": code, "message": "nope"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  map[string]any{"store_id": "store-1", "status": "provisioned", "ownership_model": "reseller_provisioned"},
			"error": nil,
		})
	})
	mux.HandleFunc("POST /v1/stores/fiscal/tse/credential", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.credentialReqs++
		status, cred := f.credentialStatus, f.credential
		f.mu.Unlock()
		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": nil, "error": map[string]any{"code": "credential_unavailable", "message": "gone"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  map[string]any{"store_id": "store-1", "operational_credential": cred},
			"error": nil,
		})
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeTSECloud) provisionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.provisionReqs)
}

func (f *fakeTSECloud) credentialCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.credentialReqs
}

// configureTSECloud points dp at the fake cloud with an explicit store
// identity — enroll's lazy registration then has nothing to add.
func configureTSECloud(dp *common.Deps, url string) {
	dp.Cfg.Marketplace.EndpointURL = url
	dp.Cfg.Marketplace.ClientID = "merchant-1"
	dp.Cfg.Marketplace.StoreID = "store-1"
	dp.Cfg.Marketplace.MerchantToken = "tok-1"
}

// restoreCurrencyAfter re-inits the process-global currency to the test
// suite's GBP baseline once this test ends: a completed DE wizard run calls
// httpx.InitCurrency("EUR") (the real handler's own move), and later tests
// in this package (e.g. shifts_page_test's "£50.00" assertions) implicitly
// depend on the GBP global that setup_page_test's GB runs leave behind.
func restoreCurrencyAfter(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { httpx.InitCurrency("GBP") })
}

// deWizardForm is a complete DE wizard submission including the business-
// identity step's fields.
func deWizardForm() url.Values {
	return url.Values{
		"pin":            {"2468"},
		"pin_confirm":    {"2468"},
		"country":        {"DE"},
		"currency":       {"EUR"},
		"tax_rate_pct":   {"19"},
		"store_name":     {"Ecke Laden"},
		"tse_legal_name": {"Ecke Laden GmbH"},
		"tse_owner_name": {"Erika Musterfrau"},
		"tse_tax_number": {"12/345/67890"},
		"tse_address":    {"Beispielstr. 1, 10115 Berlin"},
	}
}

// --- validGermanTaxNumber ---

func TestValidGermanTaxNumber(t *testing.T) {
	valid := []string{"DE123456789", "de 123 456 789", "12/345/67890", "123/456/78901", "1234567890", "12345678901", "12.345.67890"}
	for _, v := range valid {
		if !validGermanTaxNumber(v) {
			t.Errorf("validGermanTaxNumber(%q) = false, want true", v)
		}
	}
	invalid := []string{"", "abc", "DE12345", "DE1234567890", "123", "12/34", "DE12345678X", "123456789012345", "12a/345/67890"}
	for _, v := range invalid {
		if validGermanTaxNumber(v) {
			t.Errorf("validGermanTaxNumber(%q) = true, want false", v)
		}
	}
}

// --- POST /api/setup: the wizard's kickoff hook ---

// Offline-first, non-negotiable: the wizard must complete with no network,
// and the identity must already be persisted as pending BEFORE the (failed)
// kickoff attempt so the background retry can pick it up.
func TestSetupWizardDE_TSEOfflineCompletesAndLeavesPendingKickoff(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	restoreCurrencyAfter(t)
	initTestPaths(t)
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	configureTSECloud(d, dead.URL)
	dead.Close()

	rec := postForm(mux, "/api/setup", deWizardForm(), nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("wizard must complete offline: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if sessionCookie(rec) == "" {
		t.Fatal("wizard must still sign the admin in when the TSE kickoff is offline")
	}

	st, err := loadTSEProvisioningState(t.Context(), d)
	if err != nil || st == nil {
		t.Fatalf("loadTSEProvisioningState: %+v err=%v", st, err)
	}
	if st.Status != tseStatusPendingKickoff {
		t.Fatalf("status = %q, want %q", st.Status, tseStatusPendingKickoff)
	}
	if st.Country != "DE" || st.Identity.LegalName != "Ecke Laden GmbH" || st.Identity.TaxNumber != "12/345/67890" {
		t.Fatalf("persisted identity wrong: %+v", st)
	}
	// fiscal.tse_configured must NOT be set at wizard-submit time — binding.
	if v, ok, _ := d.Settings.Get(t.Context(), fiscal.KeyTSEConfigured); ok && strings.TrimSpace(v) != "" {
		t.Fatalf("fiscal.tse_configured = %q set optimistically at wizard time", v)
	}
}

// The happy path: the cloud accepts the kickoff during the wizard's own
// time-boxed attempt and the state moves to awaiting the ready directive.
func TestSetupWizardDE_TSEKickoffSuccess(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	restoreCurrencyAfter(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	configureTSECloud(d, cloud.server.URL)

	rec := postForm(mux, "/api/setup", deWizardForm(), nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("wizard: code=%d body=%s", rec.Code, rec.Body.String())
	}

	if cloud.provisionCount() != 1 {
		t.Fatalf("provision calls = %d, want 1", cloud.provisionCount())
	}
	cloud.mu.Lock()
	req := cloud.provisionReqs[0]
	cloud.mu.Unlock()
	if req["store_id"] != "store-1" || req["country"] != "DE" {
		t.Fatalf("kickoff body wrong: %+v", req)
	}
	bi, _ := req["business_identity"].(map[string]any)
	if bi["legal_name"] != "Ecke Laden GmbH" || bi["owner_name"] != "Erika Musterfrau" ||
		bi["tax_number"] != "12/345/67890" || bi["address"] != "Beispielstr. 1, 10115 Berlin" {
		t.Fatalf("business_identity wrong: %+v", bi)
	}
	if _, hasBank := bi["bank_details"]; hasBank {
		t.Fatal("no bank-details field, ever")
	}

	st, err := loadTSEProvisioningState(t.Context(), d)
	if err != nil || st == nil || st.Status != tseStatusAwaitingReady {
		t.Fatalf("state after accepted kickoff = %+v err=%v, want awaiting_ready", st, err)
	}
	// Still not configured: the credential hasn't arrived yet.
	if v, ok, _ := d.Settings.Get(t.Context(), fiscal.KeyTSEConfigured); ok && strings.TrimSpace(v) != "" {
		t.Fatalf("fiscal.tse_configured = %q before the credential arrived", v)
	}
}

// A definitive rejection (subscription_inactive / not_configured / bad data)
// must land in a loud, distinct state — not silently retried forever.
func TestSetupWizardDE_TSEKickoffRejectedIsLoud(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	restoreCurrencyAfter(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	cloud.provisionStatus = http.StatusForbidden
	cloud.provisionErrCode = "subscription_inactive"
	configureTSECloud(d, cloud.server.URL)

	rec := postForm(mux, "/api/setup", deWizardForm(), nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("a rejected kickoff must not fail the wizard: code=%d", rec.Code)
	}

	st, err := loadTSEProvisioningState(t.Context(), d)
	if err != nil || st == nil {
		t.Fatalf("state: %+v err=%v", st, err)
	}
	if st.Status != tseStatusKickoffRejected || st.ErrorCode != "subscription_inactive" {
		t.Fatalf("state = %+v, want kickoff_rejected/subscription_inactive", st)
	}
}

// Non-DE countries never collect or send business identity — the step is
// gated to the Germany pilot (ADR-0053).
func TestSetupWizardNonDE_IgnoresIdentityFields(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	restoreCurrencyAfter(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	configureTSECloud(d, cloud.server.URL)

	form := deWizardForm()
	form.Set("country", "GB")
	form.Set("currency", "GBP")
	rec := postForm(mux, "/api/setup", form, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("wizard: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if cloud.provisionCount() != 0 {
		t.Fatalf("provision called %d times for a non-DE country", cloud.provisionCount())
	}
	if st, _ := loadTSEProvisioningState(t.Context(), d); st != nil {
		t.Fatalf("state persisted for a non-DE country: %+v", st)
	}
}

// A German shop may skip the step entirely (free tier brings its own
// fiscalisation, ADR-0045) — all-blank identity is a clean no-op.
func TestSetupWizardDE_SkippedIdentityIsNoOp(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	restoreCurrencyAfter(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	configureTSECloud(d, cloud.server.URL)

	form := deWizardForm()
	form.Del("tse_legal_name")
	form.Del("tse_owner_name")
	form.Del("tse_tax_number")
	form.Del("tse_address")
	rec := postForm(mux, "/api/setup", form, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("wizard: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if cloud.provisionCount() != 0 {
		t.Fatalf("provision called %d times after a skipped step", cloud.provisionCount())
	}
	if st, _ := loadTSEProvisioningState(t.Context(), d); st != nil {
		t.Fatalf("state persisted after a skipped step: %+v", st)
	}
}

// A partially-filled identity re-renders the wizard with a specific error —
// never a half-submitted kickoff, and nothing persisted yet.
func TestSetupWizardDE_IncompleteIdentityReRenders(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	initTestPaths(t)

	form := deWizardForm()
	form.Del("tse_owner_name")
	rec := postForm(mux, "/api/setup", form, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected a re-render, got code=%d", rec.Code)
	}
	if sessionCookie(rec) != "" {
		t.Fatal("no session may be created on a validation failure")
	}
	if st, _ := loadTSEProvisioningState(t.Context(), d); st != nil {
		t.Fatalf("state persisted despite validation failure: %+v", st)
	}
	// The re-render must land the operator back on the business-identity
	// step with their typed values preserved.
	body := rec.Body.String()
	if !strings.Contains(body, "Ecke Laden GmbH") {
		t.Error("re-render lost the typed legal name")
	}
}

// A malformed tax number re-renders with the format-specific error.
func TestSetupWizardDE_BadTaxNumberReRenders(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	initTestPaths(t)

	form := deWizardForm()
	form.Set("tse_tax_number", "not-a-tax-number")
	rec := postForm(mux, "/api/setup", form, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected a re-render, got code=%d", rec.Code)
	}
	if st, _ := loadTSEProvisioningState(t.Context(), d); st != nil {
		t.Fatalf("state persisted despite a bad tax number: %+v", st)
	}
}

// --- background kickoff retry ---

func TestTSEProvisionRetryTick_KicksOffOnceBackOnline(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	_ = mux
	initTestPaths(t)

	// Persisted by an offline wizard run.
	if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{
		Status: tseStatusPendingKickoff, Country: "DE",
		Identity: tseBusinessIdentity{LegalName: "L", OwnerName: "O", TaxNumber: "DE123456789", Address: "A"},
	}); err != nil {
		t.Fatal(err)
	}

	cloud := newFakeTSECloud(t)
	configureTSECloud(d, cloud.server.URL)
	tseProvisionRetryTick(t.Context(), d)

	if cloud.provisionCount() != 1 {
		t.Fatalf("provision calls = %d, want 1", cloud.provisionCount())
	}
	st, _ := loadTSEProvisioningState(t.Context(), d)
	if st == nil || st.Status != tseStatusAwaitingReady {
		t.Fatalf("state = %+v, want awaiting_ready", st)
	}
}

// A rejected state is terminal for the retry loop: no more kickoff attempts.
func TestTSEProvisionRetryTick_DoesNotRetryRejected(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	_ = mux
	initTestPaths(t)
	if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{
		Status: tseStatusKickoffRejected, Country: "DE", ErrorCode: "subscription_inactive",
		Identity: tseBusinessIdentity{LegalName: "L", OwnerName: "O", TaxNumber: "DE123456789", Address: "A"},
	}); err != nil {
		t.Fatal(err)
	}
	cloud := newFakeTSECloud(t)
	configureTSECloud(d, cloud.server.URL)
	tseProvisionRetryTick(t.Context(), d)
	if cloud.provisionCount() != 0 {
		t.Fatalf("a rejected kickoff was retried (%d calls)", cloud.provisionCount())
	}
}

func TestStartTSEProvisionRetryShutsDownOnCtxDone(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	StartTSEProvisionRetry(ctx, d, &wg)
	cancel()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("StartTSEProvisionRetry did not return on ctx.Done() — goroutine leak")
	}
}

// --- fiscal_tse_ready directive handling ---

// Success: credential fetched, stored 0600 on disk, and ONLY then
// fiscal.tse_configured flips true (binding, ADR-0053/ut-docs#802 item 4).
func TestApplyFiscalTSEReady_StoresCredentialThenConfigures(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	configureTSECloud(d, cloud.server.URL)
	if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{
		Status: tseStatusAwaitingReady, Country: "DE",
		Identity: tseBusinessIdentity{LegalName: "L", OwnerName: "O", TaxNumber: "DE123456789", Address: "A"},
	}); err != nil {
		t.Fatal(err)
	}

	msg, err := applyFiscalTSEReady(t.Context(), d)
	if err != nil {
		t.Fatalf("applyFiscalTSEReady: %v", err)
	}
	if msg == "" {
		t.Fatal("want a human-readable result message")
	}
	if cloud.credentialCount() != 1 {
		t.Fatalf("credential fetches = %d, want 1", cloud.credentialCount())
	}

	store := fiscal.NewTSECredentialStore()
	cred, ok, err := store.Load()
	if err != nil || !ok || cred["api_key"] != "op-key-1" {
		t.Fatalf("credential not stored: ok=%v err=%v cred=%+v", ok, err, cred)
	}
	fi, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("stat credential: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("credential perm = %o, want 0600", perm)
	}
	if v, _, _ := d.Settings.Get(t.Context(), fiscal.KeyTSEConfigured); v != "true" {
		t.Fatalf("fiscal.tse_configured = %q, want true after confirmed store", v)
	}
	if st, _ := loadTSEProvisioningState(t.Context(), d); st != nil {
		t.Fatalf("provisioning state not cleared after success: %+v", st)
	}
}

// Failure path (binding): a failed fetch leaves fiscal.tse_configured
// unset/false and records a loud, distinct credential-failed state.
func TestApplyFiscalTSEReady_FetchFailureLeavesUnconfigured(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	cloud.credentialStatus = http.StatusInternalServerError
	configureTSECloud(d, cloud.server.URL)
	if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{
		Status: tseStatusAwaitingReady, Country: "DE",
		Identity: tseBusinessIdentity{LegalName: "L", OwnerName: "O", TaxNumber: "DE123456789", Address: "A"},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := applyFiscalTSEReady(t.Context(), d); err == nil {
		t.Fatal("want an error so the directive stays pending on the cloud")
	}
	if fiscal.NewTSECredentialStore().Exists() {
		t.Fatal("no credential may be stored on a failed fetch")
	}
	if v, ok, _ := d.Settings.Get(t.Context(), fiscal.KeyTSEConfigured); ok && strings.TrimSpace(v) != "" {
		t.Fatalf("fiscal.tse_configured = %q after a FAILED fetch — must stay unset", v)
	}
	st, _ := loadTSEProvisioningState(t.Context(), d)
	if st == nil || st.Status != tseStatusCredentialFailed {
		t.Fatalf("state = %+v, want credential_failed", st)
	}
}

// An empty credential map from the cloud is a failure, not a success — it
// must never flip the configured flag.
func TestApplyFiscalTSEReady_EmptyCredentialRejected(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	cloud.credential = map[string]any{}
	configureTSECloud(d, cloud.server.URL)

	if _, err := applyFiscalTSEReady(t.Context(), d); err == nil {
		t.Fatal("want an error for an empty operational_credential")
	}
	if v, ok, _ := d.Settings.Get(t.Context(), fiscal.KeyTSEConfigured); ok && strings.TrimSpace(v) != "" {
		t.Fatalf("fiscal.tse_configured = %q after an empty credential", v)
	}
}

// 410 Gone with nothing stored locally: the single-use retrieval record is
// spent/expired, so only a fresh kickoff can mint a new one — the state goes
// back to pending_kickoff for the retry loop (ADR-0053 Decision 1).
func TestApplyFiscalTSEReady_GoneWithoutLocalRequeuesKickoff(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	cloud.credentialStatus = http.StatusGone
	configureTSECloud(d, cloud.server.URL)
	if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{
		Status: tseStatusAwaitingReady, Country: "DE",
		Identity: tseBusinessIdentity{LegalName: "L", OwnerName: "O", TaxNumber: "DE123456789", Address: "A"},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := applyFiscalTSEReady(t.Context(), d); err == nil {
		t.Fatal("want an error on 410 with no local credential")
	}
	st, _ := loadTSEProvisioningState(t.Context(), d)
	if st == nil || st.Status != tseStatusPendingKickoff {
		t.Fatalf("state = %+v, want pending_kickoff (re-mint via kickoff)", st)
	}
	if st.Identity.TaxNumber != "DE123456789" {
		t.Fatalf("identity lost on requeue: %+v", st)
	}
	if v, ok, _ := d.Settings.Get(t.Context(), fiscal.KeyTSEConfigured); ok && strings.TrimSpace(v) != "" {
		t.Fatalf("fiscal.tse_configured = %q, must stay unset", v)
	}
}

// A re-served directive after the credential already landed locally (e.g.
// the previous ack never reached the cloud) is idempotent: no second fetch,
// configured stays true, applied.
func TestApplyFiscalTSEReady_IdempotentWhenCredentialAlreadyLocal(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	cloud.credentialStatus = http.StatusGone // a second real fetch would 410
	configureTSECloud(d, cloud.server.URL)

	if err := fiscal.NewTSECredentialStore().Save(map[string]any{"api_key": "op-key-1"}); err != nil {
		t.Fatal(err)
	}

	msg, err := applyFiscalTSEReady(t.Context(), d)
	if err != nil {
		t.Fatalf("re-served directive with a local credential must succeed: %v", err)
	}
	if msg == "" {
		t.Fatal("want a message")
	}
	if cloud.credentialCount() != 0 {
		t.Fatalf("credential fetched %d times despite a local copy", cloud.credentialCount())
	}
	if v, _, _ := d.Settings.Get(t.Context(), fiscal.KeyTSEConfigured); v != "true" {
		t.Fatalf("fiscal.tse_configured = %q, want true", v)
	}
}

// Regression test (Reviewer finding, ut-docs#802): a stray zero-length or
// corrupt credential file (the kind a failed write used to leave behind
// before Save became write-tmp-then-rename) must NOT satisfy the
// idempotency fast path. Before the fix, applyFiscalTSEReady used
// store.Exists() (a stat-only check), so this exact file would have flipped
// fiscal.tse_configured true over a credential nothing could ever read back.
// The directive must instead fall through to a real fetch.
func TestApplyFiscalTSEReady_CorruptExistingFileIsNotTreatedAsStored(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	configureTSECloud(d, cloud.server.URL)

	store := fiscal.NewTSECredentialStore()
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(), nil, 0o600); err != nil { // zero-length, unreadable JSON
		t.Fatal(err)
	}

	msg, err := applyFiscalTSEReady(t.Context(), d)
	if err != nil {
		t.Fatalf("applyFiscalTSEReady: %v", err)
	}
	if msg == "" {
		t.Fatal("want a human-readable result message")
	}
	if cloud.credentialCount() != 1 {
		t.Fatalf("credential fetches = %d, want 1 — a corrupt existing file must not short-circuit the fetch", cloud.credentialCount())
	}
	cred, ok, err := store.Load()
	if err != nil || !ok || cred["api_key"] != "op-key-1" {
		t.Fatalf("credential not stored correctly after overwrite: ok=%v err=%v cred=%+v", ok, err, cred)
	}
	if v, _, _ := d.Settings.Get(t.Context(), fiscal.KeyTSEConfigured); v != "true" {
		t.Fatalf("fiscal.tse_configured = %q, want true only after the real fetch succeeded", v)
	}
}

// --- Settings surfacing + dismiss ---

func TestSettingsShowsTSEProvisioningChipAndDismiss(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	getSettings := func() string {
		req := httptest.NewRequest(http.MethodGet, "/settings", nil)
		req = auth.WithUser(req, mgrUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /settings = %d", rec.Code)
		}
		return rec.Body.String()
	}

	if strings.Contains(getSettings(), `data-testid="tse-provisioning"`) {
		t.Fatal("TSE chip shown with nothing pending")
	}

	if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{
		Status: tseStatusKickoffRejected, Country: "DE", ErrorCode: "subscription_inactive",
		Identity: tseBusinessIdentity{LegalName: "L", OwnerName: "O", TaxNumber: "DE123456789", Address: "A"},
	}); err != nil {
		t.Fatal(err)
	}
	settingsBody := getSettings()
	if !strings.Contains(settingsBody, `data-testid="tse-provisioning"`) {
		t.Fatal("TSE chip not shown for a rejected kickoff")
	}

	// ut-docs#1112: the message must render in the wrapping .notice-block-warn
	// component (which wraps long text), not the nowrap-by-design .chip that
	// silently clipped it at the card edge — same regression class #1026
	// fixed for the sibling missingFiscalSigner banner.
	if !strings.Contains(settingsBody, "notice-block-warn") {
		t.Fatal("TSE provisioning message not wrapped in .notice-block-warn — regressed to the clipping .chip layout (ut-docs#1112)")
	}

	// Dismiss is manager-gated and clears the state.
	rec := postForm(mux, "/api/settings/dismiss-tse-provisioning", url.Values{}, &cashUser)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier dismiss: code=%d, want 403", rec.Code)
	}
	rec = postForm(mux, "/api/settings/dismiss-tse-provisioning", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager dismiss: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if st, _ := loadTSEProvisioningState(t.Context(), d); st != nil {
		t.Fatalf("state survived dismiss: %+v", st)
	}
	if strings.Contains(getSettings(), `data-testid="tse-provisioning"`) {
		t.Fatal("TSE chip still shown after dismiss")
	}
}

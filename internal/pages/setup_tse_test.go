package pages

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
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
		"till_name":      {"Till 1"},
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
	configureTSECloud(d, unreachableURL())

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
	// fiscal.signing_device_configured must NOT be set at wizard-submit time — binding.
	if v, ok, _ := d.Settings.Get(t.Context(), fiscal.SigningDeviceConfiguredKey("DE")); ok && strings.TrimSpace(v) != "" {
		t.Fatalf("fiscal.signing_device_configured = %q set optimistically at wizard time", v)
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
	if v, ok, _ := d.Settings.Get(t.Context(), fiscal.SigningDeviceConfiguredKey("DE")); ok && strings.TrimSpace(v) != "" {
		t.Fatalf("fiscal.signing_device_configured = %q before the credential arrived", v)
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

// newTSEReadyDeps is newFullAuthDeps for the directive tests below, on a
// German shop — the ordinary case: the till completed the DE wizard and is
// still DE when the cloud's fiscal_tse_ready lands. Since ADR-0083 the row
// finishTSEProvisioning flips is per country, and it names the PROVISIONED
// market (the provisioning record's own Country), not store.country, so the
// row these tests assert on is Germany's own,
// fiscal.SigningDeviceConfiguredKey("DE"). Those two values coincide here
// and deliberately do not in
// TestApplyFiscalTSEReady_WritesProvisionedMarketsRowNotCurrentCountrys.
func newTSEReadyDeps(t *testing.T) *common.Deps {
	t.Helper()
	_, _, d := newFullAuthDeps(t)
	setCountry(t, d, "DE")
	return d
}

// Success: credential fetched, stored 0600 on disk, and ONLY then
// fiscal.signing_device_configured.de flips true (binding, ADR-0053/ut-docs#802 item 4).
func TestApplyFiscalTSEReady_StoresCredentialThenConfigures(t *testing.T) {
	d := newTSEReadyDeps(t)
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

	store := fiscal.NewSigningDeviceCredentialStore()
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
	if v, _, _ := d.Settings.Get(t.Context(), fiscal.SigningDeviceConfiguredKey("DE")); v != "true" {
		t.Fatalf("fiscal.signing_device_configured = %q, want true after confirmed store", v)
	}
	if st, _ := loadTSEProvisioningState(t.Context(), d); st != nil {
		t.Fatalf("provisioning state not cleared after success: %+v", st)
	}
}

// ADR-0083, review finding: finishTSEProvisioning must name the row of the
// market the CREDENTIAL belongs to (the provisioning record's own Country,
// always tseProvisionCountry), never whatever store.country happens to say
// when the cloud's fiscal_tse_ready directive finally lands.
//
// The two are not the same value. The kickoff is DE-gated at wizard time,
// but completion is asynchronous — the cloud directive, or the background
// StartTSEProvisionRetry tick — and store.country is manager-writable in
// between. Crucially it is manager-writable at that exact moment with no
// owner check: requireFiscalAuthorityForCountryChange only demands owner
// authority while a signing device is already confirmed, and during
// awaiting_ready nothing is confirmed yet (that is the whole point of
// ut-docs#802 item 4). So:
//
//	set up DE, kickoff accepted (awaiting_ready)
//	-> manager sets store.country=TR (allowed: no posture to protect)
//	-> the cloud's fiscal_tse_ready arrives
//	=> a German TSE credential stamps fiscal.signing_device_configured.tr
//	=> Turkish till, fiscal.Allowed, no YN ÖKC at all
//
// which is precisely the ut-docs#1750 end state ADR-0083 exists to make
// structurally impossible, re-entered through a different writer. Splitting
// the rows only closes that class if each writer picks its row from the
// market it actually proved something about.
func TestApplyFiscalTSEReady_WritesProvisionedMarketsRowNotCurrentCountrys(t *testing.T) {
	d := newTSEReadyDeps(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	configureTSECloud(d, cloud.server.URL)
	if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{
		Status: tseStatusAwaitingReady, Country: "DE",
		Identity: tseBusinessIdentity{LegalName: "L", OwnerName: "O", TaxNumber: "DE123456789", Address: "A"},
	}); err != nil {
		t.Fatal(err)
	}
	// The shop is relabelled while the kickoff is still in flight.
	setCountry(t, d, "TR")

	if _, err := applyFiscalTSEReady(t.Context(), d); err != nil {
		t.Fatalf("applyFiscalTSEReady: %v", err)
	}
	if v, ok, _ := d.Settings.Get(t.Context(), fiscal.SigningDeviceConfiguredKey("TR")); ok && strings.TrimSpace(v) != "" {
		t.Fatalf("a German TSE credential set Turkey's posture row to %q — no ÖKC has ever answered on this till", v)
	}
	if v, _, _ := d.Settings.Get(t.Context(), fiscal.SigningDeviceConfiguredKey("DE")); v != "true" {
		t.Fatalf("%s = %q, want true — the credential is Germany's and its posture belongs on Germany's row", fiscal.SigningDeviceConfiguredKey("DE"), v)
	}
}

// Failure path (binding): a failed fetch leaves fiscal.signing_device_configured
// unset/false and records a loud, distinct credential-failed state.
func TestApplyFiscalTSEReady_FetchFailureLeavesUnconfigured(t *testing.T) {
	d := newTSEReadyDeps(t)
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
	if fiscal.NewSigningDeviceCredentialStore().Exists() {
		t.Fatal("no credential may be stored on a failed fetch")
	}
	if v, ok, _ := d.Settings.Get(t.Context(), fiscal.SigningDeviceConfiguredKey("DE")); ok && strings.TrimSpace(v) != "" {
		t.Fatalf("fiscal.signing_device_configured = %q after a FAILED fetch — must stay unset", v)
	}
	st, _ := loadTSEProvisioningState(t.Context(), d)
	if st == nil || st.Status != tseStatusCredentialFailed {
		t.Fatalf("state = %+v, want credential_failed", st)
	}
}

// An empty credential map from the cloud is a failure, not a success — it
// must never flip the configured flag.
func TestApplyFiscalTSEReady_EmptyCredentialRejected(t *testing.T) {
	d := newTSEReadyDeps(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	cloud.credential = map[string]any{}
	configureTSECloud(d, cloud.server.URL)

	if _, err := applyFiscalTSEReady(t.Context(), d); err == nil {
		t.Fatal("want an error for an empty operational_credential")
	}
	if v, ok, _ := d.Settings.Get(t.Context(), fiscal.SigningDeviceConfiguredKey("DE")); ok && strings.TrimSpace(v) != "" {
		t.Fatalf("fiscal.signing_device_configured = %q after an empty credential", v)
	}
}

// 410 Gone with nothing stored locally: the single-use retrieval record is
// spent/expired, so only a fresh kickoff can mint a new one — the state goes
// back to pending_kickoff for the retry loop (ADR-0053 Decision 1).
func TestApplyFiscalTSEReady_GoneWithoutLocalRequeuesKickoff(t *testing.T) {
	d := newTSEReadyDeps(t)
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
	if v, ok, _ := d.Settings.Get(t.Context(), fiscal.SigningDeviceConfiguredKey("DE")); ok && strings.TrimSpace(v) != "" {
		t.Fatalf("fiscal.signing_device_configured = %q, must stay unset", v)
	}
}

// A re-served directive after the credential already landed locally (e.g.
// the previous ack never reached the cloud) is idempotent: no second fetch,
// configured stays true, applied.
func TestApplyFiscalTSEReady_IdempotentWhenCredentialAlreadyLocal(t *testing.T) {
	d := newTSEReadyDeps(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	cloud.credentialStatus = http.StatusGone // a second real fetch would 410
	configureTSECloud(d, cloud.server.URL)

	if err := fiscal.NewSigningDeviceCredentialStore().Save(map[string]any{"api_key": "op-key-1"}); err != nil {
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
	if v, _, _ := d.Settings.Get(t.Context(), fiscal.SigningDeviceConfiguredKey("DE")); v != "true" {
		t.Fatalf("fiscal.signing_device_configured = %q, want true", v)
	}
}

// Regression test (Reviewer finding, ut-docs#802): a stray zero-length or
// corrupt credential file (the kind a failed write used to leave behind
// before Save became write-tmp-then-rename) must NOT satisfy the
// idempotency fast path. Before the fix, applyFiscalTSEReady used
// store.Exists() (a stat-only check), so this exact file would have flipped
// fiscal.signing_device_configured true over a credential nothing could ever read back.
// The directive must instead fall through to a real fetch.
func TestApplyFiscalTSEReady_CorruptExistingFileIsNotTreatedAsStored(t *testing.T) {
	d := newTSEReadyDeps(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	configureTSECloud(d, cloud.server.URL)

	store := fiscal.NewSigningDeviceCredentialStore()
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
	if v, _, _ := d.Settings.Get(t.Context(), fiscal.SigningDeviceConfiguredKey("DE")); v != "true" {
		t.Fatalf("fiscal.signing_device_configured = %q, want true only after the real fetch succeeded", v)
	}
}

// --- Settings surfacing + dismiss ---

// getSettingsAs renders /settings for a user and returns the body.
func getSettingsAs(t *testing.T, mux *http.ServeMux, u auth.User) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	req = auth.WithUser(req, u)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	return rec.Body.String()
}

func TestSettingsShowsTSEProvisioningChipAndDismiss(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	getSettings := func() string { return getSettingsAs(t, mux, mgrUser) }

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

	// Dismiss stays manager-gated.
	rec := postForm(mux, "/api/settings/dismiss-tse-provisioning", url.Values{}, &cashUser)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier dismiss: code=%d, want 403", rec.Code)
	}
	// ut-docs#1174: a hard-gated (DE) shop with an UNRESOLVED failure
	// (kickoff_rejected) can no longer dismiss the notice away — sales are
	// hard-blocked without a TSE, so hiding the only explanation of why
	// would leave the operator flying blind. 409, state survives.
	rec = postForm(mux, "/api/settings/dismiss-tse-provisioning", url.Values{}, &mgrUser)
	if rec.Code != http.StatusConflict {
		t.Fatalf("manager dismiss of a hard-gated unresolved state: code=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	st, _ := loadTSEProvisioningState(t.Context(), d)
	if st == nil || st.Status != tseStatusKickoffRejected {
		t.Fatalf("state after blocked dismiss = %+v, want kickoff_rejected preserved", st)
	}
	if !strings.Contains(getSettings(), `data-testid="tse-provisioning"`) {
		t.Fatal("TSE chip must survive a blocked dismiss")
	}
}

// ut-docs#1174: for a hard-gated, unresolved state the Settings block hides
// the dismiss button entirely (the 409 above is defense in depth, not the
// UI) and offers Retry instead, plus the why-you-can't-dismiss hint.
func TestSettingsTSEBlock_HardGatedUnresolvedShowsRetryHidesDismiss(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	for _, status := range []string{tseStatusKickoffRejected, tseStatusCredentialFailed} {
		if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{
			Status: status, Country: "DE", ErrorCode: "subscription_inactive",
			Identity: tseBusinessIdentity{LegalName: "L", OwnerName: "O", TaxNumber: "DE123456789", Address: "A"},
		}); err != nil {
			t.Fatal(err)
		}
		body := getSettingsAs(t, mux, mgrUser)
		if !strings.Contains(body, `data-testid="tse-provisioning-retry"`) {
			t.Fatalf("status %s: Retry button missing from Settings", status)
		}
		if strings.Contains(body, `data-testid="tse-provisioning-dismiss"`) {
			t.Fatalf("status %s: dismiss button rendered for a hard-gated unresolved state", status)
		}
	}

	// A still-progressing state (pending_kickoff) for the SAME hard-gated
	// country gets no Retry (the background ticker owns that retry) and is
	// ALSO not dismissible: the shop still has no working TSE regardless of
	// exact status, so hiding this one explanation would leave the operator
	// flying blind exactly as a kickoff_rejected would. (Fixed after
	// independent review: a status-enumerated block let a retry that landed
	// back at pending_kickoff be freely dismissed away — see
	// TestDismissTSEProvisioning_BlockedRegardlessOfStatusAfterRetry.)
	if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{
		Status: tseStatusPendingKickoff, Country: "DE",
		Identity: tseBusinessIdentity{LegalName: "L", OwnerName: "O", TaxNumber: "DE123456789", Address: "A"},
	}); err != nil {
		t.Fatal(err)
	}
	body := getSettingsAs(t, mux, mgrUser)
	if strings.Contains(body, `data-testid="tse-provisioning-dismiss"`) {
		t.Fatal("DE pending_kickoff: dismiss button must NOT be offered")
	}
	if !strings.Contains(body, `data-testid="tse-provisioning-dismiss-blocked"`) {
		t.Fatal("DE pending_kickoff: blocked-dismiss hint missing")
	}
	if strings.Contains(body, `data-testid="tse-provisioning-retry"`) {
		t.Fatal("pending_kickoff: no Retry button (the background ticker already retries)")
	}
}

// The still-allowed dismissal path (not hard-gate-blocked): a pending state
// dismisses exactly as before — 200, state cleared, chip gone — and the
// dismissal is audited.
// GB (not RequiresHardGate) so this exercises the still-fully-dismissible
// path while proving the success side effects (state cleared, audited under
// the real actor). A hard-gated country's pending_kickoff is deliberately
// NOT dismissible any more — see
// TestDismissTSEProvisioning_BlockedRegardlessOfStatusAfterRetry below.
func TestDismissTSEProvisioning_AllowedWhenPendingAndAudited(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{
		Status: tseStatusPendingKickoff, Country: "GB",
		Identity: tseBusinessIdentity{LegalName: "L", OwnerName: "O", TaxNumber: "DE123456789", Address: "A"},
	}); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/settings/dismiss-tse-provisioning", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager dismiss of pending state: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if st, _ := loadTSEProvisioningState(t.Context(), d); st != nil {
		t.Fatalf("state survived dismiss: %+v", st)
	}
	if strings.Contains(getSettingsAs(t, mux, mgrUser), `data-testid="tse-provisioning"`) {
		t.Fatal("TSE chip still shown after dismiss")
	}
	entries, err := data.NewPOSRepo(d.Db).ListAudit(t.Context(), data.AuditFilters{EntityType: "fiscal"})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Action == "tse_provisioning_dismissed" && e.EntityID == "tse" && e.ActorID == mgrUser.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("no tse_provisioning_dismissed audit entry by %s; entries=%+v", mgrUser.ID, entries)
	}
}

// A non-hard-gated country's state (RequiresHardGate false) stays fully
// dismissible even when unresolved — the block is about hard-gated markets
// only, checked via fiscal.RequiresHardGate, never a hardcoded "DE".
func TestDismissTSEProvisioning_NonHardGatedCountryStillDismissible(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{
		Status: tseStatusKickoffRejected, Country: "GB", ErrorCode: "not_configured",
		Identity: tseBusinessIdentity{LegalName: "L", OwnerName: "O", TaxNumber: "DE123456789", Address: "A"},
	}); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/settings/dismiss-tse-provisioning", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("dismiss of a non-hard-gated country's rejected state: code=%d, want 200", rec.Code)
	}
	if st, _ := loadTSEProvisioningState(t.Context(), d); st != nil {
		t.Fatalf("state survived dismiss: %+v", st)
	}
}

// --- POST /api/settings/retry-tse-provisioning (ut-docs#1174) ---

// A manager can manually re-attempt a definitively rejected kickoff (e.g.
// after activating the subscription the cloud said was missing): the state
// requeues to pending_kickoff, ONE synchronous time-boxed attempt runs, and
// the response re-renders the block with the new state.
func TestRetryTSEProvisioning_RetriesRejectedKickoff(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	configureTSECloud(d, cloud.server.URL)

	if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{
		Status: tseStatusKickoffRejected, Country: "DE", ErrorCode: "subscription_inactive",
		Identity: tseBusinessIdentity{LegalName: "L", OwnerName: "O", TaxNumber: "DE123456789", Address: "A"},
	}); err != nil {
		t.Fatal(err)
	}

	// Manager-gated exactly like dismiss.
	rec := postForm(mux, "/api/settings/retry-tse-provisioning", url.Values{}, &cashUser)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier retry: code=%d, want 403", rec.Code)
	}
	if cloud.provisionCount() != 0 {
		t.Fatalf("a forbidden retry still hit the cloud (%d calls)", cloud.provisionCount())
	}

	rec = postForm(mux, "/api/settings/retry-tse-provisioning", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager retry: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if cloud.provisionCount() != 1 {
		t.Fatalf("provision calls = %d, want exactly 1 synchronous attempt", cloud.provisionCount())
	}
	st, _ := loadTSEProvisioningState(t.Context(), d)
	if st == nil || st.Status != tseStatusAwaitingReady {
		t.Fatalf("state after accepted retry = %+v, want awaiting_ready", st)
	}
	if st.Identity.TaxNumber != "DE123456789" {
		t.Fatalf("identity lost on retry: %+v", st)
	}
	// The response is the re-rendered block (htmx swaps it in place), now
	// showing the awaiting_ready message — no Retry button any more.
	body := rec.Body.String()
	if !strings.Contains(body, `data-testid="tse-provisioning"`) {
		t.Fatalf("retry response is not the re-rendered block: %s", body)
	}
	if strings.Contains(body, `data-testid="tse-provisioning-retry"`) {
		t.Fatalf("awaiting_ready block still offers Retry: %s", body)
	}
	// The manual retry is audited with the real acting user, not "system".
	entries, err := data.NewPOSRepo(d.Db).ListAudit(t.Context(), data.AuditFilters{EntityType: "fiscal", ActorID: mgrUser.ID})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	statuses := map[string]bool{}
	for _, e := range entries {
		if e.Action == "tse_provisioning_state_changed" && e.EntityID == "tse" {
			statuses[e.DataJSON] = true
		}
	}
	wantPending, wantAwaiting := false, false
	for raw := range statuses {
		if strings.Contains(raw, tseStatusPendingKickoff) {
			wantPending = true
		}
		if strings.Contains(raw, tseStatusAwaitingReady) {
			wantAwaiting = true
		}
	}
	if !wantPending || !wantAwaiting {
		t.Fatalf("retry transitions not audited under actor %s: %+v", mgrUser.ID, entries)
	}
}

// A retry with nothing retryable stored (no state at all, or one that isn't
// a failure) answers a clean 409 — never a crash, never a cloud call.
func TestRetryTSEProvisioning_NoRetryableStateIsCleanError(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	configureTSECloud(d, cloud.server.URL)

	rec := postForm(mux, "/api/settings/retry-tse-provisioning", url.Values{}, &mgrUser)
	if rec.Code != http.StatusConflict {
		t.Fatalf("retry with no state: code=%d, want 409", rec.Code)
	}

	if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{
		Status: tseStatusAwaitingReady, Country: "DE",
		Identity: tseBusinessIdentity{LegalName: "L", OwnerName: "O", TaxNumber: "DE123456789", Address: "A"},
	}); err != nil {
		t.Fatal(err)
	}
	rec = postForm(mux, "/api/settings/retry-tse-provisioning", url.Values{}, &mgrUser)
	if rec.Code != http.StatusConflict {
		t.Fatalf("retry of awaiting_ready: code=%d, want 409", rec.Code)
	}
	if cloud.provisionCount() != 0 {
		t.Fatalf("non-retryable state still hit the cloud (%d calls)", cloud.provisionCount())
	}
}

// credential_failed is retryable too: a fresh kickoff mints a fresh
// single-use credential record (ADR-0053 Decision 1).
func TestRetryTSEProvisioning_RetriesCredentialFailed(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	configureTSECloud(d, cloud.server.URL)

	if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{
		Status: tseStatusCredentialFailed, Country: "DE", ErrorCode: "http_500",
		Identity: tseBusinessIdentity{LegalName: "L", OwnerName: "O", TaxNumber: "DE123456789", Address: "A"},
	}); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/settings/retry-tse-provisioning", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry of credential_failed: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if cloud.provisionCount() != 1 {
		t.Fatalf("provision calls = %d, want 1", cloud.provisionCount())
	}
	st, _ := loadTSEProvisioningState(t.Context(), d)
	if st == nil || st.Status != tseStatusAwaitingReady {
		t.Fatalf("state = %+v, want awaiting_ready", st)
	}
}

// A retry that requeues to pending_kickoff but then hits a TRANSIENT cloud
// failure (unreachable/5xx, not a definitive rejection) must leave the
// record just as undismissible as the kickoff_rejected it started from —
// the shop still has no working TSE and every sale is still refused.
// Regression for a review finding: the first cut of
// tseProvisioningDismissBlocked only covered kickoff_rejected/
// credential_failed, so this exact sequence (retry -> transient failure ->
// dismiss) silently discarded the record via a status the block-list didn't
// enumerate.
func TestDismissTSEProvisioning_BlockedRegardlessOfStatusAfterRetry(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	configureTSECloud(d, cloud.server.URL)
	cloud.provisionStatus = http.StatusInternalServerError // transient, not a definitive rejection

	if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{
		Status: tseStatusKickoffRejected, Country: "DE", ErrorCode: "subscription_inactive",
		Identity: tseBusinessIdentity{LegalName: "L", OwnerName: "O", TaxNumber: "DE123456789", Address: "A"},
	}); err != nil {
		t.Fatal(err)
	}

	rec := postForm(mux, "/api/settings/retry-tse-provisioning", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry: code=%d body=%s", rec.Code, rec.Body.String())
	}
	st, _ := loadTSEProvisioningState(t.Context(), d)
	if st == nil || st.Status != tseStatusPendingKickoff {
		t.Fatalf("state after a transient retry failure = %+v, want pending_kickoff", st)
	}
	// The re-rendered block must still show the blocked hint, not a Dismiss
	// button, for this pending_kickoff (server-side truth, not just the
	// handler's own answer below).
	if strings.Contains(rec.Body.String(), `data-testid="tse-provisioning-dismiss"`) {
		t.Fatalf("pending_kickoff block still offers Dismiss after retry: %s", rec.Body.String())
	}

	dismissRec := postForm(mux, "/api/settings/dismiss-tse-provisioning", url.Values{}, &mgrUser)
	if dismissRec.Code != http.StatusConflict {
		t.Fatalf("dismiss of a hard-gated pending_kickoff (post-retry): code=%d, want 409", dismissRec.Code)
	}
	if st, _ := loadTSEProvisioningState(t.Context(), d); st == nil {
		t.Fatal("state was cleared by a blocked dismiss")
	}
}

// A credential_failed state can end up with an EMPTY stored identity
// (dismiss a pending_kickoff — allowed pre-fix, or any state whose identity
// was never completed — then a later fiscal_tse_ready directive's credential
// fetch fails with nothing left to re-send). Retry must refuse rather than
// POST an empty business_identity to the cloud.
func TestRetryTSEProvisioning_IncompleteIdentityIsCleanError(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	configureTSECloud(d, cloud.server.URL)

	if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{
		Status: tseStatusCredentialFailed, Country: "DE", ErrorCode: "credential_unavailable",
	}); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/settings/retry-tse-provisioning", url.Values{}, &mgrUser)
	if rec.Code != http.StatusConflict {
		t.Fatalf("retry with no complete identity: code=%d, want 409", rec.Code)
	}
	if cloud.provisionCount() != 0 {
		t.Fatalf("incomplete-identity retry still hit the cloud (%d calls)", cloud.provisionCount())
	}
	st, _ := loadTSEProvisioningState(t.Context(), d)
	if st == nil || st.Status != tseStatusCredentialFailed {
		t.Fatalf("state changed by a refused retry: %+v", st)
	}
}

// --- audit trail for automatic transitions (ut-docs#1174 item E) ---

// The wizard-driven kickoff audits its outcome as actor "system": a
// definitive rejection lands a tse_provisioning_state_changed entry with the
// rejected status and error code.
func TestTSEAudit_WizardKickoffRejected(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	restoreCurrencyAfter(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	cloud.provisionStatus = http.StatusForbidden
	cloud.provisionErrCode = "subscription_inactive"
	configureTSECloud(d, cloud.server.URL)

	if rec := postForm(mux, "/api/setup", deWizardForm(), nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("wizard: code=%d", rec.Code)
	}
	entries, err := data.NewPOSRepo(d.Db).ListAudit(t.Context(), data.AuditFilters{EntityType: "fiscal", ActorID: "system"})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Action == "tse_provisioning_state_changed" && e.EntityID == "tse" &&
			strings.Contains(e.DataJSON, tseStatusKickoffRejected) && strings.Contains(e.DataJSON, "subscription_inactive") {
			found = true
		}
	}
	if !found {
		t.Fatalf("rejected kickoff not audited as system; entries=%+v", entries)
	}
}

// An accepted kickoff audits the awaiting_ready transition too.
func TestTSEAudit_KickoffAccepted(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	_ = mux
	initTestPaths(t)
	if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{
		Status: tseStatusPendingKickoff, Country: "DE",
		Identity: tseBusinessIdentity{LegalName: "L", OwnerName: "O", TaxNumber: "DE123456789", Address: "A"},
	}); err != nil {
		t.Fatal(err)
	}
	cloud := newFakeTSECloud(t)
	configureTSECloud(d, cloud.server.URL)
	tseProvisionRetryTick(t.Context(), d)

	ok, err := data.NewPOSRepo(d.Db).HasAuditEntry(t.Context(), "fiscal", "tse", "tse_provisioning_state_changed")
	if err != nil || !ok {
		t.Fatalf("accepted kickoff not audited: ok=%v err=%v", ok, err)
	}
}

// The credential fetch's success and failure paths both audit: success as a
// final "configured" transition, failure as credential_failed.
func TestTSEAudit_CredentialOutcomes(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	configureTSECloud(d, cloud.server.URL)
	repo := data.NewPOSRepo(d.Db)

	// Failure first: a 500 on the credential fetch audits credential_failed.
	cloud.credentialStatus = http.StatusInternalServerError
	if _, err := applyFiscalTSEReady(t.Context(), d); err == nil {
		t.Fatal("want error")
	}
	entries, err := repo.ListAudit(t.Context(), data.AuditFilters{EntityType: "fiscal", ActorID: "system"})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	foundFailed := false
	for _, e := range entries {
		if e.Action == "tse_provisioning_state_changed" && strings.Contains(e.DataJSON, tseStatusCredentialFailed) {
			foundFailed = true
		}
	}
	if !foundFailed {
		t.Fatalf("credential failure not audited; entries=%+v", entries)
	}

	// Then success: the stored-and-confirmed credential audits "configured".
	cloud.credentialStatus = 0
	if _, err := applyFiscalTSEReady(t.Context(), d); err != nil {
		t.Fatalf("applyFiscalTSEReady: %v", err)
	}
	entries, err = repo.ListAudit(t.Context(), data.AuditFilters{EntityType: "fiscal", ActorID: "system"})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	foundConfigured := false
	for _, e := range entries {
		if e.Action == "tse_provisioning_state_changed" && strings.Contains(e.DataJSON, "configured") {
			foundConfigured = true
		}
	}
	if !foundConfigured {
		t.Fatalf("configured transition not audited; entries=%+v", entries)
	}
}

// --- wizard-inline surfacing of a rejected kickoff (ut-docs#1174 item D) ---

// A fast definitive rejection during the wizard's own synchronous attempt is
// surfaced to the operator immediately: the wizard's completion redirect
// carries the marker, and the sale screen it lands on renders the same
// message Settings shows — no navigating to Settings required.
func TestSetupWizardDE_RejectedKickoffSurfacesInline(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	restoreCurrencyAfter(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	cloud.provisionStatus = http.StatusServiceUnavailable
	cloud.provisionErrCode = "not_configured"
	configureTSECloud(d, cloud.server.URL)
	registerIndex(mux, d)

	rec := postForm(mux, "/api/setup", deWizardForm(), nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("wizard: code=%d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if loc != "/?tse_setup=rejected" {
		t.Fatalf("Location = %q, want /?tse_setup=rejected", loc)
	}

	get := func(path string) string {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = auth.WithUser(req, mgrUser)
		r := httptest.NewRecorder()
		mux.ServeHTTP(r, req)
		if r.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, r.Code, r.Body.String())
		}
		return r.Body.String()
	}
	if !strings.Contains(get("/?tse_setup=rejected"), `data-testid="tse-kickoff-rejected-banner"`) {
		t.Fatal("sale screen after the wizard redirect does not surface the rejected kickoff")
	}
	// Without the marker (any later plain visit) the sale screen stays
	// clean — Settings owns the persistent surfacing.
	if strings.Contains(get("/"), `data-testid="tse-kickoff-rejected-banner"`) {
		t.Fatal("banner must not render without the wizard's redirect marker")
	}
}

// A transient (still pending) kickoff adds no marker and no banner — the
// background ticker plus Settings already cover it — so the wizard lands on
// the import page's welcome mode like any other completion (ut-docs#3709).
func TestSetupWizardDE_PendingKickoffDoesNotSurfaceInline(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	restoreCurrencyAfter(t)
	initTestPaths(t)
	configureTSECloud(d, unreachableURL())

	rec := postForm(mux, "/api/setup", deWizardForm(), nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("wizard: code=%d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/import?welcome=1" {
		t.Fatalf("Location = %q, want /import?welcome=1 for a still-pending kickoff", loc)
	}
}

// The redirect marker alone must not conjure a banner when the stored state
// is not actually kickoff_rejected (stale bookmark, hand-typed URL).
func TestIndexTSEBanner_RequiresRealRejectedState(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	registerIndex(mux, d)
	if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{
		Status: tseStatusPendingKickoff, Country: "DE",
		Identity: tseBusinessIdentity{LegalName: "L", OwnerName: "O", TaxNumber: "DE123456789", Address: "A"},
	}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/?tse_setup=rejected", nil)
	req = auth.WithUser(req, mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `data-testid="tse-kickoff-rejected-banner"`) {
		t.Fatal("banner rendered from the query param alone, without a rejected state stored")
	}
}

// --- main till only (ut-docs#3039, #2969) ---

// seedTSEStateOnReplica makes d a till that follows a main till and gives it
// the main till's synced provisioning record, the way the admin pull would.
// The record is written straight to the store: saveTSEProvisioningState
// itself refuses on a replica.
func seedTSEStateOnReplica(t *testing.T, d *common.Deps, st tseProvisioningState) {
	t.Helper()
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Settings.Set(t.Context(), common.KeyTSEProvisioningState, string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://main-till.local:8080"); err != nil {
		t.Fatal(err)
	}
}

func tseStateRaw(t *testing.T, d *common.Deps) string {
	t.Helper()
	v, _, err := d.Settings.Get(t.Context(), common.KeyTSEProvisioningState)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// ADR-0053 provisions one TSE per store, from the main till. A replica that
// receives fiscal_tse_ready must neither spend the single-use credential
// handoff nor write either shop-wide key: the credential would sit on the
// replica's disk, so "configured" would be false, and the next admin pull
// would revert both writes anyway.
func TestApplyFiscalTSEReady_ReplicaWritesNeitherKey(t *testing.T) {
	d := newTSEReadyDeps(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	configureTSECloud(d, cloud.server.URL)
	seedTSEStateOnReplica(t, d, tseProvisioningState{
		Status: tseStatusAwaitingReady, Country: "DE",
		Identity: tseBusinessIdentity{LegalName: "L", OwnerName: "O", TaxNumber: "DE123456789", Address: "A"},
	})
	before := tseStateRaw(t, d)

	if _, err := applyFiscalTSEReady(t.Context(), d); err == nil {
		t.Fatal("applyFiscalTSEReady on a replica succeeded, want a refusal")
	}
	if n := cloud.credentialCount(); n != 0 {
		t.Fatalf("replica fetched the single-use credential (%d calls)", n)
	}
	if v, _, _ := d.Settings.Get(t.Context(), fiscal.SigningDeviceConfiguredKey("DE")); v != "" {
		t.Fatalf("replica wrote %s = %q", fiscal.SigningDeviceConfiguredKey("DE"), v)
	}
	if after := tseStateRaw(t, d); after != before {
		t.Fatalf("replica rewrote %s: %q -> %q", common.KeyTSEProvisioningState, before, after)
	}
	if _, ok, _ := fiscal.NewSigningDeviceCredentialStore().Load(); ok {
		t.Fatal("replica stored a TSE credential")
	}
}

// The same holds when the replica already has a credential file on disk
// (the idempotent re-serve fast path): it must not flip the shop-wide row.
func TestApplyFiscalTSEReady_ReplicaWithLocalCredentialStillRefuses(t *testing.T) {
	d := newTSEReadyDeps(t)
	initTestPaths(t)
	if err := fiscal.NewSigningDeviceCredentialStore().Save(map[string]any{"api_key": "k"}); err != nil {
		t.Fatal(err)
	}
	seedTSEStateOnReplica(t, d, tseProvisioningState{Status: tseStatusAwaitingReady, Country: "DE"})

	if _, err := applyFiscalTSEReady(t.Context(), d); err == nil {
		t.Fatal("applyFiscalTSEReady on a replica succeeded, want a refusal")
	}
	if v, _, _ := d.Settings.Get(t.Context(), fiscal.SigningDeviceConfiguredKey("DE")); v != "" {
		t.Fatalf("replica wrote %s = %q", fiscal.SigningDeviceConfiguredKey("DE"), v)
	}
}

// ut-docs#2969: the retry tick reads the synced pending_kickoff state; on a
// replica it must not POST a kickoff for the store or rewrite the state.
func TestTSEProvisionRetryTick_SkipsOnReplica(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	configureTSECloud(d, cloud.server.URL)
	seedTSEStateOnReplica(t, d, tseProvisioningState{
		Status: tseStatusPendingKickoff, Country: "DE",
		Identity: tseBusinessIdentity{LegalName: "L", OwnerName: "O", TaxNumber: "DE123456789", Address: "A"},
	})
	before := tseStateRaw(t, d)

	tseProvisionRetryTick(t.Context(), d)

	if n := cloud.provisionCount(); n != 0 {
		t.Fatalf("replica posted %d kickoff(s)", n)
	}
	if after := tseStateRaw(t, d); after != before {
		t.Fatalf("replica rewrote the provisioning state: %q -> %q", before, after)
	}
}

// Settings on a replica shows the main till's synced record; its retry and
// dismiss answer 409 "change this on the main till" and change nothing.
func TestTSEProvisioningSettingsActions_RefusedOnReplica(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	configureTSECloud(d, cloud.server.URL)
	seedTSEStateOnReplica(t, d, tseProvisioningState{
		Status: tseStatusKickoffRejected, Country: "GB", ErrorCode: "subscription_inactive",
		Identity: tseBusinessIdentity{LegalName: "L", OwnerName: "O", TaxNumber: "DE123456789", Address: "A"},
	})
	before := tseStateRaw(t, d)
	want := httpx.T("en", "settings.error.change_on_main_till")

	page := getSettingsAs(t, mux, mgrUser)
	if !strings.Contains(page, `data-testid="tse-provisioning-main-till-only"`) {
		t.Fatal("replica Settings lacks the act-on-the-main-till hint")
	}
	for _, id := range []string{"tse-provisioning-retry", "tse-provisioning-dismiss"} {
		if strings.Contains(page, `data-testid="`+id+`"`) {
			t.Fatalf("replica Settings renders %s, which can only answer 409", id)
		}
	}

	for _, path := range []string{"/api/settings/retry-tse-provisioning", "/api/settings/dismiss-tse-provisioning"} {
		rec := postForm(mux, path, url.Values{}, &mgrUser)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("%s on a replica: code=%d body=%q, want 409 %q", path, rec.Code, rec.Body.String(), want)
		}
	}
	if n := cloud.provisionCount(); n != 0 {
		t.Fatalf("replica posted %d kickoff(s)", n)
	}
	if after := tseStateRaw(t, d); after != before {
		t.Fatalf("replica rewrote the provisioning state: %q -> %q", before, after)
	}
}

// ADR-0148 §2 review (ut-docs#3615): the operator window follows an
// operator action only. The background retry ticker's accepted kickoff
// (actor tseSystemActor) must not open one — nobody acted on this till —
// while a manager's manual retry that is accepted does.
func TestTSEKickoffOpensOperatorWindowOnlyForAnOperator(t *testing.T) {
	seed := func(t *testing.T, d *common.Deps) {
		t.Helper()
		if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{
			Status: tseStatusPendingKickoff, Country: "DE",
			Identity: tseBusinessIdentity{LegalName: "L", OwnerName: "O", TaxNumber: "DE123456789", Address: "A"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("background retry", func(t *testing.T) {
		windows := recordWindows(t)
		_, _, d := newFullAuthDeps(t)
		initTestPaths(t)
		seed(t, d)
		cloud := newFakeTSECloud(t)
		configureTSECloud(d, cloud.server.URL)
		tseProvisionRetryTick(t.Context(), d)
		if st, _ := loadTSEProvisioningState(t.Context(), d); st == nil || st.Status != tseStatusAwaitingReady {
			t.Fatalf("precondition: kickoff not accepted: %+v", st)
		}
		if w := windows(); len(w) != 0 {
			t.Fatalf("the background retry opened an operator window %v; only an operator action may", w)
		}
	})
	t.Run("manager retry", func(t *testing.T) {
		windows := recordWindows(t)
		_, _, d := newFullAuthDeps(t)
		initTestPaths(t)
		seed(t, d)
		cloud := newFakeTSECloud(t)
		configureTSECloud(d, cloud.server.URL)
		tseKickoffAttempt(t.Context(), d, mustLoadTSEState(t, d), "m1")
		if w := windows(); len(w) != 1 || w[0] != operatorCheckinWindow {
			t.Fatalf("windows = %v, want [%v] after a manager's accepted kickoff", w, operatorCheckinWindow)
		}
	})
}

func mustLoadTSEState(t *testing.T, d *common.Deps) *tseProvisioningState {
	t.Helper()
	st, err := loadTSEProvisioningState(t.Context(), d)
	if err != nil || st == nil {
		t.Fatalf("load TSE state: %v (%v)", err, st)
	}
	return st
}

// --- ut-docs#7050: restored till whose TSE credential did not travel ---

func seedRestoredTill(t *testing.T, d *common.Deps) {
	t.Helper()
	for k, v := range map[string]string{
		tseCredentialHeldKey:                    "true",
		fiscal.SigningDeviceConfiguredKey("DE"): "true",
	} {
		if err := d.Settings.Set(t.Context(), k, v); err != nil {
			t.Fatal(err)
		}
	}
}

func tseSettingVal(t *testing.T, d *common.Deps, key string) string {
	t.Helper()
	v, _, err := d.Settings.Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func credMissingIdentityForm() url.Values {
	return url.Values{
		"tse_identity_form": {"1"},
		"tse_legal_name":    {"Ecke Laden GmbH"},
		"tse_owner_name":    {"Erika Muster"},
		"tse_tax_number":    {"12/345/67890"},
		"tse_address":       {"Hauptstr. 1, 10115 Berlin"},
	}
}

func TestApplyFiscalTSEReady_SetsCredentialHeldMarker(t *testing.T) {
	d := newTSEReadyDeps(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	configureTSECloud(d, cloud.server.URL)
	if _, err := applyFiscalTSEReady(t.Context(), d); err != nil {
		t.Fatalf("applyFiscalTSEReady: %v", err)
	}
	if v := tseSettingVal(t, d, tseCredentialHeldKey); v != "true" {
		t.Fatalf("marker = %q, want true", v)
	}
	if v := tseSettingVal(t, d, fiscal.SigningDeviceConfiguredKey("DE")); v != "true" {
		t.Fatalf("configured.de = %q", v)
	}
}

func TestApplyFiscalTSEReady_IdempotentPathSetsMarker(t *testing.T) {
	d := newTSEReadyDeps(t)
	initTestPaths(t)
	if err := fiscal.NewSigningDeviceCredentialStore().Save(map[string]any{"api_key": "k"}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyFiscalTSEReady(t.Context(), d); err != nil {
		t.Fatalf("applyFiscalTSEReady: %v", err)
	}
	if v := tseSettingVal(t, d, tseCredentialHeldKey); v != "true" {
		t.Fatalf("marker = %q, want true", v)
	}
}

func TestReconcileTSECredentialMarker_BackfillsWhenCredentialReadable(t *testing.T) {
	d := newTSEReadyDeps(t)
	initTestPaths(t)
	if err := fiscal.NewSigningDeviceCredentialStore().Save(map[string]any{"api_key": "k"}); err != nil {
		t.Fatal(err)
	}
	reconcileTSECredentialMarker(t.Context(), d)
	if v := tseSettingVal(t, d, tseCredentialHeldKey); v != "true" {
		t.Fatalf("marker = %q, want true", v)
	}
	reconcileTSECredentialMarker(t.Context(), d) // idempotent
	if v := tseSettingVal(t, d, tseCredentialHeldKey); v != "true" {
		t.Fatalf("marker after second run = %q", v)
	}
	if st, _ := loadTSEProvisioningState(t.Context(), d); st != nil {
		t.Fatalf("backfill wrote a provisioning state: %+v", st)
	}
}

func TestReconcileTSECredentialMarker_NoFileNoMarker(t *testing.T) {
	d := newTSEReadyDeps(t)
	initTestPaths(t)
	// Manual own device: configured, no marker, no credential file.
	if err := d.Settings.Set(t.Context(), fiscal.SigningDeviceConfiguredKey("DE"), "true"); err != nil {
		t.Fatal(err)
	}
	reconcileTSECredentialMarker(t.Context(), d)
	if v := tseSettingVal(t, d, tseCredentialHeldKey); v != "" {
		t.Fatalf("marker = %q, want unset", v)
	}
	if st, _ := loadTSEProvisioningState(t.Context(), d); st != nil {
		t.Fatalf("state written for a manual own device: %+v", st)
	}
}

func TestReconcileTSECredentialMarker_ReplicaWritesNothing(t *testing.T) {
	t.Run("credential readable", func(t *testing.T) {
		d := newTSEReadyDeps(t)
		initTestPaths(t)
		if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://main-till.local:8080"); err != nil {
			t.Fatal(err)
		}
		if err := fiscal.NewSigningDeviceCredentialStore().Save(map[string]any{"api_key": "k"}); err != nil {
			t.Fatal(err)
		}
		reconcileTSECredentialMarker(t.Context(), d)
		if v := tseSettingVal(t, d, tseCredentialHeldKey); v != "" {
			t.Fatalf("replica wrote marker %q", v)
		}
	})
	t.Run("credential missing", func(t *testing.T) {
		d := newTSEReadyDeps(t)
		initTestPaths(t)
		seedRestoredTill(t, d)
		if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://main-till.local:8080"); err != nil {
			t.Fatal(err)
		}
		reconcileTSECredentialMarker(t.Context(), d)
		if v := tseStateRaw(t, d); v != "" {
			t.Fatalf("replica wrote state %q", v)
		}
	})
}

func TestReconcileTSECredentialMarker_RestoredInstallFlagsCredentialMissing(t *testing.T) {
	d := newTSEReadyDeps(t)
	initTestPaths(t)
	seedRestoredTill(t, d)
	gateBefore, err := fiscal.EvaluateGate(t.Context(), d.Settings, "DE", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}

	reconcileTSECredentialMarker(t.Context(), d)

	st, _ := loadTSEProvisioningState(t.Context(), d)
	if st == nil || st.Status != tseStatusCredentialMissing || st.Country != "DE" {
		t.Fatalf("state = %+v, want credential_missing/DE", st)
	}
	if v := tseSettingVal(t, d, fiscal.SigningDeviceConfiguredKey("DE")); v != "true" {
		t.Fatalf("configured.de = %q, want still true", v)
	}
	if v := tseSettingVal(t, d, tseCredentialHeldKey); v != "true" {
		t.Fatalf("marker = %q, want untouched true", v)
	}
	entries, err := data.NewPOSRepo(d.Db).ListAudit(t.Context(), data.AuditFilters{EntityType: "fiscal", ActorID: "system"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Action == "tse_provisioning_state_changed" && strings.Contains(e.DataJSON, tseStatusCredentialMissing) {
			found = true
		}
	}
	if !found {
		t.Fatalf("credential_missing not audited: %+v", entries)
	}
	gateAfter, err := fiscal.EvaluateGate(t.Context(), d.Settings, "DE", time.Now().UTC())
	if err != nil || gateAfter.Decision != gateBefore.Decision {
		t.Fatalf("gate changed: before=%v after=%v err=%v", gateBefore.Decision, gateAfter.Decision, err)
	}
}

func TestReconcileTSECredentialMarker_CorruptFileTreatedAsMissing(t *testing.T) {
	d := newTSEReadyDeps(t)
	initTestPaths(t)
	seedRestoredTill(t, d)
	store := fiscal.NewSigningDeviceCredentialStore()
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	reconcileTSECredentialMarker(t.Context(), d)
	if st, _ := loadTSEProvisioningState(t.Context(), d); st == nil || st.Status != tseStatusCredentialMissing {
		t.Fatalf("state = %+v, want credential_missing", st)
	}
}

func TestReconcileTSECredentialMarker_NeverOverwritesExistingRecord(t *testing.T) {
	for _, status := range []string{tseStatusPendingKickoff, tseStatusAwaitingReady, tseStatusKickoffRejected, tseStatusCredentialFailed} {
		t.Run(status, func(t *testing.T) {
			d := newTSEReadyDeps(t)
			initTestPaths(t)
			seedRestoredTill(t, d)
			if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{Status: status, Country: "DE"}); err != nil {
				t.Fatal(err)
			}
			before := tseStateRaw(t, d)
			reconcileTSECredentialMarker(t.Context(), d)
			if after := tseStateRaw(t, d); after != before {
				t.Fatalf("record overwritten: %q -> %q", before, after)
			}
		})
	}
}

func TestSettingsTSEBlock_CredentialMissingShowsIdentityFormNoDismiss(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	initTestPaths(t)
	if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{
		Status: tseStatusCredentialMissing, Country: "DE", ErrorCode: "credential_missing",
	}); err != nil {
		t.Fatal(err)
	}
	page := getSettingsAs(t, mux, mgrUser)
	for _, want := range []string{
		html.EscapeString(httpx.T("en", "settings.tse.credential_missing")),
		`data-testid="tse-identity-form"`, `data-testid="tse-provisioning-retry"`,
		`name="tse_legal_name"`, `name="tse_owner_name"`, `name="tse_tax_number"`, `name="tse_address"`,
		`name="tse_identity_form"`, `data-testid="tse-provisioning-dismiss-blocked"`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("settings lacks %q", want)
		}
	}
	if strings.Contains(page, `data-testid="tse-provisioning-dismiss"`) {
		t.Fatal("dismiss rendered for a hard-gated market")
	}
	if strings.Contains(page, "autofocus") && strings.Contains(page, `data-testid="tse-identity-form"`) &&
		strings.Contains(page[strings.Index(page, `data-testid="tse-identity-form"`):], `autofocus`) {
		// autofocus is only allowed on the 422 alert
		t.Fatal("initial render must not autofocus")
	}
}

func TestRetryTSEProvisioning_CredentialMissingWithIdentityKicksOff(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	setCountry(t, d, "DE")
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	configureTSECloud(d, cloud.server.URL)
	seedRestoredTill(t, d)
	reconcileTSECredentialMarker(t.Context(), d)

	rec := postForm(mux, "/api/settings/retry-tse-provisioning", credMissingIdentityForm(), &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry: code=%d body=%s", rec.Code, rec.Body.String())
	}
	st, _ := loadTSEProvisioningState(t.Context(), d)
	if st == nil || st.Status != tseStatusAwaitingReady || st.Identity.LegalName != "Ecke Laden GmbH" || st.Identity.TaxNumber != "12/345/67890" {
		t.Fatalf("state = %+v, want awaiting_ready with the entered identity", st)
	}
	if cloud.provisionCount() != 1 {
		t.Fatalf("kickoffs = %d, want 1", cloud.provisionCount())
	}
	if _, err := applyFiscalTSEReady(t.Context(), d); err != nil {
		t.Fatalf("applyFiscalTSEReady: %v", err)
	}
	store := fiscal.NewSigningDeviceCredentialStore()
	fi, err := os.Stat(store.Path())
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("credential file: %v mode=%v", err, fi)
	}
	if v := tseSettingVal(t, d, tseCredentialHeldKey); v != "true" {
		t.Fatalf("marker = %q", v)
	}
	if st, _ := loadTSEProvisioningState(t.Context(), d); st != nil {
		t.Fatalf("state not cleared: %+v", st)
	}
}

func TestRetryTSEProvisioning_IdentityFormInvalidIs422(t *testing.T) {
	long := strings.Repeat("x", maxTSEIdentityFieldLen+1)
	cases := []struct {
		name    string
		mutate  func(url.Values)
		wantKey string
	}{
		{"all blank", func(v url.Values) {
			for _, k := range []string{"tse_legal_name", "tse_owner_name", "tse_tax_number", "tse_address"} {
				v.Set(k, "")
			}
		}, "setup.error.tse_identity_incomplete"},
		{"partial", func(v url.Values) { v.Set("tse_address", "") }, "setup.error.tse_identity_incomplete"},
		{"bad tax number", func(v url.Values) { v.Set("tse_tax_number", "nope") }, "setup.error.tse_tax_number"},
		{"too long", func(v url.Values) { v.Set("tse_legal_name", long) }, "setup.error.tse_identity_too_long"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux, _, d := newFullAuthDeps(t)
			initTestPaths(t)
			cloud := newFakeTSECloud(t)
			configureTSECloud(d, cloud.server.URL)
			if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{
				Status: tseStatusCredentialMissing, Country: "DE", ErrorCode: "credential_missing",
			}); err != nil {
				t.Fatal(err)
			}
			before := tseStateRaw(t, d)
			form := credMissingIdentityForm()
			tc.mutate(form)
			rec := postForm(mux, "/api/settings/retry-tse-provisioning", form, &mgrUser)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("code=%d, want 422; body=%s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if !strings.Contains(body, html.EscapeString(httpx.T("en", tc.wantKey))) {
				t.Fatalf("body lacks error text for %s", tc.wantKey)
			}
			if !strings.Contains(body, `role="alert"`) || !strings.Contains(body, `data-testid="tse-identity-form"`) {
				t.Fatal("422 body must re-render the form with an alert")
			}
			if tc.name == "bad tax number" && !strings.Contains(body, `value="nope"`) {
				t.Fatal("entered value not re-shown")
			}
			if tc.name == "bad tax number" && !strings.Contains(body, `aria-invalid="true"`) {
				t.Fatal("offending field not aria-invalid")
			}
			if tseStateRaw(t, d) != before {
				t.Fatal("state changed by a refused retry")
			}
			if cloud.provisionCount() != 0 {
				t.Fatal("cloud was called")
			}
		})
	}
}

func TestRetryTSEProvisioning_CredentialMissingOfflineLeavesPending(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	initTestPaths(t)
	configureTSECloud(d, unreachableURL())
	if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{
		Status: tseStatusCredentialMissing, Country: "DE", ErrorCode: "credential_missing",
	}); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/settings/retry-tse-provisioning", credMissingIdentityForm(), &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, want 200", rec.Code)
	}
	st, _ := loadTSEProvisioningState(t.Context(), d)
	if st == nil || st.Status != tseStatusPendingKickoff || st.Identity.LegalName != "Ecke Laden GmbH" {
		t.Fatalf("state = %+v, want pending_kickoff with identity kept", st)
	}
	if strings.Contains(rec.Body.String(), `data-testid="tse-identity-form"`) {
		t.Fatal("form must disappear once the identity is stored")
	}
}

func TestRetryTSEProvisioning_ManualOwnDeviceStill409(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	initTestPaths(t)
	cloud := newFakeTSECloud(t)
	configureTSECloud(d, cloud.server.URL)
	if err := d.Settings.Set(t.Context(), fiscal.SigningDeviceConfiguredKey("DE"), "true"); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/settings/retry-tse-provisioning", credMissingIdentityForm(), &mgrUser)
	if rec.Code != http.StatusConflict {
		t.Fatalf("code=%d, want 409", rec.Code)
	}
	if cloud.provisionCount() != 0 {
		t.Fatal("cloud was called")
	}
}

func TestSettingsTSEBlock_CredentialFailedWithIdentityKeepsOneClickRetry(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	initTestPaths(t)
	if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{
		Status: tseStatusCredentialFailed, Country: "DE", ErrorCode: "credential_unavailable",
		Identity: tseBusinessIdentity{LegalName: "L", OwnerName: "O", TaxNumber: "DE123456789", Address: "A"},
	}); err != nil {
		t.Fatal(err)
	}
	page := getSettingsAs(t, mux, mgrUser)
	if !strings.Contains(page, `data-testid="tse-provisioning-retry"`) {
		t.Fatal("one-click retry missing")
	}
	if strings.Contains(page, `data-testid="tse-identity-form"`) {
		t.Fatal("identity form shown although the identity is stored")
	}
}

func TestSettingsTSEBlock_CredentialMissingOnReplicaIsHintOnly(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	initTestPaths(t)
	seedTSEStateOnReplica(t, d, tseProvisioningState{Status: tseStatusCredentialMissing, Country: "DE"})
	page := getSettingsAs(t, mux, mgrUser)
	if !strings.Contains(page, `data-testid="tse-provisioning-main-till-only"`) {
		t.Fatal("replica hint missing")
	}
	for _, id := range []string{"tse-identity-form", "tse-provisioning-retry", "tse-provisioning-dismiss"} {
		if strings.Contains(page, `data-testid="`+id+`"`) {
			t.Fatalf("replica renders %s", id)
		}
	}
}

// --- ut-docs#7050 fix round (Opus 5.5 findings) ---

// Finding 1: only "not exist" / decode errors mean "credential missing". Any
// other read error (here: the credential path is unreadable as a file) must
// not record credential_missing — Retry would revoke a working key.
func TestReconcileTSECredentialMarker_UnreadableCredentialWritesNothing(t *testing.T) {
	d := newTSEReadyDeps(t)
	initTestPaths(t)
	seedRestoredTill(t, d)
	store := fiscal.NewSigningDeviceCredentialStore()
	if err := os.MkdirAll(store.Path(), 0o700); err != nil { // a directory where the file belongs: ReadFile fails, not "not exist"
		t.Fatal(err)
	}
	reconcileTSECredentialMarker(t.Context(), d)
	if v := tseStateRaw(t, d); v != "" {
		t.Fatalf("a transient read error wrote state %q", v)
	}
}

// Finding 1b: the credential reads fine and the record says it is missing
// (restored, then the file came back / re-provisioned): clear and audit.
func TestReconcileTSECredentialMarker_ClearsCredentialMissingWhenCredentialReadable(t *testing.T) {
	d := newTSEReadyDeps(t)
	initTestPaths(t)
	seedRestoredTill(t, d)
	if err := fiscal.NewSigningDeviceCredentialStore().Save(map[string]any{"api_key": "k"}); err != nil {
		t.Fatal(err)
	}
	if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{
		Status: tseStatusCredentialMissing, Country: "DE", ErrorCode: "credential_missing",
	}); err != nil {
		t.Fatal(err)
	}
	reconcileTSECredentialMarker(t.Context(), d)
	if v := tseStateRaw(t, d); v != "" {
		t.Fatalf("credential_missing record not cleared: %q", v)
	}
	entries, err := data.NewPOSRepo(d.Db).ListAudit(t.Context(), data.AuditFilters{EntityType: "fiscal", ActorID: "system"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Action == "tse_provisioning_state_changed" && strings.Contains(e.DataJSON, tseStatusCredentialRestored) {
			found = true
		}
	}
	if !found {
		t.Fatalf("clearing not audited: %+v", entries)
	}
}

func TestReconcileTSECredentialMarker_KeepsOtherRecordsWhenCredentialReadable(t *testing.T) {
	d := newTSEReadyDeps(t)
	initTestPaths(t)
	seedRestoredTill(t, d)
	if err := fiscal.NewSigningDeviceCredentialStore().Save(map[string]any{"api_key": "k"}); err != nil {
		t.Fatal(err)
	}
	if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{Status: tseStatusAwaitingReady, Country: "DE"}); err != nil {
		t.Fatal(err)
	}
	before := tseStateRaw(t, d)
	reconcileTSECredentialMarker(t.Context(), d)
	if after := tseStateRaw(t, d); after != before {
		t.Fatalf("record changed: %q -> %q", before, after)
	}
}

// Finding 4a: marker + no file + configured flag false -> nothing recorded.
func TestReconcileTSECredentialMarker_MarkerNoFileNotConfiguredWritesNothing(t *testing.T) {
	d := newTSEReadyDeps(t)
	initTestPaths(t)
	if err := d.Settings.Set(t.Context(), tseCredentialHeldKey, "true"); err != nil {
		t.Fatal(err)
	}
	reconcileTSECredentialMarker(t.Context(), d)
	if v := tseStateRaw(t, d); v != "" {
		t.Fatalf("state written although the shop is not configured: %q", v)
	}
}

// Finding 4b: init.go must call the boot check. bootTSECredentialCheck is
// the one function init calls; this fails if it stops reconciling, and
// TestInit_CallsBootTSECredentialCheck fails if init stops calling it.
func TestBootTSECredentialCheck_FlagsRestoredTill(t *testing.T) {
	d := newTSEReadyDeps(t)
	initTestPaths(t)
	seedRestoredTill(t, d)
	bootTSECredentialCheck(t.Context(), d)
	if st, _ := loadTSEProvisioningState(t.Context(), d); st == nil || st.Status != tseStatusCredentialMissing {
		t.Fatalf("state = %+v, want credential_missing", st)
	}
}

func TestInit_CallsBootTSECredentialCheck(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0) // chdirRoot moves the cwd: locate init.go beside this file
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(filepath.Dir(thisFile), "init.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var initFn *ast.FuncDecl
	for _, decl := range f.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == "Init" {
			initFn = fd
		}
	}
	if initFn == nil || initFn.Body == nil {
		t.Fatal("init.go has no func Init")
	}
	// Position of the first real call to each name inside Init (a comment or
	// a string never produces a CallExpr).
	callPos := func(name string) token.Pos {
		var pos token.Pos
		ast.Inspect(initFn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var id string
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				id = fn.Name
			case *ast.SelectorExpr:
				id = fn.Sel.Name
			}
			if id == name && (pos == token.NoPos || call.Pos() < pos) {
				pos = call.Pos()
			}
			return true
		})
		return pos
	}
	boot, retry := callPos("bootTSECredentialCheck"), callPos("StartTSEProvisionRetry")
	if boot == token.NoPos {
		t.Fatal("Init no longer calls bootTSECredentialCheck (ut-docs#7050)")
	}
	if retry == token.NoPos {
		t.Fatal("Init no longer calls StartTSEProvisionRetry: the ordering check has nothing to compare against")
	}
	if boot >= retry {
		t.Fatal("Init calls bootTSECredentialCheck after StartTSEProvisionRetry; it must run before the TSE retry service (ut-docs#7050)")
	}
}

// Round 2 (1): the backfill is for the pre-#7050 install only (marker row
// unset). A marker an owner's manual configured write set to "false" is
// never re-armed by a credential file that is still on disk.
func TestReconcileTSECredentialMarker_DoesNotRearmExplicitFalse(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	setCountry(t, d, "DE")
	initTestPaths(t)
	if err := fiscal.NewSigningDeviceCredentialStore().Save(map[string]any{"api_key": "k"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Settings.Set(t.Context(), tseCredentialHeldKey, "true"); err != nil {
		t.Fatal(err)
	}
	admin := auth.User{ID: "m1", Role: "admin"}
	if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"fiscal.signing_device_configured"}, "value": {"true"}}, &admin); rec.Code != http.StatusNoContent {
		t.Fatalf("upsert: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if v := tseSettingVal(t, d, tseCredentialHeldKey); v != "false" {
		t.Fatalf("precondition: marker = %q, want false", v)
	}
	bootTSECredentialCheck(t.Context(), d)
	if v := tseSettingVal(t, d, tseCredentialHeldKey); v == "true" {
		t.Fatal("boot backfill re-armed a marker the owner's manual configured write cleared")
	}
}

// Round 2 (2): only a real transition of the configured flag clears the marker.
func TestSettingsUpsert_ResavingSameConfiguredValueKeepsMarker(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	setCountry(t, d, "DE")
	for k, v := range map[string]string{
		fiscal.SigningDeviceConfiguredKey("DE"): "true",
		tseCredentialHeldKey:                    "true",
	} {
		if err := d.Settings.Set(t.Context(), k, v); err != nil {
			t.Fatal(err)
		}
	}
	admin := auth.User{ID: "m1", Role: "admin"}
	if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"fiscal.signing_device_configured"}, "value": {"true"}}, &admin); rec.Code != http.StatusNoContent {
		t.Fatalf("upsert: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if v := tseSettingVal(t, d, tseCredentialHeldKey); v != "true" {
		t.Fatalf("no-op re-save cleared the marker: %q", v)
	}
	if rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"fiscal.signing_device_configured"}, "value": {"false"}}, &admin); rec.Code != http.StatusNoContent {
		t.Fatalf("upsert false: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if v := tseSettingVal(t, d, tseCredentialHeldKey); v != "false" {
		t.Fatalf("real change left the marker %q, want false", v)
	}
}

func TestSyncSettingsApply_ResavingSameConfiguredValueKeepsMarker(t *testing.T) {
	m := newSyncSettingsTestDeps(t)
	ctx := t.Context()
	body := settingsApplyBody(t, syncSettingsApplyRequest{Settings: one("fiscal.signing_device_configured", "true"), ActorID: "adm-1"})
	if rec := postSyncSettingsApply(m.mux, body, syncSettingsBearer); rec.Code != http.StatusOK {
		t.Fatalf("first apply: %d %s", rec.Code, rec.Body.String())
	}
	if err := m.dp.Settings.Set(ctx, tseCredentialHeldKey, "true"); err != nil {
		t.Fatal(err)
	}
	if rec := postSyncSettingsApply(m.mux, body, syncSettingsBearer); rec.Code != http.StatusOK {
		t.Fatalf("re-apply: %d %s", rec.Code, rec.Body.String())
	}
	if v := mustSetting(t, m.dp, tseCredentialHeldKey); v != "true" {
		t.Fatalf("no-op re-apply cleared the marker: %q", v)
	}
	body = settingsApplyBody(t, syncSettingsApplyRequest{Settings: one("fiscal.signing_device_configured", "false"), ActorID: "adm-1"})
	if rec := postSyncSettingsApply(m.mux, body, syncSettingsBearer); rec.Code != http.StatusOK {
		t.Fatalf("change apply: %d %s", rec.Code, rec.Body.String())
	}
	if v := mustSetting(t, m.dp, tseCredentialHeldKey); v != "false" {
		t.Fatalf("real change left the marker %q, want false", v)
	}
}

// Round 2 (4): an additional till never writes the marker locally; the
// configured flag goes to the main till, which owns the marker.
func TestSettingsUpsert_FollowerDoesNotWriteMarkerLocally(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	setCountry(t, d, "DE")
	var gotBody atomic.Value
	main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody.Store(string(b))
		http.Error(w, "stub main refuses", http.StatusTeapot)
	}))
	t.Cleanup(main.Close)
	for k, v := range map[string]string{
		"sync.primary_url":   main.URL,
		"sync.bearer":        "test-bearer",
		tseCredentialHeldKey: "true",
	} {
		if err := d.Settings.Set(t.Context(), k, v); err != nil {
			t.Fatal(err)
		}
	}
	admin := auth.User{ID: "m1", Role: "admin"}
	postForm(mux, "/api/settings/upsert", url.Values{"key": {"fiscal.signing_device_configured"}, "value": {"true"}}, &admin)
	if v := tseSettingVal(t, d, tseCredentialHeldKey); v != "true" {
		t.Fatalf("a follower till wrote the marker locally: %q", v)
	}
	b, _ := gotBody.Load().(string)
	if b == "" {
		t.Fatal("the write never reached the stub main till")
	}
	if strings.Contains(b, tseCredentialHeldKey) {
		t.Fatalf("follower sent the marker to the main till: %s", b)
	}
}

// Finding 2: the marker is cleared when the configured flag is set manually,
// and the marker key itself is not writable through the free-text upsert,
// the sync apply path or a cloud directive.
func TestSettingsUpsert_ConfiguredFlagClearsCredentialHeldMarker(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	setCountry(t, d, "DE")
	if err := d.Settings.Set(t.Context(), tseCredentialHeldKey, "true"); err != nil {
		t.Fatal(err)
	}
	admin := auth.User{ID: "m1", Role: "admin"}
	rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"fiscal.signing_device_configured"}, "value": {"true"}}, &admin)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("upsert: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if v := tseSettingVal(t, d, fiscal.SigningDeviceConfiguredKey("DE")); v != "true" {
		t.Fatalf("configured.de = %q", v)
	}
	if v := tseSettingVal(t, d, tseCredentialHeldKey); v == "true" {
		t.Fatalf("marker still %q after a manual configured write", v)
	}
}

func TestSettingsUpsert_RefusesCredentialHeldMarker(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	admin := auth.User{ID: "m1", Role: "admin"}
	for _, val := range []string{"true", "false", ""} {
		rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {tseCredentialHeldKey}, "value": {val}}, &admin)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("value %q: code=%d, want 400", val, rec.Code)
		}
	}
	if v := tseSettingVal(t, d, tseCredentialHeldKey); v != "" {
		t.Fatalf("marker written: %q", v)
	}
}

func TestRejectRemoteFiscalPostureWrite_RefusesCredentialHeldMarker(t *testing.T) {
	d := newCloudSyncTestDeps(t)
	if err := rejectRemoteFiscalPostureWrite(d, tseCredentialHeldKey); err == nil {
		t.Fatal("a cloud directive may not write the credential-held marker")
	}
}

func TestSyncSettingsApply_CredentialHeldMarker(t *testing.T) {
	m := newSyncSettingsTestDeps(t)
	body := settingsApplyBody(t, syncSettingsApplyRequest{Settings: one(tseCredentialHeldKey, "false"), ActorID: "adm-1"})
	wantSyncSettingsError(t, postSyncSettingsApply(m.mux, body, syncSettingsBearer), http.StatusBadRequest, "fiscal_not_settable")

	if err := m.dp.Settings.Set(t.Context(), tseCredentialHeldKey, "true"); err != nil {
		t.Fatal(err)
	}
	body = settingsApplyBody(t, syncSettingsApplyRequest{Settings: one("fiscal.signing_device_configured", "true"), ActorID: "adm-1"})
	if rec := postSyncSettingsApply(m.mux, body, syncSettingsBearer); rec.Code != http.StatusOK {
		t.Fatalf("apply configured: %d %s", rec.Code, rec.Body.String())
	}
	if v := mustSetting(t, m.dp, tseCredentialHeldKey); v == "true" {
		t.Fatalf("marker still %q after a replica's manual configured write", v)
	}
}

// Finding 5: the identity-form 422 path.
func TestRetryTSEProvisioning_IdentityFormNoLongerNeededFallsThrough409(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	setCountry(t, d, "DE")
	initTestPaths(t)
	// Record moved on to pending_kickoff (identity complete): the form is not needed.
	if err := saveTSEProvisioningState(t.Context(), d, &tseProvisioningState{
		Status: tseStatusPendingKickoff, Country: "DE",
		Identity: tseBusinessIdentity{LegalName: "L", OwnerName: "O", TaxNumber: "DE123456789", Address: "A"},
	}); err != nil {
		t.Fatal(err)
	}
	form := credMissingIdentityForm()
	form.Set("tse_tax_number", "") // would be a 422 if the form were still needed
	rec := postForm(mux, "/api/settings/retry-tse-provisioning", form, &mgrUser)
	if rec.Code != http.StatusConflict {
		t.Fatalf("code=%d body=%s, want 409 nothing_to_retry", rec.Code, rec.Body.String())
	}
	// No record at all: same.
	if err := saveTSEProvisioningState(t.Context(), d, nil); err != nil {
		t.Fatal(err)
	}
	rec = postForm(mux, "/api/settings/retry-tse-provisioning", form, &mgrUser)
	if rec.Code != http.StatusConflict {
		t.Fatalf("no record: code=%d, want 409", rec.Code)
	}
}

func TestRetryTSEProvisioning_IdentityFormLoadErrorIs500(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	setCountry(t, d, "DE")
	initTestPaths(t)
	if err := d.Settings.Set(t.Context(), common.KeyTSEProvisioningState, "{not json"); err != nil {
		t.Fatal(err)
	}
	form := credMissingIdentityForm()
	form.Set("tse_tax_number", "")
	rec := postForm(mux, "/api/settings/retry-tse-provisioning", form, &mgrUser)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d body=%s, want 500", rec.Code, rec.Body.String())
	}
}

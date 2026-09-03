package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
)

// newSetupRestoreImportDeps wires BOTH registerSetup and registerImport onto
// one mux against a REAL, fully-migrated database (appdb.Open) — ut-docs#1168's
// wizard-preview-to-commit bridge (commitStagedImportForSetup) drives an
// in-process request from the setup handler straight into the import
// handler, so a test of it needs both actually registered together, on a
// schema where role_permissions really does grant import_export to the
// admin role the wizard just created (045_import_issue_reporting_permissions.sql)
// — a hand-rolled fixture schema, like newFullAuthDeps's, doesn't carry that
// grant at all and would make every commit attempt read as unauthorized.
func newSetupRestoreImportDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	initAuthTestI18n(t)
	d, err := appdb.Open(filepath.Join(t.TempDir(), "setup-restore-import.db"))
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	cfg := &config.Config{Theme: "default"}
	pm, err := plugins.Init(t.Context(), cfg, d.DB)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	store := settings.NewStore(d.DB)
	svc := auth.NewService(d.DB)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       d.DB,
		Settings: store,
		AuthSvc:  svc,
		Engine:   pos.NewServiceWithResolver(pos.Config{}, stubResolver{}),
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Shell:    common.NewShellChannel(common.DefaultWindowMode),
	}
	dp.Shell.MarkExitPath()
	mux := http.NewServeMux()
	registerSetup(mux, dp, svc)
	registerImport(mux, dp)
	return mux, dp
}

// previewViaWizard posts a preview (commit=0) straight to /api/import, the
// same request web/ui/pages/setup.html's step-6 upload form issues, and
// returns the staged_id its response embeds — failing the test if the
// preview didn't succeed or didn't stage (mirrors the real fragment's
// `<input name="staged_id" ... form="import-form">`).
func previewViaWizard(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	body, ct := multipartCSV(t, importCSV, map[string]string{"commit": "0"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview via /api/import: code=%d body=%s", rec.Code, rec.Body.String())
	}
	const marker = `name="staged_id" value="`
	i := strings.Index(rec.Body.String(), marker)
	if i == -1 {
		t.Fatalf("preview response has no staged_id input: %s", rec.Body.String())
	}
	rest := rec.Body.String()[i+len(marker):]
	return rest[:strings.Index(rest, `"`)]
}

// ut-docs#1168: the wizard's restore step lets a migrating shop preview
// their export file BEFORE any admin account exists — the exact anonymous,
// first-boot-only window POST /api/setup/join and friends already get.
func TestWizardRestoreImport_PreviewAllowedDuringFirstBoot(t *testing.T) {
	mux, _ := newSetupRestoreImportDeps(t)
	stagedID := previewViaWizard(t, mux)
	if stagedID == "" {
		t.Fatal("expected a non-empty staged_id")
	}
}

// Once an admin exists (first boot is over), the SAME anonymous request
// must be refused — the exemption is exactly as narrow as every other
// first-boot-only setup endpoint, not a standing hole in /api/import's
// auth gate.
func TestWizardRestoreImport_PreviewDeniedOnceFirstBootEnds(t *testing.T) {
	mux, dp := newSetupRestoreImportDeps(t)
	// End first boot the same way the wizard's own PIN step does.
	if _, err := dp.Db.Exec(`INSERT INTO users (id, username, display_name, role, pin_hash, is_active)
		VALUES ('admin-1', 'admin', 'Admin', 'admin', 'x', 1)`); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	body, ct := multipartCSV(t, importCSV, map[string]string{"commit": "0"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("preview after first boot ended: code=%d, want 403: %s", rec.Code, rec.Body.String())
	}
}

// The headline flow (ut-docs#1168 acceptance): browse, preview, finish the
// wizard, and land in a stocked catalog — no re-upload, no second click on
// /import. staged_import_id rides the final wizard submit; setup_page.go
// replays the preview as a real commit once country/currency are saved and
// the admin session exists.
func TestWizardRestoreImport_StagedPreviewAutoCommitsOnWizardFinish(t *testing.T) {
	mux, dp := newSetupRestoreImportDeps(t)
	stagedID := previewViaWizard(t, mux)

	rec := postForm(mux, "/api/setup", url.Values{
		"pin":              {"2468"},
		"pin_confirm":      {"2468"},
		"country":          {"GB"},
		"currency":         {"GBP"},
		"currency_touched": {"1"},
		"tax_rate_pct":     {"20"},
		"restore_choice":   {"csv_excel"},
		"staged_import_id": {stagedID},
	}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/catalog" {
		t.Fatalf("wizard finish with staged import: code=%d loc=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = 'W1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("imported item W1: count=%d err=%v, want 1", n, err)
	}
	if v, _, _ := dp.Settings.Get(t.Context(), common.KeyCurrencyConfirmed); v != "true" {
		t.Fatalf("currency_confirmed = %q, want true (the operator DID touch currency here)", v)
	}
}

// ut-docs#1168 review, finding 2 (blocker): a wizard run where the operator
// never touched the pre-filled country/currency step must NEVER auto-commit
// under that guessed currency — that would silently label every imported
// price under it AND permanently suppress every future manual import's real
// ut-docs#970 currency-confirm prompt. It must fall back to /import exactly
// like a failed replay, and currency_confirmed must stay unset.
func TestWizardRestoreImport_SkipsAutoCommitWhenCurrencyNeverTouched(t *testing.T) {
	mux, dp := newSetupRestoreImportDeps(t)
	stagedID := previewViaWizard(t, mux)

	rec := postForm(mux, "/api/setup", url.Values{
		"pin":              {"2468"},
		"pin_confirm":      {"2468"},
		"country":          {"GB"},
		"currency":         {"GBP"},
		"tax_rate_pct":     {"20"},
		"restore_choice":   {"csv_excel"},
		"staged_import_id": {stagedID},
		// currency_touched intentionally omitted — the untouched, pre-filled-
		// default case.
	}, nil)
	wantLoc := "/import?staged_id=" + stagedID
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != wantLoc {
		t.Fatalf("wizard finish, currency never touched: code=%d loc=%q, want %q; body=%s",
			rec.Code, rec.Header().Get("Location"), wantLoc, rec.Body.String())
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = 'W1'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("nothing should auto-commit under an unconfirmed currency: count=%d err=%v, want 0", n, err)
	}
	if v, ok, _ := dp.Settings.Get(t.Context(), common.KeyCurrencyConfirmed); ok && v == "true" {
		t.Fatalf("currency_confirmed must stay unset — the operator never actually confirmed a currency")
	}
}

// ut-docs#1168 review, finding 5: the first-boot exemption on POST
// /api/import must cover PREVIEW only. An anonymous commit=1 attempt during
// the same window the wizard's own preview is allowed in must still be
// refused — the wizard's upload panel never sends commit=1 itself (the
// bridge to a real commit is commitStagedImportForSetup, which is always
// authenticated via auth.WithUser), so this exemption never needs to cover
// it, and leaving it open would let an anonymous LAN client write catalog
// rows and switch+confirm the till's currency before any admin exists.
func TestWizardRestoreImport_AnonymousCommitDeniedEvenDuringFirstBoot(t *testing.T) {
	mux, dp := newSetupRestoreImportDeps(t)
	stagedID := previewViaWizard(t, mux)

	body, ct := multipartCSV(t, importCSV, map[string]string{"commit": "1", "staged_id": stagedID, "confirm_currency": "GBP"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("anonymous commit during first boot: code=%d, want 403: %s", rec.Code, rec.Body.String())
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = 'W1'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("anonymous commit must not have written anything: count=%d err=%v, want 0", n, err)
	}
}

// ut-docs#1168 review, finding 4: a preview served to the wizard (wizard=1)
// must never render the problem-grid corrections, the barcode opt-in
// checkbox, or the repeated bottom Import button — none of them are wired
// to anything in the wizard (no #import-commit element, no field-forwarding
// in commitStagedImportForSetup), so left in they would silently discard
// the operator's corrections or do nothing when clicked. The exact same
// preview WITHOUT wizard=1 (the standalone /import page) must still offer
// all of them — this is a rendering suppression, not a pipeline change.
func TestWizardRestoreImport_SuppressesInteractiveControlsInWizardPreview(t *testing.T) {
	mux, _ := newSetupRestoreImportDeps(t)
	// A row with a missing name is exactly what the problem grid exists
	// for (forceableImportIssue allows correcting it).
	csv := "Name,SKU,Barcode,Price,Category,In stock\n" +
		",BROKEN1,,1.50,Snacks,1\n"

	body, ct := multipartCSV(t, csv, map[string]string{"commit": "0", "wizard": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("wizard preview: code=%d body=%s", rec.Code, rec.Body.String())
	}
	wizardBody := rec.Body.String()
	for _, marker := range []string{`name="row_include_`, `name="use_item_numbers_as_barcodes"`, `onclick="document.getElementById('import-commit')`} {
		if strings.Contains(wizardBody, marker) {
			t.Fatalf("wizard preview must not render %q, got: %s", marker, wizardBody)
		}
	}

	body2, ct2 := multipartCSV(t, csv, map[string]string{"commit": "0"})
	req2 := httptest.NewRequest(http.MethodPost, "/api/import", body2)
	req2.Header.Set("Content-Type", ct2)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if !strings.Contains(rec2.Body.String(), `name="row_include_`) {
		t.Fatalf("a standalone (non-wizard) preview should still offer the problem grid, got: %s", rec2.Body.String())
	}
}

// ut-docs#1168 review, finding 1 (blocker): the fallback form's own encoding
// must actually work. htmx posts application/x-www-form-urlencoded unless a
// form declares multipart, and POST /api/import's first line is
// r.ParseMultipartForm — a urlencoded body 400s there before ever reaching
// the staged commit. This asserts the fix (enctype="multipart/form-data" on
// import.html's staged-only form) by driving the two encodings directly.
func TestImportPage_StagedCommitForm_MustBeMultipart(t *testing.T) {
	mux, dp := newSetupRestoreImportDeps(t)
	stagedID := previewViaWizard(t, mux)

	urlencoded := strings.NewReader(url.Values{"commit": {"1"}, "staged_id": {stagedID}, "confirm_currency": {"GBP"}}.Encode())
	req := httptest.NewRequest(http.MethodPost, "/api/import", urlencoded)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("urlencoded staged commit: code=%d, want 400 (proves multipart is genuinely required): %s", rec.Code, rec.Body.String())
	}

	// Authenticated as a real admin (per finding 5's fix, above, the
	// first-boot exemption no longer covers commit=1 at all — this half of
	// the test is specifically about the encoding, not the auth gate,
	// which TestWizardRestoreImport_AnonymousCommitDeniedEvenDuringFirstBoot
	// already covers on its own).
	body, ct := multipartCSV(t, "", map[string]string{"commit": "1", "staged_id": stagedID, "confirm_currency": "GBP"})
	req2 := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/import", body), auth.User{ID: "admin-1", Role: "admin"})
	req2.Header.Set("Content-Type", ct)
	// multipartCSV always writes a "file" field; the commit path only reads
	// it when staged_id is empty, so its (empty) presence here is harmless.
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("multipart staged commit: code=%d, want 200: %s", rec2.Code, rec2.Body.String())
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = 'W1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("multipart staged commit should have imported W1: count=%d err=%v", n, err)
	}
}

// A staged preview that can no longer be replayed (expired/already consumed
// — here simulated the same way, by discarding it out from under the
// wizard) must never lose the operator's work or crash the wizard's own
// response: fall back to exactly today's /import detour, now carrying
// staged_id so the file is still one click away rather than a re-upload.
func TestWizardRestoreImport_FallsBackToImportPageWhenReplayFails(t *testing.T) {
	mux, dp := newSetupRestoreImportDeps(t)
	stagedID := previewViaWizard(t, mux)
	discardStagedCatalogUpload(stagedID)

	rec := postForm(mux, "/api/setup", url.Values{
		"pin":              {"2468"},
		"pin_confirm":      {"2468"},
		"country":          {"GB"},
		"currency":         {"GBP"},
		"currency_touched": {"1"},
		"tax_rate_pct":     {"20"},
		"restore_choice":   {"csv_excel"},
		"staged_import_id": {stagedID},
	}, nil)
	wantLoc := "/import?staged_id=" + stagedID
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != wantLoc {
		t.Fatalf("wizard finish with expired staged import: code=%d loc=%q, want %q; body=%s",
			rec.Code, rec.Header().Get("Location"), wantLoc, rec.Body.String())
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = 'W1'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("nothing should have been imported: count=%d err=%v, want 0", n, err)
	}
}

// GET /import?staged_id=... (the fallback landing page above) renders the
// simplified "press Import" form — no file input, no re-preview — while a
// bare GET /import keeps today's normal upload form untouched.
func TestImportPage_StagedIDPrefillsCommitOnlyForm(t *testing.T) {
	mux, dp := newSetupRestoreImportDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO users (id, username, display_name, role, pin_hash, is_active)
		VALUES ('admin-1', 'admin', 'Admin', 'admin', 'x', 1)`); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/import?staged_id=deadbeef", nil)
	req = auth.WithUser(req, auth.User{ID: "admin-1", Role: "admin"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /import?staged_id=...: code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `name="staged_id" value="deadbeef"`) {
		t.Fatalf("expected staged_id prefilled, got: %s", body)
	}
	if strings.Contains(body, `type="file"`) {
		t.Fatalf("staged form must not re-offer a file picker, got: %s", body)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/import", nil)
	req2 = auth.WithUser(req2, auth.User{ID: "admin-1", Role: "admin"})
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if !strings.Contains(rec2.Body.String(), `type="file"`) {
		t.Fatalf("bare GET /import should still offer a file picker, got: %s", rec2.Body.String())
	}
}

// ut-docs#1515 (AC3/AC5): the wizard's own upload/preview form must carry
// the same busy-indicator/double-tap-guard pattern ut-docs#1510 gave
// import.html — a real .bkp (thousands of receipt PDFs to parse past) can
// leave the screen looking inert for a real stretch, and the wizard's form
// was the one ut-docs#1510 missed. Same template-derived style as
// TestSetupWizardEndpointsClearTheSessionWall: assert on the markup
// directly rather than driving Alpine/htmx, which no Go test can do.
func TestSetupWizardUploadFormHasBusyIndicator(t *testing.T) {
	mux, _ := newSetupRestoreImportDeps(t)
	rec := getSetup(mux, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup: code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		// Independent review (ut-docs#1515): these two are asserted as ONE
		// contiguous string on purpose. setup.html already carried
		// hx-disabled-elt="find button[type=submit]" on the step-99 join
		// form long before this card, so asserting it on its own would pass
		// identically with or without the fix — a vacuous assertion that
		// wouldn't notice the upload form losing its double-tap guard.
		// Pinning it next to this form's own hx-indicator ties it to this
		// element specifically.
		`hx-indicator="#setup-restore-busy" hx-disabled-elt="find button[type=submit]"`,
		`id="setup-restore-busy"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /setup missing %q (wizard upload form busy indicator)", want)
		}
	}
}

// ut-docs#1515 (AC1): the detected/pre-filled country's currency is correct
// far more often than not, so step 2's tile is rarely clicked, and
// ut-docs#970's currencyTouched safety gate then means the wizard's staged
// preview never even attempts to auto-commit (see
// TestWizardRestoreImport_SkipsAutoCommitWhenCurrencyNeverTouched — that
// behaviour is deliberate and must NOT change). The fix is an explicit
// confirmation control on the upload panel itself: checking it sets the
// very same currencyTouched flag a step-2 tile click sets, and the "Next"
// button must refuse to proceed with a staged import until either that
// flag is set OR there is nothing staged to commit. This can only assert on
// the markup (a Go test can't drive Alpine's @click/@change), but it pins
// the actual mechanism: the SAME currencyTouched flag, not a parallel one
// that the server-side commit gate never sees.
//
// Independent review (ut-docs#1515), finding 1 (blocker): both the control
// and Next's disabled condition must ALSO check `currency !== ”` — country
// detection can match no seeded country at all, leaving currency blank, and
// confirming a currency that isn't even shown would mark the till
// "confirmed" against an empty string (setup_page.go never actually
// persists KeyCurrencyConfirmed for a blank currency, but
// commitStagedImportForSetup's own gate only checks currencyTouched, so it
// would still run) — reopening the exact ut-docs#970 hole this control
// exists to close.
func TestSetupWizardUploadPanelHasExplicitCurrencyConfirmControl(t *testing.T) {
	mux, _ := newSetupRestoreImportDeps(t)
	rec := getSetup(mux, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup: code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`data-setup-confirm-currency`,
		`@change="currencyTouched = $event.target.checked"`,
		`:disabled="restoreStagedId !== '' && !currencyTouched && currency !== ''"`,
		// Second review round: the `currency !== ''` guard on the WATCHER is
		// load-bearing too, and was pinned by nothing — reverting just this
		// half left the whole suite green. Without it the latch fires for a
		// blank currency, so the block renders "confirm the currency:" with
		// nothing after it, and ticking it (which is what the block asks
		// for) sets currencyTouched — and setup_page.go's auto-commit gate
		// checks ONLY currencyTouched, not that the currency is real
		// (unlike the KeyCurrencyConfirmed set beside it, which does test
		// `v != "" && IsKnownCurrency(v)`), so the import would commit
		// against a currency nobody ever saw. Same non-vacuity reasoning as
		// the busy-indicator assertion above: pin the guard, not just the
		// expression that happens to sit next to it.
		`$watch('restoreStagedId', (v) => { if (v !== '' && !currencyTouched && currency !== '') neededConfirm = true })`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /setup missing %q (explicit currency-confirm control)", want)
		}
	}
}

// ut-docs#1515 (AC2): when a staged preview lands the operator on /import
// instead of auto-committing, they must be told WHY, not just handed a bare
// "press Import" — the overwhelmingly common reason is ut-docs#970's own
// currencyTouched gate, not a failure. GET /import must show the
// currency-specific explanation while the till's currency is genuinely
// unconfirmed, and fall back to the original generic message once it is.
func TestImportPage_StagedIDExplainsUnconfirmedCurrency(t *testing.T) {
	mux, dp := newSetupRestoreImportDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO users (id, username, display_name, role, pin_hash, is_active)
		VALUES ('admin-1', 'admin', 'Admin', 'admin', 'x', 1)`); err != nil {
		t.Fatalf("seed admin: %v", err)
	}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/import?staged_id=deadbeef", nil), auth.User{ID: "admin-1", Role: "admin"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /import?staged_id=... (currency unconfirmed): code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-setup-staged-import-unconfirmed-currency`) {
		t.Fatalf("expected the currency-unconfirmed explanation, got: %s", body)
	}
	if strings.Contains(body, `data-setup-staged-import>`) {
		t.Fatalf("must not ALSO render the generic staged-import message, got: %s", body)
	}

	// Confirm the currency exactly the way a real commit does (ut-docs#970),
	// then the same staged landing page must fall back to the original
	// generic message — the explanation is specifically about the
	// unconfirmed-currency case, not a permanent replacement.
	if err := dp.Settings.Set(t.Context(), common.KeyCurrencyConfirmed, "true"); err != nil {
		t.Fatalf("mark currency confirmed: %v", err)
	}
	req2 := auth.WithUser(httptest.NewRequest(http.MethodGet, "/import?staged_id=deadbeef", nil), auth.User{ID: "admin-1", Role: "admin"})
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	body2 := rec2.Body.String()
	if !strings.Contains(body2, `data-setup-staged-import>`) {
		t.Fatalf("expected the generic staged-import message once currency is confirmed, got: %s", body2)
	}
	if strings.Contains(body2, `data-setup-staged-import-unconfirmed-currency`) {
		t.Fatalf("must not show the unconfirmed-currency explanation once currency IS confirmed, got: %s", body2)
	}
}

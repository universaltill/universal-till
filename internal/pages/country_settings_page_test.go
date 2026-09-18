package pages

// Per-country settings admin page (universaltill/ut-docs#659): manager
// gating, a real render of web/ui/pages/country_settings.html, and the two
// behaviours that must survive the HTTP layer — the retention floor is
// refused, and "delete" on a builtin country restores it rather than
// removing it.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

func newCountrySettingsTestMux(t *testing.T) (*http.ServeMux, *data.CountrySettingsRepo, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	dbase := openPagesTestDB(t)
	t.Cleanup(func() { dbase.Close() })
	d := &common.Deps{Db: dbase, Settings: settings.NewStore(dbase), Menu: []common.MenuItem{{Href: "/", Label: "Home"}}, AuthSvc: auth.NewService(dbase)}
	mux := http.NewServeMux()
	registerCountrySettings(mux, d)
	return mux, data.NewCountrySettingsRepo(dbase), d
}

// ut-docs#902: GET /country-settings must be reachable under UT_AUTH=off
// with no session, matching every other admin page's canPerform(...,
// "settings") escape hatch (ut-docs#901's precedent). Before this fix,
// requireManager read auth.FromContext directly and failed closed
// permanently under UT_AUTH=off, since no session is ever set in that mode.
func TestCountrySettingsPage_ReachableUnderAuthOff(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, _ := newCountrySettingsTestMux(t)

	req := httptest.NewRequest(http.MethodGet, "/country-settings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /country-settings under UT_AUTH=off = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// Mutating handlers, not just the GET page, must also pick up canPerform's
// UT_AUTH=off bypass — independent review finding on ut-docs#901, applied
// here too (the GET-only regression test above only pins the read path).
func TestCountrySettingsPageCreate_ReachableUnderAuthOff(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, _ := newCountrySettingsTestMux(t)

	rec := postForm(mux, "/api/country-settings", url.Values{
		"code":             {"ZZ"},
		"currency":         {"GBP"},
		"currency_symbol":  {"£"},
		"tax_rate_pct":     {"20"},
		"archive_min_days": {strconv.FormatInt(data.GlobalArchiveMinDays, 10)},
	}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/country-settings" {
		t.Fatalf("create under UT_AUTH=off: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestCountrySettingsPagePermissions(t *testing.T) {
	mux, _, _ := newCountrySettingsTestMux(t)
	cashier := auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/country-settings", nil), cashier)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier GET /country-settings = %d, want 403", rec.Code)
	}
	// ut-docs#1458: GET /country-settings must render the full layout on
	// 403 too, not a bare rail-less body (same fix class as #1455).
	if body := rec.Body.String(); !strings.Contains(body, `class="nav"`) {
		t.Fatalf("cashier's 403 on GET /country-settings has no nav rail:\n%s", body)
	}
	if rec := postForm(mux, "/api/country-settings", url.Values{"code": {"GB"}}, &cashier); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier save = %d, want 403", rec.Code)
	}
	if rec := postForm(mux, "/api/country-settings/GB/delete", url.Values{}, &cashier); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier delete = %d, want 403", rec.Code)
	}
}

// TestCountrySettingsPageDefaultShowsOnlyShopCountry pins ut-docs#1024's
// core requirement: a single-shop merchant sees their own country's row by
// default, not all 14 seeded jurisdictions. Replaces the old
// TestCountrySettingsPageRendersSeededCountries, which asserted the
// unfiltered-by-design render this card explicitly changes.
func TestCountrySettingsPageDefaultShowsOnlyShopCountry(t *testing.T) {
	mux, _, d := newCountrySettingsTestMux(t)
	d.SetState(common.RuntimeState{Country: "DE"})
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/country-settings", nil), mgr)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager GET = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "DE") {
		t.Errorf("rendered page missing shop's own country %q", "DE")
	}
	if strings.Contains(body, "FR") {
		t.Errorf("default view rendered another seeded country (%q) — should show only the shop's own", "FR")
	}
}

// TestCountrySettingsPageShowAllCountries pins the explicit "show all
// countries" affordance (?all=1): every seeded country renders, same as
// the pre-#1024 unconditional behavior.
func TestCountrySettingsPageShowAllCountries(t *testing.T) {
	mux, _, d := newCountrySettingsTestMux(t)
	d.SetState(common.RuntimeState{Country: "DE"})
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/country-settings?all=1", nil), mgr)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager GET ?all=1 = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"DE", "FR", "GBP", "Country settings"} {
		if !strings.Contains(body, want) {
			t.Errorf("?all=1 rendered page missing %q — expected the full seeded list", want)
		}
	}
}

// TestCountrySettingsPageUnknownShopCountry_ShowsAllWithExplanation pins
// the review-found fallback: when the shop's configured country matches no
// row (shouldn't happen in practice, but defensively handled), the page
// shows every country with an explanation — and must NOT offer a "show
// only my country" link, since that link would just re-enter this same
// fallback (the review's finding: it was rendered but permanently inert).
func TestCountrySettingsPageUnknownShopCountry_ShowsAllWithExplanation(t *testing.T) {
	mux, _, d := newCountrySettingsTestMux(t)
	d.SetState(common.RuntimeState{Country: "ZZ"}) // not a seeded/custom code
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/country-settings", nil), mgr)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager GET = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"DE", "FR", "GBP"} {
		if !strings.Contains(body, want) {
			t.Errorf("unknown-shop-country fallback missing %q — expected the full seeded list", want)
		}
	}
	if !strings.Contains(body, "in this list") {
		t.Errorf("unknown-shop-country fallback missing its explanation text")
	}
	if strings.Contains(body, "Show only my country") {
		t.Errorf("unknown-shop-country fallback rendered a 'show only my country' link — it would just re-enter this same fallback")
	}
}

// TestCountrySettingsPageSave_FromAllView_RedirectsBackToAllView pins the
// review-found redirect gap: a save/delete made from the "show all
// countries" view (?all=1) must land back on that same view, not silently
// drop to the filtered default where the edited row — or a newly-added
// custom country — would be invisible and read as "nothing happened."
func TestCountrySettingsPageSave_FromAllView_RedirectsBackToAllView(t *testing.T) {
	mux, _, _ := newCountrySettingsTestMux(t)
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}

	rec := postForm(mux, "/api/country-settings?all=1", url.Values{
		"code":             {"FR"},
		"currency":         {"EUR"},
		"tax_rate_pct":     {"20"},
		"archive_min_days": {strconv.FormatInt(data.GlobalArchiveMinDays, 10)},
	}, &mgr)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save from all view = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/country-settings?all=1" {
		t.Errorf("save redirect = %q, want /country-settings?all=1 (must carry the view through)", loc)
	}

	rec = postForm(mux, "/api/country-settings/FR/delete?all=1", url.Values{}, &mgr)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete from all view = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/country-settings?all=1" {
		t.Errorf("delete redirect = %q, want /country-settings?all=1 (must carry the view through)", loc)
	}
}

// TestCountrySettingsPageSave_FromDefaultView_RedirectsToDefaultView proves
// the "all=1" carry-through is opt-in, not always-on: a save from the
// default filtered view must NOT pick up all=1 out of nowhere.
func TestCountrySettingsPageSave_FromDefaultView_RedirectsToDefaultView(t *testing.T) {
	mux, _, d := newCountrySettingsTestMux(t)
	d.SetState(common.RuntimeState{Country: "GB"})
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}

	rec := postForm(mux, "/api/country-settings", url.Values{
		"code":             {"GB"},
		"currency":         {"GBP"},
		"tax_rate_pct":     {"20"},
		"archive_min_days": {strconv.FormatInt(data.GlobalArchiveMinDays, 10)},
	}, &mgr)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save from default view = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/country-settings" {
		t.Errorf("save redirect = %q, want plain /country-settings", loc)
	}
}

func TestCountrySettingsPageSaveAndFloorRefusal(t *testing.T) {
	mux, repo, _ := newCountrySettingsTestMux(t)
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}
	ctx := t.Context()

	// A legitimate edit: raise retention and change the tax rate.
	raised := data.GlobalArchiveMinDays + 100
	rec := postForm(mux, "/api/country-settings", url.Values{
		"code":             {"DE"},
		"currency":         {"EUR"},
		"currency_symbol":  {"€"},
		"tax_rate_pct":     {"7"},
		"tax_inclusive":    {"1"},
		"archive_min_days": {strconv.FormatInt(raised, 10)},
	}, &mgr)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("manager save = %d, want 303", rec.Code)
	}
	de, _, err := repo.Get(ctx, "DE")
	if err != nil {
		t.Fatal(err)
	}
	if de.TaxRateBP != 700 {
		t.Errorf("tax rate = %d bp, want 700 (7%% entered as percent)", de.TaxRateBP)
	}
	if de.ArchiveMinDays != raised {
		t.Errorf("archive_min_days = %d, want %d", de.ArchiveMinDays, raised)
	}

	// Below the floor: refused, redirected to the specific error, and the
	// stored value is untouched.
	rec = postForm(mux, "/api/country-settings", url.Values{
		"code":             {"DE"},
		"currency":         {"EUR"},
		"tax_rate_pct":     {"7"},
		"archive_min_days": {strconv.FormatInt(data.GlobalArchiveMinDays-1, 10)},
	}, &mgr)
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "below_floor") {
		t.Errorf("redirect = %q, want the below_floor error", loc)
	}
	after, _, _ := repo.Get(ctx, "DE")
	if after.ArchiveMinDays != raised {
		t.Errorf("refused save changed retention to %d, want it left at %d", after.ArchiveMinDays, raised)
	}
}

// TestCountrySettingsPageSave_OriginalCodeMismatchRejected is the regression
// test for ut-docs#2405: the dialog's `code` field is locked read-only in
// edit mode, but that guard is client-side only. If it's ever bypassed, a
// save whose `original_code` (the row's real code) differs from the
// submitted `code` must be refused rather than silently upserting a NEW row
// — POST /api/country-settings has no other way to tell "editing DE" from
// "creating FR" apart, since code is both the primary key and a plain form
// field.
func TestCountrySettingsPageSave_OriginalCodeMismatchRejected(t *testing.T) {
	mux, repo, _ := newCountrySettingsTestMux(t)
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}
	ctx := t.Context()

	before, ok, err := repo.Get(ctx, "DE")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("seeded DE row missing")
	}

	// Attempt to save an edit of DE (original_code) under a different,
	// unseeded code (ZZ) — the shape a bypassed readonly field produces.
	rec := postForm(mux, "/api/country-settings", url.Values{
		"code":             {"ZZ"},
		"original_code":    {"DE"},
		"currency":         {"GBP"},
		"tax_rate_pct":     {"20"},
		"archive_min_days": {strconv.FormatInt(data.GlobalArchiveMinDays, 10)},
	}, &mgr)
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "code_changed") {
		t.Errorf("redirect = %q, want the code_changed error", loc)
	}

	// DE (the row actually being edited) must be byte-for-byte untouched,
	// and no new ZZ row created from the bypassed code.
	after, ok, err := repo.Get(ctx, "DE")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("DE row disappeared after a refused original_code mismatch")
	}
	if after != before {
		t.Errorf("DE was mutated by a save that should have been refused: before=%+v after=%+v", before, after)
	}
	if _, ok, err := repo.Get(ctx, "ZZ"); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Error("a refused original_code mismatch created a new ZZ row instead of being rejected")
	}
}

// TestCountrySettingsPageSave_OriginalCodeMatchingCodeSucceeds proves the
// new guard doesn't block the normal edit path: original_code equal to the
// submitted code (the real dialog's prefilled state on every legitimate
// edit) must save exactly as before.
func TestCountrySettingsPageSave_OriginalCodeMatchingCodeSucceeds(t *testing.T) {
	mux, repo, _ := newCountrySettingsTestMux(t)
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}
	ctx := t.Context()

	rec := postForm(mux, "/api/country-settings", url.Values{
		"code":             {"DE"},
		"original_code":    {"DE"},
		"currency":         {"EUR"},
		"tax_rate_pct":     {"7"},
		"archive_min_days": {strconv.FormatInt(data.GlobalArchiveMinDays, 10)},
	}, &mgr)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("edit with matching original_code = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	de, _, err := repo.Get(ctx, "DE")
	if err != nil {
		t.Fatal(err)
	}
	if de.TaxRateBP != 700 {
		t.Errorf("tax rate = %d bp, want 700", de.TaxRateBP)
	}
}

// TestCountrySettingsPageCreate_BlankOriginalCodeSucceeds proves the new
// guard doesn't block create: the hidden field's template default is blank
// (record-dialog.js's create-mode reset never fills it from a row), and a
// blank original_code must never be treated as a mismatch.
func TestCountrySettingsPageCreate_BlankOriginalCodeSucceeds(t *testing.T) {
	mux, repo, _ := newCountrySettingsTestMux(t)
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}
	ctx := t.Context()

	rec := postForm(mux, "/api/country-settings", url.Values{
		"code":             {"ZZ"},
		"original_code":    {""},
		"currency":         {"GBP"},
		"tax_rate_pct":     {"20"},
		"archive_min_days": {strconv.FormatInt(data.GlobalArchiveMinDays, 10)},
	}, &mgr)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create with blank original_code = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	if _, ok, err := repo.Get(ctx, "ZZ"); err != nil {
		t.Fatal(err)
	} else if !ok {
		t.Error("create with blank original_code did not create the row")
	}
}

// The htmx-boosted dialog path must surface the same refusal in-dialog, same
// shape as every other refusal on this page (below_floor, empty code).
func TestCountrySettingsPage_HtmxOriginalCodeMismatchRendersInDialogMessage(t *testing.T) {
	mux, repo, _ := newCountrySettingsTestMux(t)
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}
	ctx := t.Context()

	rec := postFormHtmx(mux, "/api/country-settings", url.Values{
		"code":             {"ZZ"},
		"original_code":    {"DE"},
		"currency":         {"GBP"},
		"tax_rate_pct":     {"20"},
		"archive_min_days": {strconv.FormatInt(data.GlobalArchiveMinDays, 10)},
	}, &mgr)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("htmx original_code mismatch: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Location") != "" {
		t.Errorf("htmx refusal must not redirect")
	}
	if body := rec.Body.String(); strings.Contains(body, "<form") || strings.Contains(body, "<dialog") {
		t.Errorf("refusal response must be message-only: %s", body)
	}
	if _, ok, err := repo.Get(ctx, "ZZ"); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Error("a refused htmx original_code mismatch created a new ZZ row instead of being rejected")
	}
}

// TestCountrySettingsPageSavePreservesDefaultLocale is the regression test
// for ut-docs#1027: this form has no default_locale field (it's not
// operator-editable here), so a save must preserve DE's seeded "de-DE"
// rather than blanking it — the same preserve-what-the-form-doesn't-carry
// contract this handler already applies to NameKey, right above it in the
// handler itself.
func TestCountrySettingsPageSavePreservesDefaultLocale(t *testing.T) {
	mux, repo, _ := newCountrySettingsTestMux(t)
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}
	ctx := t.Context()

	before, _, err := repo.Get(ctx, "DE")
	if err != nil {
		t.Fatal(err)
	}
	if before.DefaultLocale != "de-DE" {
		t.Fatalf("seeded DE.DefaultLocale = %q, want de-DE", before.DefaultLocale)
	}

	rec := postForm(mux, "/api/country-settings", url.Values{
		"code":             {"DE"},
		"currency":         {"EUR"},
		"currency_symbol":  {"€"},
		"tax_rate_pct":     {"7"},
		"tax_inclusive":    {"1"},
		"archive_min_days": {strconv.FormatInt(data.GlobalArchiveMinDays, 10)},
	}, &mgr)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("manager save = %d, want 303", rec.Code)
	}

	after, _, err := repo.Get(ctx, "DE")
	if err != nil {
		t.Fatal(err)
	}
	if after.DefaultLocale != "de-DE" {
		t.Errorf("DE.DefaultLocale after an unrelated tax-rate edit = %q, want unchanged de-DE", after.DefaultLocale)
	}
}

func TestCountrySettingsPageDeleteRestoresBuiltin(t *testing.T) {
	mux, repo, _ := newCountrySettingsTestMux(t)
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}
	ctx := t.Context()

	if rec := postForm(mux, "/api/country-settings", url.Values{
		"code":             {"DE"},
		"currency":         {"XXX"},
		"tax_rate_pct":     {"1"},
		"archive_min_days": {strconv.FormatInt(data.GlobalArchiveMinDays+500, 10)},
	}, &mgr); rec.Code != http.StatusSeeOther {
		t.Fatalf("save = %d", rec.Code)
	}

	if rec := postForm(mux, "/api/country-settings/DE/delete", url.Values{}, &mgr); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete = %d, want 303", rec.Code)
	}

	de, ok, err := repo.Get(ctx, "DE")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("deleting a builtin country through the page removed it; it must restore defaults")
	}
	if de.Currency != "EUR" || de.TaxRateBP != 1900 || de.ArchiveMinDays != data.GlobalArchiveMinDays {
		t.Errorf("DE not restored: {%q %d %d}", de.Currency, de.TaxRateBP, de.ArchiveMinDays)
	}
}

// Percent → basis points is the one lossy-looking conversion in this page;
// 8.5% must not land on 849 through float truncation.
func TestParsePercentAsBPRounding(t *testing.T) {
	cases := map[string]int64{"0": 0, "": 0, "7": 700, "19": 1900, "8.5": 850, "2.25": 225, "0.01": 1}
	for in, want := range cases {
		got, err := parsePercentAsBP(in)
		if err != nil {
			t.Errorf("parsePercentAsBP(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parsePercentAsBP(%q) = %d, want %d", in, got, want)
		}
	}
	if _, err := parsePercentAsBP("-1"); err == nil {
		t.Error("negative percent should error")
	}
	if _, err := parsePercentAsBP("abc"); err == nil {
		t.Error("non-numeric percent should error")
	}
}

func TestFormatBPAsPercent(t *testing.T) {
	cases := map[int64]string{0: "0", 700: "7", 1900: "19", 850: "8.50", 225: "2.25"}
	for in, want := range cases {
		if got := formatBPAsPercent(in); got != want {
			t.Errorf("formatBPAsPercent(%d) = %q, want %q", in, got, want)
		}
	}
}

// ut-docs#2116: /country-settings is one of the /admin tree's six
// destinations, converted to the same two-pane master-detail shell /items
// uses (ut-docs#1950). An htmx request (from that panel) must get just the
// "content" block, plus an out-of-band refresh of the admin tree with
// /country-settings marked is-current — not the full standalone page's
// chrome.
func TestCountrySettingsPage_HXRequestReturnsContentFragmentWithOOBAdminTree(t *testing.T) {
	mux, _, _ := newCountrySettingsTestMux(t)
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/country-settings", nil), mgr)
	req.Header.Set("HX-Request", "true")
	// ut-docs#2167 (mechanism updated by ut-docs#2178): real htmx always
	// sends HX-Target with the swap target's id, and admin_tree.html's row
	// targets #admin-panel. This test models a tree-row click — no
	// X-UT-Admin-Inline-Swap header — so writeAdminTreeOOB fires; see
	// TestCountrySettings_FragmentOmitsAdminTreeOnlyWhenInlineSwapMarked
	// for the other side of the guard (and the fail-open case).
	req.Header.Set("HX-Target", "admin-panel")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("htmx GET /country-settings: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "<html") || strings.Contains(body, `class="nav"`) {
		t.Errorf("htmx request re-rendered the whole page shell: %s", body)
	}
	railStart := strings.Index(body, `id="admin-tree"`)
	if railStart < 0 || !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Fatalf("fragment missing the OOB admin-tree swap: %s", body)
	}
	rail := body[railStart:]
	idx := strings.Index(rail, `href="/country-settings"`)
	if idx < 0 {
		t.Fatalf("OOB admin tree missing the /country-settings row: %s", rail)
	}
	tagStart := strings.LastIndex(rail[:idx], "<a ")
	tagEnd := strings.Index(rail[tagStart:], ">") + tagStart
	if !strings.Contains(rail[tagStart:tagEnd], "is-current") {
		t.Errorf("the /country-settings row itself is not marked is-current: %s", rail[tagStart:tagEnd])
	}
}

// A plain browser GET (no HX-Request) must still render the exact same full
// standalone page as before this card.
func TestCountrySettingsPage_NonHXRequestStillRendersFullPage(t *testing.T) {
	mux, _, _ := newCountrySettingsTestMux(t)
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/country-settings", nil), mgr)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /country-settings: %d %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "<html") || !strings.Contains(body, `class="nav"`) {
		t.Errorf("expected the full standalone page shell, got: %s", body)
	}
}

// ut-docs#2091's Vary requirement, extended to /country-settings now that
// it is a dual-mode destination too.
func TestCountrySettingsPage_VaryHXRequestOnBothBranches(t *testing.T) {
	mux, _, _ := newCountrySettingsTestMux(t)
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}

	fragReq := auth.WithUser(httptest.NewRequest(http.MethodGet, "/country-settings", nil), mgr)
	fragReq.Header.Set("HX-Request", "true")
	fragRec := httptest.NewRecorder()
	mux.ServeHTTP(fragRec, fragReq)
	if got := fragRec.Header().Get("Vary"); got != "HX-Request" {
		t.Errorf("fragment branch: Vary header = %q, want %q", got, "HX-Request")
	}

	fullReq := auth.WithUser(httptest.NewRequest(http.MethodGet, "/country-settings", nil), mgr)
	fullRec := httptest.NewRecorder()
	mux.ServeHTTP(fullRec, fullReq)
	if got := fullRec.Header().Get("Vary"); got != "HX-Request" {
		t.Errorf("full-page branch: Vary header = %q, want %q", got, "HX-Request")
	}
}

// ut-docs#2167: the two scope links were plain <a href> anchors, so
// clicking either from inside /admin's two-pane shell was a full
// navigation that tore the user out of #admin-panel and lost the tree's
// selection state. They must instead be in-panel htmx chips targeting the
// page's own subtree.
func TestCountrySettings_ScopeFilterIsInPanelNotFullNavigation(t *testing.T) {
	mux, _, d := newCountrySettingsTestMux(t)
	d.SetState(common.RuntimeState{Country: "DE"})
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/country-settings", nil), mgr)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager GET = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-get="/country-settings?all=1"`) {
		t.Errorf("default view missing the show-all chip's hx-get: %s", body)
	}
	if !strings.Contains(body, `hx-target="#country-settings-view"`) {
		t.Errorf("default view missing hx-target=\"#country-settings-view\": %s", body)
	}
	if !strings.Contains(body, `hx-select="#country-settings-view"`) {
		t.Errorf("default view missing hx-select=\"#country-settings-view\": %s", body)
	}
	if strings.Contains(body, `<a href="/country-settings?all=1"`) {
		t.Errorf("default view still renders the old full-navigation anchor: %s", body)
	}

	req = auth.WithUser(httptest.NewRequest(http.MethodGet, "/country-settings?all=1", nil), mgr)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager GET ?all=1 = %d, want 200", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, `hx-get="/country-settings"`) {
		t.Errorf("?all=1 view missing the show-mine chip's hx-get: %s", body)
	}
	if strings.Contains(body, `<a href="/country-settings"`) {
		t.Errorf("?all=1 view still renders the old full-navigation anchor: %s", body)
	}
}

// The pressed chip must track which view actually rendered, both ways.
func TestCountrySettings_ScopeFilterPressedStateFollowsView(t *testing.T) {
	mux, _, d := newCountrySettingsTestMux(t)
	d.SetState(common.RuntimeState{Country: "DE"})
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}

	// Pin aria-pressed to the SPECIFIC chip by finding that chip's own
	// <button> opening tag by id and reading the attribute out of it.
	//
	// The first cut of this matched a literal `aria-pressed="X"` followed
	// by a newline, ten spaces and the chip's hx-get — which pinned the
	// right chip but also pinned the template's exact indentation:
	// reindenting one attribute line by a single space, changing no
	// behaviour at all, failed the test. Independent review (ut-docs#2167)
	// caught that and verified it. Attribute ORDER inside the tag still
	// matters slightly here (the regex reads forward from the id), which
	// is fine — order is meaningful to a reader in a way whitespace is not.
	chipTag := regexp.MustCompile(`(?s)<button[^>]*\bid="(cs-scope-mine|cs-scope-all)"[^>]*?\baria-pressed="(true|false)"`)
	pressedByChip := func(t *testing.T, body string) map[string]string {
		t.Helper()
		got := map[string]string{}
		for _, m := range chipTag.FindAllStringSubmatch(body, -1) {
			got[m[1]] = m[2]
		}
		if len(got) != 2 {
			t.Fatalf("expected both scope chips to render with an id and aria-pressed, got %v\n%s", got, body)
		}
		return got
	}

	for _, tc := range []struct {
		name, url, mine, all string
	}{
		{"default view", "/country-settings", "true", "false"},
		{"all-countries view", "/country-settings?all=1", "false", "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := auth.WithUser(httptest.NewRequest(http.MethodGet, tc.url, nil), mgr)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			got := pressedByChip(t, rec.Body.String())
			if got["cs-scope-mine"] != tc.mine {
				t.Errorf("show-mine chip aria-pressed = %q, want %q", got["cs-scope-mine"], tc.mine)
			}
			if got["cs-scope-all"] != tc.all {
				t.Errorf("show-all chip aria-pressed = %q, want %q", got["cs-scope-all"], tc.all)
			}
		})
	}
}

// TestCountrySettings_FragmentOmitsAdminTreeOnlyWhenInlineSwapMarked is the
// regression test for isAdminInlineSwap (ut-docs#2178, replacing
// isAdminPanelSwap/ut-docs#2167): the in-panel scope chips issue a fragment
// request carrying X-UT-Admin-Inline-Swap, so the out-of-band admin-tree
// refresh must be skipped for that request — there is no #admin-tree in
// the DOM to receive it (standalone page: none at all; the chip's own
// swap: outside the swapped subtree). A real tree-row click (no such
// header) still gets the OOB tree, AND — the fail-open case the old
// HX-Target-sniffing check could not pass — so does a fragment request
// that carries NEITHER header nor a recognizable HX-Target at all: the
// guard no longer depends on HX-Target's presence or value in any way, so
// a future admin_tree.html row (or in-page control) using a target with no
// id can never silently lose the tree the way the old check would have.
func TestCountrySettings_FragmentOmitsAdminTreeOnlyWhenInlineSwapMarked(t *testing.T) {
	mux, _, _ := newCountrySettingsTestMux(t)
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/country-settings", nil), mgr)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "country-settings-view")
	req.Header.Set("X-UT-Admin-Inline-Swap", "1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("chip-targeted fragment GET = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "hx-swap-oob") || strings.Contains(body, `id="admin-tree"`) {
		t.Errorf("an in-page swap (X-UT-Admin-Inline-Swap set) must not carry the OOB admin tree: %s", body)
	}

	req = auth.WithUser(httptest.NewRequest(http.MethodGet, "/country-settings", nil), mgr)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "admin-panel")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("panel-targeted fragment GET = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body = rec.Body.String()
	if !strings.Contains(body, "hx-swap-oob") || !strings.Contains(body, `id="admin-tree"`) {
		t.Errorf("a real tree-row click (no inline-swap header) must still carry the OOB admin tree: %s", body)
	}

	// Fail-open: no HX-Target at all (a target with no id sends none) and
	// no opt-out marker either — the tree must still be sent.
	req = auth.WithUser(httptest.NewRequest(http.MethodGet, "/country-settings", nil), mgr)
	req.Header.Set("HX-Request", "true")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("fragment GET with no HX-Target = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	body = rec.Body.String()
	if !strings.Contains(body, "hx-swap-oob") || !strings.Contains(body, `id="admin-tree"`) {
		t.Errorf("a fragment request with no HX-Target and no opt-out marker must fail OPEN (tree sent): %s", body)
	}
}

// hx-select="#country-settings-view" only works if that id exists in BOTH
// the standalone page's full HTML and the htmx fragment response — this
// pins the wrapper div is present in both.
func TestCountrySettings_ViewWrapperPresentOnStandaloneAndFragment(t *testing.T) {
	mux, _, _ := newCountrySettingsTestMux(t)
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/country-settings", nil), mgr)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `id="country-settings-view"`) {
		t.Errorf("standalone page missing id=\"country-settings-view\": %s", rec.Body.String())
	}

	req = auth.WithUser(httptest.NewRequest(http.MethodGet, "/country-settings", nil), mgr)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "country-settings-view")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `id="country-settings-view"`) {
		t.Errorf("fragment missing id=\"country-settings-view\": %s", rec.Body.String())
	}
}

// ut-docs#2187: /country-settings adopts the record_dialog/list_header
// pattern (ut-docs#2010), replacing the always-editable inline-rows-plus-
// side-create-panel layout. Mirrors categories_page_test.go's
// TestCategoriesPage_RendersRecordDialogWithFieldsSlot (the slot-contract
// reference test every adopting page copies, list-and-dialog-pattern.md's
// own instruction) — pins the list header, the row's data-record-*/
// data-field-* wiring, and that the dialog's two slots actually render
// their fields rather than executing to an empty body (a page that forgets
// a slot fails at EXECUTE time, not parse time).
func TestCountrySettingsPage_RendersRecordDialogWithFieldsSlot(t *testing.T) {
	mux, _, d := newCountrySettingsTestMux(t)
	d.SetState(common.RuntimeState{Country: "DE"})
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/country-settings", nil), mgr)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /country-settings: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// The list header: a search box wired to the rows, and an icon-only New
	// button wired to the dialog, each with an accessible name.
	if !strings.Contains(body, `class="list-header"`) {
		t.Errorf("page has no .list-header")
	}
	if !strings.Contains(body, `data-list-filter="#country-settings-table .country-row"`) {
		t.Errorf("search box is not wired to the country rows")
	}
	if !strings.Contains(body, `data-record-dialog-open="country-dialog"`) {
		t.Errorf("New button is not wired to the dialog")
	}

	// A row: NOT a per-code data-record-action (code is a form field, not a
	// URL path segment — the single POST /api/country-settings endpoint
	// handles create-or-update keyed by the submitted code) — see
	// registerCountrySettings's own doc comment.
	if !strings.Contains(body, `data-record-action="/api/country-settings"`) {
		t.Errorf("DE row missing the shared (not per-code) data-record-action: %s", body)
	}
	if strings.Contains(body, `data-record-action="/api/country-settings/DE"`) {
		t.Errorf("row must not carry a per-code data-record-action: %s", body)
	}
	if !strings.Contains(body, `data-record-destructive-action="/api/country-settings/DE/delete"`) {
		t.Errorf("DE row missing its destructive action: %s", body)
	}
	if !strings.Contains(body, `data-field-is_builtin="1"`) {
		t.Errorf("DE (builtin) row missing data-field-is_builtin=\"1\": %s", body)
	}

	// The dialog itself, and the slot contract: the body holds the form
	// with every field this screen's own fields owe it.
	dlgStart := strings.Index(body, `<dialog id="country-dialog"`)
	if dlgStart < 0 {
		t.Fatalf("page has no #country-dialog:\n%s", body)
	}
	dlgEnd := strings.Index(body[dlgStart:], `</dialog>`)
	if dlgEnd < 0 {
		t.Fatalf("dialog is not closed")
	}
	dlg := body[dlgStart : dlgStart+dlgEnd]
	bodyStart := strings.Index(dlg, `class="record-dialog-body"`)
	if bodyStart < 0 {
		t.Fatalf("dialog has no .record-dialog-body:\n%s", dlg)
	}
	dlgBody := dlg[bodyStart:]
	for _, field := range []string{`name="code"`, `name="currency"`, `name="currency_symbol"`, `name="tax_rate_pct"`, `name="tax_inclusive"`, `name="archive_min_days"`} {
		if !strings.Contains(dlgBody, field) {
			t.Errorf("record_dialog_fields slot did not actually render — missing %s:\n%s", field, dlgBody)
		}
	}

	// The destructive slot: both branches (reset for builtin, delete for
	// custom), each icon-only with an accessible name, each gated on
	// data-field-is_builtin via data-record-when.
	if !strings.Contains(dlg, `data-record-when="is_builtin=1"`) || !strings.Contains(dlg, `data-record-when="is_builtin=0"`) {
		t.Errorf("record_dialog_destructive slot did not render both is_builtin branches:\n%s", dlg)
	}
	if !strings.Contains(dlg, `aria-label="Restore defaults"`) {
		t.Errorf("destructive slot missing the builtin \"Restore defaults\" control:\n%s", dlg)
	}
	if !strings.Contains(dlg, `aria-label="Delete"`) {
		t.Errorf("destructive slot missing the custom \"Delete\" control:\n%s", dlg)
	}
}

// ut-docs#2187's htmx success shape, ported to Country settings: a
// SUCCESSFUL htmx-boosted mutation answers with HX-Redirect (a real browser
// navigation), never a bare 303 — mirrors
// TestLocationsPage_HtmxSuccessAnswersWithHXRedirectNotBareRedirect.
func TestCountrySettingsPage_HtmxSuccessAnswersWithHXRedirectNotBareRedirect(t *testing.T) {
	mux, _, _ := newCountrySettingsTestMux(t)
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}

	rec := postFormHtmx(mux, "/api/country-settings", url.Values{
		"code":             {"ZZ"},
		"currency":         {"GBP"},
		"currency_symbol":  {"£"},
		"tax_rate_pct":     {"20"},
		"archive_min_days": {strconv.FormatInt(data.GlobalArchiveMinDays, 10)},
	}, &mgr)
	if rec.Code != http.StatusOK {
		t.Fatalf("htmx create success: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Redirect"); got != "/country-settings" {
		t.Fatalf("HX-Redirect = %q, want /country-settings", got)
	}
	if rec.Header().Get("Location") != "" {
		t.Errorf("a bare Location alongside HX-Redirect would be followed by the boosted form's own fetch/XHR layer and land the wrong content in the dialog's message target — must not be set")
	}
}

// A refused htmx-boosted mutation (empty code) must render the in-dialog
// message fragment, never a redirect — mirrors
// TestLocationsPage_RefusalRendersInDialogMessageForHtmxRequest.
func TestCountrySettingsPage_RefusalRendersInDialogMessageForHtmxRequest(t *testing.T) {
	mux, _, _ := newCountrySettingsTestMux(t)
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}

	rec := postFormHtmx(mux, "/api/country-settings", url.Values{
		"code":             {"   "},
		"tax_rate_pct":     {"20"},
		"archive_min_days": {strconv.FormatInt(data.GlobalArchiveMinDays, 10)},
	}, &mgr)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("htmx empty-code create: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Location") != "" {
		t.Errorf("htmx refusal must not redirect — a redirect is exactly what closed the dialog before this card")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html (app.js's htmx:beforeSwap only force-swaps a non-2xx text/html body)", ct)
	}
	body := rec.Body.String()
	// hx-swap="innerHTML" into "#country-dialog-msg" — the aria-live region
	// ITSELF, which must never be re-rendered. So the body carries ONLY the
	// message text, no id/wrapper/form/dialog markup.
	if strings.Contains(body, "id=") || strings.Contains(body, "<form") || strings.Contains(body, "<dialog") {
		t.Errorf("response must be ONLY the message text — no wrapper, form or dialog markup: %s", body)
	}
}

// The ADR-0040 retention-floor refusal (this card's own AC, per the task
// brief) must surface the same in-dialog way as any other refusal, not as a
// closed dialog with a lost edit.
func TestCountrySettingsPage_HtmxFloorRefusalRendersInDialogMessage(t *testing.T) {
	mux, repo, _ := newCountrySettingsTestMux(t)
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}
	ctx := t.Context()

	rec := postFormHtmx(mux, "/api/country-settings", url.Values{
		"code":             {"DE"},
		"currency":         {"EUR"},
		"tax_rate_pct":     {"7"},
		"archive_min_days": {strconv.FormatInt(data.GlobalArchiveMinDays-1, 10)},
	}, &mgr)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("htmx below-floor save: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Location") != "" {
		t.Errorf("htmx floor refusal must not redirect")
	}
	if body := rec.Body.String(); strings.Contains(body, "<form") || strings.Contains(body, "<dialog") {
		t.Errorf("floor refusal response must be message-only: %s", body)
	}
	de, _, err := repo.Get(ctx, "DE")
	if err != nil {
		t.Fatal(err)
	}
	if de.ArchiveMinDays != data.GlobalArchiveMinDays {
		t.Errorf("refused htmx save changed DE's retention to %d, want it left at the seeded floor %d", de.ArchiveMinDays, data.GlobalArchiveMinDays)
	}
}

// The "all=1" carry-through (ut-docs#1024/TestCountrySettingsPageSave_FromAllView_RedirectsBackToAllView's
// non-htmx pin) must also hold for the htmx-boosted dialog path: a save made
// from the all-countries view lands back on that same view via HX-Redirect.
func TestCountrySettingsPage_HtmxSaveFromAllView_HXRedirectsToAllView(t *testing.T) {
	mux, _, _ := newCountrySettingsTestMux(t)
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}

	rec := postFormHtmx(mux, "/api/country-settings?all=1", url.Values{
		"code":             {"FR"},
		"currency":         {"EUR"},
		"tax_rate_pct":     {"20"},
		"archive_min_days": {strconv.FormatInt(data.GlobalArchiveMinDays, 10)},
	}, &mgr)
	if rec.Code != http.StatusOK {
		t.Fatalf("htmx save from all view: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Redirect"); got != "/country-settings?all=1" {
		t.Errorf("HX-Redirect = %q, want /country-settings?all=1 (must carry the view through)", got)
	}
}

// ut-docs#1027's DefaultLocale/NameKey preservation regression
// (TestCountrySettingsPageSavePreservesDefaultLocale's non-htmx pin), ported
// to the htmx-boosted dialog path — the SAME handler now answers both, but
// this proves the preserve-what-the-dialog-form-doesn't-carry contract holds
// on the branch the real dialog actually exercises.
func TestCountrySettingsPage_HtmxSavePreservesNameKeyAndDefaultLocale(t *testing.T) {
	mux, repo, _ := newCountrySettingsTestMux(t)
	mgr := auth.User{ID: "m1", Role: "manager", DisplayName: "Mgr"}
	ctx := t.Context()

	before, _, err := repo.Get(ctx, "DE")
	if err != nil {
		t.Fatal(err)
	}
	if before.NameKey == "" || before.DefaultLocale != "de-DE" {
		t.Fatalf("seeded DE = {NameKey:%q DefaultLocale:%q}, want a non-empty NameKey and de-DE", before.NameKey, before.DefaultLocale)
	}

	rec := postFormHtmx(mux, "/api/country-settings", url.Values{
		"code":             {"DE"},
		"currency":         {"EUR"},
		"currency_symbol":  {"€"},
		"tax_rate_pct":     {"9"},
		"tax_inclusive":    {"1"},
		"archive_min_days": {strconv.FormatInt(data.GlobalArchiveMinDays, 10)},
	}, &mgr)
	if rec.Code != http.StatusOK {
		t.Fatalf("htmx save: code=%d body=%s", rec.Code, rec.Body.String())
	}

	after, _, err := repo.Get(ctx, "DE")
	if err != nil {
		t.Fatal(err)
	}
	if after.NameKey != before.NameKey {
		t.Errorf("DE.NameKey after a dialog save = %q, want unchanged %q — the dialog carries no name-key field", after.NameKey, before.NameKey)
	}
	if after.DefaultLocale != "de-DE" {
		t.Errorf("DE.DefaultLocale after a dialog save = %q, want unchanged de-DE — the dialog carries no locale field", after.DefaultLocale)
	}
}

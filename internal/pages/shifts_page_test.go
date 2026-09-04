package pages

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
)

func newShiftsPageTestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}}
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
	registerShiftsPage(mux, dp)
	return mux, dp
}

func TestShiftsPage_NoOpenShiftShowsNoneOpenAndListsRegisters(t *testing.T) {
	mux, dp := newShiftsPageTestDeps(t)
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/shifts", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No open shift") {
		t.Fatalf("expected the no-open-shift banner, got: %s", body)
	}
	if strings.Contains(body, "Current Shift") {
		t.Fatalf("expected no Current Shift card when nothing is open, got: %s", body)
	}
	if !strings.Contains(body, "Front Till") {
		t.Fatalf("expected the open-new-shift form to list the seeded register, got: %s", body)
	}
}

func TestShiftsPage_OpenShiftShowsCurrentAndHistory(t *testing.T) {
	mux, dp := newShiftsPageTestDeps(t)
	ctx := t.Context()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO shifts(id,register_id,cashier_id,opened_at,opening_cash) VALUES('shift1','reg1','user1','2026-01-01T09:00:00Z',5000)`); err != nil {
		t.Fatal(err)
	}
	// closing_cash and expected_cash are deliberately DIFFERENT (1200 vs
	// 1500) so a template regression that swapped the Counted/Expected
	// columns would actually be caught, and so a non-zero variance is
	// observable.
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO shifts(id,register_id,cashier_id,opened_at,closed_at,opening_cash,closing_cash,expected_cash) VALUES('shift0','reg1','user1','2026-01-01T00:00:00Z','2026-01-01T08:00:00Z',1000,1200,1500)`); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/shifts", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Shift open since") {
		t.Fatalf("expected the open-since banner, got: %s", body)
	}
	if !strings.Contains(body, "Current Shift") {
		t.Fatalf("expected the Current Shift card, got: %s", body)
	}
	if !strings.Contains(body, "reg1") {
		t.Fatalf("expected the open shift's register in the current-shift card, got: %s", body)
	}
	if !strings.Contains(body, "£50.00") {
		t.Fatalf("expected the open shift's opening cash rendered, got: %s", body)
	}
	if !strings.Contains(body, "£12.00") {
		t.Fatalf("expected the closed shift's counted (closing) cash in history, got: %s", body)
	}
	if !strings.Contains(body, "£15.00") {
		t.Fatalf("expected the closed shift's expected cash in history, got: %s", body)
	}
	// Variance = counted(1200) - expected(1500) = -300 minor units;
	// FormatMoney hugs the symbol to the number, so GBP renders this "£-3.00".
	if !strings.Contains(body, "£-3.00") {
		t.Fatalf("expected the closed shift's variance in history, got: %s", body)
	}
}

// ut-docs#940 (follow-up from ut-docs#894's review): before #894, most shops
// had exactly one register so the picker's lack of preselection was
// harmless. After #894, multi-till shops routinely have 2+ active
// registers, so the picker must default to THIS till's own persisted
// sync.till_register_id (pos.ResolveTillRegisterID) rather than leaving the
// browser to pick whichever option happens to be first — mirrors
// TestSettingsPage_TillRegisterPickerRendersAndSelects' pattern.
func TestShiftsPage_OpenShiftPickerPreselectsOwnRegister(t *testing.T) {
	mux, dp := newShiftsPageTestDeps(t)
	ctx := t.Context()
	for _, ins := range []string{
		`INSERT INTO registers(id,name,is_active) VALUES('regA','Front Till',1)`,
		`INSERT INTO registers(id,name,is_active) VALUES('regB','Back Till',1)`,
	} {
		if _, err := dp.Db.ExecContext(ctx, ins); err != nil {
			t.Fatal(err)
		}
	}

	get := func() string {
		req := httptest.NewRequest(http.MethodGet, "/shifts", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /shifts = %d", rec.Code)
		}
		return rec.Body.String()
	}

	// Ambiguous (two active registers, nothing persisted): the picker still
	// renders both options with neither preselected -- same as the
	// Settings picker in this state, no guessing past a real ambiguity.
	body := get()
	if !strings.Contains(body, `value="regA"`) || !strings.Contains(body, `value="regB"`) {
		t.Fatalf("expected both registers as options, got:\n%s", body)
	}
	if strings.Contains(body, `value="regA" selected`) || strings.Contains(body, `value="regB" selected`) {
		t.Fatalf("expected no register pre-selected while this till's identity is ambiguous, got:\n%s", body)
	}

	// This till's own register identity is persisted (regB) -- the picker
	// must default to it, still allowing the other option to be chosen.
	if err := dp.Settings.Set(ctx, pos.SettingsKeyTillRegisterID, "regB"); err != nil {
		t.Fatal(err)
	}
	if body := get(); !strings.Contains(body, `value="regB" selected`) {
		t.Fatalf("expected regB (this till's own register) preselected, got:\n%s", body)
	} else if strings.Contains(body, `value="regA" selected`) {
		t.Fatalf("expected regA NOT preselected once regB is this till's identity, got:\n%s", body)
	}
}

// ut-docs#1274: shifts.html hardcoded "(£)" in field labels and a fixed
// 2-decimal pattern/placeholder regardless of the shop's real configured
// currency -- both must now follow currency.Display/currency.Decimals, the
// same convention #pfand-amount (ut-docs#1249) established. Covers both a
// 2-decimal currency (GBP, unaffected by this fix) and a 0-decimal one
// (IRT), where the old hardcoded pattern/placeholder was simply wrong.
func TestShiftsPage_LabelsAndPatternsAreCurrencyAware(t *testing.T) {
	mux, dp := newShiftsPageTestDeps(t)
	ctx := t.Context()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO shifts(id,register_id,cashier_id,opened_at,opening_cash) VALUES('shift1','reg1','user1','2026-01-01T09:00:00Z',5000)`); err != nil {
		t.Fatal(err)
	}

	get := func() string {
		req := httptest.NewRequest(http.MethodGet, "/shifts", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /shifts = %d: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	// GBP (2-decimal): unchanged behavior -- symbol in every label, 2-decimal
	// pattern/placeholder, same as before this fix.
	httpx.InitCurrency("GBP")
	body := get()
	if !strings.Contains(body, "(£)") {
		t.Fatalf("expected the GBP symbol in the counted-cash label, got:\n%s", body)
	}
	if !strings.Contains(body, `pattern="[0-9]+(\.[0-9]{1,2})?"`) {
		t.Fatalf("expected the 2-decimal pattern for GBP, got:\n%s", body)
	}
	if !strings.Contains(body, `placeholder="0.00"`) {
		t.Fatalf("expected the 2-decimal placeholder for GBP, got:\n%s", body)
	}
	if !strings.Contains(body, `pattern="-?[0-9]+(\.[0-9]{1,2})?"`) {
		t.Fatalf("expected the signed 2-decimal pattern for the adjustment field, got:\n%s", body)
	}
	if !strings.Contains(body, `placeholder="-50.00"`) {
		t.Fatalf("expected the signed 2-decimal placeholder for the adjustment field, got:\n%s", body)
	}
	// opening-cash specifically (the field HasOpen currently hides -- this
	// GET has an open shift, so this asserts the *closed*-shift form's
	// fields only; TestShiftsPage_CarryForwardDisplayIsCurrencyAware below
	// covers opening-cash's own pattern, on a GET where it actually renders).

	// 0-decimal currency (IRT, toman): the old hardcoded pattern/placeholder
	// was simply wrong here -- "0.00"/"\.[0-9]{1,2}" reject a valid 0-decimal
	// amount like "500". Must now show the toman symbol on every label and
	// an integer-only pattern/placeholder, with NO 2-decimal shape left over
	// anywhere in the page (a partial conversion must fail this).
	httpx.InitCurrency("IRT")
	t.Cleanup(func() { httpx.InitCurrency("GBP") }) // ut-docs#970 convention: process-global, reset for later tests in this package.
	body = get()
	if !strings.Contains(body, "(تومان)") {
		t.Fatalf("expected the IRT symbol in the counted-cash label, got:\n%s", body)
	}
	if strings.Contains(body, "(£)") {
		t.Fatalf("expected NO leftover GBP symbol on any label once currency is 0-decimal, got:\n%s", body)
	}
	if !strings.Contains(body, `pattern="[0-9]+"`) {
		t.Fatalf("expected the 0-decimal (integer-only) pattern for IRT, got:\n%s", body)
	}
	if strings.Contains(body, `pattern="[0-9]+(\.[0-9]{1,2})?"`) {
		t.Fatalf("expected NO 2-decimal pattern left over anywhere once currency is 0-decimal, got:\n%s", body)
	}
	if !strings.Contains(body, `placeholder="0"`) {
		t.Fatalf("expected the 0-decimal placeholder for IRT, got:\n%s", body)
	}
	if strings.Contains(body, `placeholder="0.00"`) {
		t.Fatalf("expected NO 2-decimal placeholder left over anywhere once currency is 0-decimal, got:\n%s", body)
	}
	if !strings.Contains(body, `pattern="-?[0-9]+"`) {
		t.Fatalf("expected the signed 0-decimal pattern for the adjustment field, got:\n%s", body)
	}
	if !strings.Contains(body, `placeholder="-50"`) {
		t.Fatalf("expected the 0-decimal negative placeholder for the adjustment field, got:\n%s", body)
	}
	if strings.Contains(body, `placeholder="-50.00"`) {
		t.Fatalf("expected NO 2-decimal negative placeholder left over once currency is 0-decimal, got:\n%s", body)
	}
}

// ut-docs#1291: the count-protocol cash-count grid (#denom-grid) hardcoded
// GBP physical note/coin denominations (£50..1p) regardless of shop
// currency. Must render from httpx.CurrencyInfo.Denominations instead, with
// no leftover GBP values/labels once a different currency is active.
func TestShiftsPage_DenomGridIsCurrencyAware(t *testing.T) {
	mux, dp := newShiftsPageTestDeps(t)
	ctx := t.Context()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO shifts(id,register_id,cashier_id,opened_at,opening_cash) VALUES('shift1','reg1','user1','2026-01-01T09:00:00Z',5000)`); err != nil {
		t.Fatal(err)
	}

	get := func() string {
		req := httptest.NewRequest(http.MethodGet, "/shifts", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /shifts = %d: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	// Scope the label assertions to the #denom-grid block itself. Review of
	// ut-docs#1291: this shift's opening cash is 5000 minor units, so the
	// page renders "£50.00" in the summary above the form regardless of what
	// the grid does — an unscoped strings.Contains(body, "£50.00") passes
	// even against the old hardcoded "£50"/"1p" grid, which would have made
	// the GBP half of this test decorative rather than load-bearing.
	gridOf := func(body string) string {
		t.Helper()
		i := strings.Index(body, `id="denom-grid"`)
		if i < 0 {
			t.Fatalf("no #denom-grid rendered on /shifts:\n%s", body)
		}
		rest := body[i:]
		// The grid holds only <label>/<input> children, so the first
		// closing </div> is the grid's own.
		j := strings.Index(rest, "</div>")
		if j < 0 {
			t.Fatalf("unterminated #denom-grid:\n%s", rest)
		}
		return rest[:j]
	}

	httpx.InitCurrency("GBP")
	body := get()
	grid := gridOf(body)
	// Denomination labels use the same `money`-formatted convention as the
	// rest of this page's currency-aware fields (£X.XX, ut-docs#1274) —
	// not the old two-tier "£50"/"1p" note-vs-coin shorthand.
	if !strings.Contains(grid, `data-denom="5000"`) || !strings.Contains(grid, "£50.00") {
		t.Fatalf("expected the GBP £50.00 denomination in the count-protocol grid, got:\n%s", grid)
	}
	if !strings.Contains(grid, `data-denom="1"`) || !strings.Contains(grid, "£0.01") {
		t.Fatalf("expected the GBP £0.01 denomination in the count-protocol grid, got:\n%s", grid)
	}

	// JPY: 0-decimal, prefix symbol, a different denomination set — must
	// show ¥-labelled JPY denominations and NO leftover GBP symbol
	// anywhere in the grid (the whole page, in fact, since GBP's £ never
	// appears once the shop's currency is JPY).
	httpx.InitCurrency("JPY")
	t.Cleanup(func() { httpx.InitCurrency("GBP") }) // ut-docs#970 convention: process-global, reset for later tests in this package.
	body = get()
	grid = gridOf(body)
	if !strings.Contains(grid, `data-denom="10000"`) || !strings.Contains(grid, "¥10,000") {
		t.Fatalf("expected the JPY ¥10,000 denomination in the count-protocol grid, got:\n%s", grid)
	}
	if !strings.Contains(grid, `data-denom="1"`) || !strings.Contains(grid, "¥1") {
		t.Fatalf("expected the JPY ¥1 denomination in the count-protocol grid, got:\n%s", grid)
	}
	// Deliberately page-wide, not grid-scoped: under a JPY shop the GBP
	// symbol should not survive anywhere on /shifts.
	if strings.Contains(body, "£") {
		t.Fatalf("expected NO leftover GBP symbol once currency is JPY, got:\n%s", body)
	}
	// GBP's £2/200p and £20/2000p denominations have no JPY equivalent
	// (JPY has no 2- or 20-based note/coin) — a leftover hardcoded GBP
	// grid would still show these.
	if strings.Contains(grid, `data-denom="2000"`) || strings.Contains(grid, `data-denom="200"`) {
		t.Fatalf("expected NO leftover GBP-only denominations (2000/200) in the count-protocol grid once currency is JPY, got:\n%s", grid)
	}
}

// ut-docs#1274: CarryForwardDisplay hardcoded `%d.%02d` against `/100`
// (internal/pages/shifts_page.go) -- silently wrong on a 0-decimal currency,
// where minor units ARE major units (500 IRT prefilled as "5.00" instead of
// "500"). Covers the actual rendered <input value> on the open-shift form,
// not just the underlying formatter (that's httpx.TestFormatMajorPlain).
func TestShiftsPage_CarryForwardDisplayIsCurrencyAware(t *testing.T) {
	mux, dp := newShiftsPageTestDeps(t)
	ctx := t.Context()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}
	// A closed shift leaves a new float this till's next open should carry
	// forward -- pos.LastClosedShiftNewFloat reads new_float, not closing_cash.
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO shifts(id,register_id,cashier_id,opened_at,closed_at,opening_cash,closing_cash,new_float) VALUES('shift0','reg1','user1','2026-01-01T00:00:00Z','2026-01-01T08:00:00Z',0,500,500)`); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, pos.SettingsKeyTillRegisterID, "reg1"); err != nil {
		t.Fatal(err)
	}

	get := func() string {
		req := httptest.NewRequest(http.MethodGet, "/shifts", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /shifts = %d: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	// Assertions are scoped to the actual #opening-cash <input> tag (id +
	// pattern + value together), not a bare `value="…"` substring -- the
	// page also renders a hidden #opening-cash-minor field prefilled
	// straight from CarryForwardMinor (ut-docs#1274 review finding: a
	// formatter that silently returned the wrong major-unit string could
	// still coincidentally satisfy an unscoped `value="500"` check via that
	// OTHER field, since 500 minor units is also this shift's raw minor
	// amount).
	httpx.InitCurrency("GBP")
	body := get()
	if !strings.Contains(body, "(£)") {
		t.Fatalf("expected the GBP symbol in the opening-cash label, got:\n%s", body)
	}
	if !strings.Contains(body, `id="opening-cash" inputmode="decimal" pattern="[0-9]+(\.[0-9]{1,2})?" required value="5.00"`) {
		t.Fatalf("expected #opening-cash's own pattern+value prefilled 5.00 under GBP, got:\n%s", body)
	}

	httpx.InitCurrency("IRT")
	t.Cleanup(func() { httpx.InitCurrency("GBP") })
	body = get()
	if !strings.Contains(body, "(تومان)") {
		t.Fatalf("expected the IRT symbol in the opening-cash label, got:\n%s", body)
	}
	if strings.Contains(body, "(£)") {
		t.Fatalf("expected NO leftover GBP symbol on the opening-cash label once currency is 0-decimal, got:\n%s", body)
	}
	if !strings.Contains(body, `id="opening-cash" inputmode="decimal" pattern="[0-9]+" required value="500"`) {
		t.Fatalf("expected #opening-cash's own pattern+value prefilled 500 (no /100) under a 0-decimal currency, got:\n%s", body)
	}
	if strings.Contains(body, `pattern="[0-9]+(\.[0-9]{1,2})?"`) || strings.Contains(body, `value="5.00"`) {
		t.Fatalf("expected NO 2-decimal pattern or carry-forward value left over once currency is 0-decimal, got:\n%s", body)
	}
}

// ut-docs#940 review finding: registers must be listed AFTER resolving this
// till's identity, mirroring settings_page.go's own ordering and its
// comment on why -- on a brand-new shop's very first shift-open, with zero
// registers yet, pos.ResolveTillRegisterID self-creates one via
// EnsureRegister (real name "Default Register") and persists it as this
// till's identity. Listing registers BEFORE resolving would capture the
// registers slice empty, miss that self-created register as a real
// <option>, and silently fall through to the template's own hardcoded
// "reg-default"/locale-text fallback -- which only happens to render
// correctly today because its literal id coincidentally matches
// EnsureRegister's. This test fails on that ordering bug: it demands the
// real, listed register (name "Default Register"), not the fallback text.
func TestShiftsPage_OpenShiftPickerBootstrapsAndPreselectsOnZeroRegisters(t *testing.T) {
	mux, _ := newShiftsPageTestDeps(t)

	req := httptest.NewRequest(http.MethodGet, "/shifts", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /shifts = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `value="reg-default" selected`) {
		t.Fatalf("expected the self-created default register listed and preselected, got:\n%s", body)
	}
	if !strings.Contains(body, "Default Register") {
		t.Fatalf("expected EnsureRegister's real register name (not just the template's locale-text fallback), got:\n%s", body)
	}
}

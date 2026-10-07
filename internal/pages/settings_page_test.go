// ut-docs#1913 review findings 3/4: uislot.CoreSettings and
// filterSettingsNavForRender's gated-key map are both hand-synced to
// web/ui/pages/settings.html with nothing tying them together — the
// independent review verified both are correct TODAY, but nothing stops a
// future card added to the template from silently going uncovered by
// either (invisible in the sidebar AND in search for CoreSettings; a
// gated card whose title leaks to viewers who can't see it for the
// filter). This test reads the real template and cross-checks both.
func TestSettingsPage_CoreSettingsAndFilterMatchTheRealTemplate(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("web", "ui", "pages", "settings.html"))
	if err != nil {
		t.Fatalf("read settings.html: %v", err)
	}
	lines := strings.Split(string(raw), "\n")
	cardRE := regexp.MustCompile(`<div class="card[^"]*" id="([^"]+)"`)
	ifRE := regexp.MustCompile(`\{\{\s*if\b`)

	type found struct {
		key    string
		gated  bool // a "{{ if" appears on one of the lines immediately above this card's div
		lineNo int
	}
	var cards []found
	for i, line := range lines {
		m := cardRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		gated := false
		// Look back over blank/comment lines to the nearest real
		// preceding line — every one of today's 5 gated cards has its
		// `{{ if }}` on the line directly above the div (see the
		// settings-data example, which sits right after a multi-line
		// HTML comment: the comment lines themselves never match ifRE, so
		// walking back past them to the actual `{{ if }}` line is
		// required, not just checking i-1 literally).
		for j := i - 1; j >= 0 && j >= i-6; j-- {
			trimmed := strings.TrimSpace(lines[j])
			if trimmed == "" || strings.HasPrefix(trimmed, "<!--") || strings.Contains(trimmed, "-->") {
				continue
			}
			if ifRE.MatchString(trimmed) {
				gated = true
			}
			break
		}
		cards = append(cards, found{key: m[1], gated: gated, lineNo: i + 1})
	}

	if len(cards) == 0 {
		t.Fatal("found no .card ids in settings.html — the regex or the file itself has drifted")
	}

	// 1. uislot.CoreSettings must declare EXACTLY these keys, in EXACTLY
	// this order — a card added/removed/reordered in the template with no
	// matching CoreSettings change is invisible to the sidebar AND to
	// settings.html's own search index (both are now built from
	// CoreSettings' resolution, not a DOM scan).
	if len(cards) != len(uislot.CoreSettings) {
		t.Fatalf("settings.html has %d .card ids, uislot.CoreSettings declares %d — got template cards %+v", len(cards), len(uislot.CoreSettings), cards)
	}
	for i, c := range cards {
		if uislot.CoreSettings[i].Key != c.key {
			t.Errorf("order/key mismatch at position %d: template has %q (line %d), CoreSettings has %q — add/reorder/rename it in BOTH places", i, c.key, c.lineNo, uislot.CoreSettings[i].Key)
		}
	}

	// 2. filterSettingsNavForRender's hardcoded gated-key map must name
	// exactly the cards the template actually wraps in a `{{ if }}` — no
	// more (a stale entry is harmless but confusing), no fewer (a real
	// gap here is the information-disclosure regression
	// TestSettingsPage_NavIndexNeverLeaksManagerOnlyRowsToCashier exists to
	// prevent, for whichever NEW card it is that this test didn't know
	// about).
	allFilteredOut := filterSettingsNavForRender(settingsnav.Resolve("en", nil), false, false, false, false)
	filteredKeys := map[string]bool{}
	for _, c := range cards {
		if c.gated {
			filteredKeys[c.key] = true
		}
	}
	survivingWithEverythingOff := map[string]bool{}
	for _, r := range allFilteredOut {
		survivingWithEverythingOff[r.Key] = true
	}
	for _, c := range cards {
		wantFiltered := filteredKeys[c.key]
		gotFiltered := !survivingWithEverythingOff[c.key]
		if wantFiltered != gotFiltered {
			t.Errorf("%q (line %d): template gating=%v (a `{{ if }}` %v found on the line above) but filterSettingsNavForRender's map disagrees — update its gated-key map to match", c.key, c.lineNo, c.gated, map[bool]string{true: "was", false: "was NOT"}[c.gated])
		}
	}
}

// ut-docs#1913 review: #settings-nav-index must never leak the title of a
// `.card` this request's own gates keep out of the DOM — a cashier session
// must not learn "Report an issue" / "Hidden menu tiles" / "All Settings"
// exist just by reading the sidebar index, even though those cards
// genuinely never render for a cashier (isManager-gated). This is the
// filterSettingsNavForRender regression TestSettingsPage_DataCardHiddenFromCashierWhenNothingPending
// already covers for settings-data specifically; this pins the other three
// manager-only rows too.
func TestSettingsPage_NavIndexNeverLeaksManagerOnlyRowsToCashier(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	body := settingsRefusedForCashier(t, mux) // ut-docs#3079: now refused outright
	for _, key := range []string{"settings-issuereport", "settings-menulayout", "settings-all"} {
		if strings.Contains(body, `data-key="`+key+`"`) {
			t.Errorf("cashier session must not see manager-only row %q in #settings-nav-index:\n%s", key, body)
		}
	}
}

// ADR-0088, ut-docs#1913, categories ut-docs#3090: with no `layout` plugin
// active, GET /settings' server-rendered #settings-nav-index lists every
// uislot.CoreSettings row gathered under its category (My shop first,
// Advanced last), each tagged with its category id, and the landing grid
// renders one tile per category — pinned at the actual HTTP-render level
// (settingsnav's own package tests pin Resolve/Categories directly; this
// proves the handler actually wires them through).
func TestSettingsPage_NavIndexResolvesCoreOrderWithNoPlugin(t *testing.T) {
	t.Setenv("UT_AUTH", "off") // ut-docs#3079: /settings is settings-gated; this test is about rendering, not permissions.
	mux, _, _ := newFullAuthDeps(t)
	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	body := rec.Body.String()
	indexStart := strings.Index(body, `id="settings-nav-index"`)
	if indexStart == -1 {
		t.Fatalf("no #settings-nav-index rendered:\n%s", body)
	}
	indexEnd := strings.Index(body[indexStart:], "</ul>")
	if indexEnd == -1 {
		t.Fatalf("unterminated #settings-nav-index")
	}
	indexHTML := body[indexStart : indexStart+indexEnd]
	for _, want := range []string{
		`data-key="settings-currency" data-group="My shop" data-cat="shop"`,
		`data-key="registration" data-group="Tills &amp; devices" data-cat="devices"`,
		`data-key="settings-all" data-group="Advanced" data-cat="advanced"`,
	} {
		if !strings.Contains(indexHTML, want) {
			t.Errorf("nav index must carry %s, got:\n%s", want, indexHTML)
		}
	}
	// Categories in declared order: My shop's rows before Tills & devices'
	// before Advanced's.
	curIdx := strings.Index(indexHTML, `data-key="settings-currency"`)
	regIdx := strings.Index(indexHTML, `data-key="registration"`)
	allIdx := strings.Index(indexHTML, `data-key="settings-all"`)
	if curIdx > regIdx || regIdx > allIdx {
		t.Fatalf("expected My shop, then Tills & devices, then Advanced, got:\n%s", indexHTML)
	}
	if !strings.Contains(indexHTML, "Till registration") {
		t.Fatalf("expected the resolved English label, not a raw key, got:\n%s", indexHTML)
	}
	// The landing grid (hidden until the page script shows it, so a
	// browser without script keeps the full page): one tile per category,
	// Advanced last, each with its icon and one-line description.
	homeStart := strings.Index(body, `id="settings-home"`)
	if homeStart == -1 || !strings.Contains(body[homeStart-200:homeStart+200], "hidden") {
		t.Fatalf("no hidden-by-default #settings-home rendered")
	}
	homeHTML := body[homeStart : homeStart+strings.Index(body[homeStart:], "</nav>")]
	var tiles []string
	for _, part := range strings.Split(homeHTML, `data-cat="`)[1:] {
		tiles = append(tiles, part[:strings.Index(part, `"`)])
	}
	if got, want := strings.Join(tiles, ","), "shop,selling,payments,receipts,staff,devices,look,backup,advanced"; got != want {
		t.Fatalf("landing tiles = %s, want %s", got, want)
	}
	for _, want := range []string{`href="#cat-shop"`, `data-icon="store"`, "Currency, language and the kind of shop you run.", "Every setting, including the technical ones."} {
		if !strings.Contains(homeHTML, want) {
			t.Errorf("landing grid must contain %q:\n%s", want, homeHTML)
		}
	}
}

// A category whose every section is gated out for this render gets no
// tile (ut-docs#3090): with every payment method turned off the Payments
// card does not render, so neither does the Payments tile.
func TestSettingsPage_NoTileForAnEmptyCategory(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, d := newFullAuthDeps(t)
	if _, err := d.Db.Exec(`UPDATE payment_methods SET is_active = 0`); err != nil {
		t.Fatalf("turn payment methods off: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, `id="settings-payments"`) {
		t.Fatalf("premise: with no active payment method there is no Payments card")
	}
	if strings.Contains(body, `href="#cat-payments"`) {
		t.Fatalf("a category with no rendered section must have no tile")
	}
	if !strings.Contains(body, `href="#cat-shop"`) || !strings.Contains(body, `href="#cat-advanced"`) {
		t.Fatalf("the other tiles must still render")
	}
}

// With the salon `layout` plugin active, GET /settings' nav index reflects
// its Settings-slot amendment: settings-theme reordered to the front, and
// settings-printer/settings-tills gathered under a shared group heading —
// the same handler-level proof TestShopTypeEndpoint_ServiceActivatesSalonLayout_SwitchAwayRemovesIt
// gives the Menu slot's /tables hide.
func TestSettingsPage_NavIndexReflectsSalonLayoutAmendment(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	ctx := t.Context()
	pm, err := plugins.Init(ctx, d.Cfg, d.Db)
	if err != nil {
		t.Fatalf("plugins.Init: %v", err)
	}
	d.Pm = pm

	rec := postForm(mux, "/api/settings/shop-type", url.Values{"shop_type": {"service"}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("save shop_type=service: code=%d body=%s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	req = auth.WithUser(req, mgrUser)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	body := rec.Body.String()
	indexStart := strings.Index(body, `id="settings-nav-index"`)
	if indexStart == -1 {
		t.Fatalf("no #settings-nav-index rendered:\n%s", body)
	}
	indexEnd := strings.Index(body[indexStart:], "</ul>")
	indexHTML := body[indexStart : indexStart+indexEnd]

	// Categories keep their order (ut-docs#3090), so the salon's Order
	// amendment moves Theme to the front of its own category, Look & feel.
	themeIdx := strings.Index(indexHTML, `data-key="settings-theme"`)
	menuLayoutIdx := strings.Index(indexHTML, `data-key="settings-menulayout"`)
	if themeIdx == -1 || menuLayoutIdx == -1 || themeIdx > menuLayoutIdx {
		t.Fatalf("salon layout must reorder settings-theme to the front of Look & feel, got:\n%s", indexHTML)
	}
	printerIdx := strings.Index(indexHTML, `data-key="settings-printer"`)
	tillsIdx := strings.Index(indexHTML, `data-key="settings-tills"`)
	if printerIdx == -1 || tillsIdx == -1 {
		t.Fatalf("printer/tills rows missing from index:\n%s", indexHTML)
	}
	if !strings.Contains(indexHTML[printerIdx:printerIdx+200], "data-group=") {
		t.Errorf("settings-printer must carry a resolved data-group, got:\n%s", indexHTML[printerIdx:printerIdx+200])
	}
	// The salon's own group is a category of its own (ut-docs#3090 AC 8):
	// a tile on the landing grid, and Printer/Tills are listed in it.
	if !strings.Contains(indexHTML[printerIdx:printerIdx+200], `data-cat="g-layout-salon-settings_group"`) {
		t.Errorf("settings-printer must sit in the salon group's category, got:\n%s", indexHTML[printerIdx:printerIdx+200])
	}
	if !strings.Contains(body, `href="#cat-g-layout-salon-settings_group"`) {
		t.Errorf("the salon group must render as a landing tile")
	}
}

// ut-docs#1125: the shop-default-locale picker must label its options with
// each locale's native name, not the bare code — the ticket's acceptance
// criterion is "no bare locale code shown anywhere (wizard or settings)", and
// settings is the half the wizard's own test cannot cover.
func TestSettingsPageDefaultLocalePickerShowsNativeNamesNotBareCodes(t *testing.T) {
	t.Setenv("UT_AUTH", "off") // ut-docs#3079: /settings is settings-gated; this test is about rendering, not permissions.
	mux, _, _ := newFullAuthDeps(t)
	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	body := rec.Body.String()

	for _, want := range []string{"العربية", "English", "فارسی", "Türkçe"} {
		if !strings.Contains(body, want) {
			t.Errorf("settings default-locale picker missing native language name %q", want)
		}
	}
	// The <option> value stays the bare code (the form posts it, and
	// settings_page.go validates it against AvailableLocales) — only the
	// visible label must change. Anchored on the option's own shape so the
	// value attribute itself can't false-positive.
	for _, code := range []string{"ar", "en", "fa", "tr"} {
		if regexp.MustCompile(`value="` + code + `"[^>]*>\s*` + code + `\s*</option>`).MatchString(body) {
			t.Errorf("settings default-locale picker still labels an option with the bare code %q", code)
		}
		if !strings.Contains(body, `value="`+code+`"`) {
			t.Errorf("settings default-locale picker no longer posts the bare code %q as the option value", code)
		}
	}
}

// SaveState's write must be all-or-nothing at the HTTP boundary too
// (ut-docs#157): a failed settings-page save must answer 5xx and must not
// apply the change to the live sale engine, or a shop would silently
// mis-price sales on the old currency/tax combination.
func TestSettingsSave_FailsClosedOnSaveError(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	// Seed a known-good baseline the same way a real shop would have one.
	if rec := postForm(mux, "/api/settings/save", url.Values{
		"currency":   {"GBP"},
		"taxRatePct": {"20"},
	}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("seed save = %d", rec.Code)
	}

	if _, err := d.Db.Exec(`
CREATE TRIGGER boom BEFORE INSERT ON settings
WHEN NEW.key = 'store.tax_rate'
BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	rec := postForm(mux, "/api/settings/save", url.Values{
		"currency":   {"EUR"},
		"taxRatePct": {"7"},
	}, &mgrUser)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("save with an aborting trigger = %d, want 500", rec.Code)
	}

	// The DB must still hold the seeded values — no partial currency-only write.
	if v, _, _ := d.Settings.Get(t.Context(), "store.currency"); v != "GBP" {
		t.Fatalf("store.currency = %q after failed save, want seeded %q (partial write not rolled back)", v, "GBP")
	}
	if v, _, _ := d.Settings.Get(t.Context(), "store.tax_rate"); v != "20" {
		t.Fatalf("store.tax_rate = %q after failed save, want seeded %q", v, "20")
	}
	// The live sale engine must not have picked up the failed-to-persist
	// currency/tax combination.
	if d.Engine.Config().TaxRateBasisPoints != 2000 {
		t.Fatalf("engine TaxRateBasisPoints = %d after failed save, want unchanged 2000 (700 would mean the failed 7%% rate was silently applied)", d.Engine.Config().TaxRateBasisPoints)
	}
}

// A rejected save must not leak into the in-memory state either — otherwise
// a later, unrelated successful save silently re-persists the change the
// operator was just told failed (ut-docs#157 review finding).
func TestSettingsSave_FailedSaveDoesNotLeakIntoLaterSave(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	if rec := postForm(mux, "/api/settings/save", url.Values{"currency": {"GBP"}, "taxRatePct": {"20"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("seed save = %d", rec.Code)
	}

	if _, err := d.Db.Exec(`
CREATE TRIGGER boom2 BEFORE INSERT ON settings
WHEN NEW.key = 'store.tax_rate'
BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	rec := postForm(mux, "/api/settings/save", url.Values{"currency": {"EUR"}, "taxRatePct": {"7"}}, &mgrUser)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("failed save = %d, want 500", rec.Code)
	}
	if d.CurrentState().Currency != "GBP" {
		t.Fatalf("in-memory currency = %q after failed save, want unchanged %q (leaked into in-memory state)", d.CurrentState().Currency, "GBP")
	}

	if _, err := d.Db.Exec(`DROP TRIGGER boom2`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}

	// An unrelated, otherwise-successful save must not silently persist the
	// previously-rejected currency/tax-rate change.
	if rec := postForm(mux, "/api/settings/ui-scale", url.Values{"scale": {"1.5"}}, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("unrelated ui-scale save = %d", rec.Code)
	}
	if v, _, _ := d.Settings.Get(t.Context(), "store.currency"); v != "GBP" {
		t.Fatalf("store.currency = %q after an unrelated save, want still %q (rejected change leaked)", v, "GBP")
	}
	if v, _, _ := d.Settings.Get(t.Context(), "store.tax_rate"); v != "20" {
		t.Fatalf("store.tax_rate = %q after an unrelated save, want still %q", v, "20")
	}
	if d.Engine.Config().TaxRateBasisPoints != 2000 {
		t.Fatalf("engine TaxRateBasisPoints = %d after an unrelated save, want unchanged 2000", d.Engine.Config().TaxRateBasisPoints)
	}
}

// GET /api/enrol/devices is read-only and stays on the flat canPerform gate
// (out of ut-docs#865's scope, same as ut-docs#796's own non-goals) — a
// denied cashier still gets the 200 HTMX swap target with a forbidden/muted
// notice, never a hard status.
func TestEnrolDevicesEndpointRefusesNonManager(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/api/enrol/devices", nil), cashUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/enrol/devices: code=%d, want 200 (HTMX swap target)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `class="error"`) && !strings.Contains(body, `class="muted"`) {
		t.Fatalf("GET /api/enrol/devices did not render a forbidden notice: %s", body)
	}
}

// ut-docs#865: claim-code and enrol/now moved off the flat canPerform gate
// onto checkOrElevate (#557/#796 mechanism) — a denied cashier now gets the
// in-place elevation prompt, not the flat forbidden span.
func TestEnrolClaimCodeAndNow_RefuseNonManagerViaElevation(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)

	for _, path := range []string{"/api/enrol/claim-code", "/api/enrol/now"} {
		req := auth.WithUser(httptest.NewRequest(http.MethodPost, path, nil), cashUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "elevation-dialog") ||
			!strings.Contains(rec.Body.String(), `name="override_pin"`) {
			t.Fatalf("POST %s: code=%d body=%s, want 200 with the elevation prompt", path, rec.Code, rec.Body.String())
		}
	}
}

// settingsForbiddenText is the exact localized string every HTMX-style
// (200-with-error-span) settings endpoint renders when canPerform() denies
// the request — httpx.T(locale, "settings.enrol.forbidden") in
// web/locales/en.json. Used below to tell "denied" apart from "past the
// gate but failed downstream for an unrelated reason" on those endpoints,
// which never answer a hard 403.
const settingsForbiddenText = "Only a manager or admin can register this till."

// TestSettingsEndpoints_RoleMatrix is ut-docs#710's role-matrix proof: every
// isManagerOrAuthOff site this card moved onto canPerform(d, r, "settings")
// (19 handler gates + the GET /settings "isManager" template flag covered
// separately by TestSettingsPage_HidesManagerOnlyCardsFromCashier) denies a
// cashier and lets manager/admin/super_admin past the auth gate — same
// table-driven role-matrix convention as #706's
// TestPluginManagementEndpoints_RealSessionGatesByRole and #707's
// TestDataManagementEndpoints_RealSessionGatesByRole
// (data_backup_manager_gate_test.go). super_admin is the row that actually
// documents canPerform()'s real broadening over the old isManagerOrAuthOff
// gate (User.IsManager() never recognized super_admin) — written to fail
// against the pre-#710 gate (a super_admin session got 403/the forbidden
// span everywhere, same as a cashier) and confirmed to pass once every site
// in settings_page.go was switched.
//
// Denied behavior comes in three flavors since ut-docs#796/#865:
//   - gate403: a hard 403 (the not-yet-elevation-wired plain handlers —
//     none remain in this file as of ut-docs#865; kept as a gateKind since
//     it's still the right shape for a future not-yet-wired site).
//   - gateForbiddenSpan: an HTMX swap target that always answers 200 with
//     the localized forbidden text in an error/muted span (HTMX drops
//     non-2xx bodies) — GET /api/enrol/devices (read-only), still on the
//     flat gate.
//   - gateElevation: the handlers wired to checkOrElevate (ut-docs#796's
//     original 8, plus #865's 10) — a denied cashier gets 200 with the
//     in-place elevation prompt dialog (neither the forbidden text nor a
//     403).
//
// "Past the gate" for manager/admin/super_admin means downstream processing
// was reached ON THEIR OWN SESSION — no PIN involved (an already-authorized
// user hits checkOrElevate's allowed branch, never needsElevation) — not
// that it necessarily succeeded (e.g. enrol/now still fails offline-first
// with no reachable marketplace, which is fine — this test only proves the
// auth gate itself).
func TestSettingsEndpoints_RoleMatrix(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	if _, err := d.Db.Exec(`INSERT INTO registers(id,name,is_active) VALUES('regA','Front Till',1)`); err != nil {
		t.Fatal(err)
	}
	// ut-docs#868: dismiss-pending-base-plugin now validates canonical_type/
	// locale against the pending list before checkOrElevate, so this row's
	// {"canonical_type": {"x"}} (locale unset -> "") must actually be
	// pending, or every case below sees the new 400 instead of the gate
	// this test is about.
	if err := savePendingBasePlugins(t.Context(), d, []basePluginSpec{{CanonicalType: "x", Locale: ""}}); err != nil {
		t.Fatal(err)
	}

	type gateKind int
	const (
		gate403 gateKind = iota
		gateForbiddenSpan
		gateElevation
	)

	type matrixCase struct {
		name, method, path string
		form               url.Values
		gate               gateKind
	}

	cases := []matrixCase{
		{"payments-default", http.MethodPost, "/api/settings/payments-default", url.Values{"method": {"cash"}}, gateElevation},
		{"payments-fee", http.MethodPost, "/api/settings/payments-fee", url.Values{"method": {"cash"}, "percent": {"1"}}, gateElevation},
		{"enrol-claim-code", http.MethodPost, "/api/enrol/claim-code", nil, gateElevation},
		{"enrol-now", http.MethodPost, "/api/enrol/now", nil, gateElevation},
		{"enrol-pair", http.MethodPost, "/api/enrol/pair", nil, gateElevation},
		{"enrol-check-plan", http.MethodPost, "/api/enrol/check-plan", nil, gateElevation},
		{"enrol-devices", http.MethodGet, "/api/enrol/devices", nil, gateForbiddenSpan},
		{"idle-lock", http.MethodPost, "/api/settings/idle-lock", url.Values{"minutes": {"10"}}, gateElevation},
		{"kiosk-idle-reset", http.MethodPost, "/api/settings/kiosk-idle-reset", url.Values{"seconds": {"30"}}, gateElevation},
		{"kiosk-payment-mode", http.MethodPost, "/api/settings/kiosk-payment-mode", url.Values{"mode": {"counter"}}, gateElevation},
		{"window-mode", http.MethodPost, "/api/settings/window-mode", url.Values{"mode": {"kiosk"}}, gateElevation},
		{"launch-on-startup", http.MethodPost, "/api/settings/launch-on-startup", url.Values{"enabled": {"true"}}, gateElevation},
		{"telemetry", http.MethodPost, "/api/settings/telemetry", url.Values{"optIn": {"on"}}, gateElevation},
		{"display-mode", http.MethodPost, "/api/settings/display-mode", url.Values{"mode": {"backoffice"}}, gateElevation},
		{"shop-type", http.MethodPost, "/api/settings/shop-type", url.Values{"shop_type": {""}}, gateElevation},
		{"store-name", http.MethodPost, "/api/settings/store-name", url.Values{"store_name": {"Corner Café"}}, gateElevation},
		{"remove-demo-catalogue", http.MethodPost, "/api/settings/remove-demo-catalogue", nil, gateElevation},
		{"dismiss-pending-base-plugin", http.MethodPost, "/api/settings/dismiss-pending-base-plugin", url.Values{"canonical_type": {"x"}}, gateElevation},
		{"till-name", http.MethodPost, "/api/settings/till-name", url.Values{"name": {"X"}}, gateElevation},
		{"till-register", http.MethodPost, "/api/settings/till-register", url.Values{"register_id": {"regA"}}, gateElevation},
		{"save", http.MethodPost, "/api/settings/save", url.Values{"currency": {"GBP"}}, gateElevation},
		{"upsert", http.MethodPost, "/api/settings/upsert", url.Values{"key": {"x"}, "value": {"y"}}, gateElevation},
		{"catalog-import-barcode-default", http.MethodPost, "/api/settings/catalog-import-barcode-default", url.Values{"enabled": {"true"}}, gateElevation},
		{"catalog-pre-pack-unit-price", http.MethodPost, "/api/settings/catalog-pre-pack-unit-price", url.Values{"enabled": {"true"}}, gateElevation},
		{"browsing-mode", http.MethodPost, "/api/settings/browsing-mode", url.Values{"mode": {"strip_overflow"}}, gateElevation},
	}

	doReq := func(tc matrixCase, u auth.User) *httptest.ResponseRecorder {
		var req *http.Request
		if tc.form != nil {
			req = httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			req = httptest.NewRequest(tc.method, tc.path, nil)
		}
		req = auth.WithUser(req, u)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	for _, tc := range cases {
		t.Run(tc.name+"/cashier_denied", func(t *testing.T) {
			rec := doReq(tc, cashUser)
			switch tc.gate {
			case gateForbiddenSpan:
				if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), settingsForbiddenText) {
					t.Fatalf("%s %s cashier = %d %q, want 200 with the forbidden text", tc.method, tc.path, rec.Code, rec.Body.String())
				}
			case gateElevation:
				if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "elevation-dialog") ||
					!strings.Contains(rec.Body.String(), `name="override_pin"`) {
					t.Fatalf("%s %s cashier = %d %q, want 200 with the elevation prompt", tc.method, tc.path, rec.Code, rec.Body.String())
				}
				if strings.Contains(rec.Body.String(), settingsForbiddenText) {
					t.Fatalf("%s %s cashier got the OLD forbidden text alongside the prompt: %s", tc.method, tc.path, rec.Body.String())
				}
			default:
				if rec.Code != http.StatusForbidden {
					t.Fatalf("%s %s cashier = %d, want 403", tc.method, tc.path, rec.Code)
				}
			}
		})
		for _, role := range []string{"manager", "admin", "super_admin"} {
			t.Run(tc.name+"/"+role+"_past_gate", func(t *testing.T) {
				u := auth.User{ID: "u-" + role, Role: role}
				rec := doReq(tc, u)
				switch tc.gate {
				case gateForbiddenSpan:
					if strings.Contains(rec.Body.String(), settingsForbiddenText) {
						t.Fatalf("%s %s %s got the forbidden text, want past the auth gate: %s", tc.method, tc.path, role, rec.Body.String())
					}
				case gateElevation:
					// An already-authorized session hits the allowed branch
					// on its own — no 403, and crucially NO elevation prompt
					// (no PIN should ever be demanded of them).
					if rec.Code == http.StatusForbidden {
						t.Fatalf("%s %s %s = 403, want past the auth gate", tc.method, tc.path, role)
					}
					if strings.Contains(rec.Body.String(), "elevation-dialog") {
						t.Fatalf("%s %s %s was shown the elevation prompt on an already-authorized session: %s", tc.method, tc.path, role, rec.Body.String())
					}
				default:
					if rec.Code == http.StatusForbidden {
						t.Fatalf("%s %s %s = 403, want past the auth gate", tc.method, tc.path, role)
					}
				}
			})
		}
	}
}

// TestSettingsPage_ElevationWiredFormsVisibleToCashier is ut-docs#867's
// template-visibility proof. Every settings form whose POST handler goes
// through checkOrElevate (ADR-0052's in-place manager-PIN dialog,
// elevation.go) must render for a cashier too — hiding it behind
// {{ if .isManager }} (the same canPerform check elevation exists to soften)
// meant the shipped UI could never trigger the dialog at all; a denied
// cashier just never saw the form. Authorization is unchanged: the server
// still answers a denied POST with the elevation prompt.
//
// Content that is NOT elevation-wired (flat canPerform/deny handlers, the
// exit-to-os AuthorizeManager flow, real business data like backup files or
// GDPR search) stays manager-gated exactly as before, as does the raw
// key/value upsert browser (elevation-wired but deliberately excepted — an
// unbounded settings-store browser has no cashier-triggerable action to
// name in a PIN prompt).
//
// The enrollment card's enrolled branch (claim-code) can't be exercised
// here — "enrolled" reads package-global enroll.CurrentStatus() — so the
// card is covered via its unenrolled branch (/api/enrol/now), the identical
// un-gating in the same card.
func TestSettingsPage_ElevationWiredFormsVisibleToCashier(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	// Seed the DATA-availability guards (not permission guards — those must
	// survive ut-docs#867 untouched) so every guarded un-gated site actually
	// renders: a payment method ({{ if .payMethods }}), a sample catalogue
	// item ({{ if gt .sampleCount 0 }}), a pending base plugin
	// ({{ range .pendingBasePlugins }}), and a register for the picker.
	// payment_methods (already seeded with cash/card/gift) and items come
	// from the real migrations openPagesTestDB now runs (ut-docs#1657/#1677)
	// -- this used to hand-roll both tables from scratch.
	for _, s := range []string{
		`INSERT INTO items (id, name, base_price, is_sample_data) VALUES ('demo-1', 'Demo Widget', 100, 1)`,
		`INSERT INTO registers(id,name,is_active) VALUES('regA','Front Till',1)`,
	} {
		if _, err := d.Db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	if err := savePendingBasePlugins(t.Context(), d, []basePluginSpec{{CanonicalType: "language", Locale: "de"}}); err != nil {
		t.Fatal(err)
	}

	get := func(u auth.User) string {
		req := httptest.NewRequest(http.MethodGet, "/settings", nil)
		req = auth.WithUser(req, u)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /settings = %d", rec.Code)
		}
		return rec.Body.String()
	}

	// One marker per elevation-wired site, unique to that form/button.
	elevationWired := []string{
		`hx-post="/api/enrol/now"`,                   // enrollment card, unenrolled branch
		`hx-post="/api/settings/display-mode"`,       // display-advanced: mode form
		`id="window-mode-form"`,                      // display-advanced: window mode
		`id="launch-on-startup-cb"`,                  // display-advanced: autostart checkbox
		`hx-post="/api/settings/payments-default"`,   // payments card (kept {{ if .payMethods }})
		`hx-post="/api/settings/payments-fee"`,       // payments fee rows
		`hx-post="/api/backup/now"`,                  // backup card: only the Backup-now button
		`data-testid="demo-remove"`,                  // data card (kept sampleCount guard)
		`data-testid="pending-base-plugin-dismiss"`,  // data card (kept pendingBasePlugins guard)
		`hx-post="/api/settings/report-retention"`,   // retention card: mode form only
		`hx-post="/api/settings/till-name"`,          // tills card: this till's own name
		`hx-post="/api/settings/till-register"`,      // tills card: register picker
		`hx-post="/api/settings/idle-lock"`,          // idle-lock card
		`hx-post="/api/settings/kiosk-idle-reset"`,   // kiosk-idle-reset card
		`hx-post="/api/settings/kiosk-payment-mode"`, // kiosk-payment-mode card
		`hx-post="/api/settings/telemetry"`,          // telemetry card
		`hx-post="/api/settings/save"`,               // currency card
		`hx-post="/api/settings/shop-type"`,          // shop-type card
		`hx-post="/api/settings/store-name"`,         // ut-docs#3115: shop-name card
		`hx-post="/api/settings/printer"`,            // ut-docs#866: printer card
		`hx-post="/api/settings/invoice"`,            // ut-docs#866: invoice card
	}

	// Manager-only content — one marker per site that must stay gated. The
	// prose markers are the empty-state strings those gated blocks render in
	// this dataless fixture (their tables/exports have no structural marker
	// until data exists).
	managerOnly := []string{
		`href="/report-issue"`,                     // issue-report card: not elevation-wired
		`hx-post="/api/update/check"`,              // update card: flat canPerform(plugin_management)
		`hx-post="/api/settings/update-schedule"`,  // update card
		`id="exit-to-os-form"`,                     // its own AuthorizeManager PIN flow, out of scope
		"No backups yet",                           // backup file table / empty-state: flat deny, real content
		`data-testid="data-reset"`,                 // reset-transactions: flat-denied fetch()
		`id="reset-archives"`,                      // archives restore/purge: flat-denied
		`id="cust-search-btn"`,                     // GDPR search: flat-denied, surfaces PII
		`id="cat-preview-btn"`,                     // catalog cleanup: flat-denied
		"No export or report plugin is installed.", // data-export section: flat-denied
		"No archived reports yet.",                 // retention coverage summary: business content
		`data-testid="retention-export"`,           // retention export: elevation-wired endpoint, but stays gated — real business content (coverage stats + a sales-report download), not just an action
		`hx-post="/api/settings/upsert"`,           // raw upsert browser: deliberate exception
		`id="new-setting"`,                         // raw upsert browser's add form
	}

	// ut-docs#3079 supersedes #867's cashier half: a cashier is sale-only
	// and gets the 403 page, so NONE of these forms reach them any more (a
	// manager signs in, or an admin grants "settings" in Users →
	// Permissions). The manager half below is unchanged.
	cashierHTML := settingsRefusedForCashier(t, mux)
	for _, marker := range elevationWired {
		if strings.Contains(cashierHTML, marker) {
			t.Errorf("cashier's 403 still carries elevation-wired site %s", marker)
		}
	}
	for _, marker := range managerOnly {
		if strings.Contains(cashierHTML, marker) {
			t.Errorf("cashier render leaks manager-only content %s", marker)
		}
	}

	// No regression for the already-working case: a manager sees everything.
	managerHTML := get(mgrUser)
	for _, marker := range elevationWired {
		if !strings.Contains(managerHTML, marker) {
			t.Errorf("manager render is missing %s", marker)
		}
	}
	for _, marker := range managerOnly {
		if !strings.Contains(managerHTML, marker) {
			t.Errorf("manager render is missing manager-only content %s", marker)
		}
	}
}

// ut-docs#1290: the payments-fee row's FixedMaj prefill (the "fixed"
// input's value attribute) hardcoded a 2-decimal `%.2f` against `/100`,
// same defect class as ut-docs#1274's CarryForwardDisplay -- silently wrong
// on a 0-decimal currency (IRR/IRT/IQD/AFN/JPY), where minor units ARE
// major units (500 minor rendered "5.00" instead of "500"). Stores the fee
// setting directly at a known minor value so this test isolates the
// display/prefill fix from the (separate, out-of-scope) fixed-field
// write-path parsing in POST /api/settings/payments-fee.
func TestSettingsPage_PaymentsFeeFixedMajIsCurrencyAware(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	// payment_methods (already seeded with cash/card/gift) comes from the
	// real migrations openPagesTestDB now runs (ut-docs#1657/#1677).
	if err := d.Settings.Set(t.Context(), "payments.fee.cash", `{"bp":0,"fixed":500}`); err != nil {
		t.Fatal(err)
	}

	get := func() string {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), mgrUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /settings = %d: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	httpx.InitCurrency("GBP")
	body := get()
	if !strings.Contains(body, `value="5.00"`) {
		t.Fatalf("expected the 2-decimal FixedMaj prefill for GBP, got:\n%s", body)
	}

	httpx.InitCurrency("IRT")
	t.Cleanup(func() { httpx.InitCurrency("GBP") }) // ut-docs#970 convention: process-global, reset for later tests in this package.
	body = get()
	if !strings.Contains(body, `value="500"`) {
		t.Fatalf("expected the 0-decimal FixedMaj prefill \"500\" (no /100) for IRT, got:\n%s", body)
	}
	if strings.Contains(body, `value="5.00"`) {
		t.Fatalf("expected NO 2-decimal FixedMaj prefill left over once currency is 0-decimal, got:\n%s", body)
	}
}

// ut-docs#867 review nit: with none of the Data card's data-availability
// guards true (no demo sample, no pending base plugin, no deferred restore
// prompt), a cashier must not get an empty bordered card containing only the
// "🧹 Data management" heading — the card itself should be absent, same as
// any other manager-only card with nothing to show.
func TestSettingsPage_DataCardHiddenFromCashierWhenNothingPending(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	_ = d

	body := settingsRefusedForCashier(t, mux) // ut-docs#3079: now refused outright
	if strings.Contains(body, "Data management") {
		t.Errorf("cashier with no pending demo/restore/plugin data still sees the Data management card heading")
	}
	if strings.Contains(body, `data-testid="demo-remove"`) || strings.Contains(body, `data-testid="restore-dismiss"`) || strings.Contains(body, `data-testid="pending-base-plugin-dismiss"`) {
		t.Errorf("cashier sees a Data-card sub-action with nothing backing it")
	}
}

// ut-docs#866 review (N3): un-gating the whole printer card would newly
// expose two flat-denied controls to a cashier — the Test print button
// (no audit trail, stays out of the elevation mechanism) and the
// receipt-designer link (a flat-denied full-page redirect, ut-docs#870) —
// producing the exact "visible but silently blocked" bug this line of work
// exists to remove (a click would fall through to app.js's generic
// server-error banner). Both stay manager-only; the mode/address form
// itself (the actual elevation-wired site) must still render.
func TestSettingsPage_PrinterCardHidesTestPrintAndDesignerLinkFromCashier(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	_ = d

	// ut-docs#3079: a cashier now gets the 403 page — not even the
	// elevation-wired printer form (#866) reaches them.
	body := settingsRefusedForCashier(t, mux)
	if strings.Contains(body, `hx-post="/api/settings/printer"`) {
		t.Fatal("cashier's 403 still carries the printer form")
	}
	if strings.Contains(body, `hx-post="/api/print/test"`) {
		t.Error("cashier render leaks the flat-denied Test print button")
	}
	if strings.Contains(body, `href="/receipt-designer"`) {
		t.Error("cashier render leaks the flat-denied receipt-designer link")
	}
	if strings.Contains(body, `id="printer-discover-btn"`) {
		t.Error("cashier render leaks the manager-only Find-printers button (ut-docs#1556) — the underlying discover-printers endpoint is hard manager-gated, so an un-gated button here would be the same visible-but-silently-blocked bug")
	}

	mgrReq := httptest.NewRequest(http.MethodGet, "/settings", nil)
	mgrReq = auth.WithUser(mgrReq, mgrUser)
	mgrRec := httptest.NewRecorder()
	mux.ServeHTTP(mgrRec, mgrReq)
	mgrBody := mgrRec.Body.String()
	if !strings.Contains(mgrBody, `hx-post="/api/print/test"`) || !strings.Contains(mgrBody, `href="/receipt-designer"`) {
		t.Error("manager render is missing the Test print button or receipt-designer link")
	}
	if !strings.Contains(mgrBody, `id="printer-discover-btn"`) {
		t.Error("manager render is missing the Find-printers button (ut-docs#1556)")
	}
}

// ut-docs#1556: the settings page's Find-printers button reuses the exact
// same manager-gated endpoint kitchen_stations.html already calls
// (POST /api/kitchen-stations/discover-printers — GET until ut-docs#1582
// made it POST) — no new route is
// introduced. This locks in that the printer card's two address fields
// (receipt + kitchen) both have their own input id for the page's JS to
// target, so a future edit can't silently drop one field's "Use for X"
// wiring without a visible test failure.
// ut-docs#1775: the settings page's charset dropdown must actually render
// the new win1254 option with its translated label — a passing
// internal/print unit test proves the ENCODER supports win1254, not that
// an operator can find and pick it on the real Settings screen.
func TestSettingsPage_PrinterCardHasWin1254CharsetOption(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)

	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	req = auth.WithUser(req, mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `value="win1254"`) {
		t.Fatal("printer charset dropdown is missing the win1254 <option>")
	}
	if !strings.Contains(body, "Windows-1254") {
		t.Fatal("printer charset dropdown is missing the win1254 option's translated label")
	}
}

func TestSettingsPage_PrinterCardHasDiscoverableAddressFieldIDs(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)

	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	req = auth.WithUser(req, mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, id := range []string{"printer-address-input", "printer-kitchen-addr-input", "printer-discover-results", "printer-discover-msg"} {
		if !strings.Contains(body, `id="`+id+`"`) {
			t.Errorf("manager render is missing expected element id=%q", id)
		}
	}
}

// ut-docs#2168: the kitchen-printer address field used to render directly
// above the receipt-policy control with no visual boundary between them —
// a real product-owner report said the receipt-policy control read as
// belonging to the kitchen printer and appeared to have been removed. It
// must now render inside its own <fieldset>/<legend> group, positioned
// after every receipt-printer control (including receipt policy), so the
// two printers' settings are visibly distinct.
func TestSettingsPage_KitchenPrinterIsGroupedSeparatelyFromReceiptPolicy(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)

	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	req = auth.WithUser(req, mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	body := rec.Body.String()

	policyIdx := strings.Index(body, `name="receiptPolicy"`)
	if policyIdx == -1 {
		t.Fatalf("expected the receipt-policy control to render")
	}
	afterPolicy := body[policyIdx:]
	fieldsetRel := strings.Index(afterPolicy, "<fieldset")
	if fieldsetRel == -1 {
		t.Fatalf("expected a <fieldset> (the kitchen-printer group) after the receipt-policy control")
	}
	kitchenSection := afterPolicy[fieldsetRel:]
	closeRel := strings.Index(kitchenSection, "</fieldset>")
	if closeRel == -1 {
		t.Fatalf("the kitchen-printer fieldset never closes")
	}
	kitchenSection = kitchenSection[:closeRel]
	if !strings.Contains(kitchenSection, `name="kitchenAddr"`) {
		t.Fatalf("expected the kitchen-printer address input inside the fieldset that follows receipt policy, got:\n%s", kitchenSection)
	}
	// Asserts the <legend> tag itself, not just the substring "Kitchen
	// printer" anywhere in the section — the field's own label text is a
	// different key (settings.printer.address, "Printer address") precisely
	// so this can't pass without a real legend (review finding, ut-docs#2168:
	// an earlier version of this assertion passed even with the <legend>
	// line deleted entirely, because kitchen_addr's own former label text
	// was the same phrase).
	if !strings.Contains(kitchenSection, "<legend>Kitchen printer</legend>") {
		t.Fatalf("expected the kitchen-printer fieldset's own translated <legend>, got:\n%s", kitchenSection)
	}
}

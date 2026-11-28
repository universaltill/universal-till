package pages

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/shoptemplate"
)

// ut-docs#6332 S7: pick -> preview (no write) -> PIN-confirmed change with
// opt-in values -> 7 day undo. Expected values are named: cafe seeds
// nothing, retail seeds sale.order_type_prompt=off, hospitality (extends
// cafe) shows Tables.

func stSet(t *testing.T, d *common.Deps, k, v string) {
	t.Helper()
	if err := d.Settings.Set(t.Context(), k, v); err != nil {
		t.Fatal(err)
	}
}

func stGet(t *testing.T, d *common.Deps, k string) string {
	t.Helper()
	v, _, err := d.Settings.Get(t.Context(), k)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// stChange POSTs a confirmed change as a manager and expects success.
func stChange(t *testing.T, mux *http.ServeMux, to string, ticked ...string) {
	t.Helper()
	form := url.Values{"to": {to}}
	for _, k := range ticked {
		form.Add("apply_values[]", k)
	}
	rec := postForm(mux, "/api/settings/shop-type", form, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("change to %s: %d %s", to, rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Redirect"); got != "/settings" {
		t.Fatalf("HX-Redirect = %q, want /settings", got)
	}
}

func TestShopTypePreview_ListsVisibilityAndWritesNothing(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	useRealShopTemplates(t, d)
	stChange(t, mux, "cafe") // cafe hides Tables
	ledgerBefore := stGet(t, d, shoptemplate.LedgerKey)

	rec := getAs(mux, "/api/settings/shop-type/preview?to=hospitality", &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Change shop type: Cafe or takeaway counter to Restaurant or bar",
		"Will appear in your menu", "<li>Tables &amp; floor plan</li>",
		"Nothing about payments, tax, staff or PINs is ever changed by a shop type.",
		`name="to" value="hospitality"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("preview lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Only the label changes") {
		t.Error("a real visibility change must not say label-only")
	}
	if got := stGet(t, d, common.KeyShopType); got != "cafe" {
		t.Fatalf("preview wrote shop.type = %q, want cafe", got)
	}
	if got := stGet(t, d, shoptemplate.LedgerKey); got != ledgerBefore {
		t.Fatalf("preview changed the ledger: %q -> %q", ledgerBefore, got)
	}
	if got := stGet(t, d, shoptemplate.UndoKey); got == "" {
		t.Fatal("the earlier confirmed change must have left its undo record untouched by the preview")
	}
}

func TestShopTypePreview_UnknownTypeIs400(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	useRealShopTemplates(t, d)
	for _, to := range []string{"", "unicorn_farm"} {
		if rec := getAs(mux, "/api/settings/shop-type/preview?to="+to, &mgrUser); rec.Code != http.StatusBadRequest {
			t.Errorf("to=%q: %d, want 400", to, rec.Code)
		}
	}
}

func TestShopTypePick_CurrentTileIsDisabledAndTagged(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	useRealShopTemplates(t, d)
	stChange(t, mux, "retail")
	rec := getAs(mux, "/api/settings/shop-type/pick", &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("pick: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if n := strings.Count(body, `role="radio"`); n != 6 {
		t.Errorf("%d tiles, want 6", n)
	}
	if !strings.Contains(body, `aria-disabled="true" disabled data-value="retail"`) || !strings.Contains(body, ">Current<") {
		t.Errorf("the current tile must be disabled and tagged Current:\n%s", body)
	}
	if !strings.Contains(body, "Stock, barcodes, receipts. No tables or kitchen screens.") {
		t.Error("tile summaries (one source with the wizard) missing")
	}
}

func TestShopTypeConfirm_NothingTickedChangesNoValue(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	useRealShopTemplates(t, d)
	stChange(t, mux, "cafe")
	stChange(t, mux, "retail") // retail suggests order_type_prompt=off, not ticked
	if got := stGet(t, d, data.OrderTypePromptModeKey); got != "" {
		t.Fatalf("%s = %q with nothing ticked, want unchanged (empty)", data.OrderTypePromptModeKey, got)
	}
	if got := stGet(t, d, common.KeyShopType); got != "retail" {
		t.Fatalf("shop.type = %q, want retail", got)
	}
	if !strings.Contains(stGet(t, d, shoptemplate.LedgerKey), `"id":"retail"`) {
		t.Fatalf("ledger = %q, want the retail template", stGet(t, d, shoptemplate.LedgerKey))
	}
}

func TestShopTypeConfirm_OnlyOfferedKeysAreAppliedNeverTheForm(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	useRealShopTemplates(t, d)
	stChange(t, mux, "cafe")
	stSet(t, d, "payments.default_method", "cash")
	stChange(t, mux, "retail", data.OrderTypePromptModeKey, "payments.default_method", "sale.display_no_scheme")
	if got := stGet(t, d, data.OrderTypePromptModeKey); got != "off" {
		t.Fatalf("ticked offered key = %q, want off", got)
	}
	if got := stGet(t, d, "payments.default_method"); got != "cash" {
		t.Fatalf("a forged payment key was written: %q", got)
	}
	if got := stGet(t, d, "sale.display_no_scheme"); got != "" {
		t.Fatalf("a key retail does not suggest was written: %q", got)
	}
}

func TestShopTypeUndo_RestoresTypeLedgerAndTickedValues(t *testing.T) {
	t.Cleanup(httpx.SnapshotStateForTests())
	mux, _, d := newFullAuthDeps(t)
	useRealShopTemplates(t, d)
	stChange(t, mux, "cafe")
	ledgerCafe := stGet(t, d, shoptemplate.LedgerKey)
	stChange(t, mux, "retail", data.OrderTypePromptModeKey)

	if !httpx.OrderTypeOff() {
		t.Fatalf("live order type prompt after the change to retail: want off")
	}
	rec := postForm(mux, "/api/settings/shop-type/undo", url.Values{}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("undo: %d %s", rec.Code, rec.Body.String())
	}
	if httpx.OrderTypeOff() {
		t.Fatalf("live order type prompt stayed off after the undo: want the pre-change default (top) without a restart")
	}
	if got := rec.Header().Get("HX-Redirect"); got != "/settings?shop_type_undone=0" {
		t.Fatalf("HX-Redirect = %q", got)
	}
	if got := stGet(t, d, common.KeyShopType); got != "cafe" {
		t.Fatalf("shop.type = %q, want cafe", got)
	}
	if got := stGet(t, d, shoptemplate.LedgerKey); got != ledgerCafe {
		t.Fatalf("ledger = %q, want the cafe ledger %q", got, ledgerCafe)
	}
	if got := stGet(t, d, data.OrderTypePromptModeKey); got != "" {
		t.Fatalf("order type prompt = %q, want the pre-change empty value", got)
	}
	if got := stGet(t, d, shoptemplate.UndoKey); got != "" {
		t.Fatalf("undo record survived its own undo: %q", got)
	}
	if rec := postForm(mux, "/api/settings/shop-type/undo", url.Values{}, &mgrUser); rec.Code != http.StatusConflict {
		t.Fatalf("second undo: %d, want 409 (nothing to undo)", rec.Code)
	}
}

func TestShopTypeUndo_LeavesAValueTheOwnerEditedSince(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	useRealShopTemplates(t, d)
	stChange(t, mux, "cafe")
	stChange(t, mux, "retail", data.OrderTypePromptModeKey) // writes off
	stSet(t, d, data.OrderTypePromptModeKey, "at_pay")      // the owner edits it

	rec := postForm(mux, "/api/settings/shop-type/undo", url.Values{}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("undo: %d %s", rec.Code, rec.Body.String())
	}
	if got := stGet(t, d, data.OrderTypePromptModeKey); got != "at_pay" {
		t.Fatalf("owner's value = %q, want at_pay kept", got)
	}
	if got := rec.Header().Get("HX-Redirect"); got != "/settings?shop_type_undone=1" {
		t.Fatalf("HX-Redirect = %q, want 1 value left alone", got)
	}
}

func TestShopTypeUndo_ExpiredRecordIsRefusedAndBannerPruned(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	useRealShopTemplates(t, d)
	stChange(t, mux, "cafe")
	stChange(t, mux, "retail")
	old := shoptemplate.NewUndo("retail", "cafe", "", nil, nil, time.Now().Add(-8*24*time.Hour))
	stSet(t, d, shoptemplate.UndoKey, old.Encode())

	if rec := postForm(mux, "/api/settings/shop-type/undo", url.Values{}, &mgrUser); rec.Code != http.StatusConflict {
		t.Fatalf("expired undo: %d, want 409", rec.Code)
	}
	if body := getAs(mux, "/settings", &mgrUser).Body.String(); strings.Contains(body, `data-testid="shop-type-undo"`) {
		t.Fatal("an expired record must not render the banner")
	}
	if got := stGet(t, d, shoptemplate.UndoKey); got != "" {
		t.Fatalf("expired record not pruned at read: %q", got)
	}
}

func TestShopTypeUndoBanner_OnSettingsAndMenuForManagerOnly(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	useRealShopTemplates(t, d)
	stChange(t, mux, "retail")
	for _, path := range []string{"/settings"} {
		body := getAs(mux, path, &mgrUser).Body.String()
		if !strings.Contains(body, `data-testid="shop-type-undo"`) || !strings.Contains(body, "Shop type changed to Retail shop.") {
			t.Errorf("%s: manager must see the undo banner", path)
		}
		if !strings.Contains(body, `role="status"`) {
			t.Errorf("%s: banner is not a status region", path)
		}
		if cash := getAs(mux, path, &cashUser).Body.String(); strings.Contains(cash, `data-testid="shop-type-undo"`) {
			t.Errorf("%s: a cashier must never see the banner", path)
		}
	}
}

func TestShopTypeUndoBanner_OnMenuWhileUnexpired(t *testing.T) {
	mux, dp := activeRetailMenuDeps(t)
	body := func() string { return getPage(t, mux, "/menu").Body.String() }
	if strings.Contains(body(), `data-testid="shop-type-undo"`) {
		t.Fatal("no undo record: no banner")
	}
	stSet(t, dp, shoptemplate.UndoKey, shoptemplate.NewUndo("retail", "cafe", "", nil, nil, time.Now()).Encode())
	b := body()
	if !strings.Contains(b, `data-testid="shop-type-undo"`) || !strings.Contains(b, "Shop type changed to Retail shop.") || !strings.Contains(b, `role="status"`) {
		t.Fatalf("menu must show the undo strip:\n%s", b)
	}
	// The type moved on by another route: nothing to undo here.
	stSet(t, dp, common.KeyShopType, "cafe")
	if strings.Contains(body(), `data-testid="shop-type-undo"`) {
		t.Fatal("banner must not show once shop.type no longer matches the record")
	}
}

func TestShopTypeDismiss_RemovesRecordAndBanner(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	useRealShopTemplates(t, d)
	stChange(t, mux, "retail")
	rec := postForm(mux, "/api/settings/shop-type/dismiss-banner", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("dismiss: %d %q, want 200 with an empty body", rec.Code, rec.Body.String())
	}
	if strings.Contains(getAs(mux, "/settings", &mgrUser).Body.String(), `data-testid="shop-type-undo"`) {
		t.Fatal("banner still renders after dismiss")
	}
}

func TestShopTypeEndpoints_CashierGets403(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	useRealShopTemplates(t, d)
	stChange(t, mux, "retail")
	for _, path := range []string{"/api/settings/shop-type/pick", "/api/settings/shop-type/panel", "/api/settings/shop-type/preview?to=cafe"} {
		if rec := getAs(mux, path, &cashUser); rec.Code != http.StatusForbidden {
			t.Errorf("cashier GET %s = %d, want 403", path, rec.Code)
		}
	}
	for _, path := range []string{"/api/settings/shop-type/undo", "/api/settings/shop-type/dismiss-banner"} {
		if rec := postForm(mux, path, url.Values{}, &cashUser); rec.Code != http.StatusForbidden {
			t.Errorf("cashier POST %s = %d, want 403", path, rec.Code)
		}
	}
	if got := stGet(t, d, shoptemplate.UndoKey); got == "" {
		t.Fatal("a refused cashier request must leave the undo record in place")
	}
}

func TestShopTypeEndpoints_SatelliteGets409WithLockText(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	useRealShopTemplates(t, d)
	stChange(t, mux, "retail")
	stSet(t, d, "sync.primary_url", "http://192.0.2.10:8080")

	for _, path := range []string{"/api/settings/shop-type/pick", "/api/settings/shop-type/preview?to=cafe"} {
		rec := getAs(mux, path, &mgrUser)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Set on the main till (192.0.2.10:8080). To change it, go there.") {
			t.Errorf("GET %s = %d %s, want 409 with the lock text", path, rec.Code, rec.Body.String())
		}
	}
	for _, path := range []string{"/api/settings/shop-type", "/api/settings/shop-type/undo", "/api/settings/shop-type/dismiss-banner"} {
		if rec := postForm(mux, path, url.Values{"to": {"cafe"}}, &mgrUser); rec.Code != http.StatusConflict {
			t.Errorf("POST %s = %d, want 409", path, rec.Code)
		}
	}
	if got := stGet(t, d, common.KeyShopType); got != "retail" {
		t.Fatalf("a satellite wrote shop.type = %q", got)
	}
	body := getAs(mux, "/settings", &mgrUser).Body.String()
	if !strings.Contains(body, "Set on the main till (192.0.2.10:8080)") || strings.Contains(body, `hx-get="/api/settings/shop-type/pick"`) {
		t.Error("satellite card must show the lock text and no Change button")
	}
	if strings.Contains(body, `data-testid="shop-type-undo"`) {
		t.Error("satellite must not show the undo banner")
	}
}

func TestShopTypeConfirm_DoubleTapIsANoOp(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	useRealShopTemplates(t, d)
	stChange(t, mux, "cafe")
	stChange(t, mux, "retail", data.OrderTypePromptModeKey)
	undo := stGet(t, d, shoptemplate.UndoKey)
	stChange(t, mux, "retail", data.OrderTypePromptModeKey) // the second tap
	if got := stGet(t, d, shoptemplate.UndoKey); got != undo {
		t.Fatalf("second confirm replaced the undo record (would make Undo a no-op):\n%s\n%s", undo, got)
	}
}

func TestShopTypeConfirm_AuditedAndTemplateReloaded(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	useRealShopTemplates(t, d)
	stChange(t, mux, "service")
	hidden := func() bool {
		for _, a := range d.MenuAmendmentsSnapshot() {
			if a.Key == "/tables" && a.Hide {
				return true
			}
		}
		return false
	}
	if !hidden() {
		t.Fatal("service must hide Tables right after the confirm (template reloaded)")
	}
	stChange(t, mux, "hospitality")
	if hidden() {
		t.Fatal("hospitality shows Tables")
	}
	if n := auditActions(t, d, "shop_type_changed"); n != 2 {
		t.Fatalf("shop_type_changed audit rows = %d, want 2", n)
	}
}

func TestShopTypeConfirm_LegacyShopAcceptsItsOwnTypeAndGetsALedger(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	useRealShopTemplates(t, d)
	stSet(t, d, common.KeyShopType, "cafe") // a shop that predates templates: type, no ledger
	if got := d.TemplateState(t.Context()).Mode; got != common.TemplateLegacy {
		t.Fatalf("setup: mode = %v, want legacy", got)
	}
	stChange(t, mux, "cafe")
	if got := d.TemplateState(t.Context()).Mode; got != common.TemplateActive {
		t.Fatalf("after accepting: mode = %v, want active", got)
	}
}

func TestShopTypeCard_RendersChangeButtonNotAForm(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	useRealShopTemplates(t, d)
	body := getAs(mux, "/settings", &mgrUser).Body.String()
	card := between(t, body, `id="settings-shop-type"`, `id="settings-all"`)
	for _, want := range []string{`hx-get="/api/settings/shop-type/pick"`, "Change shop type", "Not set. Pick one to tidy your menu."} {
		if !strings.Contains(card, want) {
			t.Errorf("card lacks %q", want)
		}
	}
	if strings.Contains(card, "Salon menu layout") {
		t.Error("the old service layout note must be gone")
	}
}

func TestSetupWizard_ShopTypeStepUsesTheSharedTileGroup(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	body := getSetup(mux, "?lang=en", "en").Body.String()
	for _, want := range []string{
		`role="radiogroup"`, `aria-labelledby="shop-type-title"`, "Cafe or takeaway counter", "Barber, salon or service",
		"Skip for now", "Skipping keeps everything visible.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("wizard step 5 lacks %q", want)
		}
	}
	if n := strings.Count(body, `class="picker-tile shop-type-tile"`); n != 6 {
		t.Errorf("%d tiles, want 6", n)
	}
}

// G10 defects: a legacy shop previewing its own type must not read "X to X",
// and settings cards must not be listed under "Will appear in your menu".
func TestShopTypePreview_LegacySameTypeTitleAndSettingsGroupedApart(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	useRealShopTemplates(t, d)
	stSet(t, d, common.KeyShopType, "cafe") // legacy: type, no ledger
	body := getAs(mux, "/api/settings/shop-type/preview?to=cafe", &mgrUser).Body.String()
	if !strings.Contains(body, "Set up your menu and settings for: Cafe or takeaway counter") || strings.Contains(body, "counter to Cafe") {
		t.Errorf("legacy same-type title wrong:\n%s", body)
	}

	stChange(t, mux, "cafe")
	stChange(t, mux, "retail") // retail tucks Order numbers / the order type prompt away
	body = getAs(mux, "/api/settings/shop-type/preview?to=hospitality", &mgrUser).Body.String()
	menu := between(t, body, "Will appear in your menu", "</ul>")
	if !strings.Contains(body, "Will move out of More settings (easier to find)") {
		t.Errorf("settings cards coming back need their own group:\n%s", body)
	}
	for _, bad := range []string{"Order numbers", "prompt"} {
		if strings.Contains(menu, bad) {
			t.Errorf("a settings card (%q) is listed under the menu group: %s", bad, menu)
		}
	}
}

// ut-docs#7191: a shop with no type yet (from == "") gets the "set up" title,
// never "Change shop type: — to X".
func TestShopTypePreviewData_NoCurrentTypeUsesSetUpTitle(t *testing.T) {
	got := shopTypePreviewData("en", "", "cafe", shoptemplate.Preview{})["Title"]
	want := "Set up your menu and settings for: " + shopTypeLabel("en", "cafe")
	if got != want {
		t.Fatalf("title = %q, want %q", got, want)
	}
	if !strings.Contains(want, "Cafe") {
		t.Fatalf("test premise: label for cafe = %q", want)
	}
}

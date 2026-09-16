package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// The prompt-placement mode reaches every base-layout page as
// body[data-order-type-prompt-mode], same "any base-layout page carries it"
// shape as osk_mode_test.go's TestOSKModeReachesThePage for data-osk: an
// unset/never-configured till reads "top" (the documented default, so an
// existing shop sees no behaviour change until it opts in), and a manager
// changing it via Settings reaches the page immediately.
func TestOrderTypePromptModeReachesThePage(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	cfg := &config.Config{Theme: "default"}
	dp := &common.Deps{Cfg: cfg, Db: db, State: common.LoadState(t.Context(), settings.NewStore(db), cfg),
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db)}
	mux := http.NewServeMux()
	registerHelp(mux, dp)

	get := func() string {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/help", nil))
		return rec.Body.String()
	}

	httpx.InitOrderTypePromptMode("") // boot with nothing persisted yet
	t.Cleanup(func() { httpx.InitOrderTypePromptMode("top") })
	if !strings.Contains(get(), `data-order-type-prompt-mode="top"`) {
		t.Fatal("an unconfigured till must default to top")
	}

	httpx.InitOrderTypePromptMode(data.OrderTypePromptModeAtPay)
	if !strings.Contains(get(), `data-order-type-prompt-mode="at_pay"`) {
		t.Fatal("at_pay did not reach the page")
	}
}

// ut-docs#2282: the dine-in/takeaway prompt-placement setting. Same
// elevation-gated/audited/live-republish shape as the order-no-scheme and
// display-mode settings this endpoint was modeled on
// (settings_upsert_display_mode_test.go's own convention for the live-flag
// assertion).

// A cashier without an approver PIN gets the in-place elevation prompt; a
// manager's write persists and is reflected by the live httpx template flag
// immediately (not just after a restart).
func TestOrderTypePromptSettingsEndpoint_ElevationAndLivePersist(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	httpx.InitOrderTypePromptMode("top")
	t.Cleanup(func() { httpx.InitOrderTypePromptMode("top") })

	rec := postForm(mux, "/api/settings/order-type-prompt", url.Values{"mode": {"before_item"}}, &cashUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "elevation-dialog") ||
		!strings.Contains(rec.Body.String(), `name="override_pin"`) {
		t.Fatalf("cashier order-type-prompt: code=%d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
	}
	// The cashier's blocked attempt must not have applied anything.
	if fn := httpx.FuncsFor("en")["ordertypepromptmode"].(func() string); fn() != "top" {
		t.Fatalf("live prompt mode after a blocked cashier write = %q, want top unchanged", fn())
	}

	rec = postForm(mux, "/api/settings/order-type-prompt", url.Values{"mode": {"before_item"}}, &mgrUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("manager order-type-prompt: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if v, _, _ := d.Settings.Get(t.Context(), data.OrderTypePromptModeKey); v != data.OrderTypePromptModeBeforeItem {
		t.Fatalf("%s = %q, want before_item", data.OrderTypePromptModeKey, v)
	}
	if fn := httpx.FuncsFor("en")["ordertypepromptmode"].(func() string); fn() != "before_item" {
		t.Fatalf("live prompt mode after manager write = %q, want before_item (must not wait for a restart)", fn())
	}

	// An unrecognized value is rejected outright, not silently clamped.
	rec = postForm(mux, "/api/settings/order-type-prompt", url.Values{"mode": {"bogus"}}, &mgrUser)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bogus mode: expected 400, got %d", rec.Code)
	}
}

// The generic key/value upsert door needs the identical live-republish side
// effect (ut-docs#2121's own lesson, applied here) — a shop that edits the
// raw key via Settings → All settings must not see a stale prompt behaviour
// until the till restarts.
func TestSettingsUpsertOrderTypePrompt_UpdatesLiveFlag(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	httpx.InitOrderTypePromptMode("top")
	t.Cleanup(func() { httpx.InitOrderTypePromptMode("top") })

	rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {data.OrderTypePromptModeKey}, "value": {"at_pay"}}, &mgrUser)
	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("upsert %s=at_pay: %d %s", data.OrderTypePromptModeKey, rec.Code, rec.Body.String())
	}
	if v, _, _ := d.Settings.Get(t.Context(), data.OrderTypePromptModeKey); v != "at_pay" {
		t.Fatalf("%s = %q, want at_pay", data.OrderTypePromptModeKey, v)
	}
	if fn := httpx.FuncsFor("en")["ordertypepromptmode"].(func() string); fn() != "at_pay" {
		t.Fatalf("live prompt mode after generic upsert = %q, want at_pay", fn())
	}
}

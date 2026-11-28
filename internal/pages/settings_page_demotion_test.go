package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/shoptemplate"
	"github.com/universaltill/universal-till/internal/shoptemplate/shoptemplatetest"
	"github.com/universaltill/universal-till/internal/uislot"
)

// useSettingsTemplate makes /settings see tm as the template in effect for the
// shop (nil = none / legacy), restoring the seam at test end.
func useSettingsTemplate(t *testing.T, tm *shoptemplate.Template) {
	t.Helper()
	prev := settingsTemplateResolved
	t.Cleanup(func() { settingsTemplateResolved = prev })
	if tm == nil {
		settingsTemplateResolved = func(context.Context, *common.Deps) *shoptemplate.Resolved { return nil }
		return
	}
	tm.ShopTypes = []string{"retail"}
	tm.ID = "retail"
	tm.Schema, tm.Version = 1, 1
	r := shoptemplatetest.TestSet(t, tm).ForType("retail")
	settingsTemplateResolved = func(context.Context, *common.Deps) *shoptemplate.Resolved { return r }
}

func demotionGetSettings(t *testing.T, mux *http.ServeMux, u *auth.User) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	if u != nil {
		req = auth.WithUser(req, *u)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	return rec.Body.String()
}

func between(t *testing.T, body, start, end string) string {
	t.Helper()
	i := strings.Index(body, start)
	if i < 0 {
		t.Fatalf("%q not rendered", start)
	}
	j := strings.Index(body[i:], end)
	if j < 0 {
		t.Fatalf("%q not terminated by %q", start, end)
	}
	return body[i : i+j]
}

func demotionSetShopType(t *testing.T, mux *http.ServeMux, v string) {
	t.Helper()
	rec := postForm(mux, "/api/settings/shop-type", url.Values{"shop_type": {v}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("save shop_type=%q: %d %s", v, rec.Code, rec.Body.String())
	}
}

var tagAttrRE = regexp.MustCompile(`data-key="([^"]+)"[^>]*data-cat="([^"]+)"`)

// Unset type and legacy mode (type set, no template in effect) render the
// nav index and landing grid byte-identically to today: pinned by a golden,
// and the two states must equal each other. Regenerate with UPDATE_GOLDEN=1.
func TestSettingsPage_NoTemplateRendersGolden(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, _ := newFullAuthDeps(t)
	useSettingsTemplate(t, nil)
	unset := demotionGetSettings(t, mux, nil)
	extract := func(body string) string {
		return between(t, body, `<ul id="settings-nav-index"`, "</ul>") + "\n" + between(t, body, `<nav class="settings-home"`, "</nav>") + "\n"
	}
	got := extract(unset)
	for _, bad := range []string{"data-tag", "data-home", "settings-footer", "settings-more-hint", "settings-more-intro"} {
		if strings.Contains(got, bad) {
			t.Errorf("no template: %q must not render", bad)
		}
	}
	golden := filepath.Join("internal", "pages", "testdata", "settings_nav_legacy.golden.html")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Errorf("unset-type nav index/landing changed; if intended run with UPDATE_GOLDEN=1\n--- got ---\n%s", got)
	}
}

// Legacy: a type is set, no template in effect. Nav index identical to unset;
// the only addition is the footer line naming the type (manager).
func TestSettingsPage_LegacyModeNavIndexIdenticalToUnset(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	useSettingsTemplate(t, nil)
	idx := func(b string) string { return between(t, b, `<ul id="settings-nav-index"`, "</ul>") }
	before := idx(demotionGetSettings(t, mux, &mgrUser))
	demotionSetShopType(t, mux, "retail")
	body := demotionGetSettings(t, mux, &mgrUser)
	if got := idx(body); got != before {
		t.Errorf("legacy nav index differs from unset:\n%s\nvs\n%s", got, before)
	}
	if !strings.Contains(body, "Set up for: Retail shop") {
		t.Errorf("legacy mode shows the footer naming the type")
	}
	if strings.Contains(body, "settings-card-tag") && strings.Contains(between(t, body, `<ul id="settings-nav-index"`, "</ul>"), "data-tag") {
		t.Errorf("legacy mode must not tag rows")
	}
}

func TestSettingsPage_FooterOnlyForManagerWithAType(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	useSettingsTemplate(t, nil)
	if strings.Contains(demotionGetSettings(t, mux, &mgrUser), "settings-footer") {
		t.Errorf("type unset: no footer")
	}
	demotionSetShopType(t, mux, "cafe")
	body := demotionGetSettings(t, mux, &mgrUser)
	for _, want := range []string{`data-testid="settings-footer"`, "Set up for: Cafe or takeaway counter", `href="#settings-shop-type"`, ">Change<", "Cannot find a setting? Search, or open More settings."} {
		if !strings.Contains(body, want) {
			t.Errorf("manager footer must contain %q", want)
		}
	}
	cashier := auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}
	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	req = auth.WithUser(req, cashier)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "settings-footer") {
		t.Errorf("cashier must never see the footer")
	}
}

// With a template in effect: the demoted row moves to More settings, tagged;
// the promoted one is tagged Recommended; footer, intro and hint render; every
// core row still appears exactly once.
func TestSettingsPage_TemplateDemotesToMoreSettings(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	useSettingsTemplate(t, &shoptemplate.Template{
		Settings: map[string]shoptemplate.Value{
			"settings-order-type-prompt": {Class: uislot.ClassAdvanced},
			"settings-stock-tracking":    {Class: uislot.ClassEssential},
		},
	})
	demotionSetShopType(t, mux, "retail")
	body := demotionGetSettings(t, mux, &mgrUser)
	idx := between(t, body, `<ul id="settings-nav-index"`, "</ul>")
	for _, want := range []string{
		`data-key="settings-order-type-prompt" data-group="Selling" data-cat="advanced" data-tag="tucked" data-home="selling"`,
		`data-key="settings-stock-tracking" data-group="Selling" data-cat="selling" data-tag="recommended"`,
		`data-tag-tucked="Usually tucked away for this shop type"`,
		`data-tag-recommended="Recommended for your shop"`,
		`data-chip="In More settings"`,
	} {
		if !strings.Contains(idx, want) {
			t.Errorf("nav index must contain %s\n%s", want, idx)
		}
	}
	for _, want := range []string{
		"Something missing? Some settings are tucked into More settings for your kind of shop.",
		"Open More settings",
		`Every setting is here. Ones marked &#34;Usually tucked away for this shop type&#34; are tucked away for your shop type but work exactly the same.`,
		"Set up for: Retail shop",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page must contain %q", want)
		}
	}
	// Each core row (as rendered) exactly once in the index.
	seen := map[string]int{}
	for _, m := range tagAttrRE.FindAllStringSubmatch(idx, -1) {
		seen[m[1]]++
	}
	for k, n := range seen {
		if n != 1 {
			t.Errorf("row %s appears %d times", k, n)
		}
	}
	for _, e := range uislot.CoreSettings {
		if e.Key == "settings-order-type-prompt" || e.Key == "settings-stock-tracking" {
			if seen[e.Key] != 1 {
				t.Errorf("%s missing from index", e.Key)
			}
		}
	}
}

// The same card renders the same fields whether or not it is demoted: the
// demoted row's card markup is untouched by the template.
func TestSettingsPage_DemotedCardMarkupIdentical(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	useSettingsTemplate(t, nil)
	region := func(b string) string {
		i := strings.Index(b, `id="settings-order-type-prompt"`)
		if i < 0 {
			t.Fatal("card missing")
		}
		return b[i : i+600]
	}
	plain := region(demotionGetSettings(t, mux, &mgrUser))
	useSettingsTemplate(t, &shoptemplate.Template{Settings: map[string]shoptemplate.Value{"settings-order-type-prompt": {Class: uislot.ClassAdvanced}}})
	if got := region(demotionGetSettings(t, mux, &mgrUser)); got != plain {
		t.Errorf("demoted card markup differs from normal:\n%s\nvs\n%s", got, plain)
	}
}

// Whatever a template says, fixed-class rows stay in their own category, and
// More settings loses no row, for a template that demotes everything it may.
func TestSettingsPage_TemplateNeverMovesFixedRowsAndLosesNoRow(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	vals := map[string]shoptemplate.Value{}
	for _, e := range uislot.CoreSettings {
		if !uislot.FixedClassFor(uislot.KindSettings, e.Key) {
			vals[e.Key] = shoptemplate.Value{Class: uislot.ClassAdvanced}
		}
	}
	useSettingsTemplate(t, &shoptemplate.Template{Settings: vals})
	body := demotionGetSettings(t, mux, &mgrUser)
	idx := between(t, body, `<ul id="settings-nav-index"`, "</ul>")
	cat := map[string]string{}
	for _, m := range tagAttrRE.FindAllStringSubmatch(idx, -1) {
		cat[m[1]] = m[2]
	}
	for _, e := range uislot.CoreSettings {
		c, ok := cat[e.Key]
		if !ok {
			continue // card gated out of this render
		}
		if uislot.FixedClassFor(uislot.KindSettings, e.Key) && e.Class != uislot.ClassAdvanced && c == uislot.SettingsAdvancedCategory {
			t.Errorf("fixed row %s was moved to More settings", e.Key)
		}
	}
	if !strings.Contains(body, `href="#cat-advanced"`) {
		t.Errorf("More settings tile must remain")
	}
}

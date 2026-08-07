package httpx

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// ut-docs#390: a replica's "Edit on the primary" banner used to
// unconditionally link the kiosk's own browser to another till's origin —
// a permanent dead end on the fullscreen, chrome-less kiosk shell (field
// report: operator stranded on till 1 with no way back to till 2). Fixed
// by gating the <a> behind crossdevicelinkactionable, same pattern as
// TestBaseLayoutUpdateChip{Says..,HasNoLink..} above test for the sibling
// ut-docs#159 fix — this pair mirrors that one intentionally, testing BOTH
// branches: a prior version of this fix only had regression coverage for
// the "no link" branch (independent review finding), which would have let
// a future refactor silently drop the working link for platforms that
// have real, recoverable browser chrome (Windows/macOS) with nothing
// catching it.
func TestInventoryBannerLinksToPrimaryWhenActionable(t *testing.T) {
	InitI18n(realI18n(t), "en")
	funcs := FuncsFor("en")
	funcs["crossdevicelinkactionable"] = func() bool { return true }
	r, err := NewRenderer(
		filepath.Join("web", "ui", "layouts", "base.html"),
		filepath.Join("web", "ui", "pages", "inventory.html"),
		funcs,
	)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	w := httptest.NewRecorder()
	data := map[string]any{
		"title": "Inventory", "theme": "", "menuItems": nil, "errKey": "",
		"SyncPrimary": "http://primary.till.local:8080",
	}
	if err := r.Render(w, "base", data); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, `href="http://primary.till.local:8080/inventory"`) {
		t.Fatalf("expected an actionable link to the primary till when crossdevicelinkactionable is true, got %.800s", body)
	}
	if !strings.Contains(body, `target="_blank"`) {
		t.Fatalf("expected the kept cross-device link to open in a new context (target=_blank), got %.800s", body)
	}
	if strings.Contains(body, "sync.banner_open_primary_unavailable") {
		t.Fatalf("did not expect the inert fallback text when the link is actionable, got %.800s", body)
	}
}

func TestInventoryBannerHasNoLinkWhenNotActionable(t *testing.T) {
	InitI18n(realI18n(t), "en")
	funcs := FuncsFor("en")
	funcs["crossdevicelinkactionable"] = func() bool { return false }
	r, err := NewRenderer(
		filepath.Join("web", "ui", "layouts", "base.html"),
		filepath.Join("web", "ui", "pages", "inventory.html"),
		funcs,
	)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	w := httptest.NewRecorder()
	data := map[string]any{
		"title": "Inventory", "theme": "", "menuItems": nil, "errKey": "",
		"SyncPrimary": "http://primary.till.local:8080",
	}
	if err := r.Render(w, "base", data); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := w.Body.String()
	if strings.Contains(body, `href="http://primary.till.local:8080/inventory"`) {
		t.Fatalf("ut-docs#390: expected NO link to the primary till when crossdevicelinkactionable is false — this strands the kiosk operator, got %.800s", body)
	}
	if !strings.Contains(body, "Edit this on the primary till") {
		t.Fatalf("expected the inert fallback text, got %.800s", body)
	}
}

func TestCatalogBannerLinksToPrimaryWhenActionable(t *testing.T) {
	InitI18n(realI18n(t), "en")
	funcs := FuncsFor("en")
	funcs["crossdevicelinkactionable"] = func() bool { return true }
	r, err := NewRenderer(
		filepath.Join("web", "ui", "layouts", "base.html"),
		filepath.Join("web", "ui", "pages", "catalog.html"),
		funcs,
		filepath.Join("web", "ui", "partials", "catalog_lookups.html"),
		filepath.Join("web", "ui", "partials", "catalog_table.html"),
		filepath.Join("web", "ui", "partials", "catalog_variants.html"),
	)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	w := httptest.NewRecorder()
	data := map[string]any{
		"title": "Catalog", "theme": "", "menuItems": nil, "errKey": "",
		"SyncPrimary": "http://primary.till.local:8080",
	}
	if err := r.Render(w, "base", data); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, `href="http://primary.till.local:8080/catalog"`) {
		t.Fatalf("expected an actionable link to the primary till when crossdevicelinkactionable is true, got %.800s", body)
	}
	if strings.Contains(body, "sync.banner_open_primary_unavailable") {
		t.Fatalf("did not expect the inert fallback text when the link is actionable, got %.800s", body)
	}
}

func TestCatalogBannerHasNoLinkWhenNotActionable(t *testing.T) {
	InitI18n(realI18n(t), "en")
	funcs := FuncsFor("en")
	funcs["crossdevicelinkactionable"] = func() bool { return false }
	r, err := NewRenderer(
		filepath.Join("web", "ui", "layouts", "base.html"),
		filepath.Join("web", "ui", "pages", "catalog.html"),
		funcs,
		filepath.Join("web", "ui", "partials", "catalog_lookups.html"),
		filepath.Join("web", "ui", "partials", "catalog_table.html"),
		filepath.Join("web", "ui", "partials", "catalog_variants.html"),
	)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	w := httptest.NewRecorder()
	data := map[string]any{
		"title": "Catalog", "theme": "", "menuItems": nil, "errKey": "",
		"SyncPrimary": "http://primary.till.local:8080",
	}
	if err := r.Render(w, "base", data); err != nil {
		t.Fatalf("Render: %v", err)
	}
	body := w.Body.String()
	if strings.Contains(body, `href="http://primary.till.local:8080/catalog"`) {
		t.Fatalf("ut-docs#390: expected NO link to the primary till when crossdevicelinkactionable is false — this strands the kiosk operator, got %.800s", body)
	}
	if !strings.Contains(body, "Edit this on the primary till") {
		t.Fatalf("expected the inert fallback text, got %.800s", body)
	}
}

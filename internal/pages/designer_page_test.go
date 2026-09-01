package pages

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/ui"
)

// registerDesigner renders the shortcut-button designer, backed by the
// shortcut_buttons table (not in seedForPages), so use a migrated database.
func newDesignerTestDeps(t *testing.T) *common.Deps {
	t.Helper()
	chdirRoot(t)
	d, err := appdb.Open(filepath.Join(t.TempDir(), "designer.db"))
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	cfg := &config.Config{Theme: "monarch", Locales: config.Locales{Currency: "GBP", Locale: "en", TaxRate: 20}}
	pm, err := plugins.Init(t.Context(), cfg, d.DB)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(d.DB), cfg)
	return &common.Deps{
		Cfg:      cfg,
		Db:       d.DB,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		BtnStore: ui.NewButtonStore(d.DB),
		Pm:       pm,
		Settings: settings.NewStore(d.DB),
	}
}

func TestDesigner_RendersPage(t *testing.T) {
	dp := newDesignerTestDeps(t)
	mux := http.NewServeMux()
	registerDesigner(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/designer", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /designer: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// The designer page composes the buttons_admin partial.
	if !strings.Contains(body, "designer") {
		t.Fatalf("expected the designer container, got %s", body)
	}
}

func TestDesigner_RendersSeededButtons(t *testing.T) {
	dp := newDesignerTestDeps(t)

	// The migrated schema seeds sample shortcut buttons; add a deterministic one
	// against a known item so the grid renders its label through ToVM.
	if _, err := dp.Db.Exec(`INSERT INTO items(id,sku,name,base_price,is_active) VALUES('itmZ','ZZZ','Zephyr Widget',500,1)`); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	if err := dp.BtnStore.Add(ui.Button{Label: "Zephyr Tile", Code: "ZZZ", ItemID: "itmZ"}); err != nil {
		t.Fatalf("add button: %v", err)
	}

	mux := http.NewServeMux()
	registerDesigner(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/designer", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /designer: code %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Zephyr Tile") {
		t.Fatalf("expected the seeded button label rendered in the grid, got %s", rec.Body.String())
	}
}

// The on-screen keyboard (web/public/osk.js) types by calling setRangeText
// then dispatchEvent(new Event('input')) — a tapped virtual key never fires
// a native keydown/keyup, only a synthetic "input". A search box trigger
// scoped to "keyup" alone (as this one was before ut-docs#196) never fires
// for OSK-driven typing, so product search silently returns nothing on
// touch tills while working fine on a desktop keyboard. Guard: the trigger
// must include "input", the event OSK actually dispatches.
func TestDesigner_SearchBoxTriggerFiresOnSyntheticInputEvent(t *testing.T) {
	dp := newDesignerTestDeps(t)
	mux := http.NewServeMux()
	registerDesigner(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/designer", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /designer: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	idx := strings.Index(body, `id="search"`)
	if idx == -1 {
		t.Fatalf("search box missing from designer page: %s", body)
	}
	tagStart := strings.LastIndex(body[:idx], "<input")
	if tagStart == -1 {
		t.Fatalf("no <input tag containing id=\"search\": %s", body)
	}
	tagEnd := strings.Index(body[idx:], ">")
	tag := body[tagStart : idx+tagEnd]
	if !strings.Contains(tag, `hx-trigger="input`) {
		t.Fatalf("search box hx-trigger must fire on the \"input\" event (OSK-compatible), got: %s", tag)
	}
}

// TestDesigner_HelpHintResolvesToOwnTopic (ut-docs#1388) guards against the
// contextual "?" on /designer resolving to an unrelated topic. It used to
// land on "Catalog, variants & barcodes" because catalog.md's front matter
// claimed the /designer route — a stale claim from before Till Designer had
// its own manual page. Anchored on data-testid="help-hint" the same way
// TestHelpHintResolvesPerPage is, independent of markup/attribute order.
func TestDesigner_HelpHintResolvesToOwnTopic(t *testing.T) {
	dp := newDesignerTestDeps(t)
	mux := http.NewServeMux()
	registerDesigner(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/designer", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /designer: code %d body %s", rec.Code, rec.Body.String())
	}
	tag := helpHintTag.FindString(rec.Body.String())
	if tag == "" {
		t.Fatal("no help hint rendered on /designer")
	}
	m := helpHintHrefAttr.FindStringSubmatch(tag)
	if m == nil {
		t.Fatalf("help hint tag has no href: %s", tag)
	}
	if m[1] != "/help/till-designer" {
		t.Errorf("hint on /designer → %s, want /help/till-designer", m[1])
	}
}

// TestDesigner_RendersAddErrorSurface (ut-docs#1220) guards the Designer's
// only channel for telling an operator that "add as button" failed. htmx
// swaps nothing into hx-target for a non-2xx, so without the dedicated
// #buttons-add-error element plus a page-level htmx:responseError listener,
// a rejected add is invisible — the exact silent failure the card reports.
// htmx:sendError covers the transport half (tablet off the LAN), which
// never reaches responseError and carries no response body, so it needs the
// locale copy rendered into the page rather than read off the xhr.
func TestDesigner_RendersAddErrorSurface(t *testing.T) {
	dp := newDesignerTestDeps(t)
	mux := http.NewServeMux()
	registerDesigner(mux, dp)

	req := httptest.NewRequest(http.MethodGet, "/designer", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /designer: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	if !strings.Contains(body, `id="buttons-add-error"`) {
		t.Fatalf("designer page has no #buttons-add-error element for a failed add to render into")
	}
	for _, ev := range []string{"htmx:responseError", "htmx:sendError"} {
		if !strings.Contains(body, ev) {
			t.Fatalf("designer page registers no %s listener — a failed add on that path stays invisible", ev)
		}
	}
	// The sendError arm has no response body to show, so its copy must be
	// the rendered locale string, not a hardcoded literal or an empty one.
	if want := httpx.T("en", buttonsErrorKey); !strings.Contains(body, want) {
		t.Fatalf("designer page does not render the localized %s copy %q for the transport-failure message", buttonsErrorKey, want)
	}
}

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
	"github.com/universaltill/universal-till/internal/settings"
)

// helpTestMux boots the help routes (plus, optionally, the index catch-all,
// to prove the pattern-based routes win over "/") on the shared page-test
// scaffolding.
func helpTestMux(t *testing.T, withIndex bool) *http.ServeMux {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	cfg := &config.Config{Theme: "default"}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Settings: settings.NewStore(db),
	}

	mux := http.NewServeMux()
	if withIndex {
		registerIndex(mux, dp)
	}
	registerHelp(mux, dp)
	return mux
}

func helpGet(t *testing.T, mux *http.ServeMux, path string, htmx bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// /help is the two-pane manual shell: a topic tree (all sections and the 16
// migrated feature topics plus the quick-start), a search box, and a topic
// panel showing a default topic.
func TestHelpPageRendersTwoPaneShell(t *testing.T) {
	mux := helpTestMux(t, false)
	rec := helpGet(t, mux, "/help", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /help: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	for _, want := range []string{`id="help-tree"`, `id="help-topic"`, `id="help-search"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("help shell missing %s", want)
		}
	}
	// All 16 migrated feature topics plus quick-start appear in the tree.
	wantTopics := []string{
		"quick-start", "sell", "catalog", "inventory", "alerts", "reports",
		"invoices", "printing", "designer", "payments", "multitill", "plugins",
		"claim", "users", "backups", "updates", "display",
	}
	for _, id := range wantTopics {
		if !strings.Contains(body, `href="/help/`+id+`"`) {
			t.Fatalf("topic tree missing link to /help/%s", id)
		}
	}
	// A few real migrated titles prove the content came across.
	for _, want := range []string{"Selling &amp; checkout", "Plugin store", "Backups"} {
		if !strings.Contains(body, want) {
			t.Fatalf("help page missing migrated title %q", want)
		}
	}
	// Search box is wired to htmx, tree links push URLs.
	if !strings.Contains(body, `hx-get="/help/search"`) {
		t.Fatalf("search box not wired to /help/search")
	}
	if !strings.Contains(body, `hx-push-url="true"`) {
		t.Fatalf("topic links do not push URLs")
	}
	// Every visible chrome string is translated (raw keys would leak).
	if i := strings.Index(body, "help.search."); i >= 0 {
		t.Fatalf("unresolved locale key on help page near: %s", body[i:i+40])
	}
}

// /help/<id> is directly linkable: a plain GET renders the full page (layout
// + tree + the topic), not a bare fragment.
func TestHelpTopicDirectLinkRendersFullPage(t *testing.T) {
	mux := helpTestMux(t, false)
	rec := helpGet(t, mux, "/help/sell", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /help/sell: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<!DOCTYPE html>") || !strings.Contains(body, `id="help-tree"`) {
		t.Fatalf("direct topic link did not render the full two-pane page")
	}
	if !strings.Contains(body, "Selling &amp; checkout") {
		t.Fatalf("topic content missing from direct render")
	}
	// Offline-first: no external scripts/styles snuck in.
	for _, bad := range []string{`src="http`, `href="http://`, `href="https://cdn`} {
		if strings.Contains(body, bad) {
			t.Fatalf("external asset reference found: %s", bad)
		}
	}
}

// An htmx request for a topic gets just the right-panel fragment.
func TestHelpTopicHtmxRequestRendersFragment(t *testing.T) {
	mux := helpTestMux(t, false)
	rec := helpGet(t, mux, "/help/backups", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /help/backups (htmx): code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "<!DOCTYPE html>") {
		t.Fatalf("htmx topic request returned a full page, want a fragment")
	}
	if !strings.Contains(body, `id="help-topic"`) {
		t.Fatalf("fragment missing the stable help-topic wrapper")
	}
	if !strings.Contains(body, "Backups") {
		t.Fatalf("fragment missing topic content")
	}
}

func TestHelpTopicUnknownIs404(t *testing.T) {
	mux := helpTestMux(t, false)
	rec := helpGet(t, mux, "/help/definitely-not-a-topic", false)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /help/definitely-not-a-topic: code %d, want 404", rec.Code)
	}
}

// /help/search returns a filtered topic-list fragment.
func TestHelpSearchFiltersTopicList(t *testing.T) {
	mux := helpTestMux(t, false)
	rec := helpGet(t, mux, "/help/search?q=barcode", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /help/search: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "<!DOCTYPE html>") {
		t.Fatalf("search returned a full page, want a fragment")
	}
	if !strings.Contains(body, `href="/help/sell"`) {
		t.Fatalf("search for 'barcode' did not surface the selling topic")
	}
	if strings.Contains(body, `href="/help/updates"`) {
		t.Fatalf("search for 'barcode' surfaced an unrelated topic (updates)")
	}

	// No matches renders a translated empty-state, not raw keys or nothing.
	rec = helpGet(t, mux, "/help/search?q=zzzzqqqq", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /help/search (no match): code %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "help.search.no_results") {
		t.Fatalf("no-results message is an unresolved locale key")
	}
	if strings.Contains(rec.Body.String(), `class="help-topic-link"`) {
		t.Fatalf("no-match search still lists topics")
	}
}

// A locale without its own topic files gets the English topic plus a
// translated "not yet translated" banner.
func TestHelpUntranslatedLocaleFallsBackWithBanner(t *testing.T) {
	mux := helpTestMux(t, false)
	rec := helpGet(t, mux, "/help/sell?lang=fa", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /help/sell?lang=fa: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	banner := httpx.T("fa", "help.topic.untranslated")
	if banner == "help.topic.untranslated" {
		t.Fatalf("fa locale is missing the help.topic.untranslated key")
	}
	if !strings.Contains(body, banner) {
		t.Fatalf("fa fallback render missing the untranslated banner %q", banner)
	}
	// The English body still serves.
	if !strings.Contains(body, "Selling &amp; checkout") {
		t.Fatalf("fa fallback lost the English topic content")
	}
	// An en render carries no banner.
	rec = helpGet(t, mux, "/help/sell?lang=en", false)
	if strings.Contains(rec.Body.String(), httpx.T("en", "help.topic.untranslated")) {
		t.Fatalf("en render shows the untranslated banner")
	}
}

// The specific GET /help and /help/{topic} patterns must win over the "/"
// catch-all in registerIndex — the manual, not the index 404 branch, serves.
func TestHelpRoutesWinOverIndexCatchAll(t *testing.T) {
	mux := helpTestMux(t, true)
	rec := helpGet(t, mux, "/help", false)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="help-tree"`) {
		t.Fatalf("GET /help with index registered: code %d, manual shell missing", rec.Code)
	}
	rec = helpGet(t, mux, "/help/sell", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /help/sell with index registered: code %d", rec.Code)
	}
}

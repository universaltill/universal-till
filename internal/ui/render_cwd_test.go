package ui

import (
	"bytes"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pos"
)

// chdirTemp switches the process CWD to a fresh empty directory for the
// duration of the test — simulates a packaged install launched from a
// working directory with no web/ subtree alongside it.
//
// Also force-clears httpx's process-lifetime template cache (ut-docs#1320
// review, finding 1): these tests exist to catch a constructor reading from
// a cwd-relative disk path instead of the embedded FS, but every one of
// them shares this package's test binary. Without the reset, only the
// first test to touch a given cache key actually parses anything — every
// later test gets a harmless cache hit from a PREVIOUS test's (correct,
// cwd-independent) parse and would pass even if this specific test's
// constructor regressed to a disk read, silently disarming the guard.
func chdirTemp(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	httpx.ResetCacheForTests()
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
		httpx.ResetCacheForTests()
	})
}

// TestNewBasketView_WorksFromAnyWorkingDirectory guards the actual checkout
// path: an independent review of the locale/template embedding fix found
// this constructor still did a disk ParseFiles read and would panic the
// instant a cashier added an item to the basket, on any install launched
// from outside the repo root. NewBasketView is on that path
// (internal/pages/pos_api.go calls it for every basket add/remove/void).
func TestNewBasketView_WorksFromAnyWorkingDirectory(t *testing.T) {
	chdirTemp(t)

	v, err := NewBasketView(httpx.FuncsFor("en"))
	if err != nil {
		t.Fatalf("NewBasketView: %v", err)
	}
	var buf bytes.Buffer
	if err := v.Render(&buf, &pos.Basket{}); err != nil {
		t.Fatalf("Render: %v", err)
	}
}

// TestNewJournalView_WorksFromAnyWorkingDirectory covers the sale-journal
// render (internal/pages/pos_api.go, right after a sale completes).
func TestNewJournalView_WorksFromAnyWorkingDirectory(t *testing.T) {
	chdirTemp(t)

	v, err := NewJournalView(httpx.FuncsFor("en"))
	if err != nil {
		t.Fatalf("NewJournalView: %v", err)
	}
	var buf bytes.Buffer
	if err := v.Render(&buf, JournalViewData{}); err != nil {
		t.Fatalf("Render: %v", err)
	}
}

// TestButtonsNewRenderer_WorksFromAnyWorkingDirectory covers the home
// screen's shortcut-button grid (web/ui/pages/index.html loads /ui/buttons
// via hx-get on every page load).
func TestButtonsNewRenderer_WorksFromAnyWorkingDirectory(t *testing.T) {
	chdirTemp(t)

	r, err := NewRenderer(
		"web/ui/layouts/base.html",
		"web/ui/pages/index.html",
		"web/ui/partials/buttons.html",
		httpx.FuncsFor("en"),
	)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	w := httptest.NewRecorder()
	if err := r.Render(w, "base", map[string]any{}); err != nil {
		t.Fatalf("Render: %v", err)
	}
}

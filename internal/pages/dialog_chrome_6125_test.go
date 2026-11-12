package pages

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// ut-docs#6125: Import, Tax codes and the stock dialog keep Lock + exit-to-OS
// reachable. Import/Tax codes render them in their swapped-in head only inside
// the /items shell dialog; the stock dialog in its title line.

func assertInlineLockExit(t *testing.T, what, region string) {
	t.Helper()
	if !strings.Contains(region, "dialog-status-inline") {
		t.Errorf("%s: missing the dialog-status-inline controls", what)
		return
	}
	lock := strings.Index(region, `action="/api/auth/logout"`)
	exit := strings.Index(region, `href="/settings#settings-display"`)
	if lock < 0 || exit < 0 || lock > exit {
		t.Errorf("%s: want Lock form (%d) before exit link (%d)", what, lock, exit)
	}
	if strings.Contains(region, "data-record-dialog-conn") {
		t.Errorf("%s: must not render its own connection chip (UX-1)", what)
	}
}

func TestImport_ShellHeadCarriesLockAndExit(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux := http.NewServeMux()
	registerImport(mux, newImportTestDeps(t))

	req := httptest.NewRequest(http.MethodGet, "/import", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	h1 := strings.Index(body, "<h1")
	if h1 < 0 {
		t.Fatalf("no h1 in fragment: %s", body)
	}
	assertInlineLockExit(t, "import fragment head", body[:h1])
	if !strings.Contains(body, "data-ut-focus-home") {
		t.Errorf("import fragment has no data-ut-focus-home (UX-2)")
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/import", nil))
	if strings.Contains(rec.Body.String(), "dialog-status-inline") {
		t.Errorf("standalone /import must keep its normal top bar, not render dialog Lock/exit")
	}
}

func TestTaxCodes_ShellHeadCarriesLockAndExit(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newTaxCodesTestDeps(t)

	req := httptest.NewRequest(http.MethodGet, "/catalog/tax-codes", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	h1 := strings.Index(body, "<h1")
	if h1 < 0 {
		t.Fatalf("no h1 in fragment: %s", body)
	}
	assertInlineLockExit(t, "tax codes fragment head", body[:h1])
	if !strings.Contains(body, "data-ut-focus-home") {
		t.Errorf("tax codes fragment has no data-ut-focus-home (UX-2)")
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/catalog/tax-codes", nil))
	if strings.Contains(rec.Body.String(), "dialog-status-inline") {
		t.Errorf("standalone tax codes page must not render dialog Lock/exit")
	}
}

func TestInventoryStockDialog_BesideChromeWithLockAndExit(t *testing.T) {
	mux, dp := newInventoryAPITestDeps(t)
	registerInventoryPage(mux, dp)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/inventory", nil))
	body := rec.Body.String()
	tag := regexp.MustCompile(`<dialog[^>]*id="stock-dialog"[^>]*>`).FindString(body)
	if !strings.Contains(tag, "dialog-beside-chrome") || !strings.Contains(tag, "record-dialog") {
		t.Fatalf("#stock-dialog tag = %q, want record-dialog + dialog-beside-chrome", tag)
	}
	start := strings.Index(body, `id="stock-dialog-title"`)
	titleLine := body[strings.Index(body, `id="stock-dialog"`):start]
	assertInlineLockExit(t, "stock dialog title line", titleLine)
	if !strings.Contains(body[start:], "data-ut-focus-home") {
		t.Errorf("stock dialog title is not the focus home (UX-2)")
	}
}

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

// ut-docs#155: the sale screen's autofocused scan input used to pop the
// on-screen keyboard (and Android's IME) the moment the page loaded. The
// keyboard is on-demand now: the scan input carries a static
// inputmode="none" so no IME opens at load, and the scan row ships a
// data-osk-toggle button (hidden until osk.js enables it) for manual entry.
func TestIndexScanRowKeyboardIsOnDemand(t *testing.T) {
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
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{Cfg: cfg, Db: db, State: state,
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db)}
	mux := http.NewServeMux()
	registerIndex(mux, dp)

	get := func() string {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /: %d", rec.Code)
		}
		return rec.Body.String()
	}
	scanTag := func(body string) string {
		scanStart := strings.Index(body, `name="code"`)
		if scanStart < 0 {
			t.Fatal("sale screen has no scan input")
		}
		tagStart := strings.LastIndex(body[:scanStart], "<input")
		tagEnd := scanStart + strings.Index(body[scanStart:], ">")
		return body[tagStart : tagEnd+1]
	}

	httpx.InitOSKMode("auto")
	defer httpx.InitOSKMode("auto")

	body := get()
	tag := scanTag(body)
	if !strings.Contains(tag, `inputmode="none"`) {
		t.Errorf("scan input must carry inputmode=\"none\" so no IME auto-opens at load; got: %s", tag)
	}
	if !strings.Contains(tag, "autofocus") {
		t.Errorf("scan input must keep autofocus (wedge scanners rely on it); got: %s", tag)
	}
	if !strings.Contains(body, "data-osk-toggle") {
		t.Error("sale screen must ship a data-osk-toggle button for on-demand keyboard")
	}

	// OSK mode off: osk.js bails out entirely, so a static inputmode="none"
	// would leave a touch till with NO way to type a barcode (no IME, no
	// OSK, no toggle) — the template must drop it.
	httpx.InitOSKMode("off")
	if tag := scanTag(get()); strings.Contains(tag, `inputmode="none"`) {
		t.Errorf("with OSK mode off the scan input must NOT force inputmode=\"none\"; got: %s", tag)
	}
}

// The custom on-screen keyboard is appended to <body>. A native showModal()
// dialog puts itself in the top layer and makes that keyboard inert, so the
// payout dialog must use the keyboard-compatible non-modal show() pattern.
//
// ut-docs#1334: the deposit-refund entry point moved from the sale screen
// (GET /) to the Menu page (GET /menu) — asserted there now, not on /.
func TestPfandDialogStaysKeyboardReachableAndUsesEnglishLabel(t *testing.T) {
	mux, _ := newMenuPageTestDeps(t, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/menu", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /menu: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `getElementById('pfand-modal').show()`) {
		t.Fatal("Pfandrückgabe opener must use show() so the custom keyboard remains reachable")
	}
	if strings.Contains(body, `getElementById('pfand-modal').showModal()`) {
		t.Fatal("Pfandrückgabe opener must not use showModal(), which makes the custom keyboard inert")
	}
	if !strings.Contains(body, "Deposit refund") {
		t.Fatal("English Pfandrückgabe label must be translated as Deposit refund")
	}
}

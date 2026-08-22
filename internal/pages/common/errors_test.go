package common

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
)

// realI18n loads the actual web/locales checkout — the same production
// translations, not a stub. Unlike internal/pages/catalog (whose
// TestMain os.Chdir's to the module root, so its tests can use a bare
// "web/locales") or internal/httpx (two directories deep, so
// filepath.Join("..", "..", ...) reaches it), this package has no
// chdir'ing TestMain and sits three directories under the module root
// (internal/pages/common), hence the extra "..".
func realI18n(t *testing.T) *config.I18n {
	t.Helper()
	i18n, err := config.NewI18n(filepath.Join("..", "..", "..", "web", "locales"), "en")
	if err != nil {
		t.Fatalf("NewI18n: %v", err)
	}
	return i18n
}

func TestLocalizedErrorTranslatesAndSetsStatus(t *testing.T) {
	httpx.InitI18n(nil, "en") // no translator wired: T falls back to the key itself

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want %d", w.Code, http.StatusForbidden)
	}
	if got := strings.TrimSpace(w.Body.String()); got != "common.error.manager_or_admin_required" {
		t.Fatalf("body = %q; want the locale key (no translator wired)", got)
	}
}

// TestLocalizedErrorActuallyTranslates wires the REAL translator (the
// no-translator case above only proves LocalizedError writes *a* body, not
// that it ever calls T — a stub that skipped httpx.T entirely would still
// pass that test). This is the same web/locales checkout production loads,
// so it also catches the locale key itself going missing/renamed.
func TestLocalizedErrorActuallyTranslates(t *testing.T) {
	httpx.InitI18n(realI18n(t), "en")
	t.Cleanup(func() { httpx.InitI18n(nil, "en") })

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")

	got := strings.TrimSpace(w.Body.String())
	if got == "common.error.manager_or_admin_required" {
		t.Fatalf("body = %q; T() was not actually called (fell back to the raw key)", got)
	}
	if got != "Manager or admin required" {
		t.Fatalf("body = %q; want the en.json translation", got)
	}
}

// TestLocalizedErrorResolvesLocaleFromRequest proves LocalizedError goes
// through ResolveLocale (query/cookie/default), not a hardcoded "en" — a
// stub that always translated for "en" would pass every test above but
// silently ignore every non-English operator.
func TestLocalizedErrorResolvesLocaleFromRequest(t *testing.T) {
	httpx.InitI18n(realI18n(t), "en")
	t.Cleanup(func() { httpx.InitI18n(nil, "en") })

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/?lang=tr", nil)

	LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")

	got := strings.TrimSpace(w.Body.String())
	if got != "Yönetici veya admin gerekli" {
		t.Fatalf("body = %q; want the tr.json translation for ?lang=tr", got)
	}
}

func TestLogAndLocalizedErrorNeverLeaksRawErrorToBody(t *testing.T) {
	httpx.InitI18n(nil, "en")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	// The real shape this codebase's repository layer produces (e.g.
	// internal/data/catalog_repo.go's `fmt.Errorf("insert item: %w", err)`
	// wrapping a SQLite driver error) — this is a SQLite POS, not Postgres.
	raw := errors.New("insert item: UNIQUE constraint failed: items.sku (id=internal-uuid-1234)")

	LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", raw)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d; want %d", w.Code, http.StatusInternalServerError)
	}
	body := w.Body.String()
	if strings.Contains(body, "internal-uuid-1234") || strings.Contains(body, "UNIQUE constraint") {
		t.Fatalf("body leaked raw error detail: %q", body)
	}
	if got := strings.TrimSpace(body); got != "catalog.error.server" {
		t.Fatalf("body = %q; want the locale key (no translator wired)", got)
	}
}

// TestLogAndLocalizedErrorLogsTheRealError proves the raw error still
// reaches the server log — the whole point of this helper over plain
// LocalizedError is that the detail isn't lost, just kept off the
// operator's screen. Deleting the log.Printf call would still pass every
// other test in this file, so this closes that gap explicitly.
func TestLogAndLocalizedErrorLogsTheRealError(t *testing.T) {
	httpx.InitI18n(nil, "en")

	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	raw := errors.New("insert item: UNIQUE constraint failed: items.sku (id=internal-uuid-1234)")

	LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", raw)

	logged := buf.String()
	if !strings.Contains(logged, "internal-uuid-1234") || !strings.Contains(logged, "UNIQUE constraint") {
		t.Fatalf("log output = %q; want it to contain the real error detail", logged)
	}
	if !strings.Contains(logged, "[catalog]") {
		t.Fatalf("log output = %q; want the logTag prefix", logged)
	}
}

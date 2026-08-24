package common

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
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
// operator's screen. Deleting the logging.L().Errorf call would still pass
// every other test in this file, so this closes that gap explicitly.
//
// Asserts via logging.Recent() (the app's own leveled logger, ut-docs#947)
// rather than capturing stdlib log's writer — LogAndLocalizedError no
// longer goes through stdlib log at all, and Recent() is the sanctioned
// way to observe what the leveled logger recorded (see its own doc
// comment). This also proves the ADR-0018 Problems-feed side effect: a
// LogAndLocalizedError call is now visible to logging.Recent() the same
// way any other app Error-level log line is.
func TestLogAndLocalizedErrorLogsTheRealError(t *testing.T) {
	httpx.InitI18n(nil, "en")
	logging.ResetRecent()
	t.Cleanup(logging.ResetRecent)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	raw := errors.New("insert item: UNIQUE constraint failed: items.sku (id=internal-uuid-1234)")

	LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", raw)

	recent := logging.Recent()
	if len(recent) == 0 {
		t.Fatalf("logging.Recent() is empty; want the real error recorded")
	}
	got := recent[0] // newest-first
	if got.Level != "ERROR" {
		t.Fatalf("logged level = %q; want ERROR", got.Level)
	}
	if !strings.Contains(got.Msg, "internal-uuid-1234") || !strings.Contains(got.Msg, "UNIQUE constraint") {
		t.Fatalf("logged message = %q; want it to contain the real error detail", got.Msg)
	}
	if !strings.Contains(got.Msg, "[catalog]") {
		t.Fatalf("logged message = %q; want the logTag prefix", got.Msg)
	}
}

// TestLogAndLocalizedError4xxDoesNotPolluteRecent proves a 4xx-status call
// (a routinely operator-triggerable mistake — a malformed form, a declined
// tender) no longer competes with real 5xx problems for the Problems
// ring's limited slots (ut-docs#954, follow-up to #947). Before this fix,
// every LogAndLocalizedError call landed at Error level regardless of
// status, so a cashier fat-fingering a form repeatedly could evict a
// genuine server problem from both logging.Recent() (cap 50) and the
// cloud-sync heartbeat digest (cap 20, ADR-0018).
func TestLogAndLocalizedError4xxDoesNotPolluteRecent(t *testing.T) {
	httpx.InitI18n(nil, "en")
	logging.ResetRecent()
	t.Cleanup(logging.ResetRecent)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	raw := errors.New("malformed form: missing field sku")

	LogAndLocalizedError(w, r, http.StatusBadRequest, "catalog.error.invalid_request", "catalog", raw)

	// Existing behavior (translated key to the operator, real error
	// server-side) must stay unchanged.
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d", w.Code, http.StatusBadRequest)
	}
	if got := strings.TrimSpace(w.Body.String()); got != "catalog.error.invalid_request" {
		t.Fatalf("body = %q; want the locale key (no translator wired)", got)
	}

	if n := len(logging.Recent()); n != 0 {
		t.Fatalf("logging.Recent() = %d entries; want 0 — a 4xx call must not reach the Problems ring", n)
	}
}

// TestLogAndLocalizedError5xxStillReachesRecentAmid4xxNoise proves the
// derived-level fix doesn't quietly suppress real problems too: a mix of
// 4xx and 5xx calls still surfaces the 5xx one at Error in Recent(), with
// no regression from #947's shipped behavior.
func TestLogAndLocalizedError5xxStillReachesRecentAmid4xxNoise(t *testing.T) {
	httpx.InitI18n(nil, "en")
	logging.ResetRecent()
	t.Cleanup(logging.ResetRecent)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	LogAndLocalizedError(w, r, http.StatusBadRequest, "catalog.error.invalid_request", "catalog", errors.New("bad form"))
	LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", errors.New("db write failed"))
	LogAndLocalizedError(w, r, http.StatusPaymentRequired, "refund.error.provider_declined", "refund", errors.New("card declined"))

	recent := logging.Recent()
	if len(recent) != 1 {
		t.Fatalf("logging.Recent() = %d entries; want exactly 1 (only the 5xx call)", len(recent))
	}
	if recent[0].Level != "ERROR" || !strings.Contains(recent[0].Msg, "db write failed") {
		t.Fatalf("recent[0] = %+v; want the 5xx error at ERROR level", recent[0])
	}
}

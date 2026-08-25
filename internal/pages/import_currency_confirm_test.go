package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// TestImport_UnconfirmedCurrency covers ut-docs#970: a fresh till defaults to
// GBP, and importing priced items into it (e.g. a German catalogue) must not
// silently relabel those prices as GBP just because nobody has ever
// confirmed the till's actual currency.
const germanCSV = "Name,SKU,Barcode,Price,Category,In stock\n" +
	"Latte Macchiato,LM1,,5.00,Getränke,0\n"

// TestImport_UnconfirmedCurrency_BlocksCommitWithoutConfirmation is the core
// regression case: committing a catalogue import into a till whose currency
// was never explicitly confirmed must prompt instead of silently writing
// rows under the (unconfirmed) default currency.
func TestImport_UnconfirmedCurrency_BlocksCommitWithoutConfirmation(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDepsWithCurrencyState(t, false)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	body, ct := multipartCSV(t, germanCSV, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Latte Macchiato") {
		t.Fatalf("unconfirmed-currency commit rendered import results instead of a confirmation prompt: %s", rec.Body.String())
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = 'LM1'`).Scan(&n); err != nil {
		t.Fatalf("count items: %v", err)
	}
	if n != 0 {
		t.Fatalf("unconfirmed-currency commit wrote %d rows, want 0 (must not commit without confirmation)", n)
	}
	confirmed, ok, err := dp.Settings.Get(t.Context(), common.KeyCurrencyConfirmed)
	if err != nil {
		t.Fatalf("read confirmed flag: %v", err)
	}
	if ok && confirmed == "true" {
		t.Fatalf("currency marked confirmed without the operator ever confirming anything")
	}
}

// TestImport_UnconfirmedCurrency_PreviewStillWorksButWarns: preview writes
// nothing regardless, so it must not be gated — but it should tell the
// operator the currency is unconfirmed rather than saying nothing.
func TestImport_UnconfirmedCurrency_PreviewStillWorksButWarns(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDepsWithCurrencyState(t, false)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	body, ct := multipartCSV(t, germanCSV, nil) // no commit
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Latte Macchiato") {
		t.Fatalf("preview must still show parsed rows even when currency is unconfirmed: %s", rec.Body.String())
	}
	// ut-docs#970 review (F5): this test's own name promises "...ButWarns" —
	// assert the warning actually renders, not just that the rows do. It
	// passed unchanged when the warning line was neutered out entirely,
	// which is exactly the class of tautological test this pipeline's
	// tester skill exists to catch.
	if !strings.Contains(rec.Body.String(), "notice-block-warn") || !strings.Contains(rec.Body.String(), "GBP") {
		t.Fatalf("preview must render the currency-unconfirmed warning (with the active currency code): %s", rec.Body.String())
	}
}

// TestImport_UnconfirmedCurrency_ConfirmSameCurrencyCommits: the operator
// looks at the prompt and confirms the till's current (still-default)
// currency really is correct — a single request with confirm_currency
// matching ActiveCurrency() must commit immediately, no second round trip
// needed, and must record the confirmation so it isn't asked again.
func TestImport_UnconfirmedCurrency_ConfirmSameCurrencyCommits(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDepsWithCurrencyState(t, false)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	body, ct := multipartCSV(t, germanCSV, map[string]string{"commit": "1", "confirm_currency": "GBP"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
	var priceMinor int64
	if err := dp.Db.QueryRow(`SELECT base_price FROM items WHERE sku = 'LM1'`).Scan(&priceMinor); err != nil {
		t.Fatalf("item not created: %v (body: %s)", err, rec.Body.String())
	}
	if priceMinor != 500 {
		t.Fatalf("price = %d minor units, want 500 (GBP, 2 decimals)", priceMinor)
	}
	confirmed, ok, err := dp.Settings.Get(t.Context(), common.KeyCurrencyConfirmed)
	if err != nil || !ok || confirmed != "true" {
		t.Fatalf("currency_confirmed = (%q, %v, %v), want (true, true, nil)", confirmed, ok, err)
	}
}

// TestImport_UnconfirmedCurrency_ConfirmDifferentCurrencySwitchesTillAndReparses
// is the scenario ut-docs#970 was actually filed for: the operator sees the
// prompt and says "no, this file is in EUR, not GBP" — that must switch the
// till's currency (not just relabel the same numbers) and, critically,
// re-parse the source amounts under the CONFIRMED currency's decimal count
// rather than committing whatever was parsed under the old (wrong) one. IRT
// (0 decimals) vs GBP (2 decimals) makes a silently-stale parse observable:
// "5.00" parsed as GBP is 500 minor units, but re-parsed as IRT it must be 5.
func TestImport_UnconfirmedCurrency_ConfirmDifferentCurrencySwitchesTillAndReparses(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	t.Cleanup(func() { httpx.InitCurrency("GBP") }) // ut-docs#970: InitCurrency is process-global (internal/httpx/httpx.go) — reset for later tests in this package, same convention as internal/pages/catalog/cost_currency_test.go.
	dp := newImportTestDepsWithCurrencyState(t, false)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	body, ct := multipartCSV(t, germanCSV, map[string]string{"commit": "1", "confirm_currency": "IRT"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
	var priceMinor int64
	if err := dp.Db.QueryRow(`SELECT base_price FROM items WHERE sku = 'LM1'`).Scan(&priceMinor); err != nil {
		t.Fatalf("item not created: %v (body: %s)", err, rec.Body.String())
	}
	if priceMinor != 5 {
		t.Fatalf("price = %d minor units, want 5 (IRT, 0 decimals — proves the source was re-parsed under the confirmed currency, not just relabeled)", priceMinor)
	}
	cur, ok, err := dp.Settings.Get(t.Context(), common.KeyCurrency)
	if err != nil || !ok || cur != "IRT" {
		t.Fatalf("store.currency = (%q, %v, %v), want (IRT, true, nil) — confirming a different currency must switch the till to it", cur, ok, err)
	}
	if got := httpx.ActiveCurrency().Code; got != "IRT" {
		t.Fatalf("httpx.ActiveCurrency() = %q, want IRT (must take effect immediately, not just on next restart)", got)
	}
}

// TestImport_UnconfirmedCurrency_RejectsUnknownConfirmCurrency is the direct
// regression test for review finding F1 (blocker): the original validation
// was `httpx.CurrencyByCode(v).Code == v`, which is ALWAYS true for an
// already-uppercased/trimmed v (CurrencyByCode fabricates a plausible
// CurrencyInfo for any unknown code rather than rejecting it) — so an
// operator (or a crafted request) confirming a currency that isn't in the
// registry at all previously switched the till to it anyway and marked it
// confirmed.
func TestImport_UnconfirmedCurrency_RejectsUnknownConfirmCurrency(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDepsWithCurrencyState(t, false)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	body, ct := multipartCSV(t, germanCSV, map[string]string{"commit": "1", "confirm_currency": "XYZ"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code %d, want 400 for an unknown currency code (body: %s)", rec.Code, rec.Body.String())
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE sku = 'LM1'`).Scan(&n); err != nil {
		t.Fatalf("count items: %v", err)
	}
	if n != 0 {
		t.Fatalf("an unknown confirm_currency must not commit anything, got %d rows", n)
	}
	cur, ok, err := dp.Settings.Get(t.Context(), common.KeyCurrency)
	if err == nil && ok && cur == "XYZ" {
		t.Fatalf("till currency must not switch to an unknown code, got %q", cur)
	}
	confirmed, ok, err := dp.Settings.Get(t.Context(), common.KeyCurrencyConfirmed)
	if err != nil {
		t.Fatalf("read confirmed flag: %v", err)
	}
	if ok && confirmed == "true" {
		t.Fatalf("currency must not be marked confirmed off a rejected confirm_currency")
	}
}

// TestImport_UnconfirmedCurrency_BkpPathGatedAndReparses is F10's coverage
// gap: the .bkp path (a speedy kasse / pepperm cashbox backup — the ticket's
// own headline scenario) had zero currency-gate test coverage; every other
// test in this file drives the CSV path. Latte's seeded SalesPrice is 3.20 —
// 320 minor units under GBP (2 decimals), 3 under IRT (0 decimals), so a
// wrong decimal count is observable exactly like the CSV re-parse test.
func TestImport_UnconfirmedCurrency_BkpPathGatedAndReparses(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	t.Cleanup(func() { httpx.InitCurrency("GBP") })
	dp := newImportTestDepsWithCurrencyState(t, false)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	zipBytes := buildBkpZipForPagesTest(t)

	// First attempt: unconfirmed currency must gate, not commit.
	body, ct := multipartFile(t, "Backup 2026-08-09.bkp", zipBytes, map[string]string{"commit": "1"})
	req := httptest.NewRequest(http.MethodPost, "/api/import", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Latte") {
		t.Fatalf("unconfirmed .bkp commit should prompt, not import: code %d body %s", rec.Code, rec.Body.String())
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM items WHERE name = 'Latte'`).Scan(&n); err != nil {
		t.Fatalf("count items: %v", err)
	}
	if n != 0 {
		t.Fatalf("unconfirmed .bkp commit wrote %d rows, want 0", n)
	}

	// Confirm as IRT (0 decimals, unlike GBP's 2) — proves the .bkp bytes
	// get re-parsed under the confirmed currency, not just relabeled.
	body2, ct2 := multipartFile(t, "Backup 2026-08-09.bkp", zipBytes, map[string]string{"commit": "1", "confirm_currency": "IRT"})
	req2 := httptest.NewRequest(http.MethodPost, "/api/import", body2)
	req2.Header.Set("Content-Type", ct2)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("confirmed .bkp commit: code %d body %s", rec2.Code, rec2.Body.String())
	}
	var priceMinor int64
	if err := dp.Db.QueryRow(`SELECT base_price FROM items WHERE name = 'Latte'`).Scan(&priceMinor); err != nil {
		t.Fatalf("Latte not created: %v (body: %s)", err, rec2.Body.String())
	}
	if priceMinor != 3 {
		t.Fatalf(".bkp price = %d minor units, want 3 (IRT, 0 decimals — proves the .bkp bytes were re-parsed, not just relabeled)", priceMinor)
	}
}

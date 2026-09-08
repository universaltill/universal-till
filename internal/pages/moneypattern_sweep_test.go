package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#1530: four call sites (menu.html's #pfand-amount, promotions.html's
// two value_amount fields, settings.html's payments-fee "fixed" field)
// hand-rolled the `{{ if eq currency.Decimals 0 }}...{{ else }}...{{ end }}`
// pattern/placeholder ternary instead of using the shared
// httpx.MoneyPatternAttr/MoneyPlaceholderAttr helpers already used elsewhere
// (shifts.html, catalog.html) via the {{ moneypattern }}/{{ moneyplaceholder }}
// template funcs. This file pins the exact rendered `pattern="…"`/
// `placeholder="…"` attribute strings at each site for both a 0-decimal
// (IRT) and a 2-decimal (GBP) currency -- the only two decimal counts any
// shipped currency in httpx's registry actually uses -- so the sweep from
// hand-rolled ternary to shared helper is proven byte-identical, not just
// asserted.

// valueAmountTags extracts every rendered <input ...> tag whose name
// attribute is "value_amount" from body, in document order -- promotions.html
// has two such fields (the inline edit row and the "new promotion" dialog),
// and this keeps the assertions anchored to the real, on-page markup instead
// of guessing offsets.
func valueAmountTags(body string) []string {
	return regexp.MustCompile(`<input[^>]*name="value_amount"[^>]*/?>`).FindAllString(body, -1)
}

// fixedFieldTags extracts every rendered <input ...> tag whose name
// attribute is "fixed" -- settings.html renders one payments-fee row per
// payment method (cash/card/gift are seeded by the real migrations), so this
// can return more than one tag; every one of them must reflect the same
// active currency's decimals.
func fixedFieldTags(body string) []string {
	return regexp.MustCompile(`<input[^>]*name="fixed"[^>]*>`).FindAllString(body, -1)
}

// pfandAmountTag extracts menu.html's single #pfand-amount input tag.
func pfandAmountTag(body string) string {
	m := regexp.MustCompile(`<input[^>]*id="pfand-amount"[^>]*>`).FindString(body)
	return m
}

// ut-docs#1530: menu.html's #pfand-amount field hand-rolled BOTH the pattern
// and placeholder ternary. GBP (2-decimal) must render exactly the same
// pattern/placeholder it always has; IRT (0-decimal) must render the
// integer-only pattern/placeholder, with no 2-decimal shape left over.
func TestMenuPage_PfandAmountPatternAndPlaceholderAreCurrencyAware(t *testing.T) {
	mux, _ := newMenuPageTestDeps(t, nil)

	get := func() string {
		req := httptest.NewRequest(http.MethodGet, "/menu", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /menu = %d: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	httpx.InitCurrency("GBP")
	tag := pfandAmountTag(get())
	if tag == "" {
		t.Fatalf("expected the #pfand-amount input to render")
	}
	if !strings.Contains(tag, `pattern="[0-9]+(\.[0-9]{1,2})?"`) {
		t.Errorf("GBP: expected the 2-decimal pattern, got: %s", tag)
	}
	if !strings.Contains(tag, `placeholder="0.00"`) {
		t.Errorf("GBP: expected the 2-decimal placeholder, got: %s", tag)
	}

	httpx.InitCurrency("IRT")
	t.Cleanup(func() { httpx.InitCurrency("GBP") }) // ut-docs#970 convention: process-global, reset for later tests in this package.
	tag = pfandAmountTag(get())
	if tag == "" {
		t.Fatalf("expected the #pfand-amount input to render")
	}
	if !strings.Contains(tag, `pattern="[0-9]+"`) {
		t.Errorf("IRT: expected the 0-decimal (integer-only) pattern, got: %s", tag)
	}
	if strings.Contains(tag, `pattern="[0-9]+(\.[0-9]{1,2})?"`) {
		t.Errorf("IRT: expected NO 2-decimal pattern left over, got: %s", tag)
	}
	if !strings.Contains(tag, `placeholder="0"`) {
		t.Errorf("IRT: expected the 0-decimal placeholder, got: %s", tag)
	}
	if strings.Contains(tag, `placeholder="0.00"`) {
		t.Errorf("IRT: expected NO 2-decimal placeholder left over, got: %s", tag)
	}
}

// ut-docs#1530: promotions.html's two value_amount fields (the "new
// promotion" dialog, always rendered, and the inline edit row on an
// existing amount-type promotion) hand-rolled the pattern ternary (no
// placeholder ternary at either site -- their placeholder attributes are
// either a static i18n label or absent). Both fields must move to
// {{ moneypattern currency.Decimals false }} with byte-identical output.
func TestPromotionsPage_ValueAmountPatternIsCurrencyAware(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newPromotionsTestMux(t)

	// Seed one amount-type promotion so the inline edit row (the second
	// value_amount site) actually renders -- an empty list only exercises
	// the "new promotion" dialog's static copy of the field.
	if rec := postForm(mux, "/api/promotions", url.Values{
		"code": {"SWEEP1530"}, "type": {"amount"}, "value_amount": {"5.00"},
	}, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("seed promotion: code=%d body=%s", rec.Code, rec.Body.String())
	}

	get := func() string {
		req := httptest.NewRequest(http.MethodGet, "/promotions", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /promotions = %d: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	httpx.InitCurrency("GBP")
	tags := valueAmountTags(get())
	// The dialog's own copy of the field is always present, plus one inline
	// row per amount-type promotion already seeded (seedForPages seeds one,
	// SWEEP1530 above adds a second) -- so at least 2, exact count left
	// loose rather than pinned to seedForPages' fixture contents.
	if len(tags) < 2 {
		t.Fatalf("expected at least 2 value_amount fields (dialog + inline row(s)), got %d: %v", len(tags), tags)
	}
	for _, tag := range tags {
		if !strings.Contains(tag, `pattern="[0-9]+(\.[0-9]{1,2})?"`) {
			t.Errorf("GBP: expected the 2-decimal pattern on %s", tag)
		}
	}

	httpx.InitCurrency("IRT")
	t.Cleanup(func() { httpx.InitCurrency("GBP") }) // ut-docs#970 convention: process-global, reset for later tests in this package.
	body := get()
	tags = valueAmountTags(body)
	if len(tags) < 2 {
		t.Fatalf("expected at least 2 value_amount fields (dialog + inline row(s)), got %d: %v", len(tags), tags)
	}
	for _, tag := range tags {
		if !strings.Contains(tag, `pattern="[0-9]+"`) {
			t.Errorf("IRT: expected the 0-decimal (integer-only) pattern on %s", tag)
		}
		if strings.Contains(tag, `pattern="[0-9]+(\.[0-9]{1,2})?"`) {
			t.Errorf("IRT: expected NO 2-decimal pattern left over on %s", tag)
		}
	}
	// The percent-type sibling field (name="value_percent") is NOT part of
	// this sweep -- basis-point percentages stay 2-decimal regardless of
	// the shop's money currency -- so it must be unaffected by the
	// currency switch either way.
	percentTags := regexp.MustCompile(`<input[^>]*name="value_percent"[^>]*/?>`).FindAllString(body, -1)
	if len(percentTags) < 1 {
		t.Fatalf("expected at least 1 value_percent field (the dialog's), got %d: %v", len(percentTags), percentTags)
	}
	for _, tag := range percentTags {
		if !strings.Contains(tag, `pattern="[0-9]+(\.[0-9]{1,2})?"`) {
			t.Errorf("IRT: expected value_percent's own 2-decimal pattern to remain unaffected by currency, got: %s", tag)
		}
	}
}

// ut-docs#1530: settings.html's payments-fee "fixed" field hand-rolled the
// pattern ternary (its placeholder is a static "0.00", not a ternary, so
// it's out of this sweep's scope and must NOT be touched or asserted here).
func TestSettingsPage_PaymentsFeeFixedPatternIsCurrencyAware(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)

	get := func() string {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), mgrUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /settings = %d: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	httpx.InitCurrency("GBP")
	tags := fixedFieldTags(get())
	if len(tags) == 0 {
		t.Fatalf("expected at least one payments-fee 'fixed' field to render")
	}
	for _, tag := range tags {
		if !strings.Contains(tag, `pattern="[0-9]+(\.[0-9]{1,2})?"`) {
			t.Errorf("GBP: expected the 2-decimal pattern on %s", tag)
		}
	}

	httpx.InitCurrency("IRT")
	t.Cleanup(func() { httpx.InitCurrency("GBP") }) // ut-docs#970 convention: process-global, reset for later tests in this package.
	tags = fixedFieldTags(get())
	if len(tags) == 0 {
		t.Fatalf("expected at least one payments-fee 'fixed' field to render")
	}
	for _, tag := range tags {
		if !strings.Contains(tag, `pattern="[0-9]+"`) {
			t.Errorf("IRT: expected the 0-decimal (integer-only) pattern on %s", tag)
		}
		if strings.Contains(tag, `pattern="[0-9]+(\.[0-9]{1,2})?"`) {
			t.Errorf("IRT: expected NO 2-decimal pattern left over on %s", tag)
		}
	}
}

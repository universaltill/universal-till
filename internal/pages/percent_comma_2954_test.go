package pages

import (
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#2954: #2925 made every money input comma-tolerant, but the
// percent inputs kept a hand-typed dot-only pattern and a server-side
// strconv.ParseFloat, so a German/Turkish keyboard's "1,5" was refused in
// the device OS language (and "1e3" was accepted). Every percent field's
// value is now read by httpx.ParsePercentBP, so no template may keep the
// hand-typed dot-only decimal pattern on a percent field.
func TestPercentTemplates_NoDotOnlyPattern(t *testing.T) {
	chdirRoot(t)
	percentField := regexp.MustCompile(`name="(value_percent|percent|tax_rate_pct|rate|takeawayRate)"`)
	var hits []string
	err := filepath.WalkDir(filepath.Join("web", "ui"), func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			if percentField.MatchString(line) && strings.Contains(line, `(\.[0-9]{1,2})?`) {
				hits = append(hits, path+":"+strconv.Itoa(i+1))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) > 0 {
		t.Fatalf("percent inputs still using the dot-only pattern (use {{ percentpatternlocal }}; ut-docs#2954):\n%s", strings.Join(hits, "\n"))
	}
}

func TestPromotionsCreate_ValuePercentAcceptsDecimalComma(t *testing.T) {
	mux, d := newPromotionsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}

	rec := postForm(mux, "/api/promotions", url.Values{
		"code": {"KOMMA15"}, "type": {"percent"}, "value_percent": {"1,5"},
	}, &manager)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/promotions" {
		t.Fatalf("create 1,5%%: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	var value int64
	if err := d.Db.QueryRow(`SELECT value FROM promotions WHERE code = 'KOMMA15'`).Scan(&value); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if value != 150 {
		t.Fatalf("value = %d, want 150 bp", value)
	}

	for i, bad := range []string{"1e3", "NaN", "1,555", "0", "0,00", "100,01", "-5"} {
		rec = postForm(mux, "/api/promotions", url.Values{
			"code": {"BADPCT" + strconv.Itoa(i)}, "type": {"percent"}, "value_percent": {bad},
		}, &manager)
		if rec.Header().Get("Location") != "/promotions?err=promotions.error.value_invalid" {
			t.Errorf("value_percent %q: loc=%q, want value_invalid", bad, rec.Header().Get("Location"))
		}
	}
}

func TestPaymentsFee_PercentAcceptsDecimalComma(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	rec := postForm(mux, "/api/settings/payments-fee", url.Values{
		"method": {"card"}, "percent": {"1,5"}, "fixed": {"0,30"},
	}, &mgrUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("percent 1,5: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var fee struct {
		BP    int64 `json:"bp"`
		Fixed int64 `json:"fixed"`
	}
	raw, _, _ := d.Settings.Get(t.Context(), "payments.fee.card")
	if err := json.Unmarshal([]byte(raw), &fee); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	if fee.BP != 150 || fee.Fixed != 30 {
		t.Fatalf("stored fee = %+v, want {BP:150 Fixed:30}", fee)
	}

	// Empty percent stays a zero percentage, as before.
	rec = postForm(mux, "/api/settings/payments-fee", url.Values{"method": {"cash"}, "fixed": {"0,10"}}, &mgrUser)
	if !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("empty percent: %s", rec.Body.String())
	}

	// A malformed percent is refused, never silently stored as 0.
	for _, bad := range []string{"1e3", "NaN", "abc", "1,555", "-1", "100,5"} {
		rec = postForm(mux, "/api/settings/payments-fee", url.Values{"method": {"card"}, "percent": {bad}}, &mgrUser)
		if !strings.Contains(rec.Body.String(), "range") {
			t.Errorf("percent %q: body=%s, want the range refusal", bad, rec.Body.String())
		}
	}
	raw, _, _ = d.Settings.Get(t.Context(), "payments.fee.card")
	if err := json.Unmarshal([]byte(raw), &fee); err != nil || fee.BP != 150 {
		t.Fatalf("a refused percent changed the stored fee: %q", raw)
	}
}

func TestCountrySettings_TaxRateAcceptsDecimalComma(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, repo, _ := newCountrySettingsTestMux(t)

	rec := postForm(mux, "/api/country-settings", url.Values{
		"code": {"ZZ"}, "currency": {"EUR"}, "currency_symbol": {"€"},
		"tax_rate_pct":     {"7,5"},
		"archive_min_days": {strconv.FormatInt(data.GlobalArchiveMinDays, 10)},
	}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create 7,5%%: code=%d loc=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	cs, ok, err := repo.Get(t.Context(), "ZZ")
	if err != nil || !ok {
		t.Fatalf("read back ZZ: ok=%v err=%v", ok, err)
	}
	if cs.TaxRateBP != 750 {
		t.Fatalf("TaxRateBP = %d, want 750", cs.TaxRateBP)
	}

	rec = postForm(mux, "/api/country-settings", url.Values{
		"code": {"ZY"}, "currency": {"EUR"}, "currency_symbol": {"€"},
		"tax_rate_pct":     {"1e3"},
		"archive_min_days": {strconv.FormatInt(data.GlobalArchiveMinDays, 10)},
	}, nil)
	if _, ok, _ := repo.Get(t.Context(), "ZY"); ok {
		t.Fatalf("tax_rate_pct 1e3 was accepted (code=%d loc=%q)", rec.Code, rec.Header().Get("Location"))
	}
}

func TestTaxCodes_RateAcceptsDecimalComma(t *testing.T) {
	for _, c := range []struct {
		rate, takeaway string
		wantRate       int
		wantTakeaway   int
	}{{"7,5", "5,5", 750, 550}, {"19.5", "7", 1950, 700}} {
		if bp, err := parsePercentToBP(c.rate); err != nil || bp != c.wantRate {
			t.Errorf("parsePercentToBP(%q) = %d, %v; want %d", c.rate, bp, err, c.wantRate)
		}
		if bp, err := parsePercentToBP(c.takeaway); err != nil || bp != c.wantTakeaway {
			t.Errorf("parsePercentToBP(%q) = %d, %v; want %d", c.takeaway, bp, err, c.wantTakeaway)
		}
	}
	for _, bad := range []string{"1e3", "NaN", "Inf", "1,555", "-1", "100,01", ""} {
		if bp, err := parsePercentToBP(bad); err == nil {
			t.Errorf("parsePercentToBP(%q) = %d, want an error", bad, bp)
		}
	}
}

// ut-docs#2954 review: on a 0-decimal-currency shop the money message is
// "whole number, digits only", which is wrong for a percent field (7,5% is
// fine). Percent fields read <body data-percent-invalid>, which carries the
// decimal message whatever the currency.
func TestPercentInvalidMessage_IgnoresCurrencyDecimals(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newPromotionsTestMux(t)
	httpx.InitCurrency("IRT")
	t.Cleanup(func() { httpx.InitCurrency("GBP") }) // ut-docs#970 convention: process-global
	req := httptest.NewRequest(http.MethodGet, "/promotions", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	want := `data-percent-invalid="` + template.HTMLEscapeString(httpx.T("en", "common.money.invalid")) + `"`
	if !strings.Contains(body, want) {
		t.Fatalf("IRT shop: body lacks %s", want)
	}
	if !strings.Contains(body, `name="value_percent"`) || !strings.Contains(body, `data-money-local="percent" name="value_percent"`) {
		t.Fatalf("value_percent does not opt into the percent message")
	}
}

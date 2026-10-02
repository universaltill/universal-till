package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// --- ADR-0136 Decision 8 (ut-docs#3309): a German shop's tax codes may only
// carry one of fiskaly's five DSFinV-K rates, so the VAT-rate cause of a
// cannot-sign tender is stopped at the source. Every other country is
// unaffected. ---

func postTaxCodeForm(t *testing.T, mux *http.ServeMux, path, form string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func setShopCountry(dp *common.Deps, cc string) {
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = cc })
}

const deFiscalRateMsg = "the rates Germany's fiscal signing service recognises"

func TestTaxCodesAPI_DE_RefusesNonFiscalRate(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	for _, tc := range []struct{ name, form string }{
		{"sale rate", "name=Odd&rate=15"},
		{"takeaway rate", "name=Odd&rate=19&takeawayRate=16"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux, dp := newTaxCodesTestDeps(t)
			setShopCountry(dp, "DE")
			rec := postTaxCodeForm(t, mux, "/api/catalog/tax-codes", tc.form)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400 for a non-DSFinV-K rate in a DE shop, got %d: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), deFiscalRateMsg) {
				t.Fatalf("want the taxcodes.err.rate_not_de_fiscal message, got %q", rec.Body.String())
			}
			var n int
			if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM tax_codes WHERE name='Odd'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatalf("refused code must not be saved, got %d rows", n)
			}
		})
	}
}

func TestTaxCodesAPI_DE_AcceptsEveryFiscalBucket(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newTaxCodesTestDeps(t)
	setShopCountry(dp, "DE")
	for i, rate := range []string{"19", "7", "10.7", "5.5", "0"} {
		form := "name=Bucket" + string(rune('A'+i)) + "&rate=" + rate + "&takeawayRate=7"
		if rec := postTaxCodeForm(t, mux, "/api/catalog/tax-codes", form); rec.Code != http.StatusOK {
			t.Fatalf("rate %s%% is a DSFinV-K bucket and must save in a DE shop, got %d: %s", rate, rec.Code, rec.Body.String())
		}
	}
}

func TestTaxCodesAPI_DE_UpdateRefusesNonFiscalRate(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newTaxCodesTestDeps(t)
	setShopCountry(dp, "DE")
	if rec := postTaxCodeForm(t, mux, "/api/catalog/tax-codes", "name=Std&rate=19"); rec.Code != http.StatusOK {
		t.Fatalf("seed create: %d %s", rec.Code, rec.Body.String())
	}
	id := rateCodeID(t, dp, "Std")
	rec := postTaxCodeForm(t, mux, "/api/catalog/tax-codes/update", "id="+id+"&name=Std&rate=16&active=1")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), deFiscalRateMsg) {
		t.Fatalf("want 400 rate_not_de_fiscal on update, got %d: %s", rec.Code, rec.Body.String())
	}
	var bp int
	if err := dp.Db.QueryRow(`SELECT rate_basis_points FROM tax_codes WHERE id=?`, id).Scan(&bp); err != nil {
		t.Fatal(err)
	}
	if bp != 1900 {
		t.Fatalf("refused update must leave the stored rate at 1900, got %d", bp)
	}
}

// Every other country's save is unaffected: an arbitrary rate still saves.
func TestTaxCodesAPI_NonDE_AcceptsArbitraryRate(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	for _, cc := range []string{"GB", "TR", ""} {
		t.Run("country="+cc, func(t *testing.T) {
			mux, dp := newTaxCodesTestDeps(t)
			setShopCountry(dp, cc)
			rec := postTaxCodeForm(t, mux, "/api/catalog/tax-codes", "name=Odd&rate=15&takeawayRate=16")
			if rec.Code != http.StatusOK {
				t.Fatalf("a non-DE shop must save an arbitrary rate, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// Cashier tip entry at checkout. Test basket: ABC is 100 + 20% tax = 120.

type tipPayRow struct {
	amount, tip, change int64
	method, recipient   string
}

func tipPayments(t *testing.T, dp *common.Deps) []tipPayRow {
	t.Helper()
	rows, err := dp.Db.Query(`SELECT method_id, amount, tip_amount, change_given, COALESCE(tip_recipient,'') FROM payments ORDER BY rowid`)
	if err != nil {
		t.Fatalf("query payments: %v", err)
	}
	defer rows.Close()
	var out []tipPayRow
	for rows.Next() {
		var r tipPayRow
		if err := rows.Scan(&r.method, &r.amount, &r.tip, &r.change, &r.recipient); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func tipSetup(t *testing.T, tipsOn bool) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, dp := newPOSTestDeps(t)
	if tipsOn {
		if err := dp.Settings.Set(t.Context(), common.KeyTipsEnabled, "1"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	return mux, dp
}

func TestTender_PercentTipIsStoredAndIncludedInAmount(t *testing.T) {
	for _, tc := range []struct {
		form          string
		wantTip, want int64
	}{
		{"method=cash&amount=0&tip_bp=1000", 12, 132},      // 10% of 120
		{"method=card&amount=0&tip_bp=1500", 18, 138},      // 15% of 120
		{"method=cash&amount=0&tip_bp=1250", 15, 135},      // 12.5% of 120 = 15 exactly
		{"method=cash&amount=0&tip_bp=0", 0, 120},          // tip zero
		{"method=cash&amount=0", 0, 120},                   // no tip field at all
		{"method=cash&amount=0&tip=7", 7, 127},             // typed amount
		{"method=cash&amount=0&tip=0", 0, 120},             // typed zero
		{"method=cash&amount=0&tip=&tip_bp=", 0, 120},      // empty fields
		{"method=cash&amount=0&tip_bp=1000&tip=5", 5, 125}, // a typed amount wins over a percent
	} {
		t.Run(tc.form, func(t *testing.T) {
			mux, dp := tipSetup(t, true)
			rec := posPostForm(mux, "/api/pos/tender", tc.form)
			if rec.Code != http.StatusOK {
				t.Fatalf("tender: %d %s", rec.Code, rec.Body.String())
			}
			got := tipPayments(t, dp)
			if len(got) != 1 {
				t.Fatalf("payments = %+v, want one", got)
			}
			if got[0].amount != tc.want || got[0].tip != tc.wantTip {
				t.Fatalf("amount/tip = %d/%d, want %d/%d", got[0].amount, got[0].tip, tc.want, tc.wantTip)
			}
			if tc.wantTip > 0 && got[0].recipient != "employee" {
				t.Fatalf("tip_recipient = %q, want the ADR-0061 default employee", got[0].recipient)
			}
		})
	}
}

// Tips are off for a shop that has not turned them on: a stray field changes nothing.
func TestTender_TipIgnoredWhenTipsDisabled(t *testing.T) {
	mux, dp := tipSetup(t, false)
	rec := posPostForm(mux, "/api/pos/tender", "method=cash&amount=0&tip_bp=1000&tip=9")
	if rec.Code != http.StatusOK {
		t.Fatalf("tender: %d %s", rec.Code, rec.Body.String())
	}
	got := tipPayments(t, dp)
	if len(got) != 1 || got[0].amount != 120 || got[0].tip != 0 {
		t.Fatalf("payments = %+v, want one of 120 with no tip", got)
	}
}

func TestTender_TipRefusedWhenAbsurdOrMalformed(t *testing.T) {
	for _, form := range []string{
		"method=cash&amount=0&tip=121",      // above 100% of the 120 total
		"method=cash&amount=0&tip=-5",       // negative
		"method=cash&amount=0&tip=abc",      // not a number
		"method=cash&amount=0&tip_bp=10001", // above 100%
		"method=cash&amount=0&tip_bp=-100",  // negative percent
	} {
		t.Run(form, func(t *testing.T) {
			mux, dp := tipSetup(t, true)
			rec := posPostForm(mux, "/api/pos/tender", form)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			if got := tipPayments(t, dp); len(got) != 0 {
				t.Fatalf("a refused tip must record no payment, got %+v", got)
			}
		})
	}
}

// A voucher-method leg can never carry a tip (netPayments refuses it): the
// form tip is dropped for it, the sale still completes.
func TestTender_VoucherMethodLegNeverCarriesFormTip(t *testing.T) {
	mux, dp := tipSetup(t, true)
	rec := posPostForm(mux, "/api/pos/tender", "method=voucher&amount=0&tip_bp=1000")
	if rec.Code != http.StatusOK {
		t.Fatalf("tender: %d %s", rec.Code, rec.Body.String())
	}
	if got := tipPayments(t, dp); len(got) != 1 || got[0].amount != 120 || got[0].tip != 0 {
		t.Fatalf("payments = %+v, want one voucher leg of 120 with no tip", got)
	}
}

// Split tender: each leg's amount includes its tip; the tip is held on the leg that took it.
func TestTender_SplitCardAndCashWithTip(t *testing.T) {
	mux, dp := tipSetup(t, true)
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(
		`{"payments":[{"method":"card","amount":70},{"method":"cash","amount":62,"tip":12}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tender: %d %s", rec.Code, rec.Body.String())
	}
	got := tipPayments(t, dp)
	if len(got) != 2 || got[0].amount != 70 || got[0].tip != 0 || got[1].amount != 62 || got[1].tip != 12 {
		t.Fatalf("payments = %+v, want card 70/0 and cash 62/12", got)
	}
}

// A split leg whose amount does not cover the tip is refused, never silently
// counted toward the sale (ut-docs#2975).
func TestTender_SplitTipMustNotCoverSaleMoney(t *testing.T) {
	mux, dp := tipSetup(t, true)
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(
		`{"payments":[{"method":"cash","amount":120,"tip":12}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if got := tipPayments(t, dp); len(got) != 0 {
		t.Fatalf("120 with a 12 tip only covers 108 of 120: want no sale, got %+v (status %d)", got, rec.Code)
	}
}

// Review 2026-11-27: the split path gets the same slip guard as the quick-pay
// row. A tip typed as 500 instead of 5.00 on a card leg would charge the
// customer 620 for a 120 bill; tips across the legs may not exceed the bill.
func TestTender_SplitTipsAboveTheBillAreRefused(t *testing.T) {
	for name, body := range map[string]string{
		"one leg":  `{"payments":[{"method":"card","amount":620,"tip":500}],"offline":true}`,
		"two legs": `{"payments":[{"method":"card","amount":130,"tip":70},{"method":"cash","amount":111,"tip":51}],"offline":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			mux, dp := tipSetup(t, true)
			req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			if got := tipPayments(t, dp); len(got) != 0 {
				t.Fatalf("a refused tip must record no payment, got %+v", got)
			}
		})
	}
}

// A tip of exactly the bill (100%) is the limit and is accepted on a split leg.
func TestTender_SplitTipEqualToTheBillIsAccepted(t *testing.T) {
	mux, dp := tipSetup(t, true)
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(
		`{"payments":[{"method":"card","amount":240,"tip":120}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tender: %d %s", rec.Code, rec.Body.String())
	}
	if got := tipPayments(t, dp); len(got) != 1 || got[0].amount != 240 || got[0].tip != 120 {
		t.Fatalf("payments = %+v, want card 240 with tip 120", got)
	}
}

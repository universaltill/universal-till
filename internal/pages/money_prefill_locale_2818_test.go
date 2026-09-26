package pages

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#2818: server-rendered money-input prefills and placeholders use
// the shop language's decimal separator -- a German till showed "5.00" in
// an otherwise German screen and its own keyboard typed a comma into it.
// Every field below is a MoneyPatternLocal field whose reader accepts
// either separator, so only the display changes.

func TestMoneyPrefill_SettingsPaymentsFeeFixed_LocaleDecimal(t *testing.T) {
	httpx.InitCurrency("GBP")
	mux, _, d := newFullAuthDeps(t)
	if err := d.Settings.Set(t.Context(), "payments.fee.cash", `{"bp":0,"fixed":500}`); err != nil {
		t.Fatal(err)
	}
	get := func(lang string) string {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings?lang="+lang, nil), mgrUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /settings = %d: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	if body := get("de"); !strings.Contains(body, `value="5,00" placeholder="0,00"`) {
		t.Fatalf("expected the fixed-fee prefill 5,00 / placeholder 0,00 on a German till, got:\n%s", body)
	}
	if body := get("en"); !strings.Contains(body, `value="5.00" placeholder="0.00"`) {
		t.Fatalf("expected the fixed-fee prefill 5.00 / placeholder 0.00 on an English till, got:\n%s", body)
	}
}

func TestMoneyPrefill_ShiftsOpeningCash_LocaleDecimal(t *testing.T) {
	httpx.InitCurrency("GBP")
	mux, dp := newShiftsPageTestDeps(t)
	ctx := t.Context()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO registers(id,name,is_active) VALUES('reg1','Front Till',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO shifts(id,register_id,cashier_id,opened_at,closed_at,opening_cash,closing_cash,new_float) VALUES('shift0','reg1','user1','2026-01-01T00:00:00Z','2026-01-01T08:00:00Z',0,500,500)`); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, pos.SettingsKeyTillRegisterID, "reg1"); err != nil {
		t.Fatal(err)
	}
	get := func(lang string) string {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/shifts?lang="+lang, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /shifts = %d: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	body := get("de")
	if !regexp.MustCompile(`id="opening-cash"[^>]*value="5,00"`).MatchString(body) {
		t.Fatalf("expected #opening-cash prefilled 5,00 on a German till, got:\n%s", body)
	}
	if body := get("en"); !regexp.MustCompile(`id="opening-cash"[^>]*value="5\.00"`).MatchString(body) {
		t.Fatalf("expected #opening-cash prefilled 5.00 on an English till, got:\n%s", body)
	}
}

func TestMoneyPrefill_PromotionAmount_LocaleDecimal(t *testing.T) {
	httpx.InitCurrency("GBP")
	mux, d := newPromotionsTestMux(t)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	if _, err := d.Db.Exec(`INSERT INTO promotions(code, type, value, is_active) VALUES('FIVE2818', 'amount', 500, 1)`); err != nil {
		t.Fatal(err)
	}
	get := func(lang string) string {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/promotions?lang="+lang, nil), manager)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /promotions = %d: %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	if body := get("de"); !strings.Contains(body, `value="5,00"`) {
		t.Fatalf("expected the amount prefill 5,00 on a German till, got:\n%s", body)
	}
	if body := get("en"); !strings.Contains(body, `value="5.00"`) {
		t.Fatalf("expected the amount prefill 5.00 on an English till, got:\n%s", body)
	}
}

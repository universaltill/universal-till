package pages

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// ADR-0163 amendment 2026-10-16 (ut-docs#4539): the shop currency the cloud
// set for a my.-born store reaches the till. Before its first sale the main
// till adopts it; after a sale it never changes by itself, it reports the
// mismatch (open problem + a manager status chip).

func newShopCurrencyDeps(t *testing.T, tillCurrency, shopCurrency string) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, _, d := newFullAuthDeps(t)
	resetProcessGlobals(t)
	logging.ResolveProblems(problemKeyShopCurrency)
	t.Cleanup(func() { logging.ResolveProblems(problemKeyShopCurrency) })
	d.SetState(common.RuntimeState{Currency: tillCurrency})
	if err := d.Settings.SetMany(context.Background(), map[string]string{
		common.KeyCurrency:          tillCurrency,
		entitlement.KeyShopCurrency: shopCurrency,
	}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	httpx.InitCurrency(tillCurrency)
	return mux, d
}

// resetShopCurrencyMemo clears reconcileShopCurrency's process-global
// dedupe memory (review L7), so one test's warned pair can't silence the
// next test's warning. Called by resetProcessGlobals.
func resetShopCurrencyMemo() {
	shopCurrencyMu.Lock()
	defer shopCurrencyMu.Unlock()
	shopCurrencyWarnedPair = ""
	shopCurrencyDecimalsAt = ""
}

func seedShopCurrencySale(t *testing.T, d *common.Deps) {
	t.Helper()
	if _, err := d.Db.Exec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('s-1','R-1','completed','sale','GBP',100,0,0,100,datetime('now'))`); err != nil {
		t.Fatalf("seed sale: %v", err)
	}
}

func adoptAuditCount(t *testing.T, d *common.Deps) (n int, data string) {
	t.Helper()
	if err := d.Db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(data_json),'') FROM audit_log WHERE action = 'currency_adopted_from_cloud' AND actor_id = 'system'`).Scan(&n, &data); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	return n, data
}

func TestReconcileShopCurrency_NoSalesAdopts(t *testing.T) {
	_, d := newShopCurrencyDeps(t, "GBP", "EUR")
	logging.L().WarnProblemf(problemKeyShopCurrency, "stale mismatch")
	reconcileShopCurrency(context.Background(), d)

	if got := settingValue(t, d, common.KeyCurrency); got != "EUR" {
		t.Fatalf("store.currency = %q, want EUR", got)
	}
	if got := settingValue(t, d, common.KeyCurrencyConfirmed); got != "true" {
		t.Fatalf("store.currency_confirmed = %q, want true", got)
	}
	if got := d.CurrentState().Currency; got != "EUR" {
		t.Fatalf("runtime currency = %q, want EUR", got)
	}
	if got := httpx.ActiveCurrency().Code; got != "EUR" {
		t.Fatalf("httpx active currency = %q, want EUR", got)
	}
	n, payload := adoptAuditCount(t, d)
	if n != 1 || !strings.Contains(payload, `"from":"GBP"`) || !strings.Contains(payload, `"to":"EUR"`) {
		t.Fatalf("audit rows = %d payload = %s, want one system row from GBP to EUR", n, payload)
	}
	if logging.HasOpenProblem(problemKeyShopCurrency) {
		t.Fatal("problem still open after adopting")
	}

	// Idempotent: a second tick changes nothing and audits nothing more.
	reconcileShopCurrency(context.Background(), d)
	if n, _ := adoptAuditCount(t, d); n != 1 {
		t.Fatalf("second reconcile audited again: %d rows", n)
	}
}

func TestReconcileShopCurrency_WithSalesNeverChanges(t *testing.T) {
	_, d := newShopCurrencyDeps(t, "GBP", "EUR")
	seedShopCurrencySale(t, d)
	reconcileShopCurrency(context.Background(), d)

	if got := settingValue(t, d, common.KeyCurrency); got != "GBP" {
		t.Fatalf("store.currency = %q, want GBP kept", got)
	}
	if got := d.CurrentState().Currency; got != "GBP" {
		t.Fatalf("runtime currency = %q, want GBP kept", got)
	}
	if n, _ := adoptAuditCount(t, d); n != 0 {
		t.Fatalf("audited an adoption on a till with sales: %d", n)
	}
	if !logging.HasOpenProblem(problemKeyShopCurrency) {
		t.Fatal("no open problem for a mismatch on a till with sales")
	}
	// No log spam: a repeat tick does not add another WARN.
	before := countShopCurrencyProblems(problemKeyShopCurrency)
	reconcileShopCurrency(context.Background(), d)
	if after := countShopCurrencyProblems(problemKeyShopCurrency); after != before {
		t.Fatalf("repeat tick logged again: %d -> %d problems", before, after)
	}
}

func TestReconcileShopCurrency_VoidedSaleCountsAsSale(t *testing.T) {
	_, d := newShopCurrencyDeps(t, "GBP", "EUR")
	if _, err := d.Db.Exec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('s-v','R-v','voided','sale','GBP',100,0,0,100,datetime('now'))`); err != nil {
		t.Fatalf("seed voided sale: %v", err)
	}
	reconcileShopCurrency(context.Background(), d)
	if got := settingValue(t, d, common.KeyCurrency); got != "GBP" {
		t.Fatalf("store.currency = %q, want GBP kept", got)
	}
}

func TestReconcileShopCurrency_MatchResolvesProblem(t *testing.T) {
	_, d := newShopCurrencyDeps(t, "EUR", "EUR")
	logging.L().WarnProblemf(problemKeyShopCurrency, "stale mismatch")
	reconcileShopCurrency(context.Background(), d)
	if logging.HasOpenProblem(problemKeyShopCurrency) {
		t.Fatal("problem still open once the currencies match")
	}
	if n, _ := adoptAuditCount(t, d); n != 0 {
		t.Fatalf("matching currency audited an adoption: %d", n)
	}
}

func TestReconcileShopCurrency_NoShopCurrencyResolvesProblem(t *testing.T) {
	_, d := newShopCurrencyDeps(t, "GBP", "")
	logging.L().WarnProblemf(problemKeyShopCurrency, "stale mismatch")
	reconcileShopCurrency(context.Background(), d)
	if logging.HasOpenProblem(problemKeyShopCurrency) {
		t.Fatal("problem still open with no shop currency")
	}
	if got := settingValue(t, d, common.KeyCurrency); got != "GBP" {
		t.Fatalf("store.currency = %q, want GBP kept", got)
	}
}

func TestReconcileShopCurrency_FollowerTillDoesNothing(t *testing.T) {
	_, d := newShopCurrencyDeps(t, "GBP", "EUR")
	if err := d.Settings.Set(context.Background(), "sync.primary_url", "http://main.local:8080"); err != nil {
		t.Fatalf("seed primary url: %v", err)
	}
	reconcileShopCurrency(context.Background(), d)
	if got := settingValue(t, d, common.KeyCurrency); got != "GBP" {
		t.Fatalf("follower changed store.currency to %q", got)
	}
	if n, _ := adoptAuditCount(t, d); n != 0 {
		t.Fatalf("follower audited an adoption: %d", n)
	}
	if logging.HasOpenProblem(problemKeyShopCurrency) {
		t.Fatal("follower reported a problem the main till owns")
	}
}

func TestReconcileShopCurrency_UnknownCodeNeverAdopted(t *testing.T) {
	_, d := newShopCurrencyDeps(t, "GBP", "XQZ")
	reconcileShopCurrency(context.Background(), d)
	if got := settingValue(t, d, common.KeyCurrency); got != "GBP" {
		t.Fatalf("adopted an unknown currency: store.currency = %q", got)
	}
	if n, _ := adoptAuditCount(t, d); n != 0 {
		t.Fatalf("audited adopting an unknown currency: %d", n)
	}
	if !logging.HasOpenProblem(problemKeyShopCurrency) {
		t.Fatal("unknown shop currency not reported")
	}
}

func countShopCurrencyProblems(key string) int {
	n := 0
	for _, p := range logging.Recent() {
		if p.Key == key {
			n++
		}
	}
	return n
}

// --- status chip ---

const shopCurrencyChipLabel = "Currency differs from shop"

func TestShopCurrencyChip_ManagerSeesMismatch(t *testing.T) {
	mux, d := newShopCurrencyDeps(t, "GBP", "EUR")
	registerShopCurrencyChip(mux, d)
	rec := getAs(mux, "/ui/shop-currency-chip", &mgrUser)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, shopCurrencyChipLabel) {
		t.Fatalf("code=%d body=%s, want %q", rec.Code, body, shopCurrencyChipLabel)
	}
	if !strings.Contains(body, `data-testid="sb-shop-currency"`) || !strings.Contains(body, `href="/settings#settings-currency"`) {
		t.Fatalf("chip must carry its testid and link to Settings → Currency: %s", body)
	}
	if !strings.Contains(body, "GBP") || !strings.Contains(body, "EUR") {
		t.Fatalf("chip must name both currencies: %s", body)
	}
	for _, modal := range []string{"<dialog", "showModal", `role="alertdialog"`} {
		if strings.Contains(body, modal) {
			t.Fatalf("chip carries %q — must never be a modal: %s", modal, body)
		}
	}
}

func TestShopCurrencyChip_CashierSeesNothing(t *testing.T) {
	mux, d := newShopCurrencyDeps(t, "GBP", "EUR")
	registerShopCurrencyChip(mux, d)
	rec := getAs(mux, "/ui/shop-currency-chip", &cashUser)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("cashier: code=%d body=%q, want an empty 200", rec.Code, rec.Body.String())
	}
}

func TestShopCurrencyChip_EmptyWhenMatchingOrNoShopCurrency(t *testing.T) {
	for name, shop := range map[string]string{"matching": "GBP", "none": ""} {
		mux, d := newShopCurrencyDeps(t, "GBP", shop)
		registerShopCurrencyChip(mux, d)
		rec := getAs(mux, "/ui/shop-currency-chip", &mgrUser)
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "" {
			t.Fatalf("%s: code=%d body=%q, want an empty 200", name, rec.Code, rec.Body.String())
		}
	}
}

// --- review fixes ---

// H1: never adopt with a sale in progress — a cashier, kiosk or self-order
// basket with lines, or a parked (held) sale. The next tick retries.
func TestReconcileShopCurrency_NeverAdoptsMidBasket(t *testing.T) {
	line := pos.BasketLine{SKU: "S1", Name: "Coffee", ItemID: "i1", PriceCents: 250}
	for name, tc := range map[string]struct {
		busy  func(d *common.Deps)
		clear func(d *common.Deps)
	}{
		"cashier basket": {
			busy:  func(d *common.Deps) { d.Engine.AddLineWithModifiers(line, 1, nil) },
			clear: func(d *common.Deps) { d.Engine.Reset() },
		},
		"kiosk basket": {
			busy:  func(d *common.Deps) { d.KioskEngine.AddLineWithModifiers(line, 1, nil) },
			clear: func(d *common.Deps) { d.KioskEngine.Reset() },
		},
		"self-order session": {
			busy: func(d *common.Deps) {
				_, svc, ok := d.SelfOrderSessions.Create()
				if !ok {
					t.Fatal("create self-order session")
				}
				svc.AddLineWithModifiers(line, 1, nil)
			},
			clear: func(d *common.Deps) {
				d.SelfOrderSessions = pos.NewSessionBasketManager(func() *pos.Service { return pos.NewServiceWithResolver(pos.Config{}, stubResolver{}) })
			},
		},
		"held sale": {
			busy: func(d *common.Deps) {
				if err := data.NewHeldSalesRepo(d.Db).Upsert(context.Background(), data.HeldSale{ID: "h1", Label: "Table 4", Payload: `{}`}); err != nil {
					t.Fatalf("seed held sale: %v", err)
				}
			},
			clear: func(d *common.Deps) {
				if err := data.NewHeldSalesRepo(d.Db).Delete(context.Background(), "h1"); err != nil {
					t.Fatalf("delete held sale: %v", err)
				}
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, d := newShopCurrencyDeps(t, "GBP", "EUR")
			d.KioskEngine = pos.NewServiceWithResolver(pos.Config{}, stubResolver{})
			d.SelfOrderSessions = pos.NewSessionBasketManager(func() *pos.Service { return pos.NewServiceWithResolver(pos.Config{}, stubResolver{}) })
			tc.busy(d)
			reconcileShopCurrency(context.Background(), d)
			if got := settingValue(t, d, common.KeyCurrency); got != "GBP" {
				t.Fatalf("adopted mid-sale: store.currency = %q", got)
			}
			if got := d.CurrentState().Currency; got != "GBP" {
				t.Fatalf("adopted mid-sale: runtime currency = %q", got)
			}
			tc.clear(d)
			reconcileShopCurrency(context.Background(), d)
			if got := settingValue(t, d, common.KeyCurrency); got != "EUR" {
				t.Fatalf("after the sale cleared: store.currency = %q, want EUR", got)
			}
		})
	}
}

// M1: adopt once per shop value. After adopting EUR, a manager choosing
// GBP again is a decision: the till never flips back, it shows the chip
// and reports the problem instead.
func TestReconcileShopCurrency_AdoptsOncePerShopValue(t *testing.T) {
	mux, d := newShopCurrencyDeps(t, "GBP", "EUR")
	reconcileShopCurrency(context.Background(), d)
	if got := settingValue(t, d, entitlement.KeyShopCurrencyAdopted); got != "EUR" {
		t.Fatalf("adopted marker = %q, want EUR", got)
	}
	// The manager switches back to GBP in Settings.
	if err := d.Settings.Set(context.Background(), common.KeyCurrency, "GBP"); err != nil {
		t.Fatalf("manager switch: %v", err)
	}
	d.UpdateState(func(s *common.RuntimeState) { s.Currency = "GBP" })
	reconcileShopCurrency(context.Background(), d)
	if got := settingValue(t, d, common.KeyCurrency); got != "GBP" {
		t.Fatalf("re-adopted over the manager's choice: store.currency = %q", got)
	}
	if n, _ := adoptAuditCount(t, d); n != 1 {
		t.Fatalf("adoption audited %d times, want 1", n)
	}
	if !logging.HasOpenProblem(problemKeyShopCurrency) {
		t.Fatal("no open problem once the manager's choice differs from the shop")
	}
	registerShopCurrencyChip(mux, d)
	if rec := getAs(mux, "/ui/shop-currency-chip", &mgrUser); !strings.Contains(rec.Body.String(), shopCurrencyChipLabel) {
		t.Fatalf("no chip once the manager's choice differs: %s", rec.Body.String())
	}

	// A NEW shop value (the shop moved to USD) is adopted again while the
	// till still has no sales.
	if err := d.Settings.Set(context.Background(), entitlement.KeyShopCurrency, "USD"); err != nil {
		t.Fatalf("seed new shop currency: %v", err)
	}
	reconcileShopCurrency(context.Background(), d)
	if got := settingValue(t, d, common.KeyCurrency); got != "USD" {
		t.Fatalf("new shop value not adopted: store.currency = %q", got)
	}
}

// M2: a till that follows a main till shows no chip, like the hook.
func TestShopCurrencyChip_EmptyOnFollowerTill(t *testing.T) {
	mux, d := newShopCurrencyDeps(t, "GBP", "EUR")
	if err := d.Settings.Set(context.Background(), "sync.primary_url", "http://main.local:8080"); err != nil {
		t.Fatalf("seed primary url: %v", err)
	}
	registerShopCurrencyChip(mux, d)
	rec := getAs(mux, "/ui/shop-currency-chip", &mgrUser)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("follower: code=%d body=%q, want an empty 200", rec.Code, rec.Body.String())
	}
}

// M4: a failed persist never becomes the runtime state.
func TestReconcileShopCurrency_FailedWriteKeepsRuntime(t *testing.T) {
	_, d := newShopCurrencyDeps(t, "GBP", "EUR")
	if _, err := d.Db.Exec(`CREATE TRIGGER fail_currency BEFORE UPDATE ON settings WHEN NEW.key = 'store.currency' BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatalf("install trigger: %v", err)
	}
	reconcileShopCurrency(context.Background(), d)
	if got := d.CurrentState().Currency; got != "GBP" {
		t.Fatalf("runtime currency = %q after a failed write, want GBP", got)
	}
	if got := httpx.ActiveCurrency().Code; got != "GBP" {
		t.Fatalf("httpx currency = %q after a failed write, want GBP", got)
	}
	if n, _ := adoptAuditCount(t, d); n != 0 {
		t.Fatalf("audited a failed adoption: %d", n)
	}
}

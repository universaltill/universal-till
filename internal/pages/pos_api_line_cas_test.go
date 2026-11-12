package pages

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#5417 (review of #5411, finding 4): the line handler read the
// line, decided whether the change was a reduction, and only then applied
// qty absolutely. A concurrent raise in between turned a "raise / same
// qty" post into an ungated, unrecorded reduction. The update is now a
// compare-and-set on the qty the gate saw.

// setLineSnapshotTestHook installs the handler's race seam for one test.
func setLineSnapshotTestHook(t *testing.T, f func()) {
	t.Helper()
	lineSnapshotTestHook = f
	t.Cleanup(func() { lineSnapshotTestHook = nil })
}

func TestLineHandler_ConcurrentRaiseRefusesStaleUpdate(t *testing.T) {
	cases := []struct {
		name string
		user auth.User
		form func(key string) url.Values
	}{
		{"manager reduction", auth.User{ID: "mgr-cas", Role: "manager"}, func(k string) url.Values { return url.Values{"key": {k}, "qty": {"1"}} }},
		{"cashier same-qty discount", auth.User{ID: "cashier-1", Role: "cashier"}, func(k string) url.Values { return url.Values{"key": {k}, "qty": {"2"}, "discount": {"10"}} }},
		{"cashier plus stepper", auth.User{ID: "cashier-1", Role: "cashier"}, func(k string) url.Values { return url.Values{"key": {k}, "delta": {"1"}} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux, dp := newPOSMuxRealSession(t)
			if _, err := dp.Engine.ScanQty("ABC", 2); err != nil {
				t.Fatal(err)
			}
			key := dp.Engine.Basket().Lines[0].LineKey
			setLineSnapshotTestHook(t, func() {
				if err := dp.Engine.UpdateLineByKey(key, 3, 0); err != nil {
					t.Error(err)
				}
			})
			rec := postForm(mux, "/api/pos/line", tc.form(key), &tc.user)
			if rec.Code != http.StatusConflict {
				t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "data-line-changed") {
				t.Fatalf("body lacks the line-changed note: %s", rec.Body.String())
			}
			l := dp.Engine.Basket().Lines[0]
			if l.Qty != 3 || !l.LineDiscount.IsZero() {
				t.Fatalf("line must stay as the concurrent writer left it, got qty %v disc %v", l.Qty, l.LineDiscount)
			}
			var n int
			if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM shrinkage_events`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("shrinkage rows = %d (%v), want 0", n, err)
			}
			if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type='pos_line'`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("audit rows = %d (%v), want 0", n, err)
			}
		})
	}
}

// The line was removed on another screen: the 409 still names the problem
// (basket-level note), the edit does not silently vanish (UX #5417).
func TestLineHandler_RemovedLineShowsBasketLevelNote(t *testing.T) {
	mux, dp := newPOSMuxRealSession(t)
	if _, err := dp.Engine.ScanQty("ABC", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Engine.ScanQty("DEF", 1); err != nil {
		t.Logf("second item unavailable: %v", err)
	}
	key := dp.Engine.Basket().Lines[0].LineKey
	setLineSnapshotTestHook(t, func() { dp.Engine.RemoveLine(key) })
	rec := postForm(mux, "/api/pos/line", url.Values{"key": {key}, "qty": {"3"}}, &auth.User{ID: "cashier-1", Role: "cashier"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "data-line-changed") || !strings.Contains(body, `role="alert"`) {
		t.Fatalf("no visible note for a removed line: %s", body)
	}
}

func TestLineHandler_ElevationPromptResyncsAfterSettle(t *testing.T) {
	mux, key, _, _ := seedPricedLine(t, 3)
	rec := postForm(mux, "/api/pos/line", url.Values{"key": {key}, "qty": {"1"}}, &auth.User{ID: "cashier-1", Role: "cashier"})
	if !isElevationPrompt(rec) {
		t.Fatalf("want prompt, got %d", rec.Code)
	}
	if got := rec.Header().Get("HX-Trigger-After-Settle"); got != "ut-basket-resync" {
		t.Fatalf("HX-Trigger-After-Settle = %q", got)
	}
	if got := rec.Header().Get("HX-Trigger"); got != "" {
		t.Fatalf("HX-Trigger must not be set, got %q", got)
	}
}

var discInputTagRE = regexp.MustCompile(`<input class="disc-input"[^>]*>`)

func seedWeighedLine(t *testing.T) (*http.ServeMux, *common.Deps, string) {
	t.Helper()
	mux, dp := newPOSMuxRealSession(t)
	dp.Engine.AddLineWithModifiers(pos.BasketLine{SKU: "W", ItemID: "w", Name: "Cheese", PriceCents: 1000, IsWeighed: true}, 0.125, nil)
	return mux, dp, dp.Engine.Basket().Lines[0].LineKey
}

// ut-docs#5417 finding 6: the discount box re-posted the row's 2-dp qty
// ("0.12" for 0.125), which read as a reduction.
func TestLineHandler_WeighedDiscountEditIsNotReduction(t *testing.T) {
	mux, dp, key := seedWeighedLine(t)
	cashier := &auth.User{ID: "cashier-1", Role: "cashier"}

	rec := postForm(mux, "/api/pos/line", url.Values{"key": {key}, "discount": {"10"}}, cashier)
	if rec.Code != http.StatusOK || isElevationPrompt(rec) {
		t.Fatalf("discount edit: code %d prompt %v", rec.Code, isElevationPrompt(rec))
	}
	l := dp.Engine.Basket().Lines[0]
	if l.Qty != 0.125 || l.LineDiscount != money.FromMinor(10) {
		t.Fatalf("qty %v disc %v", l.Qty, l.LineDiscount)
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM shrinkage_events`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("shrinkage = %d", n)
	}
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type='pos_line'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("audit = %d", n)
	}

	// The rendered discount box must not include the row (and so not qty).
	tag := discInputTagRE.FindString(rec.Body.String())
	if tag == "" {
		t.Fatalf("no disc-input in basket: %s", rec.Body.String())
	}
	if strings.Contains(tag, "hx-include") {
		t.Fatalf("disc-input must not hx-include the row: %s", tag)
	}
}

// Documents why the include went: the main-era field set is a reduction.
func TestLineHandler_WeighedTwoDecimalQtyPostIsGated(t *testing.T) {
	mux, _, key := seedWeighedLine(t)
	rec := postForm(mux, "/api/pos/line", url.Values{"key": {key}, "qty": {"0.12"}, "discount": {"10"}}, &auth.User{ID: "cashier-1", Role: "cashier"})
	if !isElevationPrompt(rec) {
		t.Fatalf("want prompt, got %d", rec.Code)
	}
}

func TestLineHandler_WeighedStepperAndTypedQty(t *testing.T) {
	mux, dp, key := seedWeighedLine(t)
	cashier := &auth.User{ID: "cashier-1", Role: "cashier"}
	if rec := postForm(mux, "/api/pos/line", url.Values{"key": {key}, "delta": {"1"}, "discount": {"10"}}, cashier); rec.Code != http.StatusOK {
		t.Fatalf("stepper: %d", rec.Code)
	}
	l := dp.Engine.Basket().Lines[0]
	if l.Qty != 1.125 || l.LineDiscount != money.FromMinor(10) {
		t.Fatalf("stepper: qty %v disc %v", l.Qty, l.LineDiscount)
	}
	if rec := postForm(mux, "/api/pos/line", url.Values{"key": {key}, "qty": {"0.1"}}, cashier); !isElevationPrompt(rec) {
		t.Fatalf("typed lower qty must prompt, got %d", rec.Code)
	}
}

// AC3: 8 clients hammer one line. Whatever interleaving happens, no
// decrease may go unrecorded: final == 5 + successful raises - shrinkage.
func TestLineHandler_RaceNoUngatedReduction(t *testing.T) {
	mux, dp := newPOSMuxRealSession(t)
	if _, err := dp.Engine.ScanQty("ABC", 5); err != nil {
		t.Fatal(err)
	}
	key := dp.Engine.Basket().Lines[0].LineKey
	cashier := &auth.User{ID: "cashier-1", Role: "cashier"}

	var raises int64
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				var form url.Values
				if (g+i)%2 == 0 {
					form = url.Values{"key": {key}, "delta": {"1"}}
				} else {
					cur := dp.Engine.Basket().Lines[0].Qty
					form = url.Values{"key": {key}, "qty": {strconv.FormatFloat(cur, 'f', -1, 64)}, "discount": {"1"}}
				}
				rec := postForm(mux, "/api/pos/line", form, cashier)
				if rec.Code == http.StatusOK && !isElevationPrompt(rec) && form.Get("delta") != "" {
					atomic.AddInt64(&raises, 1)
				}
			}
		}(g)
	}
	wg.Wait()

	var lost float64
	if err := dp.Db.QueryRow(`SELECT COALESCE(SUM(quantity),0) FROM shrinkage_events`).Scan(&lost); err != nil {
		t.Fatal(err)
	}
	final := dp.Engine.Basket().Lines[0].Qty
	want := 5 + float64(atomic.LoadInt64(&raises)) - lost
	if final != want {
		t.Fatalf("final qty %v, want %v (5 + %d raises - %v recorded): an unrecorded change happened", final, want, raises, lost)
	}
	if lost != 0 {
		t.Fatalf("cashier is denied void_comp_waste, shrinkage must be 0, got %v", lost)
	}
}

package pages

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#5411 / universal-till#2191: /api/pos/line (the − stepper, the qty
// field, a bare qty=0) removed or shrank a priced line with no
// void_comp_waste check, bypassing the manager-PIN gate /api/pos/remove
// enforces. Every reduction of a priced line's quantity is a partial or
// full void and must clear the same gate server-side.

func seedPricedLine(t *testing.T, qty float64) (*http.ServeMux, string, func() []pos.BasketLine, func() int) {
	t.Helper()
	mux, dp := newPOSMuxRealSession(t)
	if _, err := dp.Engine.ScanQty("ABC", qty); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	key := dp.Engine.Basket().Lines[0].LineKey
	lines := func() []pos.BasketLine { return dp.Engine.Basket().Lines }
	shrink := func() int {
		var n int
		if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM shrinkage_events`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	return mux, key, lines, shrink
}

func TestLineHandler_CashierReductionNeedsManagerPIN(t *testing.T) {
	cashier := auth.User{ID: "cashier-1", Role: "cashier"}
	cases := []struct {
		name  string
		start float64
		form  func(key string) url.Values
	}{
		{"minus on qty 1", 1, func(k string) url.Values { return url.Values{"key": {k}, "delta": {"-1"}} }},
		{"minus on qty 3", 3, func(k string) url.Values { return url.Values{"key": {k}, "delta": {"-1"}} }},
		{"qty field to 0", 2, func(k string) url.Values { return url.Values{"key": {k}, "qty": {"0"}} }},
		{"qty field 3 to 1", 3, func(k string) url.Values { return url.Values{"key": {k}, "qty": {"1"}} }},
		{"huge negative delta", 2, func(k string) url.Values { return url.Values{"key": {k}, "delta": {"-99"}} }},
		{"code-addressed qty 0", 2, func(string) url.Values { return url.Values{"code": {"ABC"}, "qty": {"0"}} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux, key, lines, shrink := seedPricedLine(t, tc.start)
			rec := postForm(mux, "/api/pos/line", tc.form(key), &cashier)
			if !isElevationPrompt(rec) {
				t.Fatalf("want elevation prompt, got %d: %s", rec.Code, rec.Body.String())
			}
			ls := lines()
			if len(ls) != 1 || ls[0].Qty != tc.start {
				t.Fatalf("line must be untouched pending approval, got %+v", ls)
			}
			if shrink() != 0 {
				t.Fatal("no shrinkage event before approval")
			}
		})
	}
}

func TestLineHandler_CashierWrongPINRefused(t *testing.T) {
	mux, dp := newPOSMuxRealSession(t)
	_, blockedID := newElevationTestPrincipals(t, dp, "mgr-wrong-1", "cashier-wrong-1", "445566")
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	key := dp.Engine.Basket().Lines[0].LineKey
	rec := postForm(mux, "/api/pos/line", url.Values{"key": {key}, "delta": {"-1"}, "override_pin": {"000000"}}, &auth.User{ID: blockedID, Role: "cashier"})
	if !isElevationPrompt(rec) {
		t.Fatalf("wrong PIN: want prompt, got %d", rec.Code)
	}
	if len(dp.Engine.Basket().Lines) != 1 {
		t.Fatal("line must stay on a wrong PIN")
	}
}

func TestLineHandler_ManagerPINApprovesReductionAndAudits(t *testing.T) {
	mux, dp := newPOSMuxRealSession(t)
	mgrID, blockedID := newElevationTestPrincipals(t, dp, "mgr-line-1", "cashier-line-1", "445566")
	if _, err := dp.Engine.ScanQty("ABC", 3); err != nil {
		t.Fatal(err)
	}
	key := dp.Engine.Basket().Lines[0].LineKey

	rec := postForm(mux, "/api/pos/line", url.Values{"key": {key}, "qty": {"1"}, "override_pin": {"445566"}}, &auth.User{ID: blockedID, Role: "cashier"})
	if isElevationPrompt(rec) || rec.Code != http.StatusOK {
		t.Fatalf("approved: got %d prompt=%v", rec.Code, isElevationPrompt(rec))
	}
	if ls := dp.Engine.Basket().Lines; len(ls) != 1 || ls[0].Qty != 1 {
		t.Fatalf("want qty 1 after approved reduction, got %+v", ls)
	}
	var qty float64
	var reason, actor, approver string
	if err := dp.Db.QueryRow(`SELECT reason_category, actor_id, approver_id, quantity FROM shrinkage_events`).Scan(&reason, &actor, &approver, &qty); err != nil {
		t.Fatalf("want a shrinkage row: %v", err)
	}
	if reason != "void" || actor != blockedID || approver != mgrID || qty != 2 {
		t.Fatalf("shrinkage = %s/%s/%s/qty %v, want void/%s/%s/2", reason, actor, approver, qty, blockedID, mgrID)
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type='pos_line' AND blocked_actor_id=?`, blockedID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("want 1 elevated audit row, got %d (%v)", n, err)
	}
}

func TestLineHandler_ManagerReductionNeedsNoPINButIsAudited(t *testing.T) {
	mux, dp := newPOSMuxRealSession(t)
	mgrID, _ := newElevationTestPrincipals(t, dp, "mgr-line-2", "unused-cashier-2", "998877")
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	key := dp.Engine.Basket().Lines[0].LineKey
	rec := postForm(mux, "/api/pos/line", url.Values{"key": {key}, "delta": {"-1"}}, &auth.User{ID: mgrID, Role: "manager"})
	if rec.Code != http.StatusOK || len(dp.Engine.Basket().Lines) != 0 {
		t.Fatalf("manager: code=%d lines=%d", rec.Code, len(dp.Engine.Basket().Lines))
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM shrinkage_events`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("manager reduction must still be recorded, got %d (%v)", n, err)
	}
}

func TestLineHandler_CashierNonReductionsStayFree(t *testing.T) {
	cashier := auth.User{ID: "cashier-1", Role: "cashier"}
	mux, key, lines, shrink := seedPricedLine(t, 2)
	if rec := postForm(mux, "/api/pos/line", url.Values{"key": {key}, "delta": {"1"}}, &cashier); rec.Code != http.StatusOK || isElevationPrompt(rec) {
		t.Fatalf("plus: %d", rec.Code)
	}
	if lines()[0].Qty != 3 {
		t.Fatalf("plus: qty=%v", lines()[0].Qty)
	}
	// same-qty re-post (the discount field posts the row's qty) is no reduction
	if rec := postForm(mux, "/api/pos/line", url.Values{"key": {key}, "qty": {"3"}, "discount": {"10"}}, &cashier); isElevationPrompt(rec) || rec.Code != http.StatusOK {
		t.Fatalf("same-qty discount edit: %d", rec.Code)
	}
	if lines()[0].Qty != 3 || shrink() != 0 {
		t.Fatal("discount edit changed qty or wrote shrinkage")
	}
}

func TestLineHandler_CashierMayReduceZeroValueLine(t *testing.T) {
	mux, dp := newPOSMuxRealSession(t)
	if _, err := dp.Engine.Scan("FREE"); err != nil {
		t.Fatal(err)
	}
	key := dp.Engine.Basket().Lines[0].LineKey
	rec := postForm(mux, "/api/pos/line", url.Values{"key": {key}, "delta": {"-1"}}, &auth.User{ID: "cashier-1", Role: "cashier"})
	if isElevationPrompt(rec) || len(dp.Engine.Basket().Lines) != 0 {
		t.Fatalf("a free line carries no loss: code=%d lines=%d", rec.Code, len(dp.Engine.Basket().Lines))
	}
}

// A blank or unparseable qty used to fall through to 0 and silently void
// the line with no gate at all.
func TestLineHandler_BadQtyNeverVoids(t *testing.T) {
	cashier := auth.User{ID: "cashier-1", Role: "cashier"}
	for _, q := range []string{"", "abc", "-1", "NaN", "Inf"} {
		mux, key, lines, _ := seedPricedLine(t, 2)
		rec := postForm(mux, "/api/pos/line", url.Values{"key": {key}, "qty": {q}}, &cashier)
		if ls := lines(); len(ls) != 1 || ls[0].Qty != 2 {
			t.Fatalf("qty=%q: line must be untouched, got %+v (code %d)", q, ls, rec.Code)
		}
	}
	mux, key, lines, _ := seedPricedLine(t, 2)
	postForm(mux, "/api/pos/line", url.Values{"key": {key}}, &cashier)
	if ls := lines(); len(ls) != 1 || ls[0].Qty != 2 {
		t.Fatalf("no qty at all: line must be untouched, got %+v", ls)
	}
}

// Review finding 1: a reduction worth under half a minor unit must still be
// gated (the unit price is non-zero), or micro-steps drain a line unrecorded.
func TestLineHandler_SubCentReductionStillGated(t *testing.T) {
	mux, key, lines, shrink := seedPricedLine(t, 2)
	rec := postForm(mux, "/api/pos/line", url.Values{"key": {key}, "qty": {"1.996"}}, &auth.User{ID: "cashier-1", Role: "cashier"})
	if !isElevationPrompt(rec) {
		t.Fatalf("sub-cent reduction: want prompt, got %d", rec.Code)
	}
	if lines()[0].Qty != 2 || shrink() != 0 {
		t.Fatal("line changed or shrinkage written without approval")
	}
}

// Review finding 2: a reduction the engine refuses (discount above the new
// line total) changes nothing and must record nothing.
func TestLineHandler_RefusedDiscountRecordsNoShrinkage(t *testing.T) {
	mux, dp := newPOSMuxRealSession(t)
	mgrID, _ := newElevationTestPrincipals(t, dp, "mgr-line-3", "unused-cashier-3", "998877")
	if _, err := dp.Engine.ScanQty("ABC", 3); err != nil {
		t.Fatal(err)
	}
	key := dp.Engine.Basket().Lines[0].LineKey
	postForm(mux, "/api/pos/line", url.Values{"key": {key}, "qty": {"1"}, "discount": {"999999"}}, &auth.User{ID: mgrID, Role: "manager"})
	if dp.Engine.Basket().Lines[0].Qty != 3 {
		t.Fatal("engine should have refused the update")
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM shrinkage_events`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("refused update must not record shrinkage, got %d (%v)", n, err)
	}
}

// Review finding 3: a code several lines share is refused (key required).
func TestLineHandler_AmbiguousCodeRefused(t *testing.T) {
	mux, dp := newPOSMuxRealSession(t)
	dp.Engine.AddLineWithModifiers(pos.BasketLine{SKU: "ABC", Name: "A", PriceCents: 100, NoMerge: true}, 1, nil)
	dp.Engine.AddLineWithModifiers(pos.BasketLine{SKU: "ABC", Name: "A", PriceCents: 100, NoMerge: true}, 1, nil)
	if n := len(dp.Engine.Basket().Lines); n != 2 {
		t.Skipf("could not seed two same-SKU lines (%d)", n)
	}
	rec := postForm(mux, "/api/pos/line", url.Values{"code": {"ABC"}, "qty": {"0"}}, &auth.User{ID: "cashier-1", Role: "cashier"})
	if rec.Code != http.StatusBadRequest || len(dp.Engine.Basket().Lines) != 2 {
		t.Fatalf("want 400 and both lines kept, got %d / %d lines", rec.Code, len(dp.Engine.Basket().Lines))
	}
}

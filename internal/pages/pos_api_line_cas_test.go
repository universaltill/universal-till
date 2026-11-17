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
	"time"

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

// ut-docs#5417 review R1: a key-addressed edit whose line was already gone
// when the handler read the basket gets the same 409 + note, not a silent 200.
func TestLineHandler_AlreadyRemovedLineShowsNote(t *testing.T) {
	mux, dp := newPOSMuxRealSession(t)
	if _, err := dp.Engine.ScanQty("ABC", 2); err != nil {
		t.Fatal(err)
	}
	key := dp.Engine.Basket().Lines[0].LineKey
	dp.Engine.RemoveLine(key)
	rec := postForm(mux, "/api/pos/line", url.Values{"key": {key}, "qty": {"3"}}, &auth.User{ID: "cashier-1", Role: "cashier"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "data-line-changed") {
		t.Fatalf("no visible note for an already-removed line: %s", body)
	}
	if n := len(dp.Engine.Basket().Lines); n != 0 {
		t.Fatalf("edit re-created the line: %d lines", n)
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
	// Yield between the handler's read and its update so the interleavings
	// the CAS must survive actually happen on every run (without it the
	// window is a few instructions wide and an unfixed handler passes about
	// half the time). Scheduling only: the hook changes no basket state.
	setLineSnapshotTestHook(t, func() { time.Sleep(20 * time.Microsecond) })

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

// ut-docs#6113: the elevation prompt for a line reduction summarises "from X
// to Y" but replayed only the absolute target, so if the line changed between
// prompt and approval the approved retry applied a different reduction than
// the approver agreed to. The prompt now replays from_qty and a stale
// approval is refused as a changed line.

var hiddenInputRE = regexp.MustCompile(`<input type="hidden" name="([^"]*)" value="([^"]*)">`)

// promptHiddenFields returns the replay fields of an elevation prompt body.
func promptHiddenFields(t *testing.T, body string) url.Values {
	t.Helper()
	v := url.Values{}
	for _, m := range hiddenInputRE.FindAllStringSubmatch(body, -1) {
		v.Set(m[1], m[2])
	}
	return v
}

func TestLineHandler_ElevationReplaysFromQty(t *testing.T) {
	mux, dp := newPOSMuxRealSession(t)
	if _, err := dp.Engine.ScanQty("ABC", 3); err != nil {
		t.Fatal(err)
	}
	key := dp.Engine.Basket().Lines[0].LineKey
	rec := postForm(mux, "/api/pos/line", url.Values{"key": {key}, "qty": {"1"}}, &auth.User{ID: "cashier-1", Role: "cashier"})
	if !isElevationPrompt(rec) {
		t.Fatalf("want prompt, got %d", rec.Code)
	}
	if got := promptHiddenFields(t, rec.Body.String()).Get("from_qty"); got != "3" {
		t.Fatalf("from_qty = %q, want 3; body: %s", got, rec.Body.String())
	}
}

func TestLineHandler_StaleApprovalRefusedWhenLineChangedSincePrompt(t *testing.T) {
	mux, dp := newPOSMuxRealSession(t)
	_, blockedID := newElevationTestPrincipals(t, dp, "mgr-6113-a", "cashier-6113-a", "445566")
	if _, err := dp.Engine.ScanQty("ABC", 3); err != nil {
		t.Fatal(err)
	}
	key := dp.Engine.Basket().Lines[0].LineKey
	cashier := &auth.User{ID: blockedID, Role: "cashier"}
	rec := postForm(mux, "/api/pos/line", url.Values{"key": {key}, "qty": {"1"}}, cashier)
	if !isElevationPrompt(rec) {
		t.Fatalf("want prompt, got %d", rec.Code)
	}
	retry := promptHiddenFields(t, rec.Body.String())
	// Another screen raises the line 3 -> 5 while the prompt is open.
	if err := dp.Engine.UpdateLineByKey(key, 5, 0); err != nil {
		t.Fatal(err)
	}
	retry.Set("override_pin", "445566")
	rec = postForm(mux, "/api/pos/line", retry, cashier)
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "data-line-changed") {
		t.Fatalf("body lacks the line-changed note: %s", rec.Body.String())
	}
	if rec.Header().Get("HX-Retarget") != "#basket" || rec.Header().Get("HX-Reswap") != "outerHTML" {
		t.Fatalf("retarget headers: %v", rec.Header())
	}
	if q := dp.Engine.Basket().Lines[0].Qty; q != 5 {
		t.Fatalf("line must stay 5, got %v", q)
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM shrinkage_events`); n != 0 {
		t.Fatalf("shrinkage rows = %d, want 0", n)
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM audit_log WHERE entity_type='pos_line'`); n != 0 {
		t.Fatalf("audit rows = %d, want 0", n)
	}
}

func TestLineHandler_MatchingFromQtyApprovalStillApplies(t *testing.T) {
	mux, dp := newPOSMuxRealSession(t)
	_, blockedID := newElevationTestPrincipals(t, dp, "mgr-6113-b", "cashier-6113-b", "445566")
	if _, err := dp.Engine.ScanQty("ABC", 3); err != nil {
		t.Fatal(err)
	}
	key := dp.Engine.Basket().Lines[0].LineKey
	cashier := &auth.User{ID: blockedID, Role: "cashier"}
	rec := postForm(mux, "/api/pos/line", url.Values{"key": {key}, "qty": {"1"}}, cashier)
	if !isElevationPrompt(rec) {
		t.Fatalf("want prompt, got %d", rec.Code)
	}
	retry := promptHiddenFields(t, rec.Body.String())
	retry.Set("override_pin", "445566")
	rec = postForm(mux, "/api/pos/line", retry, cashier)
	if rec.Code != http.StatusOK || isElevationPrompt(rec) {
		t.Fatalf("approved retry: %d prompt=%v", rec.Code, isElevationPrompt(rec))
	}
	if q := dp.Engine.Basket().Lines[0].Qty; q != 1 {
		t.Fatalf("want qty 1, got %v", q)
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM shrinkage_events`); n != 1 {
		t.Fatalf("shrinkage rows = %d, want 1", n)
	}
}

func TestLineHandler_InvalidFromQtyIs400(t *testing.T) {
	for _, bad := range []string{"abc", "NaN", "Inf", "-1"} {
		t.Run(bad, func(t *testing.T) {
			mux, dp := newPOSMuxRealSession(t)
			if _, err := dp.Engine.ScanQty("ABC", 3); err != nil {
				t.Fatal(err)
			}
			key := dp.Engine.Basket().Lines[0].LineKey
			rec := postForm(mux, "/api/pos/line", url.Values{"key": {key}, "qty": {"1"}, "from_qty": {bad}}, &auth.User{ID: "cashier-1", Role: "cashier"})
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid from_qty") {
				t.Fatalf("want 400 invalid from_qty, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// ut-docs#6113 review F2: a stale retry is refused before the PIN check, so
// even a wrong PIN gets the 409, not a re-prompt (and never counts toward
// the PIN lockout).
func TestLineHandler_StaleApprovalWithWrongPINIsConflictNotPrompt(t *testing.T) {
	mux, dp := newPOSMuxRealSession(t)
	_, blockedID := newElevationTestPrincipals(t, dp, "mgr-6113-c", "cashier-6113-c", "445566")
	if _, err := dp.Engine.ScanQty("ABC", 3); err != nil {
		t.Fatal(err)
	}
	key := dp.Engine.Basket().Lines[0].LineKey
	cashier := &auth.User{ID: blockedID, Role: "cashier"}
	retry := promptHiddenFields(t, postForm(mux, "/api/pos/line", url.Values{"key": {key}, "qty": {"1"}}, cashier).Body.String())
	if err := dp.Engine.UpdateLineByKey(key, 5, 0); err != nil {
		t.Fatal(err)
	}
	retry.Set("override_pin", "000000") // wrong: must never reach the PIN check
	rec := postForm(mux, "/api/pos/line", retry, cashier)
	if rec.Code != http.StatusConflict || isElevationPrompt(rec) {
		t.Fatalf("stale retry must 409 before elevation, got %d prompt=%v", rec.Code, isElevationPrompt(rec))
	}
}

// ut-docs#6113 review F1: a code-addressed (no key) approval whose line has
// gone since the prompt still shows the basket-level note on its 409.
func TestLineHandler_StaleCodeAddressedApprovalShowsNote(t *testing.T) {
	mux, dp := newPOSMuxRealSession(t)
	_, blockedID := newElevationTestPrincipals(t, dp, "mgr-6113-d", "cashier-6113-d", "445566")
	if _, err := dp.Engine.ScanQty("ABC", 3); err != nil {
		t.Fatal(err)
	}
	key := dp.Engine.Basket().Lines[0].LineKey
	cashier := &auth.User{ID: blockedID, Role: "cashier"}
	rec := postForm(mux, "/api/pos/line", url.Values{"code": {"ABC"}, "qty": {"1"}}, cashier)
	if !isElevationPrompt(rec) {
		t.Fatalf("want prompt, got %d", rec.Code)
	}
	retry := promptHiddenFields(t, rec.Body.String())
	if retry.Get("key") != "" || retry.Get("code") != "ABC" {
		t.Fatalf("want a code-addressed replay, got %v", retry)
	}
	dp.Engine.RemoveLine(key)
	retry.Set("override_pin", "445566")
	rec = postForm(mux, "/api/pos/line", retry, cashier)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "data-line-changed") {
		t.Fatalf("want 409 with the note, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := len(dp.Engine.Basket().Lines); n != 0 {
		t.Fatalf("approval re-created the line: %d lines", n)
	}
}

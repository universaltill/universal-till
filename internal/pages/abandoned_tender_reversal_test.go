package pages

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#6602: a tender abandoned while locked (leg 1 captured, leg 2 of
// unknown outcome) sends its captured legs back.

const (
	twoLegTender      = `{"payments":[{"method":"demopay","amount":60},{"method":"demopay","amount":60}]}`
	toastReturnedText = "Card payments already taken were returned"
	toastCheckText    = "A card payment for this sale may still stand"
)

func firstApprovedThenUnknown(n int) string {
	if n == 1 {
		return `{"provider":"demopay","outcome":"approved","transaction_ref":"ch_TESTREF9876"}`
	}
	return "UNKNOWN"
}

// lockTwoLegTender seeds the plugin, rings ABC and posts the two-leg tender
// so the basket is locked on it.
func lockTwoLegTender(t *testing.T, mux *http.ServeMux, dp *common.Deps, refundErr error) (refunds func() []map[string]any, authIDs func() []string) {
	t.Helper()
	refunds, authIDs = seedCardPluginWithAnswerFunc(t, dp, refundErr, firstApprovedThenUnknown)
	lockAgain(t, mux, dp)
	return refunds, authIDs
}

func lockAgain(t *testing.T, mux *http.ServeMux, dp *common.Deps) {
	t.Helper()
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := postTender(t, mux, twoLegTender)
	if !strings.Contains(rec.Body.String(), unknownOutcomeToast) || dp.Engine.TenderLock() == "" {
		t.Fatalf("setup: tender must be locked: %s", rec.Body.String())
	}
}

func abandonPost(t *testing.T, mux *http.ServeMux, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func wantOneReversal(t *testing.T, refunds func() []map[string]any, authIDs func() []string) {
	t.Helper()
	got := refunds()
	if len(got) != 1 {
		t.Fatalf("want exactly one reversal, got %+v", got)
	}
	r := got[0]
	if amt, _ := r["amount"].(float64); int64(amt) != 60 || r["reversal"] != true || r["authorize_request_id"] != authIDs()[0] {
		t.Fatalf("bad reversal payload: %+v", r)
	}
	op, _ := r["original_payments"].([]any)
	if len(op) != 1 || op[0].(map[string]any)["transaction_ref"] != "ch_TESTREF9876" {
		t.Fatalf("reversal must carry the transaction ref: %+v", r)
	}
}

func TestAbandon_ResetReversesCapturedLeg(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	refunds, authIDs := lockTwoLegTender(t, mux, dp, nil)
	rec := abandonPost(t, mux, "/api/pos/reset", nil)
	wantOneReversal(t, refunds, authIDs)
	if len(dp.Engine.Basket().Lines) != 0 || dp.Engine.TenderLock() != "" {
		t.Fatalf("basket must be empty and unlocked")
	}
	for _, r := range refunds() {
		if r["authorize_request_id"] == authIDs()[1] {
			t.Fatalf("the unknown leg must never be refunded")
		}
	}
	if !strings.Contains(rec.Body.String(), toastReturnedText) {
		t.Fatalf("want the returned/unknown toast: %s", rec.Body.String())
	}
}

func TestAbandon_ResetReparkOfResumedOrderReverses(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	registerHoldAPI(mux, dp)
	refunds, authIDs := seedCardPluginWithAnswerFunc(t, dp, nil, firstApprovedThenUnknown)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	abandonPost(t, mux, "/api/pos/hold", url.Values{})
	id := heldID(t, dp)
	abandonPost(t, mux, "/api/pos/resume", url.Values{"id": {id}})
	if dp.Engine.HeldOrigin().IsZero() {
		t.Fatal("setup: basket must be a resumed order")
	}
	rec := postTender(t, mux, twoLegTender)
	if !strings.Contains(rec.Body.String(), unknownOutcomeToast) {
		t.Fatalf("setup: %s", rec.Body.String())
	}
	rec = abandonPost(t, mux, "/api/pos/reset", nil)
	wantOneReversal(t, refunds, authIDs)
	if n := countRows(t, dp, "SELECT COUNT(*) FROM held_sales"); n != 1 {
		t.Fatalf("order must be re-parked, held rows=%d", n)
	}
	if !strings.Contains(rec.Body.String(), toastReturnedText) {
		t.Fatalf("want abandon toast: %s", rec.Body.String())
	}
}

func heldID(t *testing.T, dp *common.Deps) string {
	t.Helper()
	var id string
	if err := dp.Db.QueryRow(`SELECT id FROM held_sales ORDER BY rowid LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestAbandon_HoldReverses(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	registerHoldAPI(mux, dp)
	refunds, authIDs := lockTwoLegTender(t, mux, dp, nil)
	rec := abandonPost(t, mux, "/api/pos/hold", url.Values{})
	wantOneReversal(t, refunds, authIDs)
	if n := countRows(t, dp, "SELECT COUNT(*) FROM held_sales"); n != 1 {
		t.Fatalf("held rows=%d", n)
	}
	if dp.Engine.TenderLock() != "" {
		t.Fatalf("engine must be unlocked")
	}
	if !strings.Contains(rec.Body.String(), toastReturnedText) {
		t.Fatalf("want abandon toast: %s", rec.Body.String())
	}
}

func TestAbandon_ResumeOtherOrderReverses(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	registerHoldAPI(mux, dp)
	refunds, authIDs := seedCardPluginWithAnswerFunc(t, dp, nil, firstApprovedThenUnknown)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	abandonPost(t, mux, "/api/pos/hold", url.Values{})
	h1 := heldID(t, dp)
	lockAgain(t, mux, dp)
	rec := abandonPost(t, mux, "/api/pos/resume", url.Values{"id": {h1}})
	wantOneReversal(t, refunds, authIDs)
	if !strings.Contains(rec.Body.String(), toastReturnedText) {
		t.Fatalf("want abandon toast: %s", rec.Body.String())
	}
	// H1 is live now; the auto-parked locked basket is the one held row.
	if heldID(t, dp) == h1 || countRows(t, dp, "SELECT COUNT(*) FROM held_sales") != 1 || dp.Engine.HeldOrigin().ID != h1 {
		t.Fatalf("want H1 live and the auto-parked basket held")
	}
}

func TestAbandon_OpenOrdersResumeReverses(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	registerHoldAPI(mux, dp)
	registerOpenOrders(mux, dp)
	refunds, authIDs := seedCardPluginWithAnswerFunc(t, dp, nil, firstApprovedThenUnknown)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	abandonPost(t, mux, "/api/pos/hold", url.Values{})
	h1 := heldID(t, dp)
	lockAgain(t, mux, dp)
	rec := abandonPost(t, mux, "/open-orders/resume", url.Values{"id": {h1}})
	wantOneReversal(t, refunds, authIDs)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "warn=pos.toast.tender_abandoned_returned_unknown") ||
		strings.Contains(rec.Header().Get("Location"), "msg=") {
		t.Fatalf("want 303 with the abandon warn (error level, not msg), got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// D1: the abandon warning from /open-orders is carried at error level and the
// sale screen renders it as a persistent role=alert notice, not the green
// auto-fading success banner.
func TestSaleScreen_WarnParamRendersErrorNotice(t *testing.T) {
	mux, _ := quickPayTestMux(t)
	get := func(target string) string {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		return rec.Body.String()
	}
	body := get("/?warn=pos.toast.tender_abandoned_returned_unknown")
	if !strings.Contains(body, `data-testid="resume-warning-banner"`) ||
		!strings.Contains(body, `class="pos-notice error" role="alert" data-testid="resume-warning-banner"`) ||
		!strings.Contains(body, httpx.T("en", "pos.toast.tender_abandoned_returned_unknown")) {
		t.Fatalf("want an error-level role=alert banner with the text, got: %s", body)
	}
	if strings.Contains(body, "resume-notice-banner") {
		t.Fatalf("warning must not render as the success banner")
	}
	if g := get("/?warn=<b>evil</b>"); strings.Contains(g, "<b>evil</b>") || strings.Contains(g, "evil") {
		t.Fatalf("unknown warn text must not be echoed")
	}
}

func TestAbandon_OpenOrdersCounterOpenWarnsAtErrorLevel(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	registerHoldAPI(mux, dp)
	registerOpenOrders(mux, dp)
	refunds, authIDs := seedCardPluginWithAnswerFunc(t, dp, nil, firstApprovedThenUnknown)
	lockAgain(t, mux, dp)
	legacy, err := data.NewKioskCounterOrdersRepo(dp.Db).Create(context.Background(), data.KioskCounterOrder{
		OrderType: "takeaway", Lines: []data.KioskCounterOrderLine{{Name: "Zzz Unmatched", Qty: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := abandonPost(t, mux, "/open-orders/counter/open", url.Values{"id": {legacy.ID}})
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.Contains(loc, "warn=pos.toast.tender_abandoned_") || strings.Contains(loc, "msg=") {
		t.Fatalf("want 303 with warn=, got %d %q", rec.Code, loc)
	}
	wantOneReversal(t, refunds, authIDs)
}

func TestAbandon_RestoreHeldDirectReverses(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	refunds, authIDs := lockTwoLegTender(t, mux, dp, nil)
	dp.Engine.RestoreHeld(pos.BasketSnapshot{}, pos.HeldOrigin{})
	wantOneReversal(t, refunds, authIDs)
}

func TestAbandon_SuccessfulRetrySendsNoRefund(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	refunds, _ := seedCardPluginWithAnswerFunc(t, dp, nil, func(n int) string {
		if n == 2 {
			return "UNKNOWN"
		}
		return `{"provider":"demopay","outcome":"approved"}`
	})
	lockAgain(t, mux, dp)
	postTender(t, mux, twoLegTender)
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 1 {
		t.Fatalf("retry must complete the sale, got %d", n)
	}
	abandonPost(t, mux, "/api/pos/reset", nil)
	if n := len(refunds()); n != 0 {
		t.Fatalf("a completed sale must never be refunded on reset, got %d", n)
	}
}

func TestAbandon_ReversalFailureRaisesMaskedProblem(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	logging.ResetRecent()
	_, authIDs := lockTwoLegTender(t, mux, dp, errors.New("terminal offline"))
	rec := abandonPost(t, mux, "/api/pos/reset", nil)
	if rec.Code != http.StatusOK || len(dp.Engine.Basket().Lines) != 0 {
		t.Fatalf("reset must still succeed: %d", rec.Code)
	}
	n := 0
	for _, p := range logging.Recent() {
		if !strings.Contains(p.Msg, "reversing captured payment FAILED") {
			continue
		}
		n++
		for _, want := range []string{"demopay", "60", "GBP", authIDs()[0], "9876"} {
			if !strings.Contains(p.Msg, want) {
				t.Fatalf("problem lacks %q: %s", want, p.Msg)
			}
		}
		if strings.Contains(p.Msg, "ch_TESTREF9876") {
			t.Fatalf("full transaction ref leaked: %s", p.Msg)
		}
	}
	if n != 1 {
		t.Fatalf("want one failed-reversal problem, got %d", n)
	}
	if !strings.Contains(rec.Body.String(), toastCheckText) {
		t.Fatalf("want the check-provider toast: %s", rec.Body.String())
	}
}

func TestAbandon_UnknownLegGetsProblemNotRefund(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	logging.ResetRecent()
	refunds, authIDs := lockTwoLegTender(t, mux, dp, nil)
	abandonPost(t, mux, "/api/pos/reset", nil)
	found := false
	for _, p := range logging.Recent() {
		if strings.Contains(p.Msg, "UNKNOWN outcome") && strings.Contains(p.Msg, "demopay") && strings.Contains(p.Msg, "60") && strings.Contains(p.Msg, authIDs()[1]) {
			found = true
		}
	}
	if !found {
		t.Fatalf("want an unknown-outcome problem; recent %+v", logging.Recent())
	}
	for _, r := range refunds() {
		if r["authorize_request_id"] == authIDs()[1] {
			t.Fatalf("unknown leg refunded")
		}
	}
}

func TestAbandon_SingleUnknownLegResetWarnsNoRefund(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	logging.ResetRecent()
	refunds, _ := seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string { return "UNKNOWN" })
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	postTender(t, mux, `{"payments":[{"method":"demopay","amount":120}]}`)
	rec := abandonPost(t, mux, "/api/pos/reset", nil)
	if len(refunds()) != 0 {
		t.Fatalf("no refund for an unknown leg")
	}
	if !strings.Contains(rec.Body.String(), toastCheckText) {
		t.Fatalf("want check-provider toast: %s", rec.Body.String())
	}
	ok := false
	for _, p := range logging.Recent() {
		ok = ok || strings.Contains(p.Msg, "UNKNOWN outcome")
	}
	if !ok {
		t.Fatalf("want a problem item")
	}
}

func TestAbandon_LockedDifferentTenderStillRefused(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	refunds, authIDs := lockTwoLegTender(t, mux, dp, nil)
	before := len(authIDs())
	rec := postTender(t, mux, `{"payments":[{"method":"cash","amount":120}]}`)
	if !strings.Contains(rec.Body.String(), unknownOutcomeToast) || len(refunds()) != 0 || len(authIDs()) != before {
		t.Fatalf("different tender must be refused untouched: %s", rec.Body.String())
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 0 {
		t.Fatalf("sales=%d", n)
	}
}

func TestAbandon_NoLockResetSendsNothing(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	refunds, _ := seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string { return "REALDECLINE" })
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := abandonPost(t, mux, "/api/pos/reset", nil)
	if len(refunds()) != 0 || strings.Contains(rec.Body.String(), toastCheckText) || strings.Contains(rec.Body.String(), toastReturnedText) {
		t.Fatalf("plain reset: no refund, no abandon toast")
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	postTender(t, mux, `{"payments":[{"method":"demopay","amount":120}]}`)
	rec = abandonPost(t, mux, "/api/pos/reset", nil)
	if len(refunds()) != 0 || strings.Contains(rec.Body.String(), toastCheckText) || strings.Contains(rec.Body.String(), toastReturnedText) {
		t.Fatalf("declined tender then reset: no refund, no abandon toast")
	}
}

func TestAbandon_NextBasketGetsNewAttemptID(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	calls := 0
	_, authIDs := seedCardPluginWithAnswerFunc(t, dp, nil, func(n int) string {
		calls = n
		return firstApprovedThenUnknownN(n)
	})
	lockAgain(t, mux, dp)
	abandonPost(t, mux, "/api/pos/reset", nil)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	postTender(t, mux, `{"payments":[{"method":"demopay","amount":120}]}`)
	ids := authIDs()
	if calls < 3 {
		t.Fatalf("expected 3 authorizes, got %v", ids)
	}
	if strings.SplitN(ids[0], ":", 2)[0] == strings.SplitN(ids[2], ":", 2)[0] {
		t.Fatalf("next basket reused the attempt id: %v", ids)
	}
}

func firstApprovedThenUnknownN(n int) string {
	if n == 2 {
		return "UNKNOWN"
	}
	return `{"provider":"demopay","outcome":"approved"}`
}

func TestTender_LockedRetryDeclineReversesEarlierAttemptLegs(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	logging.ResetRecent()
	refunds, authIDs := seedCardPluginWithAnswerFunc(t, dp, nil, func(n int) string {
		switch n {
		case 3:
			return "UNKNOWN"
		case 4:
			return "REALDECLINE"
		}
		return `{"provider":"demopay","outcome":"approved"}`
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	three := `{"payments":[{"method":"demopay","amount":40},{"method":"demopay","amount":40},{"method":"demopay","amount":40}]}`
	postTender(t, mux, three)
	postTender(t, mux, three)
	ids := authIDs()
	got := refunds()
	if len(got) != 2 {
		t.Fatalf("want 2 reversals (legs 0 and 1), got %+v", got)
	}
	seen := map[any]bool{got[0]["authorize_request_id"]: true, got[1]["authorize_request_id"]: true}
	if !seen[ids[0]] || !seen[ids[1]] {
		t.Fatalf("reversals must name legs 0 and 1: %v vs %+v", ids, got)
	}
	found := false
	for _, p := range logging.Recent() {
		found = found || (strings.Contains(p.Msg, "UNKNOWN outcome") && strings.Contains(p.Msg, ids[2]))
	}
	if !found {
		t.Fatalf("want a problem naming the unknown leg %s", ids[2])
	}
}

func TestTender_RetryWithChangedReferenceReversesSupersededCapture(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	refunds, authIDs := seedCardPluginWithAnswerFunc(t, dp, nil, func(n int) string {
		if n == 2 {
			return "UNKNOWN"
		}
		return `{"provider":"demopay","outcome":"approved"}`
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	postTender(t, mux, `{"payments":[{"method":"demopay","amount":60,"reference":"a"},{"method":"demopay","amount":60}]}`)
	postTender(t, mux, `{"payments":[{"method":"demopay","amount":60,"reference":"b"},{"method":"demopay","amount":60}]}`)
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 1 {
		t.Fatalf("want 1 sale, got %d", n)
	}
	got := refunds()
	if len(got) != 1 || got[0]["authorize_request_id"] != authIDs()[0] {
		t.Fatalf("want exactly the superseded leg-0 capture reversed: %+v / %v", got, authIDs())
	}
}

// ut-docs#6805: the superseded earlier capture is reversed even when the
// client leaves during the retry's last authorize (the stale-capture reversal
// must not run on the request ctx).
func TestTender_ClientDisconnectOnRetryStillReversesSupersededCapture(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	refunds, authIDs := seedCardPluginWithAnswerFunc(t, dp, nil, func(n int) string {
		switch n {
		case 2:
			return "UNKNOWN"
		case 4:
			cancel() // the retry's last leg: the client leaves mid-authorize
		}
		return `{"provider":"demopay","outcome":"approved"}`
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	postTender(t, mux, `{"payments":[{"method":"demopay","amount":60,"reference":"a"},{"method":"demopay","amount":60}]}`)
	postTenderCtx(t, mux, ctx, `{"payments":[{"method":"demopay","amount":60,"reference":"b"},{"method":"demopay","amount":60}]}`)
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 1 {
		t.Fatalf("want 1 sale, got %d", n)
	}
	got := refunds()
	if len(got) != 1 || got[0]["authorize_request_id"] != authIDs()[0] {
		t.Fatalf("want exactly the superseded leg-0 capture reversed: %+v / %v", got, authIDs())
	}
}

// seedOKCApprovingWithoutEvidence installs a fiscal-device ("okc") payment
// plugin that approves without a fiscal_device receipt.
func seedOKCApprovingWithoutEvidence(t *testing.T, dp *common.Deps) {
	t.Helper()
	const id = "com.universaltill.tax-tr"
	for _, q := range []string{
		`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, is_deprecated, min_pos_version, api_version, published_at)
		 VALUES ('com.universaltill.tax-tr', '1.0.0', 'Fiscal device', 'okc', 'wasm', 'plugin.wasm', 'https://example.test/tax-tr.wasm', 'deadbeef', 'auth', 'site', '[]', 0, '0.0.0', '1', datetime('now'))`,
		`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES ('com.universaltill.tax-tr', 'Fiscal device', '1.0.0', 'plugin.wasm', 'wasm', 1)`,
		`INSERT INTO plugin_entries (id, plugin_id, key, label, type, trigger_event, is_active) VALUES ('e2', 'com.universaltill.tax-tr', 'okc', 'OKC', 'payment', 'payment.okc.requested', 1)`,
		`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES ('h3', 'com.universaltill.tax-tr', 'payment.okc.authorize', 'handle_authorize', 1)`,
		`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES ('p2', 'com.universaltill.tax-tr', 'events:receive', 1)`,
	} {
		if _, err := dp.Db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	bus := plugins.SharedBus(dp.Db)
	bus.SetEventMode("payment.okc.authorize", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), id, []string{"payment.okc.authorize"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			return json.RawMessage(`{"provider":"okc","outcome":"approved"}`), nil
		}); err != nil {
		t.Fatal(err)
	}
}

func TestTender_FiscalNoReceiptReversesEarlierLeg(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	refunds, authIDs := seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string { return `{"provider":"demopay","outcome":"approved"}` })
	seedOKCApprovingWithoutEvidence(t, dp)
	_ = fiscal.MethodKeyOKC
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	postTender(t, mux, `{"payments":[{"method":"demopay","amount":60},{"method":"okc","amount":60}],"offline":true}`)
	got := refunds()
	if len(got) != 1 || got[0]["authorize_request_id"] != authIDs()[0] {
		t.Fatalf("want the demopay leg reversed once: %+v", got)
	}
	postTender(t, mux, `{"payments":[{"method":"demopay","amount":120}]}`)
	ids := authIDs()
	if strings.SplitN(ids[0], ":", 2)[0] == strings.SplitN(ids[len(ids)-1], ":", 2)[0] {
		t.Fatalf("attempt id must rotate after the refusal: %v", ids)
	}
}

func TestTender_DuplicateVoucherRefusedBeforeAuthorize(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	refunds, authIDs := seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string { return `{"provider":"demopay","outcome":"approved"}` })
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	postTender(t, mux, `{"payments":[{"method":"demopay","amount":60},{"method":"voucher","amount":30,"voucher_id":"V1"},{"method":"voucher","amount":30,"voucher_id":"V1"}]}`)
	if len(authIDs()) != 0 || len(refunds()) != 0 {
		t.Fatalf("duplicate voucher must be refused before any authorize: auth=%v refunds=%v", authIDs(), refunds())
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 0 {
		t.Fatalf("sales=%d", n)
	}
}

// A retry that stays locked (leg 2 unknown again) must keep the captured leg
// (same request id across retries, merged once): reset reverses it once.
func TestAbandon_LockedRetryKeepsEarlierCaptureForReversal(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	registerHoldAPI(mux, dp)
	refunds, authIDs := seedCardPluginWithAnswerFunc(t, dp, nil, func(n int) string {
		if n%2 == 0 {
			return "UNKNOWN"
		}
		return `{"provider":"demopay","outcome":"approved"}`
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	postTender(t, mux, twoLegTender)
	postTender(t, mux, twoLegTender)
	if dp.Engine.TenderLock() == "" {
		t.Fatalf("setup: tender must still be locked after the retry")
	}
	abandonPost(t, mux, "/api/pos/reset", nil)
	got := refunds()
	if len(got) != 1 || got[0]["authorize_request_id"] != authIDs()[0] {
		t.Fatalf("want leg 0 (same request id across the retry) reversed exactly once, got %+v", got)
	}
}

// AC4: on resume-over, the locked basket's captured legs are reversed and the
// other order is then restored, whatever the reversal's outcome.
func TestAbandon_ResumeOverReversalFailureStillRestoresOtherOrder(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	registerHoldAPI(mux, dp)
	_, _ = seedCardPluginWithAnswerFunc(t, dp, errors.New("terminal offline"), firstApprovedThenUnknown)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	abandonPost(t, mux, "/api/pos/hold", url.Values{})
	h1 := heldID(t, dp)
	lockAgain(t, mux, dp)
	rec := abandonPost(t, mux, "/api/pos/resume", url.Values{"id": {h1}})
	if rec.Code != http.StatusOK || dp.Engine.HeldOrigin().ID != h1 || len(dp.Engine.Basket().Lines) == 0 {
		t.Fatalf("other order must be restored even when the reversal fails: %d origin=%q", rec.Code, dp.Engine.HeldOrigin().ID)
	}
	if !strings.Contains(rec.Body.String(), toastCheckText) {
		t.Fatalf("want the check-provider toast: %s", rec.Body.String())
	}
}

// ut-docs#6602 review M1: a cashier abandons the basket while a locked retry
// is still waiting on the reader. The retry must not complete the sale, and
// no leg may be sent back twice.
func runAbandonDuringRetry(t *testing.T, bAnswer string) (refunds func() []map[string]any, authIDs func() []string, dp *common.Deps, retry *httptest.ResponseRecorder) {
	t.Helper()
	mux, dp := newFiscalSignDeps(t)
	started := make(chan struct{})
	release := make(chan struct{})
	refunds, authIDs = seedCardPluginWithAnswerFunc(t, dp, nil, func(n int) string {
		switch n {
		case 2:
			return "UNKNOWN"
		case 4: // the retry's leg B: waits on the reader
			close(started)
			<-release
			return bAnswer
		}
		return `{"provider":"demopay","outcome":"approved","transaction_ref":"ch_TESTREF9876"}`
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	if rec := postTender(t, mux, twoLegTender); !strings.Contains(rec.Body.String(), unknownOutcomeToast) {
		t.Fatalf("setup: %s", rec.Body.String())
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- postTender(t, mux, twoLegTender) }()
	<-started
	abandonPost(t, mux, "/api/pos/reset", nil)
	close(release)
	retry = <-done
	return refunds, authIDs, dp, retry
}

func refundCounts(refunds func() []map[string]any) map[any]int {
	c := map[any]int{}
	for _, r := range refunds() {
		c[r["authorize_request_id"]]++
	}
	return c
}

func TestTender_AbandonedDuringRetry_DeclineReversesLegOnce(t *testing.T) {
	refunds, authIDs, dp, retry := runAbandonDuringRetry(t, "REALDECLINE")
	ids := authIDs()
	if c := refundCounts(refunds); len(c) != 1 || c[ids[0]] != 1 {
		t.Fatalf("leg A must be reversed exactly once, got %v (%+v)", c, refunds())
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 0 {
		t.Fatalf("no sale may be recorded, got %d", n)
	}
	_ = retry
}

func TestTender_AbandonedDuringRetry_ApprovalDoesNotCompleteSale(t *testing.T) {
	refunds, authIDs, dp, retry := runAbandonDuringRetry(t, `{"provider":"demopay","outcome":"approved"}`)
	ids := authIDs()
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 0 {
		t.Fatalf("no sale may be recorded for an abandoned tender, got %d", n)
	}
	c := refundCounts(refunds)
	if len(c) != 2 || c[ids[0]] != 1 || c[ids[1]] != 1 {
		t.Fatalf("leg A (hand-off) and leg B (late approval) must each be reversed exactly once, got %v", c)
	}
	if !strings.Contains(retry.Body.String(), toastCheckText) {
		t.Fatalf("the cashier must be told the payment was not completed: %s", retry.Body.String())
	}
}

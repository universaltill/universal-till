package pages

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/paybroker"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#6653 slice 3 (ADR-0136 Decision 9.6): the manager's list of payment
// legs needing attention, Retry (one explicit re-send of a reverse_failed leg)
// and Resolve.

type legEnv struct {
	*tapEnv
	card *readerPlugin // a non-tap card method "cardpay"
}

func legRig(t *testing.T) *legEnv {
	t.Helper()
	e := tapRig(t)
	return &legEnv{tapEnv: e, card: seedReaderPlugin(t, e.dp, "p.card", "cardpay", false)}
}

// seed journals a leg of method through states (amount 135 = 120 + tip 15).
func (e *legEnv) seed(t *testing.T, id, method string, states ...string) {
	t.Helper()
	for _, s := range states {
		le := data.LegEntry{RequestID: id, State: s, MethodID: method, AmountMinor: 135, Currency: "EUR", TipMinor: 15, ReaderRef: "rdr_1", TransactionRef: "ch_FULLREF9876"}
		if err := legRepo(e.dp).Append(context.Background(), le); err != nil {
			t.Fatalf("seed %s %s: %v", id, s, err)
		}
	}
}

var failedChain = []string{data.LegAsked, data.LegCaptured, data.LegReversing, data.LegReverseFailed}

func (e *legEnv) chain(t *testing.T, id string) []string {
	t.Helper()
	rows, err := e.dp.Db.Query(`SELECT state FROM payment_leg_journal WHERE request_id = ? ORDER BY seq`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func (e *legEnv) last(t *testing.T, id string) data.LegEntry {
	t.Helper()
	got, ok, err := legRepo(e.dp).Latest(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("latest %s: %v %v", id, ok, err)
	}
	return got
}

func (e *legEnv) retry(t *testing.T, u auth.User, id, pin string) *httptest.ResponseRecorder {
	t.Helper()
	f := url.Values{"request_id": {id}}
	if pin != "" {
		f.Set("override_pin", pin)
	}
	return e.do(t, u, http.MethodPost, "/api/pay/legs/retry", f)
}

func (e *legEnv) resolve(t *testing.T, u auth.User, id, pin, note string) *httptest.ResponseRecorder {
	t.Helper()
	f := url.Values{"request_id": {id}, "note": {note}}
	if pin != "" {
		f.Set("override_pin", pin)
	}
	return e.do(t, u, http.MethodPost, "/api/pay/legs/resolve", f)
}

func (e *legEnv) audit(t *testing.T, action string) []map[string]any {
	t.Helper()
	rows, err := e.dp.Db.Query(`SELECT data_json FROM audit_log WHERE action = ? ORDER BY created_at`, action)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		m := map[string]any{}
		_ = json.Unmarshal([]byte(s), &m)
		out = append(out, m)
	}
	return out
}

func (e *legEnv) fragment(t *testing.T, u auth.User) *httptest.ResponseRecorder {
	t.Helper()
	return e.do(t, u, http.MethodGet, "/ui/settings/payment-attention", nil)
}

// wantFmt checks the response carries the English copy of key around its %s.
func wantFmt(t *testing.T, rec *httptest.ResponseRecorder, key string) {
	t.Helper()
	for _, part := range strings.Split(enText(t, key), "%s") {
		if !strings.Contains(bodyText(rec), part) {
			t.Errorf("response lacks %s (%q): %d %s", key, part, rec.Code, rec.Body.String())
		}
	}
}

func (e *legEnv) wantElevationPrompt(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Header().Get("X-UT-Response") != "elevation-prompt" {
		t.Fatalf("want the elevation prompt, got %d %s", rec.Code, rec.Body.String())
	}
}

// AC1
func TestLegAttention_ListManagerOnly(t *testing.T) {
	e := legRig(t)
	e.seed(t, "att:0:fail", "cardpay", failedChain...)
	e.seed(t, "att:0:warn", "cardpay", data.LegAsked, data.LegUnknown, data.LegWarned)
	e.seed(t, "att:0:done", "cardpay", data.LegAsked, data.LegCaptured, data.LegSettled)
	e.seed(t, "att:0:rev", "cardpay", data.LegAsked, data.LegCaptured, data.LegReversing, data.LegReversed)
	e.orphan(t, "orphan-1", "stripe")

	rec := e.do(t, e.manager, http.MethodGet, "/api/pay/attempts/attention", nil)
	var out struct {
		Data struct {
			Attempts  []map[string]any `json:"attempts"`
			Count     int              `json:"count"`
			Legs      []map[string]any `json:"legs"`
			LegsCount int              `json:"legs_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != 200 {
		t.Fatalf("list = %d %s (%v)", rec.Code, rec.Body.String(), err)
	}
	if out.Data.Count != 1 || len(out.Data.Attempts) != 1 {
		t.Fatalf("attempts changed shape: %+v", out.Data)
	}
	if out.Data.LegsCount != 2 || len(out.Data.Legs) != 2 {
		t.Fatalf("legs = %+v, want the failed and the warned leg only", out.Data.Legs)
	}
	l := out.Data.Legs[0]
	if l["request_id"] != "att:0:fail" || l["method"] != "cardpay" || l["amount_minor"] != float64(135) || l["currency"] != "EUR" ||
		l["state"] != data.LegReverseFailed || l["transaction_ref"] != "…9876" || l["at"] == "" {
		t.Fatalf("leg DTO = %+v", l)
	}
	if _, ok := l["sale_id"]; ok {
		t.Fatalf("a leg has no sale id: %+v", l)
	}
	if strings.Contains(rec.Body.String(), "FULLREF") {
		t.Fatalf("the full provider reference leaked: %s", rec.Body.String())
	}
	if out.Data.Legs[1]["request_id"] != "att:0:warn" || out.Data.Legs[1]["state"] != data.LegWarned || out.Data.Legs[1]["reason"] != "" {
		t.Fatalf("second leg = %+v", out.Data.Legs[1])
	}

	frag := e.fragment(t, e.manager)
	body := bodyText(frag)
	if frag.Code != 200 || !strings.Contains(body, `data-testid="pay-attention"`) || !strings.Contains(body, "/api/pay/legs/retry") || !strings.Contains(body, "/api/pay/legs/resolve") ||
		!strings.Contains(body, httpx.FormatMoneyInDisplay(135, "EUR", "en")) || !strings.Contains(body, "9876") || strings.Contains(body, "FULLREF") {
		t.Fatalf("fragment = %d %s", frag.Code, body)
	}
	if strings.Count(body, "/api/pay/legs/retry") != 1 {
		t.Fatalf("only the reverse_failed row has Retry: %s", body)
	}
	e.wantText(t, frag, "pay.attention.title", "pay.attention.state.reverse_failed", "pay.attention.state.warned", "pay.attention.check_first")

	// a cashier: 403 JSON, an empty fragment (no card, no error on Settings)
	rec = e.do(t, e.cashier, http.MethodGet, "/api/pay/attempts/attention", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier list = %d, want 403", rec.Code)
	}
	if frag := e.fragment(t, e.cashier); frag.Code != 200 || strings.TrimSpace(frag.Body.String()) != "" {
		t.Fatalf("cashier fragment = %d %q, want 200 and empty", frag.Code, frag.Body.String())
	}
}

func TestLegAttention_EmptyListIsAnEmptyFragment(t *testing.T) {
	e := legRig(t)
	if frag := e.fragment(t, e.manager); frag.Code != 200 || strings.TrimSpace(frag.Body.String()) != "" {
		t.Fatalf("empty fragment = %d %q", frag.Code, frag.Body.String())
	}
}

// AC6 authz for Retry.
func TestLegRetry_NeedsManagerAndFreshPIN(t *testing.T) {
	e := legRig(t)
	e.seed(t, "att:0:x", "cardpay", failedChain...)
	before := countRows(t, e.dp, "SELECT COUNT(*) FROM payment_leg_journal")

	if rec := e.retry(t, e.cashier, "att:0:x", dataAPITestManagerPIN); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier = %d, want 403", rec.Code)
	}
	e.wantElevationPrompt(t, e.retry(t, e.manager, "att:0:x", ""))
	e.wantElevationPrompt(t, e.retry(t, e.manager, "att:0:x", "000000"))
	if n := len(e.card.refundCalls()); n != 0 {
		t.Fatalf("refunds = %d, want 0", n)
	}
	if after := countRows(t, e.dp, "SELECT COUNT(*) FROM payment_leg_journal"); after != before {
		t.Fatalf("journal rows %d -> %d", before, after)
	}
}

// AC2
func TestLegRetry_SuccessSendsOnceWithCounterKey(t *testing.T) {
	e := legRig(t)
	e.seed(t, "att:0:x", "cardpay", failedChain...)

	rec := e.retry(t, e.manager, "att:0:x", dataAPITestManagerPIN)
	if rec.Code != 200 {
		t.Fatalf("retry = %d %s", rec.Code, rec.Body.String())
	}
	wantFmt(t, rec, "pay.attention.retry_done")
	got := e.card.refundCalls()
	if len(got) != 1 {
		t.Fatalf("refunds = %d, want exactly 1", len(got))
	}
	r := got[0]
	op, _ := r["original_payments"].([]any)
	if r["_event_id"] != "reversal:att:0:x:2" || r["amount"] != float64(135) || r["reversal"] != true || r["authorize_request_id"] != "att:0:x" || r["reader_id"] != "rdr_1" ||
		len(op) != 1 || op[0].(map[string]any)["transaction_ref"] != "ch_FULLREF9876" || op[0].(map[string]any)["tip_amount"] != float64(15) {
		t.Fatalf("retry payload = %+v", r)
	}
	if c := e.chain(t, "att:0:x"); strings.Join(c, ",") != "asked,captured,reversing,reverse_failed,reversing,reversed" {
		t.Fatalf("chain = %v", c)
	}
	frag := e.fragment(t, e.manager)
	if strings.TrimSpace(frag.Body.String()) != "" {
		t.Fatalf("a reversed leg must leave the list: %s", frag.Body.String())
	}
	a := e.audit(t, "payment_leg_retry")
	if len(a) != 1 || a[0]["manager_id"] != e.manager.ID || a[0]["prior_state"] != data.LegReverseFailed || a[0]["attempt"] != float64(2) || a[0]["outcome"] != data.LegReversed ||
		a[0]["amount_minor"] != float64(135) || a[0]["transaction_ref"] != "…9876" {
		t.Fatalf("audit = %+v", a)
	}
}

// AC3
func TestLegRetry_FailureStaysListedNextIsThree(t *testing.T) {
	e := legRig(t)
	e.seed(t, "att:0:x", "cardpay", failedChain...)
	e.card.onRefund = func(context.Context, plugins.Event) error { return errors.New("provider says no") }

	rec := e.retry(t, e.manager, "att:0:x", dataAPITestManagerPIN)
	wantFmt(t, rec, "pay.attention.retry_failed")
	if c := e.chain(t, "att:0:x"); strings.Join(c, ",") != "asked,captured,reversing,reverse_failed,reversing,reverse_failed" {
		t.Fatalf("chain = %v", c)
	}
	if got := e.last(t, "att:0:x"); got.Reason != data.LegReasonReversalFailed {
		t.Fatalf("reason = %q, want reversal_failed", got.Reason)
	}
	if !strings.Contains(e.fragment(t, e.manager).Body.String(), "att:0:x") {
		t.Fatal("a failed retry must stay listed")
	}
	e.retry(t, e.manager, "att:0:x", dataAPITestManagerPIN)
	got := e.card.refundCalls()
	if len(got) != 2 || got[0]["_event_id"] != "reversal:att:0:x:2" || got[1]["_event_id"] != "reversal:att:0:x:3" {
		t.Fatalf("event ids = %+v, want :2 then :3", got)
	}
}

// AC7, main till
func TestLegRetry_TapLegBindsRefundKeyN(t *testing.T) {
	e := legRig(t)
	e.seed(t, "att:0:tap", "tapstripe", failedChain...)
	var bindings []paybroker.Binding
	var mu sync.Mutex
	e.plugin.onRefund = func(ctx context.Context, _ plugins.Event) error {
		if b, ok := paybroker.BindingFrom(ctx); ok {
			mu.Lock()
			bindings = append(bindings, b)
			mu.Unlock()
		}
		return nil
	}
	rec := e.retry(t, e.manager, "att:0:tap", dataAPITestManagerPIN)
	mu.Lock()
	defer mu.Unlock()
	if len(bindings) != 1 || bindings[0].Kind != paybroker.BindRefund || bindings[0].RefundKey != "refund:reversal:att:0:tap:2" || bindings[0].PI != "ch_FULLREF9876" ||
		bindings[0].MaxRefundMinor != 135 {
		t.Fatalf("binding = %+v (%d %s)", bindings, rec.Code, rec.Body.String())
	}
	if got := e.last(t, "att:0:tap"); got.State != data.LegReversed {
		t.Fatalf("state = %s, want reversed", got.State)
	}
}

// AC7, additional till
func TestLegRetry_AdditionalTillTapRefusedMainOnly(t *testing.T) {
	e := legRig(t)
	e.joinTill(t, data.TillRoleAdditional)
	e.seed(t, "att:0:tap", "tapstripe", failedChain...)
	rec := e.retry(t, e.manager, "att:0:tap", dataAPITestManagerPIN)
	if rec.Code != http.StatusConflict {
		t.Fatalf("retry = %d, want 409 %s", rec.Code, rec.Body.String())
	}
	e.wantText(t, rec, "tap.reverse.main_only")
	if n := len(e.plugin.refundCalls()); n != 0 {
		t.Fatalf("an additional till sent %d refunds", n)
	}
	if c := e.chain(t, "att:0:tap"); strings.Join(c, ",") != strings.Join(failedChain, ",") {
		t.Fatalf("chain = %v, want unchanged", c)
	}
}

// AC4
func TestLegRetry_RefusedForWarnedAndSettledStates(t *testing.T) {
	e := legRig(t)
	e.seed(t, "w", "cardpay", data.LegAsked, data.LegUnknown, data.LegWarned)
	e.seed(t, "s", "cardpay", data.LegAsked, data.LegCaptured, data.LegSettled)
	e.seed(t, "c", "cardpay", data.LegAsked, data.LegCaptured)
	e.seed(t, "r", "cardpay", failedChain...)
	e.seed(t, "r", "cardpay", data.LegResolved)
	for _, id := range []string{"w", "s", "c", "r"} {
		before := countRows(t, e.dp, "SELECT COUNT(*) FROM payment_leg_journal")
		rec := e.retry(t, e.manager, id, dataAPITestManagerPIN)
		if rec.Code != http.StatusConflict {
			t.Fatalf("retry of %s = %d, want 409", id, rec.Code)
		}
		e.wantText(t, rec, "pay.attention.not_retryable")
		if after := countRows(t, e.dp, "SELECT COUNT(*) FROM payment_leg_journal"); after != before {
			t.Fatalf("a refused retry of %s wrote rows", id)
		}
	}
	if n := len(e.card.refundCalls()); n != 0 {
		t.Fatalf("refunds = %d, want 0 (a warned leg is never re-sent)", n)
	}
	rec := e.retry(t, e.manager, "nope", dataAPITestManagerPIN)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown leg = %d, want 404", rec.Code)
	}
	e.wantText(t, rec, "pay.attention.gone")
	if rec := e.retry(t, e.manager, strings.Repeat("x", 201), dataAPITestManagerPIN); rec.Code != http.StatusBadRequest {
		t.Fatalf("long id = %d, want 400", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("error content type = %q, want text/html", ct)
	}
}

// AC5
func TestLegRetry_ConcurrentPostsSendOnce(t *testing.T) {
	e := legRig(t)
	e.seed(t, "att:0:x", "cardpay", failedChain...)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.card.onRefund = func(context.Context, plugins.Event) error {
		once.Do(func() { close(entered) })
		<-release
		return nil
	}
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- e.retry(t, e.manager, "att:0:x", dataAPITestManagerPIN) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the first retry never reached the provider")
	}
	second := e.retry(t, e.manager, "att:0:x", dataAPITestManagerPIN)
	if second.Code != http.StatusConflict {
		close(release)
		t.Fatalf("second retry = %d, want 409 %s", second.Code, second.Body.String())
	}
	e.wantText(t, second, "pay.attention.in_progress")
	close(release)
	if rec := <-first; rec.Code != 200 {
		t.Fatalf("first retry = %d %s", rec.Code, rec.Body.String())
	}
	if n := len(e.card.refundCalls()); n != 1 {
		t.Fatalf("refunds = %d, want 1", n)
	}
}

// The durable backstop when the in-process guard is lost.
func TestLegRetry_ReversingRowRefusedWithoutGuard(t *testing.T) {
	e := legRig(t)
	e.seed(t, "att:0:x", "cardpay", failedChain...)
	if _, n, err := legRepo(e.dp).BeginRetry(context.Background(), "att:0:x"); err != nil || n != 2 {
		t.Fatalf("first BeginRetry = %d %v", n, err)
	}
	rec := e.retry(t, e.manager, "att:0:x", dataAPITestManagerPIN)
	if rec.Code != http.StatusConflict || len(e.card.refundCalls()) != 0 {
		t.Fatalf("retry with a reversing row = %d, refunds %d, want 409 and 0", rec.Code, len(e.card.refundCalls()))
	}
	if _, _, err := legRepo(e.dp).BeginRetry(context.Background(), "att:0:x"); !errors.Is(err, data.ErrLegJournalTransition) {
		t.Fatalf("second BeginRetry = %v, want ErrLegJournalTransition", err)
	}
}

// F3
func TestLegRetry_NoRefundPluginIsAFailure(t *testing.T) {
	e := legRig(t)
	e.seed(t, "att:0:x", "unknownpay", failedChain...) // no plugin entry for this method
	rec := e.retry(t, e.manager, "att:0:x", dataAPITestManagerPIN)
	wantFmt(t, rec, "pay.attention.retry_failed")
	if got := e.last(t, "att:0:x"); got.State != data.LegReverseFailed || got.Reason != data.LegReasonNoRefundPlugin {
		t.Fatalf("leg = %s/%q, want reverse_failed/no_refund_plugin, never reversed", got.State, got.Reason)
	}
}

// Fail closed: nothing is in flight yet, so a journal that cannot record the
// reversing row means nothing is sent.
func TestLegRetry_JournalWriteFailureSendsNothing(t *testing.T) {
	e := legRig(t)
	e.seed(t, "att:0:x", "cardpay", failedChain...)
	if _, err := e.dp.Db.Exec(`CREATE TRIGGER no_reversing BEFORE INSERT ON payment_leg_journal WHEN NEW.state = 'reversing' BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	rec := e.retry(t, e.manager, "att:0:x", dataAPITestManagerPIN)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("retry = %d, want 500 %s", rec.Code, rec.Body.String())
	}
	if n := len(e.card.refundCalls()); n != 0 {
		t.Fatalf("refunds = %d, want 0 when the journal cannot be written", n)
	}
	if got := e.last(t, "att:0:x"); got.State != data.LegReverseFailed {
		t.Fatalf("state = %s", got.State)
	}
}

// AC9 and #6805: the send runs detached, a hanging provider never stops cash.
func TestLegRetry_HangingProviderDoesNotBlockCashSale(t *testing.T) {
	e := legRig(t)
	e.seed(t, "att:0:x", "cardpay", failedChain...)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.card.onRefund = func(context.Context, plugins.Event) error {
		once.Do(func() { close(entered) })
		<-release
		return nil
	}
	done := make(chan struct{})
	go func() { defer close(done); e.retry(t, e.manager, "att:0:x", dataAPITestManagerPIN) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the retry never reached the provider")
	}
	saleDone := make(chan struct{})
	go func() {
		defer close(saleDone)
		req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(`{"payments":[{"method":"cash","amount":120}]}`))
		req.Header.Set("Content-Type", "application/json")
		e.mux.ServeHTTP(httptest.NewRecorder(), auth.WithUser(req, e.cashier))
	}()
	select {
	case <-saleDone:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("a cash sale was blocked by a hanging retry")
	}
	if e.sales(t) != 1 {
		t.Fatalf("sales = %d, want 1", e.sales(t))
	}
	close(release)
	<-done
}

// The detached context: a dropped connection mid-send still records the outcome.
func TestLegRetry_ClientDisconnectStillRecordsOutcome(t *testing.T) {
	e := legRig(t)
	e.seed(t, "att:0:x", "cardpay", failedChain...)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.card.onRefund = func(context.Context, plugins.Event) error { cancel(); return nil }
	e.doCtx(t, ctx, e.manager, http.MethodPost, "/api/pay/legs/retry", url.Values{"request_id": {"att:0:x"}, "override_pin": {dataAPITestManagerPIN}})
	if got := e.last(t, "att:0:x"); got.State != data.LegReversed {
		t.Fatalf("state = %s, want reversed after the client left", got.State)
	}
	if len(e.audit(t, "payment_leg_retry")) != 1 {
		t.Fatal("the audit row was lost with the connection")
	}
}

// AC6
func TestLegResolve_WarnedAndReverseFailed(t *testing.T) {
	e := legRig(t)
	e.seed(t, "w", "cardpay", data.LegAsked, data.LegUnknown, data.LegWarned)
	e.seed(t, "f", "cardpay", failedChain...)
	e.seed(t, "s", "cardpay", data.LegAsked, data.LegCaptured, data.LegSettled)

	if rec := e.resolve(t, e.cashier, "w", dataAPITestManagerPIN, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier = %d", rec.Code)
	}
	e.wantElevationPrompt(t, e.resolve(t, e.manager, "w", "", ""))
	e.wantElevationPrompt(t, e.resolve(t, e.manager, "w", "000000", ""))
	if got := e.last(t, "w"); got.State != data.LegWarned {
		t.Fatalf("an unauthorised resolve changed the journal: %s", got.State)
	}
	for _, bad := range []string{"two\nlines", strings.Repeat("x", 201), "tab\there"} {
		if rec := e.resolve(t, e.manager, "w", dataAPITestManagerPIN, bad); rec.Code != http.StatusBadRequest {
			t.Fatalf("note %q = %d, want 400", bad, rec.Code)
		}
	}
	if rec := e.resolve(t, e.manager, "s", dataAPITestManagerPIN, ""); rec.Code != http.StatusConflict {
		t.Fatalf("settled = %d, want 409", rec.Code)
	} else {
		e.wantText(t, rec, "pay.attention.not_resolvable")
	}

	rec := e.resolve(t, e.manager, "w", dataAPITestManagerPIN, " refunded in the dashboard ")
	if rec.Code != 200 {
		t.Fatalf("resolve = %d %s", rec.Code, rec.Body.String())
	}
	e.wantText(t, rec, "pay.attention.resolved")
	if rec := e.resolve(t, e.manager, "f", dataAPITestManagerPIN, ""); rec.Code != 200 {
		t.Fatalf("resolve failed leg = %d", rec.Code)
	}
	for _, id := range []string{"w", "f"} {
		if got := e.last(t, id); got.State != data.LegResolved || got.Reason != data.LegReasonManagerResolved {
			t.Fatalf("%s = %s/%q", id, got.State, got.Reason)
		}
	}
	if strings.TrimSpace(e.fragment(t, e.manager).Body.String()) != "" {
		t.Fatal("resolved legs must leave the list")
	}
	if n := len(e.card.refundCalls()); n != 0 {
		t.Fatalf("Resolve sent %d refunds, want 0 (no provider call)", n)
	}
	a := e.audit(t, "payment_leg_resolve")
	if len(a) != 2 || a[0]["manager_id"] != e.manager.ID || a[0]["prior_state"] != data.LegWarned || a[0]["note"] != "refunded in the dashboard" || a[1]["prior_state"] != data.LegReverseFailed {
		t.Fatalf("audit = %+v", a)
	}
	// The free-text note never reaches the journal.
	if n := countRows(t, e.dp, `SELECT COUNT(*) FROM payment_leg_journal WHERE reason LIKE '%dashboard%'`); n != 0 {
		t.Fatalf("the note leaked into the journal")
	}
}

// AC8 (the #6908 regression): a failed reversal from tapUnlock is listed.
func TestTapUnlockFailedReversalIsListed_6908(t *testing.T) {
	e := legRig(t)
	e.splitPending(t)
	refundErrOverrides.Store(e.dp.Db, errors.New("provider down"))
	t.Cleanup(func() { refundErrOverrides.Delete(e.dp.Db) })
	e.broker.setStatus(piStatus("requires_payment_method", 0))
	rec := e.do(t, e.manager, http.MethodPost, "/manager/pay/tap/resolve", url.Values{"decision": {"not_charged"}, "override_pin": {dataAPITestManagerPIN}})
	if rec.Code >= 500 {
		t.Fatalf("resolve = %d %s", rec.Code, rec.Body.String())
	}
	var id string
	if err := e.dp.Db.QueryRow(`SELECT request_id FROM payment_leg_journal WHERE state = 'reverse_failed'`).Scan(&id); err != nil {
		t.Fatalf("no reverse_failed leg after a failed tapUnlock reversal: %v", err)
	}
	if got := e.last(t, id); got.State != data.LegReverseFailed {
		t.Fatalf("state = %s", got.State)
	}
	if body := e.fragment(t, e.manager).Body.String(); !strings.Contains(body, id) {
		t.Fatalf("the leg is not listed: %s", body)
	}
	if body := e.do(t, e.manager, http.MethodGet, "/api/pay/attempts/attention", nil).Body.String(); !strings.Contains(body, id) {
		t.Fatalf("the leg is not in the JSON list: %s", body)
	}
}

// F1: a refused tap reversal on an additional till opens the chain, so the
// failure is a real reverse_failed row and is listed.
func TestReversal_MainOnlyJournalsReversingThenReverseFailed(t *testing.T) {
	e := legRig(t)
	e.joinTill(t, data.TillRoleAdditional)
	e.seed(t, "att:0:tap", "tapstripe", data.LegAsked, data.LegCaptured)
	cl := pos.CapturedLeg{MethodID: "tapstripe", Currency: "EUR", RequestID: "att:0:tap", Amount: money.FromMinor(135), TipAmount: money.FromMinor(15), TransactionRef: "ch_FULLREF9876"}
	if f, m := reversePaymentLegsDetail(context.Background(), e.dp, []pos.CapturedLeg{cl}, "test"); f != 1 || m != 1 {
		t.Fatalf("failed=%d mainOnly=%d, want 1 1", f, m)
	}
	if c := e.chain(t, "att:0:tap"); strings.Join(c, ",") != "asked,captured,reversing,reverse_failed" {
		t.Fatalf("chain = %v", c)
	}
	if got := e.last(t, "att:0:tap"); got.Reason != data.LegReasonMainTillOnly {
		t.Fatalf("reason = %q", got.Reason)
	}
	if n := len(e.plugin.refundCalls()); n != 0 {
		t.Fatalf("sent %d refunds", n)
	}
	if !strings.Contains(e.fragment(t, e.manager).Body.String(), "att:0:tap") {
		t.Fatal("the refused tap reversal is not listed")
	}
}

// F4: the live reversal keeps its key at :1 whatever the journal holds.
func TestReversal_LiveSendKeyStaysOne(t *testing.T) {
	e := legRig(t)
	// a chain that already failed once: a journal-derived counter would say 2
	e.seed(t, "att:0:tap", "tapstripe", failedChain...)
	var key string
	e.plugin.onRefund = func(ctx context.Context, _ plugins.Event) error {
		if b, ok := paybroker.BindingFrom(ctx); ok {
			key = b.RefundKey
		}
		return nil
	}
	cl := pos.CapturedLeg{MethodID: "tapstripe", Currency: "EUR", RequestID: "att:0:tap", Amount: money.FromMinor(135), TransactionRef: "ch_FULLREF9876"}
	if f := reversePaymentLegs(context.Background(), e.dp, []pos.CapturedLeg{cl}, "test"); f != 0 {
		t.Fatalf("failed = %d", f)
	}
	if key != "refund:reversal:att:0:tap:1" {
		t.Fatalf("live key = %q, want refund:reversal:att:0:tap:1", key)
	}
	got := e.plugin.refundCalls()
	if len(got) != 1 || got[0]["_event_id"] != "reversal:att:0:tap" {
		t.Fatalf("live event id = %+v, want reversal:att:0:tap", got)
	}
}

func TestReversal_KeyHelpers(t *testing.T) {
	for _, tc := range []struct {
		n         int
		key, evID string
	}{
		{0, "refund:reversal:x:0", "reversal:x"},
		{1, "refund:reversal:x:1", "reversal:x"},
		{2, "refund:reversal:x:2", "reversal:x:2"},
		{3, "refund:reversal:x:3", "reversal:x:3"},
	} {
		if got := reversalRefundKey("x", tc.n); got != tc.key {
			t.Errorf("reversalRefundKey(x,%d) = %q", tc.n, got)
		}
		if got := reversalEventID("x", tc.n); got != tc.evID {
			t.Errorf("reversalEventID(x,%d) = %q", tc.n, got)
		}
	}
}

func TestDemoMode_LegAttentionRoutesDenied(t *testing.T) {
	for _, p := range []string{"POST /api/pay/legs/retry", "POST /api/pay/legs/resolve"} {
		if !demoDeniedRoutes[p] {
			t.Errorf("%s is not in demoDeniedRoutes", p)
		}
	}
	// The lazy-loaded Settings fragment is read-only and empty on a demo till:
	// denying it left an orphan refusal line on Settings.
	if demoDeniedRoutes["GET /ui/settings/payment-attention"] {
		t.Error("the read-only payment-attention fragment must stay allowed in demo")
	}
}

// S1: Resolve stamps the resolved row now, so a leg warned long ago is not
// swept the moment it is resolved.
func TestLegResolve_OldWarnedLegSurvivesSweep(t *testing.T) {
	e := legRig(t)
	old := time.Now().Add(-100 * 24 * time.Hour)
	for _, s := range []string{data.LegAsked, data.LegUnknown, data.LegWarned} {
		le := data.LegEntry{RequestID: "old", State: s, MethodID: "cardpay", AmountMinor: 135, Currency: "EUR", At: old}
		if err := legRepo(e.dp).Append(context.Background(), le); err != nil {
			t.Fatal(err)
		}
	}
	if rec := e.resolve(t, e.manager, "old", dataAPITestManagerPIN, ""); rec.Code != 200 {
		t.Fatalf("resolve = %d", rec.Code)
	}
	if got := e.last(t, "old"); time.Since(got.At) > time.Hour {
		t.Fatalf("resolved row at = %v, want now", got.At)
	}
	e.seed(t, "other", "cardpay", data.LegAsked) // an asked row runs the sweep
	if len(e.chain(t, "old")) != 4 {
		t.Fatalf("chain swept: %v", e.chain(t, "old"))
	}
}

// N1: the reference tail never cuts a multi-byte character.
func TestLegRefTail_RuneSafe(t *testing.T) {
	if got := legRefTail("ab日本語cd"); got != "本語cd" {
		t.Errorf("tail = %q", got)
	}
	if got := legRefTail("日本語ab"); got != "本語ab" {
		t.Errorf("tail = %q", got)
	}
	if got := legRefTail("日本語a"); got != "" {
		t.Errorf("four runes = %q, want empty", got)
	}
}

// --- ut-docs#7025: one row per tap payment in doubt; either settle closes both ---

// tapOrphan seeds what a restart leaves for a tap payment in doubt: an attempt
// unknown/till_restarted and a journal chain asked -> unknown -> warned under
// the same request id.
func (e *legEnv) tapOrphan(t *testing.T, id string) {
	t.Helper()
	e.orphan(t, id, "stripe")
	e.seed(t, id, "tapstripe", data.LegAsked, data.LegUnknown, data.LegWarned)
}

// resolveTap posts the card's tap-row form: a decision, an optional reason and note.
func (e *legEnv) resolveTap(t *testing.T, u auth.User, id, decision, reason, pin, note string) *httptest.ResponseRecorder {
	t.Helper()
	f := url.Values{"request_id": {id}, "note": {note}}
	if decision != "" {
		f.Set("decision", decision)
	}
	if reason != "" {
		f.Set("reason", reason)
	}
	if pin != "" {
		f.Set("override_pin", pin)
	}
	return e.do(t, u, http.MethodPost, "/api/pay/legs/resolve", f)
}

type legAttnJSON struct {
	Data struct {
		Attempts  []map[string]any `json:"attempts"`
		Count     int              `json:"count"`
		Legs      []map[string]any `json:"legs"`
		LegsCount int              `json:"legs_count"`
	} `json:"data"`
}

func (e *legEnv) listJSON(t *testing.T) legAttnJSON {
	t.Helper()
	rec := e.do(t, e.manager, http.MethodGet, "/api/pay/attempts/attention", nil)
	var out legAttnJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != 200 {
		t.Fatalf("list = %d %s (%v)", rec.Code, rec.Body.String(), err)
	}
	return out
}

func (e *legEnv) wantGateLifted(t *testing.T) {
	t.Helper()
	before := e.bindingCount()
	e.tender(t)
	if e.bindingCount() != before+1 {
		t.Fatal("the tap gate did not admit a new tap on the provider")
	}
}

func (e *legEnv) wantNothingChanged(t *testing.T, id string) {
	t.Helper()
	if a := e.attemptByKey(t, id); a.State != data.AttemptUnknown {
		t.Fatalf("attempt changed: %s", a.State)
	}
	if got := e.last(t, id); got.State != data.LegWarned {
		t.Fatalf("journal changed: %s", got.State)
	}
	if n := len(e.audit(t, "tap_resolve")) + len(e.audit(t, "payment_leg_resolve")); n != 0 {
		t.Fatalf("a refused answer was audited (%d rows)", n)
	}
}

// AC1
func TestLegAttention_TapLegListedOnce_7025(t *testing.T) {
	e := legRig(t)
	e.tapOrphan(t, "tap-1")
	frag := e.fragment(t, e.manager)
	body := frag.Body.String()
	if n := strings.Count(body, `data-testid="pay-attention-row"`); n != 1 {
		t.Fatalf("rows = %d, want 1: %s", n, body)
	}
	for _, want := range []string{`data-testid="pay-attention-tap-form"`, `data-testid="pay-attention-not-charged"`, `data-testid="pay-attention-charged"`, `name="decision"`} {
		if !strings.Contains(body, want) {
			t.Errorf("tap row lacks %s: %s", want, body)
		}
	}
	if strings.Contains(bodyText(frag), enText(t, "pay.attention.resolve")) {
		t.Errorf("a tap row has no decision-less Mark as handled: %s", body)
	}
	l := e.listJSON(t)
	if l.Data.Count != 1 || len(l.Data.Attempts) != 1 || l.Data.Attempts[0]["leg_state"] != data.LegWarned {
		t.Fatalf("attempts = %+v", l.Data)
	}
	if l.Data.LegsCount != 0 || len(l.Data.Legs) != 0 {
		t.Fatalf("the payment is described twice: legs = %+v", l.Data.Legs)
	}
}

// AC1
func TestTapAttentionList_UnmatchedLegAndAttemptBothListed_7025(t *testing.T) {
	e := legRig(t)
	e.seed(t, "card-1", "cardpay", data.LegAsked, data.LegUnknown, data.LegWarned)
	e.orphan(t, "orphan-x", "stripe")
	l := e.listJSON(t)
	if l.Data.Count != 1 || l.Data.LegsCount != 1 || len(l.Data.Legs) != 1 || len(l.Data.Attempts) != 1 {
		t.Fatalf("lists = %+v", l.Data)
	}
	if _, ok := l.Data.Attempts[0]["leg_state"]; ok {
		t.Fatalf("an attempt with no journal leg carries leg_state: %+v", l.Data.Attempts[0])
	}
	body := e.fragment(t, e.manager).Body.String()
	if n := strings.Count(body, `data-testid="pay-attention-row"`); n != 1 || strings.Contains(body, "pay-attention-tap-form") {
		t.Fatalf("the plain leg must stay a plain row: %s", body)
	}
}

// AC2
func TestTapAttentionResolve_ClosesWarnedLeg_7025(t *testing.T) {
	e := legRig(t)
	e.tapOrphan(t, "tap-1")
	e.broker.setStatus(piStatus("requires_payment_method", 0))
	rec := e.resolveAttn(t, e.manager, "tap-1", "not_charged", "", dataAPITestManagerPIN)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"leg_resolved":true`) {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	if a := e.attemptByKey(t, "tap-1"); a.State != data.AttemptResolvedNotCharged {
		t.Fatalf("attempt = %s", a.State)
	}
	if c := e.chain(t, "tap-1"); strings.Join(c, ",") != "asked,unknown,warned,resolved" {
		t.Fatalf("chain = %v", c)
	}
	if strings.TrimSpace(e.fragment(t, e.manager).Body.String()) != "" {
		t.Fatal("the settled payment is still on the card")
	}
	e.wantGateLifted(t)
}

// AC2: no journal leg means no leg_state in the audit (not an empty one).
func TestTapAttentionResolve_AuditOmitsLegStateWithoutLeg_7025(t *testing.T) {
	e := legRig(t)
	e.orphan(t, "tap-1", "stripe")
	e.broker.setStatus(piStatus("requires_payment_method", 0))
	rec := e.resolveAttn(t, e.manager, "tap-1", "not_charged", "", dataAPITestManagerPIN)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"leg_resolved":false`) {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	a := e.audit(t, "tap_resolve")
	if len(a) != 1 {
		t.Fatalf("tap_resolve audit = %+v", a)
	}
	if v, ok := a[0]["leg_state"]; ok {
		t.Fatalf("leg_state = %#v, want absent when no leg was found", v)
	}
}

// AC2: the money decision stands when the journal cannot be written.
func TestTapAttentionResolve_JournalFailureStillSettles_7025(t *testing.T) {
	e := legRig(t)
	e.tapOrphan(t, "tap-1")
	e.broker.setStatus(piStatus("requires_payment_method", 0))
	if _, err := e.dp.Db.Exec(`CREATE TRIGGER no_resolved BEFORE INSERT ON payment_leg_journal WHEN NEW.state = 'resolved' BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	rec := e.resolveAttn(t, e.manager, "tap-1", "not_charged", "", dataAPITestManagerPIN)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"leg_resolved":false`) {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	if a := e.attemptByKey(t, "tap-1"); a.State != data.AttemptResolvedNotCharged {
		t.Fatalf("attempt = %s", a.State)
	}
	if got := e.last(t, "tap-1"); got.State != data.LegWarned {
		t.Fatalf("leg = %s, want still warned", got.State)
	}
	body := e.fragment(t, e.manager).Body.String()
	if n := strings.Count(body, `data-testid="pay-attention-row"`); n != 1 || strings.Contains(body, "pay-attention-tap-form") {
		t.Fatalf("the stale leg must stay listed as a plain row: %s", body)
	}
}

// AC2: the card route's twin: the settle stands when the journal write fails.
func TestLegResolve_TapRowJournalFailureStillSettles_7025(t *testing.T) {
	e := legRig(t)
	e.tapOrphan(t, "tap-1")
	e.broker.setStatus(piStatus("requires_payment_method", 0))
	if _, err := e.dp.Db.Exec(`CREATE TRIGGER no_resolved BEFORE INSERT ON payment_leg_journal WHEN NEW.state = 'resolved' BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	rec := e.resolveTap(t, e.manager, "tap-1", "not_charged", "", dataAPITestManagerPIN, "")
	if rec.Code != 200 {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	e.wantText(t, rec, "pay.attention.journal_failed")
	if a := e.attemptByKey(t, "tap-1"); a.State != data.AttemptResolvedNotCharged {
		t.Fatalf("attempt = %s", a.State)
	}
	if got := e.last(t, "tap-1"); got.State != data.LegWarned {
		t.Fatalf("leg = %s, want still warned", got.State)
	}
	if n := len(e.audit(t, "tap_resolve")); n != 1 {
		t.Fatalf("tap_resolve audits = %d", n)
	}
	body := e.fragment(t, e.manager).Body.String()
	if n := strings.Count(body, `data-testid="pay-attention-row"`); n != 1 || strings.Contains(body, "pay-attention-tap-form") {
		t.Fatalf("the stale leg must stay listed as a plain row: %s", body)
	}
	e.wantGateLifted(t)
}

// AC3
func TestLegResolve_TapRowNotCharged_ClearsGate_7025(t *testing.T) {
	e := legRig(t)
	e.tapOrphan(t, "tap-1")
	e.broker.setStatus(piStatus("requires_payment_method", 0))
	rec := e.resolveTap(t, e.manager, "tap-1", "not_charged", "", dataAPITestManagerPIN, "")
	if rec.Code != 200 {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	e.wantText(t, rec, "pay.attention.tap_resolved.not_charged")
	if a := e.attemptByKey(t, "tap-1"); a.State != data.AttemptResolvedNotCharged {
		t.Fatalf("attempt = %s", a.State)
	}
	if got := e.last(t, "tap-1"); got.State != data.LegResolved {
		t.Fatalf("leg = %s", got.State)
	}
	a := e.audit(t, "tap_resolve")
	if len(a) != 1 || a[0]["context"] != "needs_attention" || a[0]["source"] != "settings_card" || a[0]["leg_state"] != data.LegWarned {
		t.Fatalf("tap_resolve audit = %+v", a)
	}
	if n := len(e.audit(t, "payment_leg_resolve")); n != 0 {
		t.Fatalf("a second audit names the same settle: %d", n)
	}
	if e.broker.count("session_cancel") < 1 {
		t.Fatal("not_charged must cancel at the broker first")
	}
	e.wantGateLifted(t)
}

// AC3
func TestLegResolve_TapRowChargedRefundAtProvider_7025(t *testing.T) {
	e := legRig(t)
	e.tapOrphan(t, "tap-1")
	e.broker.setStatus(piStatus("succeeded", 120))
	rec := e.resolveTap(t, e.manager, "tap-1", "charged_refund_at_provider", "", dataAPITestManagerPIN, "")
	if rec.Code != 200 {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(bodyText(rec), strings.Split(enText(t, "pay.attention.tap_resolved.charged"), "%s")[0]) {
		t.Fatalf("flash missing: %s", rec.Body.String())
	}
	if a := e.attemptByKey(t, "tap-1"); a.State != data.AttemptApproved || a.Reason != data.AttemptReasonChargedNoSale {
		t.Fatalf("attempt = %+v", a)
	}
	if got := e.last(t, "tap-1"); got.State != data.LegResolved {
		t.Fatalf("leg = %s", got.State)
	}
	if e.sales(t) != 0 {
		t.Fatal("a sale was invented")
	}
	e.wantGateLifted(t)
	_, _, conf, err := tapAuthorize(context.Background(), e.dp, "tapstripe", "tap-1", money.FromMinor(120), "gbp", map[string]any{"method": "tapstripe", "amount": int64(120)})
	if err == nil || conf != nil {
		t.Fatalf("a charged_no_sale key confirmed a payment: conf=%v err=%v", conf, err)
	}
}

// AC3: a tap row never settles without an outcome.
func TestLegResolve_TapRowWithoutDecisionRefused_7025(t *testing.T) {
	e := legRig(t)
	e.tapOrphan(t, "tap-1")
	rec := e.resolveTap(t, e.manager, "tap-1", "", "", dataAPITestManagerPIN, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	e.wantText(t, rec, "pay.attention.choose_outcome")
	e.wantNothingChanged(t, "tap-1")
	if !strings.Contains(rec.Body.String(), "pay-attention-tap-form") {
		t.Fatalf("the card must show the decision buttons: %s", rec.Body.String())
	}
	e.wantRefusedForAttention(t, e.tender(t), 0)
}

// AC3: the PIN dialog keeps the choice.
func TestLegResolve_PINDialogCarriesDecision_7025(t *testing.T) {
	e := legRig(t)
	e.tapOrphan(t, "tap-1")
	rec := e.resolveTap(t, e.manager, "tap-1", "not_charged", "customer_left", "", "a note")
	e.wantElevationPrompt(t, rec)
	body := rec.Body.String()
	for _, want := range []string{`name="decision" value="not_charged"`, `name="request_id" value="tap-1"`, `name="reason" value="customer_left"`, `name="note" value="a note"`} {
		if !strings.Contains(body, want) {
			t.Errorf("prompt lacks %s: %s", want, body)
		}
	}
	if !strings.Contains(bodyText(rec), strings.Split(enText(t, "pay.attention.tap_summary.not_charged"), "%s")[2]) {
		t.Errorf("the prompt summary is not the not-charged one: %s", body)
	}
	e.wantNothingChanged(t, "tap-1")
	if e.broker.count("session_status")+e.broker.count("session_cancel") != 0 {
		t.Fatal("broker asked before the PIN was proven")
	}
}

// AC4
func TestLegResolve_TapRowBrokerPaidRefusesNotCharged_7025(t *testing.T) {
	e := legRig(t)
	e.tapOrphan(t, "tap-1")
	e.broker.setStatus(piStatus("succeeded", 120))
	rec := e.resolveTap(t, e.manager, "tap-1", "not_charged", "", dataAPITestManagerPIN, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	e.wantText(t, rec, "pay.attention.tap_says_paid")
	e.wantNothingChanged(t, "tap-1")
	e.wantRefusedForAttention(t, e.tender(t), 0)
}

// AC4 (UX: the card must not say "Approve it instead")
func TestLegResolve_TapRowBrokerNotPaidRefusesCharged_7025(t *testing.T) {
	e := legRig(t)
	e.tapOrphan(t, "tap-1")
	e.broker.setStatus(piStatus("requires_payment_method", 0))
	rec := e.resolveTap(t, e.manager, "tap-1", "charged_refund_at_provider", "", dataAPITestManagerPIN, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	e.wantText(t, rec, "pay.attention.tap_says_not_paid")
	e.wantNothingChanged(t, "tap-1")
}

// AC4
func TestLegResolve_TapRowBrokerUnreachableNeedsReason_7025(t *testing.T) {
	e := legRig(t)
	e.tapOrphan(t, "tap-1")
	e.broker.setStatus("")
	rec := e.resolveTap(t, e.manager, "tap-1", "not_charged", "", dataAPITestManagerPIN, "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	e.wantText(t, rec, "pay.attention.tap_reason_required")
	e.wantNothingChanged(t, "tap-1")
	rec = e.resolveTap(t, e.manager, "tap-1", "not_charged", "customer_left", dataAPITestManagerPIN, "")
	if rec.Code != 200 {
		t.Fatalf("with a reason = %d %s", rec.Code, rec.Body.String())
	}
	if a := e.attemptByKey(t, "tap-1"); a.State != data.AttemptResolvedNotCharged || a.Reason != "manager_override" {
		t.Fatalf("attempt = %+v", a)
	}
	if got := e.last(t, "tap-1"); got.State != data.LegResolved {
		t.Fatalf("leg = %s", got.State)
	}
}

// AC4: a decision or reason outside the set is refused before anything runs.
func TestLegResolve_TapRowBadChoiceRefused_7025(t *testing.T) {
	e := legRig(t)
	e.tapOrphan(t, "tap-1")
	for _, c := range [][2]string{{"approve", ""}, {"not_charged", "mismatch_refund_later"}, {"not_charged", "nope"}} {
		rec := e.resolveTap(t, e.manager, "tap-1", c[0], c[1], dataAPITestManagerPIN, "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%v = %d %s", c, rec.Code, rec.Body.String())
		}
	}
	e.wantNothingChanged(t, "tap-1")
}

// AC4: the attempt was settled elsewhere while the journal write failed.
func TestLegResolve_AttemptSettledElsewhereWithDecision_7025(t *testing.T) {
	e := legRig(t)
	e.tapOrphan(t, "tap-1")
	e.broker.setStatus(piStatus("requires_payment_method", 0))
	if _, err := e.dp.Db.Exec(`CREATE TRIGGER no_resolved BEFORE INSERT ON payment_leg_journal WHEN NEW.state = 'resolved' BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	if rec := e.resolveAttn(t, e.manager, "tap-1", "not_charged", "", dataAPITestManagerPIN); rec.Code != 200 {
		t.Fatalf("json settle = %d %s", rec.Code, rec.Body.String())
	}
	rec := e.resolveTap(t, e.manager, "tap-1", "not_charged", "", dataAPITestManagerPIN, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	e.wantText(t, rec, "pay.attention.attempt_settled")
	if _, err := e.dp.Db.Exec(`DROP TRIGGER no_resolved`); err != nil {
		t.Fatal(err)
	}
	if rec := e.resolve(t, e.manager, "tap-1", dataAPITestManagerPIN, ""); rec.Code != 200 {
		t.Fatalf("plain resolve = %d %s", rec.Code, rec.Body.String())
	}
	if got := e.last(t, "tap-1"); got.State != data.LegResolved {
		t.Fatalf("leg = %s", got.State)
	}
}

// AC5: a decision on a leg with no open attempt is refused; the leg stays.
func TestLegResolve_NonTapIgnoresStrayDecision_7025(t *testing.T) {
	e := legRig(t)
	e.seed(t, "card-1", "cardpay", data.LegAsked, data.LegUnknown, data.LegWarned)
	rec := e.resolveTap(t, e.manager, "card-1", "not_charged", "", dataAPITestManagerPIN, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	e.wantText(t, rec, "pay.attention.attempt_settled")
	if got := e.last(t, "card-1"); got.State != data.LegWarned {
		t.Fatalf("leg = %s", got.State)
	}
}

// AC6
func TestLegResolve_TapRowCashierAndWrongPIN_7025(t *testing.T) {
	e := legRig(t)
	e.tapOrphan(t, "tap-1")
	e.broker.setStatus(piStatus("requires_payment_method", 0))
	if rec := e.resolveTap(t, e.cashier, "tap-1", "not_charged", "", dataAPITestManagerPIN, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier card = %d", rec.Code)
	}
	if rec := e.resolveAttn(t, e.cashier, "tap-1", "not_charged", "", dataAPITestManagerPIN); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier json = %d", rec.Code)
	}
	e.wantElevationPrompt(t, e.resolveTap(t, e.manager, "tap-1", "not_charged", "", "000000", ""))
	if rec := e.resolveAttn(t, e.manager, "tap-1", "not_charged", "", "000000"); rec.Code != http.StatusForbidden {
		t.Fatalf("wrong pin json = %d", rec.Code)
	}
	e.wantNothingChanged(t, "tap-1")
	if n := len(e.broker.log()); n != 0 {
		t.Fatalf("broker got %d calls", n)
	}
}

// AC6
func TestTapLegSettle_ConcurrentBothPaths_7025(t *testing.T) {
	e := legRig(t)
	e.tapOrphan(t, "tap-1")
	e.broker.setStatus(piStatus("requires_payment_method", 0))
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.broker.setOnCall(func(p string) {
		if strings.HasSuffix(p, "/session_status") {
			once.Do(func() { close(entered); <-release })
		}
	})
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- e.resolveAttn(t, e.manager, "tap-1", "not_charged", "", dataAPITestManagerPIN) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the first settle never reached the broker")
	}
	second := e.resolveTap(t, e.manager, "tap-1", "not_charged", "", dataAPITestManagerPIN, "")
	if second.Code != http.StatusConflict {
		close(release)
		t.Fatalf("concurrent card settle = %d, want 409 %s", second.Code, second.Body.String())
	}
	e.wantText(t, second, "pay.attention.in_progress")
	close(release)
	if rec := <-first; rec.Code != 200 {
		t.Fatalf("first settle = %d %s", rec.Code, rec.Body.String())
	}
	if n := countRows(t, e.dp, `SELECT COUNT(*) FROM payment_leg_journal WHERE request_id = 'tap-1' AND state = 'resolved'`); n != 1 {
		t.Fatalf("resolved rows = %d", n)
	}
	if n := len(e.audit(t, "tap_resolve")); n != 1 {
		t.Fatalf("tap_resolve audits = %d", n)
	}

	// Sequential pairs: whichever goes second is refused, never a 500.
	e.tapOrphan(t, "tap-2")
	if rec := e.resolveAttn(t, e.manager, "tap-2", "not_charged", "", dataAPITestManagerPIN); rec.Code != 200 {
		t.Fatalf("json first = %d %s", rec.Code, rec.Body.String())
	}
	rec := e.resolveTap(t, e.manager, "tap-2", "not_charged", "", dataAPITestManagerPIN, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("card second = %d %s", rec.Code, rec.Body.String())
	}
	e.wantText(t, rec, "pay.attention.not_resolvable")
	e.tapOrphan(t, "tap-3")
	if rec := e.resolveTap(t, e.manager, "tap-3", "not_charged", "", dataAPITestManagerPIN, ""); rec.Code != 200 {
		t.Fatalf("card first = %d %s", rec.Code, rec.Body.String())
	}
	rec = e.resolveAttn(t, e.manager, "tap-3", "not_charged", "", dataAPITestManagerPIN)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "settled") {
		t.Fatalf("json second = %d %s", rec.Code, rec.Body.String())
	}
}

// AC6, reverse order: the card route is in the broker call, the JSON route arrives.
func TestTapLegSettle_ConcurrentCardFirstThenJSON_7025(t *testing.T) {
	e := legRig(t)
	e.tapOrphan(t, "tap-1")
	e.broker.setStatus(piStatus("requires_payment_method", 0))
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.broker.setOnCall(func(p string) {
		if strings.HasSuffix(p, "/session_status") {
			once.Do(func() { close(entered); <-release })
		}
	})
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- e.resolveTap(t, e.manager, "tap-1", "not_charged", "", dataAPITestManagerPIN, "") }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the first settle never reached the broker")
	}
	second := e.resolveAttn(t, e.manager, "tap-1", "not_charged", "", dataAPITestManagerPIN)
	if second.Code != http.StatusConflict || !strings.Contains(second.Body.String(), "in_progress") {
		close(release)
		t.Fatalf("concurrent JSON settle = %d, want 409 in_progress %s", second.Code, second.Body.String())
	}
	close(release)
	if rec := <-first; rec.Code != 200 {
		t.Fatalf("first settle = %d %s", rec.Code, rec.Body.String())
	}
	if n := countRows(t, e.dp, `SELECT COUNT(*) FROM payment_leg_journal WHERE request_id = 'tap-1' AND state = 'resolved'`); n != 1 {
		t.Fatalf("resolved rows = %d", n)
	}
	if n := len(e.audit(t, "tap_resolve")); n != 1 {
		t.Fatalf("tap_resolve audits = %d", n)
	}
}

// --- ut-docs#7028: a captured tap leg with no sale at restart ---

const chargedPIID = "pi_3NabcdEF1234"

// capturedTap seeds what a restart leaves for a charged tap with no sale: an
// approved attempt (reason "") and a journal chain asked -> captured under the
// same request id. withAttempt=false leaves out the attempt row.
func (e *legEnv) capturedTap(t *testing.T, id string, withAttempt bool) {
	t.Helper()
	if withAttempt {
		repo := data.NewPaymentAttemptRepo(e.dp.Db)
		if _, err := repo.Begin(context.Background(), data.PaymentAttempt{Key: id, Provider: "stripe", AmountMinor: 120, Currency: "gbp", Deadline: time.Now().Add(2 * time.Minute)}); err != nil {
			t.Fatal(err)
		}
		if err := repo.Mark(context.Background(), id, data.AttemptApproved, data.AttemptUpdate{PIID: chargedPIID, CardBrand: "visa", Last4: "4242"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []string{data.LegAsked, data.LegCaptured} {
		le := data.LegEntry{RequestID: id, State: s, MethodID: "tapstripe", AmountMinor: 135, Currency: "GBP", TipMinor: 15, ReaderRef: "rdr_1", TransactionRef: chargedPIID}
		if err := legRepo(e.dp).Append(context.Background(), le); err != nil {
			t.Fatal(err)
		}
	}
}

func (e *legEnv) refundCount() int {
	e.card.mu.Lock()
	defer e.card.mu.Unlock()
	e.plugin.mu.Lock()
	defer e.plugin.mu.Unlock()
	return len(e.card.refunds) + len(e.plugin.refunds)
}

// bootCapturedTap seeds a captured tap leg and runs the boot pass.
func (e *legEnv) bootCapturedTap(t *testing.T, id string, withAttempt bool) LegReconcileResult {
	t.Helper()
	e.capturedTap(t, id, withAttempt)
	return ReconcilePaymentLegs(context.Background(), e.dp, time.Now().Add(time.Minute))
}

func (e *legEnv) wantChargedNoSaleListed(t *testing.T, id string) {
	t.Helper()
	got := e.last(t, id)
	if got.State != data.LegWarned || got.Reason != data.LegReasonTapChargedNoSale {
		t.Fatalf("leg = %s/%q, want warned/tap_charged_no_sale", got.State, got.Reason)
	}
}

func TestLegReconcile_CapturedTapIsWarnedNotRefunded_7028(t *testing.T) {
	e := legRig(t)
	e.capturedTap(t, "tap-1", true)
	attBefore := e.attemptByKey(t, "tap-1")
	cutoff := time.Now().Add(time.Minute)
	res := ReconcilePaymentLegs(context.Background(), e.dp, cutoff)
	if res.TapChargedNoSale != 1 || res.TapLeftToManager != 0 || res.Reversed != 0 {
		t.Fatalf("result = %+v, want 1 tap_charged_no_sale", res)
	}
	got := e.last(t, "tap-1")
	if got.State != data.LegWarned || got.Reason != data.LegReasonTapChargedNoSale || got.AmountMinor != 135 || got.TipMinor != 15 ||
		got.Currency != "GBP" || got.ReaderRef != "rdr_1" || got.TransactionRef != chargedPIID || got.MethodID != "tapstripe" {
		t.Fatalf("leg = %+v", got)
	}
	if n := e.refundCount(); n != 0 {
		t.Fatalf("a refund was dispatched: %d", n)
	}
	if attAfter := e.attemptByKey(t, "tap-1"); attAfter != attBefore {
		t.Fatalf("attempt changed: %+v -> %+v", attBefore, attAfter)
	}
	// second boot: nothing
	rows := countRows(t, e.dp, "SELECT COUNT(*) FROM payment_leg_journal")
	if res := ReconcilePaymentLegs(context.Background(), e.dp, cutoff); res != (LegReconcileResult{}) {
		t.Fatalf("second boot = %+v, want zero", res)
	}
	if n := countRows(t, e.dp, "SELECT COUNT(*) FROM payment_leg_journal"); n != rows {
		t.Fatalf("second boot appended %d rows", n-rows)
	}
}

func TestLegReconcile_CapturedTapWithoutAttemptRowIsWarned_7028(t *testing.T) {
	e := legRig(t)
	res := e.bootCapturedTap(t, "tap-1", false)
	if res.TapChargedNoSale != 1 {
		t.Fatalf("result = %+v", res)
	}
	e.wantChargedNoSaleListed(t, "tap-1")
}

func TestLegReconcile_CapturedLegBothReadsFailStaysCaptured_7028(t *testing.T) {
	e := legRig(t)
	e.capturedTap(t, "tap-1", true)
	for _, tbl := range []string{"payment_attempts", "plugin_entries"} {
		if _, err := e.dp.Db.Exec(`DROP TABLE ` + tbl); err != nil {
			t.Fatal(err)
		}
	}
	res := ReconcilePaymentLegs(context.Background(), e.dp, time.Now().Add(time.Minute))
	if res.TapLeftToManager != 1 || res.TapChargedNoSale != 0 || res.Reversed != 0 || res.ReverseFailed != 0 {
		t.Fatalf("result = %+v, want 1 left to manager only", res)
	}
	if got := e.last(t, "tap-1"); got.State != data.LegCaptured {
		t.Fatalf("leg = %s, want captured", got.State)
	}
	if n := e.refundCount(); n != 0 {
		t.Fatalf("refund dispatched: %d", n)
	}
}

func (e *legEnv) dropTable(t *testing.T, tbl string) {
	t.Helper()
	if _, err := e.dp.Db.Exec(`DROP TABLE ` + tbl); err != nil {
		t.Fatal(err)
	}
}

// Single-read failures: never reverse on doubt (ut-docs#7028).
func TestLegReconcile_AttemptUnreadablePluginSaysNotTapStaysCaptured_7028(t *testing.T) {
	e := legRig(t)
	e.seed(t, "leg-1", "cardpay", data.LegAsked, data.LegCaptured)
	e.dropTable(t, "payment_attempts")
	res := ReconcilePaymentLegs(context.Background(), e.dp, time.Now().Add(time.Minute))
	if res.TapLeftToManager != 1 || res.Reversed != 0 || res.ReverseFailed != 0 || res.TapChargedNoSale != 0 {
		t.Fatalf("result = %+v, want 1 left to manager only", res)
	}
	if got := e.last(t, "leg-1"); got.State != data.LegCaptured {
		t.Fatalf("leg = %s, want captured", got.State)
	}
	if n := e.refundCount(); n != 0 {
		t.Fatalf("refund dispatched: %d", n)
	}
}

func TestLegReconcile_AttemptAbsentPluginListUnreadableStaysCaptured_7028(t *testing.T) {
	e := legRig(t)
	e.capturedTap(t, "tap-1", false)
	e.dropTable(t, "plugin_entries")
	res := ReconcilePaymentLegs(context.Background(), e.dp, time.Now().Add(time.Minute))
	if res.TapLeftToManager != 1 || res.Reversed != 0 || res.ReverseFailed != 0 || res.TapChargedNoSale != 0 {
		t.Fatalf("result = %+v, want 1 left to manager only", res)
	}
	if got := e.last(t, "tap-1"); got.State != data.LegCaptured {
		t.Fatalf("leg = %s, want captured", got.State)
	}
	if n := e.refundCount(); n != 0 {
		t.Fatalf("refund dispatched: %d", n)
	}
}

func TestLegReconcile_AttemptUnreadablePluginSaysTapIsWarned_7028(t *testing.T) {
	e := legRig(t)
	e.capturedTap(t, "tap-1", false)
	e.dropTable(t, "payment_attempts")
	res := ReconcilePaymentLegs(context.Background(), e.dp, time.Now().Add(time.Minute))
	if res.TapChargedNoSale != 1 || res.TapLeftToManager != 0 || res.Reversed != 0 {
		t.Fatalf("result = %+v, want 1 tap_charged_no_sale", res)
	}
	e.wantChargedNoSaleListed(t, "tap-1")
}

func TestChargedNoSale_BootToCardToSettle_7028(t *testing.T) {
	e := legRig(t)
	e.bootCapturedTap(t, "tap-1", true)

	frag := e.fragment(t, e.manager)
	body := frag.Body.String()
	if n := strings.Count(body, `data-testid="pay-attention-row"`); n != 1 {
		t.Fatalf("rows = %d: %s", n, body)
	}
	e.wantText(t, frag, "pay.attention.state.charged_no_sale", "pay.attention.explain.charged_no_sale", "pay.attention.tap_charged")
	if !strings.Contains(bodyText(frag), "1234") {
		t.Errorf("reference tail missing: %s", body)
	}
	for _, bad := range []string{`data-testid="pay-attention-not-charged"`, `name="reason"`, `type="radio"`} {
		if strings.Contains(body, bad) {
			t.Errorf("card has %s: %s", bad, body)
		}
	}
	if !strings.Contains(body, `name="decision" value="charged_refund_at_provider"`) {
		t.Errorf("card lacks the single charged button: %s", body)
	}
	l := e.listJSON(t)
	if l.Data.LegsCount != 1 || len(l.Data.Legs) != 1 || l.Data.Legs[0]["reason"] != data.LegReasonTapChargedNoSale || l.Data.Legs[0]["state"] != data.LegWarned || len(l.Data.Attempts) != 0 {
		t.Fatalf("json = %+v", l.Data)
	}

	rec := e.resolveTap(t, e.manager, "tap-1", "charged_refund_at_provider", "", dataAPITestManagerPIN, "refunded in dashboard")
	if rec.Code != 200 {
		t.Fatalf("settle = %d %s", rec.Code, rec.Body.String())
	}
	wantFmt(t, rec, "pay.attention.charged_no_sale.resolved")
	if got := e.last(t, "tap-1"); got.State != data.LegResolved {
		t.Fatalf("leg = %s", got.State)
	}
	if a := e.attemptByKey(t, "tap-1"); a.State != data.AttemptApproved || a.Reason != data.AttemptReasonChargedNoSale {
		t.Fatalf("attempt = %+v", a)
	}
	audits := e.audit(t, "tap_resolve")
	if len(audits) != 1 {
		t.Fatalf("tap_resolve audits = %d", len(audits))
	}
	a := audits[0]
	if a["decision"] != "charged_refund_at_provider" || a["outcome"] != "charged_no_sale" || a["mode"] != "core_confirmed" || a["context"] != "captured_no_sale" ||
		a["source"] != "settings_card" || a["leg_state"] != data.LegWarned || a["attempt_state"] != data.AttemptApproved || a["note"] != "refunded in dashboard" || a["stamped"] != true {
		t.Fatalf("audit = %+v", a)
	}
	if n := len(e.broker.log()); n != 0 {
		t.Fatalf("broker called %d times", n)
	}
	if n := e.refundCount(); n != 0 {
		t.Fatalf("refund dispatched: %d", n)
	}
	if strings.TrimSpace(e.fragment(t, e.manager).Body.String()) != "" {
		t.Fatal("settled payment still on the card")
	}
	_, _, conf, err := tapAuthorize(context.Background(), e.dp, "tapstripe", "tap-1", money.FromMinor(120), "gbp", map[string]any{"method": "tapstripe", "amount": int64(120)})
	if err == nil || conf != nil {
		t.Fatalf("the key confirmed a payment: conf=%v err=%v", conf, err)
	}
}

func TestChargedNoSale_NotChargedAndNoDecisionRefused_7028(t *testing.T) {
	e := legRig(t)
	e.bootCapturedTap(t, "tap-1", true)
	rec := e.resolveTap(t, e.manager, "tap-1", "not_charged", "", dataAPITestManagerPIN, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("not_charged = %d %s", rec.Code, rec.Body.String())
	}
	e.wantText(t, rec, "pay.attention.charged_no_sale.not_charged_refused")
	rec = e.resolveTap(t, e.manager, "tap-1", "", "", dataAPITestManagerPIN, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("no decision = %d %s", rec.Code, rec.Body.String())
	}
	e.wantText(t, rec, "pay.attention.charged_no_sale.choose")
	e.wantChargedNoSaleListed(t, "tap-1")
	if a := e.attemptByKey(t, "tap-1"); a.Reason != "" || a.State != data.AttemptApproved {
		t.Fatalf("attempt changed: %+v", a)
	}
	if n := len(e.audit(t, "tap_resolve")) + len(e.audit(t, "payment_leg_resolve")); n != 0 {
		t.Fatalf("refusal audited: %d", n)
	}
}

func TestChargedNoSale_CashierAndWrongPIN_7028(t *testing.T) {
	e := legRig(t)
	e.bootCapturedTap(t, "tap-1", true)
	if rec := e.resolveTap(t, e.cashier, "tap-1", "charged_refund_at_provider", "", dataAPITestManagerPIN, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier = %d", rec.Code)
	}
	e.wantElevationPrompt(t, e.resolveTap(t, e.manager, "tap-1", "charged_refund_at_provider", "", "000000", ""))
	e.wantElevationPrompt(t, e.resolveTap(t, e.manager, "tap-1", "charged_refund_at_provider", "", "", ""))
	e.wantChargedNoSaleListed(t, "tap-1")
	if a := e.attemptByKey(t, "tap-1"); a.Reason != "" {
		t.Fatalf("attempt stamped: %+v", a)
	}
}

func TestChargedNoSale_PINSummaryIsNotBrokerWording_7028(t *testing.T) {
	e := legRig(t)
	e.bootCapturedTap(t, "tap-1", true)
	rec := e.resolveTap(t, e.manager, "tap-1", "charged_refund_at_provider", "", "", "")
	e.wantElevationPrompt(t, rec)
	wantFmt(t, rec, "pay.attention.charged_no_sale.summary")
	if strings.Contains(bodyText(rec), strings.Split(enText(t, "pay.attention.tap_summary.charged"), "%s")[2]) {
		t.Errorf("the prompt carries the broker-first summary: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `name="decision" value="charged_refund_at_provider"`) {
		t.Errorf("prompt lost the decision: %s", rec.Body.String())
	}
}

func TestChargedNoSale_SecondResolveConflictsOneAudit_7028(t *testing.T) {
	e := legRig(t)
	e.bootCapturedTap(t, "tap-1", true)
	if rec := e.resolveTap(t, e.manager, "tap-1", "charged_refund_at_provider", "", dataAPITestManagerPIN, ""); rec.Code != 200 {
		t.Fatalf("first = %d %s", rec.Code, rec.Body.String())
	}
	rec := e.resolveTap(t, e.manager, "tap-1", "charged_refund_at_provider", "", dataAPITestManagerPIN, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("second = %d %s", rec.Code, rec.Body.String())
	}
	e.wantText(t, rec, "pay.attention.not_resolvable")
	if n := len(e.audit(t, "tap_resolve")); n != 1 {
		t.Fatalf("audits = %d", n)
	}
}

func TestChargedNoSale_ConcurrentPostsOneAudit_7028(t *testing.T) {
	e := legRig(t)
	e.bootCapturedTap(t, "tap-1", true)
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = e.resolveTap(t, e.manager, "tap-1", "charged_refund_at_provider", "", dataAPITestManagerPIN, "").Code
		}()
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		if c == 200 {
			ok++
		} else if c != http.StatusConflict {
			t.Fatalf("codes = %v", codes)
		}
	}
	if ok != 1 {
		t.Fatalf("codes = %v, want exactly one 200", codes)
	}
	if n := len(e.audit(t, "tap_resolve")); n != 1 {
		t.Fatalf("audits = %d", n)
	}
	if n := countRows(t, e.dp, `SELECT COUNT(*) FROM payment_leg_journal WHERE request_id='tap-1' AND state='resolved'`); n != 1 {
		t.Fatalf("resolved rows = %d", n)
	}
}

func TestChargedNoSale_TapGateStaysOpenWhileListed_7028(t *testing.T) {
	e := legRig(t)
	e.bootCapturedTap(t, "tap-1", true)
	e.wantChargedNoSaleListed(t, "tap-1")
	e.wantGateLifted(t)
}

func TestChargedNoSale_AttemptMissingClosesJournalOnly_7028(t *testing.T) {
	e := legRig(t)
	e.bootCapturedTap(t, "tap-1", false)
	rec := e.resolveTap(t, e.manager, "tap-1", "charged_refund_at_provider", "", dataAPITestManagerPIN, "")
	if rec.Code != 200 {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	if got := e.last(t, "tap-1"); got.State != data.LegResolved {
		t.Fatalf("leg = %s", got.State)
	}
	audits := e.audit(t, "tap_resolve")
	if len(audits) != 1 {
		t.Fatalf("audits = %d", len(audits))
	}
	if _, ok := audits[0]["attempt_state"]; ok {
		t.Fatalf("audit has attempt_state with no attempt: %+v", audits[0])
	}
	if audits[0]["stamped"] != false {
		t.Fatalf("stamped = %v", audits[0]["stamped"])
	}
}

func TestChargedNoSale_JournalFailureRetryAuditsOnce_7028(t *testing.T) {
	e := legRig(t)
	e.bootCapturedTap(t, "tap-1", true)
	if _, err := e.dp.Db.Exec(`CREATE TRIGGER no_resolved BEFORE INSERT ON payment_leg_journal WHEN NEW.state = 'resolved' BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	rec := e.resolveTap(t, e.manager, "tap-1", "charged_refund_at_provider", "", dataAPITestManagerPIN, "")
	if rec.Code != 200 {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	e.wantText(t, rec, "pay.attention.charged_no_sale.journal_failed")
	if a := e.attemptByKey(t, "tap-1"); a.Reason != data.AttemptReasonChargedNoSale {
		t.Fatalf("attempt = %+v", a)
	}
	if n := len(e.audit(t, "tap_resolve")); n != 0 {
		t.Fatalf("audited before the journal closed: %d", n)
	}
	if _, err := e.dp.Db.Exec(`DROP TRIGGER no_resolved`); err != nil {
		t.Fatal(err)
	}
	if rec := e.resolveTap(t, e.manager, "tap-1", "charged_refund_at_provider", "", dataAPITestManagerPIN, ""); rec.Code != 200 {
		t.Fatalf("retry = %d %s", rec.Code, rec.Body.String())
	}
	if got := e.last(t, "tap-1"); got.State != data.LegResolved {
		t.Fatalf("leg = %s", got.State)
	}
	if n := len(e.audit(t, "tap_resolve")); n != 1 {
		t.Fatalf("audits = %d, want 1", n)
	}
}

// setAttempt rewrites an attempt's state and reason straight in the table.
func (e *legEnv) setAttempt(t *testing.T, id, state, reason string) {
	t.Helper()
	if _, err := e.dp.Db.Exec(`UPDATE payment_attempts SET state = ?, reason = ? WHERE idempotency_key = ?`, state, reason, id); err != nil {
		t.Fatal(err)
	}
}

func TestChargedNoSale_NonApprovedAttemptStillClosable_7028(t *testing.T) {
	e := legRig(t)
	e.capturedTap(t, "tap-1", true)
	e.setAttempt(t, "tap-1", data.AttemptResolvedNotCharged, "")
	ReconcilePaymentLegs(context.Background(), e.dp, time.Now().Add(time.Minute))
	e.wantChargedNoSaleListed(t, "tap-1")
	before := e.attemptByKey(t, "tap-1")
	rec := e.resolveTap(t, e.manager, "tap-1", "charged_refund_at_provider", "", dataAPITestManagerPIN, "")
	if rec.Code != 200 {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	if got := e.last(t, "tap-1"); got.State != data.LegResolved {
		t.Fatalf("leg = %s", got.State)
	}
	if after := e.attemptByKey(t, "tap-1"); after != before {
		t.Fatalf("attempt changed: %+v -> %+v", before, after)
	}
	audits := e.audit(t, "tap_resolve")
	if len(audits) != 1 || audits[0]["attempt_state"] != data.AttemptResolvedNotCharged || audits[0]["stamped"] != false {
		t.Fatalf("audits = %+v", audits)
	}
}

func TestChargedNoSale_AuditModeFollowsPriorReason_7028(t *testing.T) {
	for _, tc := range []struct{ reason, mode string }{{"manager_override", "manager_override"}, {"", "core_confirmed"}} {
		e := legRig(t)
		e.capturedTap(t, "tap-1", true)
		e.setAttempt(t, "tap-1", data.AttemptApproved, tc.reason)
		ReconcilePaymentLegs(context.Background(), e.dp, time.Now().Add(time.Minute))
		if rec := e.resolveTap(t, e.manager, "tap-1", "charged_refund_at_provider", "", dataAPITestManagerPIN, ""); rec.Code != 200 {
			t.Fatalf("= %d %s", rec.Code, rec.Body.String())
		}
		audits := e.audit(t, "tap_resolve")
		if len(audits) != 1 || audits[0]["mode"] != tc.mode || audits[0]["attempt_reason"] != tc.reason || audits[0]["stamped"] != true {
			t.Fatalf("reason %q: audits = %+v, want mode %s", tc.reason, audits, tc.mode)
		}
	}
}

// After a journal failure the stamp is kept; the retry's audit says so.
func TestChargedNoSale_RetryAuditSaysAlreadyStamped_7028(t *testing.T) {
	e := legRig(t)
	e.bootCapturedTap(t, "tap-1", true)
	if _, err := e.dp.Db.Exec(`CREATE TRIGGER no_resolved BEFORE INSERT ON payment_leg_journal WHEN NEW.state = 'resolved' BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	e.resolveTap(t, e.manager, "tap-1", "charged_refund_at_provider", "", dataAPITestManagerPIN, "")
	if _, err := e.dp.Db.Exec(`DROP TRIGGER no_resolved`); err != nil {
		t.Fatal(err)
	}
	if rec := e.resolveTap(t, e.manager, "tap-1", "charged_refund_at_provider", "", dataAPITestManagerPIN, ""); rec.Code != 200 {
		t.Fatalf("retry = %d %s", rec.Code, rec.Body.String())
	}
	audits := e.audit(t, "tap_resolve")
	if len(audits) != 1 || audits[0]["stamped"] != false || audits[0]["already_stamped"] != true || audits[0]["attempt_reason"] != data.AttemptReasonChargedNoSale {
		t.Fatalf("audits = %+v", audits)
	}
}

// --- ut-docs#7027: boot closes a tap leg whose attempt a manager already settled ---

// seedAttempt writes a payment_attempts row under key in the given state
// (and, for approved + charged_no_sale, the manager's stamp).
func (e *legEnv) seedAttempt(t *testing.T, key, state, reason string) {
	t.Helper()
	repo := data.NewPaymentAttemptRepo(e.dp.Db)
	ctx := context.Background()
	if _, err := repo.Begin(ctx, data.PaymentAttempt{Key: key, Provider: "stripe", AmountMinor: 120, Currency: "gbp", Deadline: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if state == data.AttemptPending {
		return
	}
	if err := repo.Mark(ctx, key, state, data.AttemptUpdate{}); err != nil {
		t.Fatal(err)
	}
	if reason == data.AttemptReasonChargedNoSale {
		if _, _, err := repo.MarkChargedNoSale(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
}

func (e *legEnv) bootUnknown(t *testing.T, id string, states ...string) (LegReconcileResult, time.Time) {
	t.Helper()
	e.seed(t, id, "tapstripe", states...)
	start := time.Now().Add(-time.Second)
	return ReconcilePaymentLegs(context.Background(), e.dp, time.Now().Add(time.Minute)), start
}

func (e *legEnv) wantNotListed(t *testing.T, id string) {
	t.Helper()
	if body := e.fragment(t, e.manager).Body.String(); strings.Contains(body, `data-testid="pay-attention-row"`) || strings.Contains(body, id) {
		t.Fatalf("card lists the closed leg: %s", body)
	}
	l := e.listJSON(t)
	if len(l.Data.Legs) != 0 || len(l.Data.Attempts) != 0 || l.Data.LegsCount != 0 {
		t.Fatalf("json lists the closed leg: %+v", l.Data)
	}
}

func (e *legEnv) wantClosed(t *testing.T, id string) {
	t.Helper()
	ch := e.chain(t, id)
	if n := len(ch); n < 2 || ch[n-1] != data.LegResolved || ch[n-2] != data.LegWarned {
		t.Fatalf("chain = %v, want ... warned resolved", ch)
	}
	if got := e.last(t, id); got.Reason != data.LegReasonAttemptSettled {
		t.Fatalf("resolved reason = %q, want %q", got.Reason, data.LegReasonAttemptSettled)
	}
}

// AC1
func TestLegReconcile_UnknownTapWithResolvedAttemptIsClosed_7027(t *testing.T) {
	e := legRig(t)
	e.seedAttempt(t, "tap-1", data.AttemptResolvedNotCharged, "")
	res, start := e.bootUnknown(t, "tap-1", data.LegAsked, data.LegUnknown)
	if res.Closed != 1 || res.Warned != 0 {
		t.Fatalf("result = %+v, want Closed=1 Warned=0", res)
	}
	e.wantClosed(t, "tap-1")
	rows, err := e.dp.Db.Query(`SELECT state, reason, at FROM payment_leg_journal WHERE request_id = 'tap-1' ORDER BY seq DESC LIMIT 2`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := []struct{ state, reason string }{{data.LegResolved, data.LegReasonAttemptSettled}, {data.LegWarned, data.LegReasonRestart}}
	for i := 0; rows.Next(); i++ {
		var s, r, at string
		_ = rows.Scan(&s, &r, &at)
		ts, _ := time.Parse(time.RFC3339Nano, at)
		if s != want[i].state || r != want[i].reason || ts.Before(start) {
			t.Fatalf("row %d = %s/%s at %s, want %s/%s after %s", i, s, r, at, want[i].state, want[i].reason, start)
		}
	}
	e.wantNotListed(t, "tap-1")
}

// AC2
func TestLegReconcile_UnknownTapChargedNoSaleAttemptIsClosedAttemptUntouched_7027(t *testing.T) {
	e := legRig(t)
	e.seedAttempt(t, "tap-1", data.AttemptApproved, data.AttemptReasonChargedNoSale)
	before := e.attemptByKey(t, "tap-1")
	res, _ := e.bootUnknown(t, "tap-1", data.LegAsked, data.LegUnknown)
	if res.Closed != 1 {
		t.Fatalf("result = %+v", res)
	}
	e.wantClosed(t, "tap-1")
	if after := e.attemptByKey(t, "tap-1"); after != before {
		t.Fatalf("attempt changed: %+v -> %+v", before, after)
	}
	if n := e.refundCount(); n != 0 {
		t.Fatalf("refund dispatched: %d", n)
	}
	e.wantNotListed(t, "tap-1")
}

// E4
func TestLegReconcile_AskedTapWithCancelledOrDeclinedAttemptIsClosed_7027(t *testing.T) {
	for _, att := range []string{data.AttemptCancelled, data.AttemptDeclined} {
		for _, chain := range [][]string{{data.LegAsked}, {data.LegAsked, data.LegUnknown}} {
			e := legRig(t)
			e.seedAttempt(t, "tap-1", att, "")
			res, _ := e.bootUnknown(t, "tap-1", chain...)
			if res.Closed != 1 {
				t.Fatalf("%s %v: result = %+v", att, chain, res)
			}
			e.wantClosed(t, "tap-1")
		}
	}
}

// AC7
func TestLegReconcile_DecidedTapLegClosedOnceSecondBootNoop_7027(t *testing.T) {
	e := legRig(t)
	e.seedAttempt(t, "tap-1", data.AttemptResolvedNotCharged, "")
	_, _ = e.bootUnknown(t, "tap-1", data.LegAsked, data.LegUnknown)
	e.wantClosed(t, "tap-1")
	rows := countRows(t, e.dp, "SELECT COUNT(*) FROM payment_leg_journal")
	if res := ReconcilePaymentLegs(context.Background(), e.dp, time.Now().Add(time.Minute)); res != (LegReconcileResult{}) {
		t.Fatalf("second boot = %+v, want zero", res)
	}
	if n := countRows(t, e.dp, "SELECT COUNT(*) FROM payment_leg_journal"); n != rows {
		t.Fatalf("second boot appended %d rows", n-rows)
	}
	e.wantClosed(t, "tap-1")
}

// AC8
func TestLegReconcile_ClosedTapLegLogsNoRefundWarning_7027(t *testing.T) {
	e := legRig(t)
	buf := &syncBuffer{}
	t.Cleanup(logging.CaptureForTest(buf))
	e.seedAttempt(t, "tap-1", data.AttemptResolvedNotCharged, "")
	_, _ = e.bootUnknown(t, "tap-1", data.LegAsked, data.LegUnknown)
	e.wantClosed(t, "tap-1")
	out := buf.String()
	if strings.Contains(out, "refund it there") {
		t.Fatalf("closed leg logged the refund warning: %s", out)
	}
	if !strings.Contains(out, "tap-1") || !strings.Contains(out, data.AttemptResolvedNotCharged) {
		t.Fatalf("no info line naming the id and attempt state: %s", out)
	}
}

// AC3 (regression guard)
func TestLegReconcile_UnknownTapWithOpenAttemptStaysTapRow_7027(t *testing.T) {
	e := legRig(t)
	e.orphan(t, "tap-1", "stripe") // attempt unknown/till_restarted
	res, _ := e.bootUnknown(t, "tap-1", data.LegAsked, data.LegUnknown)
	if res.Warned != 1 || res.Closed != 0 {
		t.Fatalf("result = %+v", res)
	}
	if got := e.last(t, "tap-1"); got.State != data.LegWarned || got.Reason != data.LegReasonRestart {
		t.Fatalf("leg = %s/%s", got.State, got.Reason)
	}
	if l := e.listJSON(t); l.Data.Count != 1 {
		t.Fatalf("json = %+v", l.Data)
	}
}

// AC4, E3 (regression guard) and the reversing exemption
func TestLegReconcile_ApprovedWithoutDecisionStaysListed_7027(t *testing.T) {
	e := legRig(t)
	e.seedAttempt(t, "tap-1", data.AttemptApproved, "")
	res, _ := e.bootUnknown(t, "tap-1", data.LegAsked, data.LegUnknown)
	if res.Warned != 1 || res.Closed != 0 {
		t.Fatalf("result = %+v", res)
	}
	if got := e.last(t, "tap-1"); got.State != data.LegWarned {
		t.Fatalf("leg = %s, want warned", got.State)
	}
	if body := e.fragment(t, e.manager).Body.String(); !strings.Contains(body, `data-testid="pay-attention-row"`) {
		t.Fatalf("not listed: %s", body)
	}
}

func TestLegReconcile_ReversingLegWithDecidedAttemptStaysWarned_7027(t *testing.T) {
	e := legRig(t)
	e.seedAttempt(t, "tap-1", data.AttemptResolvedNotCharged, "")
	res, _ := e.bootUnknown(t, "tap-1", data.LegAsked, data.LegCaptured, data.LegReversing)
	if res.Warned != 1 || res.Closed != 0 {
		t.Fatalf("result = %+v", res)
	}
	if got := e.last(t, "tap-1"); got.State != data.LegWarned {
		t.Fatalf("leg = %s, want warned", got.State)
	}
}

// AC6 (regression guard)
func TestLegReconcile_AttemptUnreadableUnknownLegIsWarned_7027(t *testing.T) {
	e := legRig(t)
	e.seedAttempt(t, "tap-1", data.AttemptResolvedNotCharged, "")
	e.dropTable(t, "payment_attempts")
	res, _ := e.bootUnknown(t, "tap-1", data.LegAsked, data.LegUnknown)
	if res.Warned != 1 || res.Closed != 0 {
		t.Fatalf("result = %+v", res)
	}
	ch := e.chain(t, "tap-1")
	if ch[len(ch)-1] != data.LegWarned {
		t.Fatalf("chain = %v, want ending warned with no resolved", ch)
	}
}

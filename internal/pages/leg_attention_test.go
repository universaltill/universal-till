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

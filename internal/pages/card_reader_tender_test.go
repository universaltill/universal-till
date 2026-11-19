package pages

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/cardreaders"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#6566: per-till card reader registry, tender side.

type authCall struct {
	ID      string
	Payload map[string]any
}

// readerPlugin is a payment plugin ("<method>") scripted by a test: it records
// every authorize/refund payload and answers `.readers` from state.
type readerPlugin struct {
	PluginID, Method string

	mu       sync.Mutex
	auths    []authCall
	refunds  []map[string]any
	answer   func(n int) string // authorize answer, "" = approved with a ref
	state    func(refs []string) (json.RawMessage, error)
	stateReq int
}

func (rp *readerPlugin) authCalls() []authCall {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	return append([]authCall(nil), rp.auths...)
}

func (rp *readerPlugin) refundCalls() []map[string]any {
	rp.mu.Lock()
	defer rp.mu.Unlock()
	return append([]map[string]any(nil), rp.refunds...)
}

// seedReaderPlugin installs the plugin; hook declares payment.<m>.readers.
func seedReaderPlugin(t *testing.T, dp *common.Deps, pluginID, method string, hook bool) *readerPlugin {
	t.Helper()
	rp := &readerPlugin{PluginID: pluginID, Method: method}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := dp.Db.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, is_deprecated, min_pos_version, api_version, published_at)
	          VALUES (?, '1.0.0', ?, 'demo', 'wasm', 'demo.wasm', 'https://example.test/demo.wasm', 'deadbeef', 'auth', 'site', '[]', 0, '0.0.0', '1', datetime('now'))`, pluginID, "Pay "+method)
	mustExec(`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES (?, ?, '1.0.0', 'demo.wasm', 'wasm', 1)`, pluginID, "Pay "+method)
	mustExec(`INSERT INTO plugin_entries (id, plugin_id, key, label, type, trigger_event, is_active) VALUES (?, ?, ?, ?, 'payment', ?, 1)`,
		"e-"+method, pluginID, method, "Pay "+method, "payment."+method+".requested")
	events := []string{"payment." + method + ".authorize", "payment." + method + ".refund"}
	for i, ev := range events {
		mustExec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES (?, ?, ?, 'h', 1)`, fmt.Sprintf("h%s%d", method, i), pluginID, ev)
	}
	if hook {
		ev := "payment." + method + ".readers"
		events = append(events, ev)
		mustExec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES (?, ?, ?, 'h', 1)`, "hr"+method, pluginID, ev)
	}
	mustExec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted) VALUES (?, ?, 'events:receive', 1)`, "p-"+method, pluginID)
	bus := plugins.SharedBus(dp.Db)
	for _, ev := range events {
		bus.SetEventMode(ev, plugins.Blocking)
	}
	if _, err := bus.SubscribeWithHandler(context.Background(), pluginID, events, func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		var p map[string]any
		_ = json.Unmarshal(ev.Payload, &p)
		switch ev.Type {
		case "payment." + method + ".authorize":
			rp.mu.Lock()
			rp.auths = append(rp.auths, authCall{ID: ev.ID, Payload: p})
			n := len(rp.auths)
			ans := rp.answer
			rp.mu.Unlock()
			a := ""
			if ans != nil {
				a = ans(n)
			}
			switch a {
			case "DECLINE":
				return nil, &plugins.HandlerExitError{Code: 2, Stdout: json.RawMessage(`{"decline_code":"card_declined"}`)}
			case "UNKNOWN":
				return nil, &plugins.HandlerExitError{Code: 2, Stdout: json.RawMessage(`{"decline_code":"outcome_unknown"}`)}
			case "":
				return json.RawMessage(fmt.Sprintf(`{"outcome":"approved","transaction_ref":"txn_%s_%d"}`, method, n)), nil
			}
			return json.RawMessage(a), nil
		case "payment." + method + ".refund":
			rp.mu.Lock()
			rp.refunds = append(rp.refunds, p)
			rp.mu.Unlock()
			return json.RawMessage(`{"outcome":"refunded"}`), nil
		case "payment." + method + ".readers":
			rp.mu.Lock()
			rp.stateReq++
			st := rp.state
			rp.mu.Unlock()
			var refs []string
			if rr, ok := p["readers"].([]any); ok {
				for _, x := range rr {
					refs = append(refs, fmt.Sprint(x))
				}
			}
			if st == nil {
				return nil, errors.New("no state")
			}
			return st(refs)
		}
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	return rp
}

func addReader(t *testing.T, dp *common.Deps, rp *readerPlugin, name, ref string) data.CardReader {
	t.Helper()
	c, err := data.NewCardReaderRepo(dp.Db).Create(context.Background(), data.CardReader{Name: name, MethodKey: rp.Method, PluginID: rp.PluginID, ReaderRef: ref})
	if err != nil {
		t.Fatalf("add reader %s: %v", name, err)
	}
	return c
}

func readerRig(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, dp := newFiscalSignDeps(t)
	cardreaders.SharedCache().Reset()
	t.Cleanup(cardreaders.SharedCache().Reset)
	if _, err := dp.Db.Exec(`UPDATE inventory SET quantity = 100000 WHERE id = 'inv1'`); err != nil {
		t.Fatal(err)
	}
	return mux, dp
}

func tenderBody(legs ...string) string { return `{"payments":[` + strings.Join(legs, ",") + `]}` }

func leg(method string, amount int, reader string) string {
	if reader == "" {
		return fmt.Sprintf(`{"method":%q,"amount":%d}`, method, amount)
	}
	return fmt.Sprintf(`{"method":%q,"amount":%d,"reader":%q}`, method, amount, reader)
}

func scan(t *testing.T, dp *common.Deps) {
	t.Helper()
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
}

func errCode(t *testing.T, body string) (code, method string) {
	t.Helper()
	var env struct {
		Data  any `json:"data"`
		Error struct {
			Code   string `json:"code"`
			Method string `json:"method"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("error body is not the JSON envelope: %q", body)
	}
	return env.Error.Code, env.Error.Method
}

// A method with no registry row keeps today's payload and request id byte for byte.
func TestReaderTender_LegacyMethodPayloadAndRequestIDUnchanged(t *testing.T) {
	mux, dp := readerRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", false)
	scan(t, dp)
	rec := postTender(t, mux, tenderBody(leg("demopay", 120, "")))
	if rec.Code != http.StatusOK || countRows(t, dp, "SELECT COUNT(*) FROM sales") != 1 {
		t.Fatalf("legacy sale failed: %d %s", rec.Code, rec.Body.String())
	}
	calls := rp.authCalls()
	if len(calls) != 1 {
		t.Fatalf("want 1 authorize, got %d", len(calls))
	}
	for _, k := range []string{"reader_id", "reader_kind", "reader_name"} {
		if _, ok := calls[0].Payload[k]; ok {
			t.Errorf("legacy payload must not carry %s: %+v", k, calls[0].Payload)
		}
	}
	golden := sha256.Sum256([]byte(`{"amount":120,"currency":"GBP","method":"demopay","reference":""}`))
	if !strings.HasSuffix(calls[0].ID, fmt.Sprintf(":0:%x", golden[:8])) {
		t.Fatalf("legacy request id digest changed: %s", calls[0].ID)
	}
}

// A method with a row but a plugin without the hook stays legacy.
func TestReaderTender_RowWithoutPluginHookStaysLegacy(t *testing.T) {
	mux, dp := readerRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", false)
	addReader(t, dp, rp, "Front", "tmr_1")
	addReader(t, dp, rp, "Back", "tmr_2")
	scan(t, dp)
	rec := postTender(t, mux, tenderBody(leg("demopay", 120, "")))
	if countRows(t, dp, "SELECT COUNT(*) FROM sales") != 1 {
		t.Fatalf("a plugin that cannot honour reader_id must keep selling as today: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := rp.authCalls()[0].Payload["reader_id"]; ok {
		t.Fatalf("an old plugin must never be sent reader_id")
	}
}

func TestReaderTender_ManagedSingleRowIsUsedWithoutAPicker(t *testing.T) {
	mux, dp := readerRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	row := addReader(t, dp, rp, "Front", "tmr_FRONT")
	scan(t, dp)
	rec := postTender(t, mux, tenderBody(leg("demopay", 120, "")))
	if rec.Code != http.StatusOK || countRows(t, dp, "SELECT COUNT(*) FROM sales") != 1 {
		t.Fatalf("sale failed: %d %s", rec.Code, rec.Body.String())
	}
	p := rp.authCalls()[0].Payload
	if p["reader_id"] != "tmr_FRONT" || p["reader_kind"] != "cloud" {
		t.Fatalf("payload must carry the registry reader: %+v", p)
	}
	if _, ok := p["reader_name"]; ok {
		t.Fatalf("reader_name is display only and never sent")
	}
	if amt, ok := p["amount"].(float64); !ok || amt != 120 || amt != float64(int64(amt)) {
		t.Fatalf("amount must stay integer minor units: %v", p["amount"])
	}
	var term string
	if err := dp.Db.QueryRow(`SELECT terminal_id FROM payments`).Scan(&term); err != nil || term != "tmr_FRONT" {
		t.Fatalf("payments.terminal_id = %q err=%v, want the reader ref", term, err)
	}
	got, _ := data.NewCardReaderRepo(dp.Db).Get(context.Background(), row.ID)
	if got.LastUsedAt == "" {
		t.Fatalf("last_used_at must be set on an approved authorize")
	}
}

func TestReaderTender_ManagedSeveralRowsNeedAChoice(t *testing.T) {
	mux, dp := readerRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	addReader(t, dp, rp, "Front", "tmr_1")
	addReader(t, dp, rp, "Back", "tmr_2")
	scan(t, dp)
	rec := postTender(t, mux, tenderBody(leg("demopay", 120, "")))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if code, m := errCode(t, rec.Body.String()); code != "reader_required" || m != "demopay" {
		t.Fatalf("error = %q/%q", code, m)
	}
	if len(rp.authCalls()) != 0 || countRows(t, dp, "SELECT COUNT(*) FROM sales") != 0 {
		t.Fatalf("no money may move before the reader is chosen")
	}
	if len(dp.Engine.Basket().Lines) != 1 {
		t.Fatalf("basket must be kept")
	}
}

func TestReaderTender_ChosenRowReachesItsOwnProvider(t *testing.T) {
	mux, dp := readerRig(t)
	sumup := seedReaderPlugin(t, dp, "p.sumup", "sumup", true)
	stripe := seedReaderPlugin(t, dp, "p.stripe", "stripe", true)
	addReader(t, dp, sumup, "Counter 1", "rdr_1")
	addReader(t, dp, sumup, "Counter 2", "rdr_2")
	c3 := addReader(t, dp, sumup, "Counter 3", "rdr_3")
	st := addReader(t, dp, stripe, "Tablet", "tmr_T")

	scan(t, dp)
	if rec := postTender(t, mux, tenderBody(leg("sumup", 120, c3.ID))); countRows(t, dp, "SELECT COUNT(*) FROM sales") != 1 {
		t.Fatalf("sumup sale: %d %s", rec.Code, rec.Body.String())
	}
	if len(sumup.authCalls()) != 1 || sumup.authCalls()[0].Payload["reader_id"] != "rdr_3" || len(stripe.authCalls()) != 0 {
		t.Fatalf("sumup row must go to payment.sumup.authorize only: %+v / %+v", sumup.authCalls(), stripe.authCalls())
	}
	scan(t, dp)
	if rec := postTender(t, mux, tenderBody(leg("stripe", 120, st.ID))); countRows(t, dp, "SELECT COUNT(*) FROM sales") != 2 {
		t.Fatalf("stripe sale: %d %s", rec.Code, rec.Body.String())
	}
	if len(stripe.authCalls()) != 1 || stripe.authCalls()[0].Payload["reader_id"] != "tmr_T" {
		t.Fatalf("stripe row must go to payment.stripe.authorize: %+v", stripe.authCalls())
	}
}

func TestReaderTender_InvalidReaderIsRefusedBeforeAnyPlugin(t *testing.T) {
	mux, dp := readerRig(t)
	sumup := seedReaderPlugin(t, dp, "p.sumup", "sumup", true)
	stripe := seedReaderPlugin(t, dp, "p.stripe", "stripe", true)
	su := addReader(t, dp, sumup, "S1", "rdr_1")
	addReader(t, dp, sumup, "S2", "rdr_2")
	st := addReader(t, dp, stripe, "T1", "tmr_1")
	gone := addReader(t, dp, sumup, "Gone", "rdr_9")
	if _, err := data.NewCardReaderRepo(dp.Db).SoftRemove(context.Background(), gone.ID); err != nil {
		t.Fatal(err)
	}
	scan(t, dp)
	for name, reader := range map[string]string{
		"other method's row": st.ID, // a stripe row sent for a sumup leg
		"removed row":        gone.ID,
		"unknown id":         "00000000-0000-0000-0000-000000000000",
		"provider ref":       "rdr_1", // a client must never send the provider ref
	} {
		rec := postTender(t, mux, tenderBody(leg("sumup", 120, reader)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d: %s", name, rec.Code, rec.Body.String())
			continue
		}
		if code, _ := errCode(t, rec.Body.String()); code != "card_reader.invalid" {
			t.Errorf("%s: code %q", name, code)
		}
	}
	if len(sumup.authCalls())+len(stripe.authCalls()) != 0 {
		t.Fatalf("an invalid reader must reach no plugin")
	}
	_ = su
}

func TestReaderTender_SplitLegsOnTwoReadersAndReversalKeepsEachReader(t *testing.T) {
	mux, dp := readerRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	a := addReader(t, dp, rp, "A", "tmr_A")
	b := addReader(t, dp, rp, "B", "tmr_B")
	rp.answer = func(n int) string {
		if n == 2 {
			return "DECLINE"
		}
		return ""
	}
	scan(t, dp)
	postTender(t, mux, tenderBody(leg("demopay", 60, a.ID), leg("demopay", 60, b.ID)))
	calls := rp.authCalls()
	if len(calls) != 2 || calls[0].Payload["reader_id"] != "tmr_A" || calls[1].Payload["reader_id"] != "tmr_B" {
		t.Fatalf("each leg carries its own reader: %+v", calls)
	}
	if calls[0].ID == calls[1].ID {
		t.Fatalf("two legs, two request ids")
	}
	refunds := rp.refundCalls()
	if len(refunds) != 1 {
		t.Fatalf("leg 1 must be reversed once, got %+v", refunds)
	}
	r := refunds[0]
	if r["reader_id"] != "tmr_A" || r["authorize_request_id"] != calls[0].ID || r["reversal"] != true {
		t.Fatalf("reversal must carry leg 1's reader and request id: %+v", r)
	}
	orig, _ := r["original_payments"].([]any)
	if len(orig) != 1 || orig[0].(map[string]any)["reader_id"] != "tmr_A" {
		t.Fatalf("original_payments[] must carry reader_id: %+v", r["original_payments"])
	}
	if amt, _ := r["amount"].(float64); int64(amt) != 60 {
		t.Fatalf("reversal amount = %v", r["amount"])
	}
}

func TestReaderTender_UnknownOutcomeIsPinnedToItsReader(t *testing.T) {
	mux, dp := readerRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	a := addReader(t, dp, rp, "A", "tmr_A")
	b := addReader(t, dp, rp, "B", "tmr_B")
	rp.answer = func(n int) string {
		if n == 1 {
			return "UNKNOWN"
		}
		return ""
	}
	scan(t, dp)
	postTender(t, mux, tenderBody(leg("demopay", 120, a.ID)))
	// the same tender on ANOTHER reader is a different tender: refused by the lock, no plugin asked.
	rec := postTender(t, mux, tenderBody(leg("demopay", 120, b.ID)))
	if !strings.Contains(rec.Body.String(), unknownOutcomeToast) {
		t.Fatalf("another reader must be refused while locked: %d %s", rec.Code, rec.Body.String())
	}
	if len(rp.authCalls()) != 1 {
		t.Fatalf("a locked tender must not ask a plugin on another reader, got %d calls", len(rp.authCalls()))
	}
	// the same reader retries with the SAME request id and settles.
	postTender(t, mux, tenderBody(leg("demopay", 120, a.ID)))
	calls := rp.authCalls()
	if len(calls) != 2 || calls[0].ID != calls[1].ID {
		t.Fatalf("retry must reuse the request id: %+v", calls)
	}
	if countRows(t, dp, "SELECT COUNT(*) FROM sales") != 1 {
		t.Fatalf("retry on the pinned reader must settle")
	}
}

func TestReaderTender_ReaderInFlightIsBusy(t *testing.T) {
	mux, dp := readerRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	a := addReader(t, dp, rp, "A", "tmr_A")
	addReader(t, dp, rp, "B", "tmr_B")
	if !cardreaders.SharedInFlight().Acquire(a.ID) {
		t.Fatal("acquire")
	}
	defer cardreaders.SharedInFlight().Release(a.ID)
	scan(t, dp)
	rec := postTender(t, mux, tenderBody(leg("demopay", 120, a.ID)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if code, _ := errCode(t, rec.Body.String()); code != "card_reader.busy" {
		t.Fatalf("code %q", code)
	}
	if len(rp.authCalls()) != 0 {
		t.Fatalf("a busy reader must not be asked")
	}
}

func TestReaderTender_InFlightIsReleasedAfterTheTender(t *testing.T) {
	mux, dp := readerRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	a := addReader(t, dp, rp, "A", "tmr_A")
	rp.answer = func(int) string { return "DECLINE" }
	scan(t, dp)
	postTender(t, mux, tenderBody(leg("demopay", 120, a.ID)))
	if cardreaders.SharedInFlight().Has(a.ID) {
		t.Fatalf("the in-flight mark must be released when the tender ends")
	}
}

func TestResolveReaderLegs_KioskRules(t *testing.T) {
	_, dp := readerRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	repo := data.NewCardReaderRepo(dp.Db)
	ctx := context.Background()
	resolve := func(source string) (string, error) {
		legs := []pos.PaymentInput{{MethodID: "demopay"}}
		release, err := resolveReaderLegs(ctx, dp, legs, source)
		if release != nil {
			defer release()
		}
		return legs[0].ReaderRowID, err
	}
	a := addReader(t, dp, rp, "A", "tmr_A")
	if id, err := resolve("kiosk"); err != nil || id != a.ID {
		t.Fatalf("one reader: the kiosk uses it: %v %v", id, err)
	}
	b := addReader(t, dp, rp, "B", "tmr_B")
	var cErr *cardReaderError
	if _, err := resolve("kiosk"); !errors.As(err, &cErr) || cErr.Code != "card_reader.kiosk_none" {
		t.Fatalf("several readers and no unattended default: kiosk declines, got %v", err)
	}
	if err := repo.SetUnattendedDefault(ctx, b.ID, true); err != nil {
		t.Fatal(err)
	}
	if id, err := resolve("kiosk"); err != nil || id != b.ID {
		t.Fatalf("unattended default must be used: %v %v", id, err)
	}
	if _, err := resolve("pos"); !errors.As(err, &cErr) || cErr.Code != "reader_required" {
		t.Fatalf("the till still asks the cashier: %v", err)
	}
}

func TestReaderRefund_OriginalPaymentsCarryTheReaderEvenWhenRemoved(t *testing.T) {
	mux, dp := readerRig(t)
	registerRefund(mux, dp)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	a := addReader(t, dp, rp, "A", "tmr_A")
	addReader(t, dp, rp, "B", "tmr_B")
	scan(t, dp)
	postTender(t, mux, tenderBody(leg("demopay", 120, a.ID)))
	var receipt string
	if err := dp.Db.QueryRow(`SELECT receipt_no FROM sales WHERE sale_type != 'return'`).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	// the reader is removed (and offline) afterwards: the refund must not care.
	if _, err := data.NewCardReaderRepo(dp.Db).SoftRemove(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receipt+"&qty_0=1&method=demopay"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code >= 400 {
		t.Fatalf("refund = %d: %s", rec.Code, rec.Body.String())
	}
	refunds := rp.refundCalls()
	if len(refunds) != 1 {
		t.Fatalf("want one provider refund, got %+v", refunds)
	}
	orig, _ := refunds[0]["original_payments"].([]any)
	if len(orig) != 1 {
		t.Fatalf("original_payments: %+v", refunds[0])
	}
	op := orig[0].(map[string]any)
	if op["reader_id"] != "tmr_A" || op["transaction_ref"] != "txn_demopay_1" {
		t.Fatalf("original_payments[0] = %+v", op)
	}
	if _, top := refunds[0]["reader_id"]; top {
		t.Fatalf("ordinary refunds carry reader_id per leg only, not at the top")
	}
}

func TestReaderTender_SettleEventCarriesReader(t *testing.T) {
	mux, dp := readerRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	addReader(t, dp, rp, "A", "tmr_A")
	var mu sync.Mutex
	var settles []map[string]any
	bus := plugins.SharedBus(dp.Db)
	bus.SetEventMode("payment.demopay.requested", plugins.Blocking)
	t.Cleanup(func() { bus.SetEventMode("payment.demopay.requested", plugins.NonBlocking) })
	if _, err := dp.Db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES ('hs', 'p.demo', 'payment.demopay.requested', 'h', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := bus.SubscribeWithHandler(context.Background(), "p.demo", []string{"payment.demopay.requested"}, func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		var p map[string]any
		_ = json.Unmarshal(ev.Payload, &p)
		mu.Lock()
		settles = append(settles, p)
		mu.Unlock()
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	scan(t, dp)
	postTender(t, mux, tenderBody(leg("demopay", 120, "")))
	mu.Lock()
	defer mu.Unlock()
	if len(settles) != 1 || settles[0]["reader_id"] != "tmr_A" {
		t.Fatalf("settle must carry reader_id: %+v", settles)
	}
}

// ut-docs#6566 D4: the legacy *_reader_id import runs after every plugin sync
// (install order does not matter) and never rewrites the setting.
func TestReaderMigration_RunsOnPluginReload(t *testing.T) {
	_, dp := readerRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	if _, err := dp.Db.Exec(`INSERT INTO plugin_settings(id,plugin_id,key,value_json,scope) VALUES('s1','p.demo','demopay_reader_id','"tmr_LEGACY"','register')`); err != nil {
		t.Fatal(err)
	}
	if rows, _ := data.NewCardReaderRepo(dp.Db).ListActive(context.Background()); len(rows) != 0 {
		t.Fatalf("precondition: no rows yet")
	}
	if err := dp.Pm.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, _ := data.NewCardReaderRepo(dp.Db).ListActive(context.Background())
	if len(rows) != 1 || rows[0].ReaderRef != "tmr_LEGACY" || rows[0].MethodKey != "demopay" || rows[0].Source != data.CardReaderSourceMigrated {
		t.Fatalf("want one migrated row, got %+v", rows)
	}
	if err := dp.Pm.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rows, _ = data.NewCardReaderRepo(dp.Db).ListActive(context.Background()); len(rows) != 1 {
		t.Fatalf("second reload must not duplicate: %d", len(rows))
	}
	var v string
	_ = dp.Db.QueryRow(`SELECT value_json FROM plugin_settings WHERE id='s1'`).Scan(&v)
	if v != `"tmr_LEGACY"` {
		t.Fatalf("setting rewritten: %s", v)
	}
	_ = rp
}

// Reviewer (ut-docs#6566, Architect risk 1): a tender locked on an unknown
// outcome while its method was still legacy must not be re-asked with a
// reader_id once the plugin gains the readers hook and a registry row: the
// payload digest, and so the request id, would change and the provider could
// not dedupe (a second charge). The lock refuses it; no plugin is asked.
func TestReaderTender_LegacyLockIsNotRetriedAsManagedAfterThePluginGainsTheHook(t *testing.T) {
	mux, dp := readerRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", false)
	rp.answer = func(n int) string {
		if n == 1 {
			return "UNKNOWN"
		}
		return ""
	}
	scan(t, dp)
	postTender(t, mux, tenderBody(leg("demopay", 120, "")))
	if dp.Engine.TenderLock() == "" || len(rp.authCalls()) != 1 {
		t.Fatalf("setup: the legacy tender must be locked after one authorize, calls=%d", len(rp.authCalls()))
	}
	// the plugin is updated mid-lock: it now declares the hook, and a reader row exists.
	if _, err := dp.Db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES ('hr-late', 'p.demo', 'payment.demopay.readers', 'h', 1)`); err != nil {
		t.Fatal(err)
	}
	row := addReader(t, dp, rp, "Front", "tmr_FRONT")
	for _, reader := range []string{"", row.ID} {
		rec := postTender(t, mux, tenderBody(leg("demopay", 120, reader)))
		if !strings.Contains(rec.Body.String(), unknownOutcomeToast) {
			t.Fatalf("reader %q: a locked legacy tender must stay refused: %d %s", reader, rec.Code, rec.Body.String())
		}
	}
	if n := len(rp.authCalls()); n != 1 {
		t.Fatalf("no authorize with a new request id may be sent while locked, got %d calls", n)
	}
	if countRows(t, dp, "SELECT COUNT(*) FROM sales") != 0 {
		t.Fatalf("no sale may complete")
	}
}

// Reviewer (ut-docs#6566): the reader of a locked tender is removed by a
// manager. The retry is refused before any plugin (never moved to another
// reader or to the plugin's own setting), and the lock stays.
func TestReaderTender_LockedTenderWhoseReaderWasRemovedAsksNoPlugin(t *testing.T) {
	mux, dp := readerRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	a := addReader(t, dp, rp, "A", "tmr_A")
	rp.answer = func(n int) string {
		if n == 1 {
			return "UNKNOWN"
		}
		return ""
	}
	scan(t, dp)
	postTender(t, mux, tenderBody(leg("demopay", 120, a.ID)))
	lock := dp.Engine.TenderLock()
	if lock == "" {
		t.Fatal("setup: tender must be locked")
	}
	if _, err := data.NewCardReaderRepo(dp.Db).SoftRemove(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	// no row left: the method is legacy again; with or without the old row id
	// nothing may reach the plugin.
	for _, reader := range []string{a.ID, ""} {
		postTender(t, mux, tenderBody(leg("demopay", 120, reader)))
	}
	if n := len(rp.authCalls()); n != 1 {
		t.Fatalf("a locked tender must not be re-asked without its reader, got %d calls", n)
	}
	if dp.Engine.TenderLock() != lock || countRows(t, dp, "SELECT COUNT(*) FROM sales") != 0 {
		t.Fatalf("the lock must stay and no sale may complete")
	}
}

// Reviewer (ut-docs#6566): reader_id is part of the digest behind the request
// id. A leg declined on reader A and tried again on reader B (same basket,
// same attempt, same amount) must be a NEW request id, or the provider's own
// dedupe would answer B's request with A's attempt.
func TestReaderTender_AnotherReaderAfterADeclineIsANewRequestID(t *testing.T) {
	mux, dp := readerRig(t)
	rp := seedReaderPlugin(t, dp, "p.demo", "demopay", true)
	a := addReader(t, dp, rp, "A", "tmr_A")
	b := addReader(t, dp, rp, "B", "tmr_B")
	rp.answer = func(n int) string {
		if n == 1 {
			return "DECLINE"
		}
		return ""
	}
	scan(t, dp)
	postTender(t, mux, tenderBody(leg("demopay", 120, a.ID)))
	postTender(t, mux, tenderBody(leg("demopay", 120, b.ID)))
	calls := rp.authCalls()
	if len(calls) != 2 {
		t.Fatalf("want 2 authorize calls, got %d", len(calls))
	}
	if calls[0].Payload["reader_id"] != "tmr_A" || calls[1].Payload["reader_id"] != "tmr_B" {
		t.Fatalf("readers: %+v", calls)
	}
	if calls[0].ID == calls[1].ID {
		t.Fatalf("a different reader must be a different request id, both were %s", calls[0].ID)
	}
	want := sha256.Sum256([]byte(`{"amount":120,"currency":"GBP","method":"demopay","reader_id":"tmr_B","reader_kind":"cloud","reference":""}`))
	if !strings.HasSuffix(calls[1].ID, fmt.Sprintf(":0:%x", want[:8])) {
		t.Fatalf("request id must be the digest of the payload sent (reader included): %s", calls[1].ID)
	}
}

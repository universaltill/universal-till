package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#6438: a payment plugin's authorize answer may carry
// transaction_ref; core keeps it in payments.trace_id and hands it back on
// payment.<key>.refund as original_payments, so a refund never depends on
// per-sale plugin storage (capped at 1,024 keys).

// refCardRig is a card plugin that answers each authorize with the ref
// refFor(n) and records every settle (.requested) payload.
type refCardRig struct {
	mux     *http.ServeMux
	dp      *common.Deps
	refunds func() []map[string]any
	settles func() []map[string]any
}

func newRefCardRig(t *testing.T, refFor func(n int) string, extra string) *refCardRig {
	t.Helper()
	mux, dp := newFiscalSignDeps(t)
	dp.AuthSvc = auth.NewService(dp.Db)
	registerRefund(mux, dp)
	if _, err := dp.Db.Exec(`UPDATE inventory SET quantity = 100000 WHERE id = 'inv1'`); err != nil {
		t.Fatal(err)
	}
	refunds, _ := seedCardPluginWithAnswerFunc(t, dp, nil, func(n int) string {
		ref := refFor(n)
		if ref == "RAW-NONE" {
			return `{"provider":"demopay","outcome":"approved"}`
		}
		return fmt.Sprintf(`{"provider":"demopay","outcome":"approved","transaction_ref":%s%s}`, ref, extra)
	})
	if _, err := dp.Db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
	          VALUES ('h3', 'com.universaltill.payment-demo', 'payment.demopay.requested', 'handle_requested', 1)`); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var settles []map[string]any
	bus := plugins.SharedBus(dp.Db)
	// Blocking only so the test handler runs synchronously and records the
	// settle payload; production delivers .requested non-blocking.
	bus.SetEventMode("payment.demopay.requested", plugins.Blocking)
	t.Cleanup(func() { bus.SetEventMode("payment.demopay.requested", plugins.NonBlocking) })
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.payment-demo",
		[]string{"payment.demopay.requested"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			var p map[string]any
			_ = json.Unmarshal(ev.Payload, &p)
			mu.Lock()
			settles = append(settles, p)
			mu.Unlock()
			return nil, nil
		}); err != nil {
		t.Fatal(err)
	}
	return &refCardRig{mux: mux, dp: dp, refunds: refunds, settles: func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), settles...)
	}}
}

func (r *refCardRig) sell(t *testing.T, payments string) {
	t.Helper()
	if _, err := r.dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	var before int
	_ = r.dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&before)
	rec := postTender(t, r.mux, `{"payments":`+payments+`}`)
	var after int
	_ = r.dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&after)
	if rec.Code != http.StatusOK || after != before+1 {
		t.Fatalf("tender did not record a sale (code %d, sales %d -> %d): %s", rec.Code, before, after, rec.Body.String())
	}
}

func (r *refCardRig) receiptNo(t *testing.T, nth int) string {
	t.Helper()
	var no string
	if err := r.dp.Db.QueryRow(`SELECT receipt_no FROM sales WHERE sale_type != 'return' ORDER BY rowid LIMIT 1 OFFSET ?`, nth-1).Scan(&no); err != nil {
		t.Fatalf("receipt %d: %v", nth, err)
	}
	return no
}

func (r *refCardRig) refund(t *testing.T, receipt string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader("receipt="+receipt+"&qty_0=1&method=demopay"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	if rec.Code >= 400 {
		t.Fatalf("refund %s = %d: %s", receipt, rec.Code, rec.Body.String())
	}
}

func (r *refCardRig) traceIDs(t *testing.T) []sql.NullString {
	t.Helper()
	rows, err := r.dp.Db.Query(`SELECT p.trace_id FROM payments p JOIN sales s ON s.id = p.sale_id WHERE s.sale_type != 'return' ORDER BY s.rowid, p.rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []sql.NullString
	for rows.Next() {
		var v sql.NullString
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

func waitSettles(t *testing.T, r *refCardRig, n int) []map[string]any {
	t.Helper()
	for i := 0; i < 200; i++ {
		if s := r.settles(); len(s) >= n {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("want %d settle events, got %d", n, len(r.settles()))
	return nil
}

func originalRefs(t *testing.T, refund map[string]any) []string {
	t.Helper()
	raw, _ := refund["original_payments"].([]any)
	var out []string
	for _, e := range raw {
		out = append(out, e.(map[string]any)["transaction_ref"].(string))
	}
	return out
}

// H1: well past the 1,024-key plugin storage cap, sale 1 and sale 1,100
// still refund against their own provider reference.
func TestTxnRef_1100CardSalesRefundFirstAndLastByOwnRef(t *testing.T) {
	if testing.Short() {
		t.Skip("1,100 sales")
	}
	t.Setenv("UT_AUTH", "off")
	rig := newRefCardRig(t, func(n int) string { return fmt.Sprintf(`"ref-%d"`, n) }, "")
	for i := 0; i < 1100; i++ {
		rig.sell(t, `[{"method":"demopay","amount":120}]`)
	}
	var nSales, nPay int
	_ = rig.dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&nSales)
	_ = rig.dp.Db.QueryRow(`SELECT COUNT(*) FROM payments`).Scan(&nPay)
	t.Logf("sales=%d payments=%d", nSales, nPay)
	ids := rig.traceIDs(t)
	if len(ids) != 1100 || !ids[0].Valid || ids[0].String != "ref-1" || ids[1099].String != "ref-1100" {
		t.Fatalf("trace_id not persisted per sale: n=%d first=%v last=%v", len(ids), ids[0], ids[len(ids)-1])
	}
	for _, c := range []struct {
		nth  int
		want string
	}{{1, "ref-1"}, {1100, "ref-1100"}} {
		before := len(rig.refunds())
		rig.refund(t, rig.receiptNo(t, c.nth))
		got := rig.refunds()
		if len(got) != before+1 {
			t.Fatalf("sale %d: want one more refund event, got %d", c.nth, len(got)-before)
		}
		refs := originalRefs(t, got[len(got)-1])
		if len(refs) != 1 || refs[0] != c.want {
			t.Fatalf("sale %d: original_payments refs = %v, want [%s]", c.nth, refs, c.want)
		}
		op := got[len(got)-1]["original_payments"].([]any)[0].(map[string]any)
		if op["amount"].(float64) != 120 {
			t.Fatalf("sale %d: original amount = %v, want 120", c.nth, op["amount"])
		}
	}
}

// H2: a malformed reference is ignored; the sale still completes.
func TestTxnRef_InvalidTransactionRefIgnored(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	long := `"` + strings.Repeat("x", 129) + `"`
	bad := []string{long, `"ab\u0007cd"`, `"has space"`, `12345`, `""`, `"   "`, "RAW-NONE", `"café"`}
	rig := newRefCardRig(t, func(n int) string { return bad[n-1] }, "")
	for range bad {
		rig.sell(t, `[{"method":"demopay","amount":120}]`)
	}
	for i, v := range rig.traceIDs(t) {
		if v.Valid {
			t.Errorf("case %d (%s): trace_id = %q, want NULL", i, bad[i], v.String)
		}
	}
	rig.refund(t, rig.receiptNo(t, 1))
	got := rig.refunds()
	if _, has := got[len(got)-1]["original_payments"]; has {
		t.Fatalf("a sale with no ref must not carry original_payments (H5): %v", got[len(got)-1])
	}
}

// A ref with surrounding whitespace is trimmed and accepted.
func TestTxnRef_TransactionRefTrimmed(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	rig := newRefCardRig(t, func(int) string { return `"  pi_abc  "` }, "")
	rig.sell(t, `[{"method":"demopay","amount":120}]`)
	if ids := rig.traceIDs(t); len(ids) != 1 || ids[0].String != "pi_abc" {
		t.Fatalf("trace_id = %v, want pi_abc", ids)
	}
}

// H3: split tender, two card legs: both refs persisted, the settle payloads
// carry distinct authorize_request_id / transaction_ref, and the refund lists
// both in payment order.
func TestTxnRef_SplitCardLegsKeepEachRef(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	rig := newRefCardRig(t, func(n int) string { return fmt.Sprintf(`"pi_%d"`, n) }, "")
	rig.sell(t, `[{"method":"demopay","amount":50},{"method":"demopay","amount":70}]`)
	ids := rig.traceIDs(t)
	if len(ids) != 2 || ids[0].String != "pi_1" || ids[1].String != "pi_2" {
		t.Fatalf("trace_ids = %v", ids)
	}
	s := waitSettles(t, rig, 2)
	seen := map[string]bool{}
	for i, p := range s {
		ref, _ := p["transaction_ref"].(string)
		rid, _ := p["authorize_request_id"].(string)
		if ref == "" || rid == "" || seen[rid] {
			t.Fatalf("settle %d lacks distinct ref/authorize id: %v", i, p)
		}
		seen[rid] = true
	}
	rig.refund(t, rig.receiptNo(t, 1))
	got := rig.refunds()
	if refs := originalRefs(t, got[len(got)-1]); len(refs) != 2 || refs[0] != "pi_1" || refs[1] != "pi_2" {
		t.Fatalf("original_payments refs = %v, want [pi_1 pi_2]", refs)
	}
}

// H4: the cannot-sign reversal carries original_payments for its leg.
func TestTxnRef_CannotSignReversalCarriesOriginalPayments(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	refunds, _ := seedCannotSignCardPluginAnswering(t, dp, nil, `{"provider":"demopay","outcome":"approved","transaction_ref":"pi_rev1","tip_amount":50}`)
	subscribeCannotSignSigner(t, dp)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	assertCannotSignRefused(t, dp, postTender(t, mux, `{"payments":[{"method":"demopay","amount":120}]}`))
	got := refunds()
	if len(got) != 1 {
		t.Fatalf("want one reversal, got %d", len(got))
	}
	ops, _ := got[0]["original_payments"].([]any)
	if len(ops) != 1 {
		t.Fatalf("reversal original_payments = %v", got[0]["original_payments"])
	}
	op := ops[0].(map[string]any)
	if op["transaction_ref"] != "pi_rev1" || op["amount"].(float64) != 170 || op["tip_amount"].(float64) != 50 {
		t.Fatalf("reversal original payment = %v, want ref pi_rev1 amount 170 tip 50", op)
	}
}

// H8: the reference is a trace id, never the receipt-printed auth code.
func TestTxnRef_TransactionRefNotStoredAsAuthCode(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	rig := newRefCardRig(t, func(int) string { return `"pi_secretref"` }, "")
	rig.sell(t, `[{"method":"demopay","amount":120}]`)
	var auth sql.NullString
	if err := rig.dp.Db.QueryRow(`SELECT auth_code FROM payments LIMIT 1`).Scan(&auth); err != nil {
		t.Fatal(err)
	}
	if auth.Valid {
		t.Fatalf("auth_code = %q, want NULL (transaction_ref belongs in trace_id)", auth.String)
	}
}

func TestTxnRef_PluginReportedTransactionRef(t *testing.T) {
	for _, c := range []struct {
		resp string
		want string
		ok   bool
	}{
		{`{"transaction_ref":"pi_1"}`, "pi_1", true},
		{`{"transaction_ref":" pi_1 "}`, "pi_1", true},
		{`{"transaction_ref":"a b"}`, "", false},
		{`{"transaction_ref":""}`, "", false},
		{`{"transaction_ref":7}`, "", false},
		{`{}`, "", false},
		{``, "", false},
		{`not json`, "", false},
		{`{"transaction_ref":"` + strings.Repeat("a", 128) + `"}`, strings.Repeat("a", 128), true},
		{`{"transaction_ref":"` + strings.Repeat("a", 129) + `"}`, "", false},
	} {
		got, ok := pluginReportedTransactionRef(json.RawMessage(c.resp))
		if got != c.want || ok != c.ok {
			t.Errorf("pluginReportedTransactionRef(%.40s) = (%q,%v), want (%q,%v)", c.resp, got, ok, c.want, c.ok)
		}
	}
}

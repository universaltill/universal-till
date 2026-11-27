package pages

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#7096: New sale (/api/pos/reset) and held-order cancel
// (/api/pos/held/cancel) take a manager PIN only with the prompt's single-use
// approval, bound to the basket / order the approver saw. Same mechanism as
// #7085 (pos_elevation_bind_7085_test.go).

func (e gateEnv) noResetRows(t *testing.T) {
	t.Helper()
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log`); n != 0 {
		t.Fatalf("audit rows = %d, want 0", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM cleared_baskets`); n != 0 {
		t.Fatalf("cleared_baskets rows = %d, want 0", n)
	}
}

func TestElevationBind7096_Reset_UnchangedRetryClears(t *testing.T) {
	e := bindEnv(t)
	e.seedLine(t, 2)
	retry := promptRetry(t, e, "/api/pos/reset", url.Values{}, e.cashier)
	if retry.Get("approval") == "" {
		t.Fatalf("prompt carries no approval: %v", retry)
	}
	rec := e.post("/api/pos/reset", retry)
	if isElevationPrompt(rec) {
		t.Fatalf("approved retry prompted again: %s", rec.Body.String())
	}
	if n := len(e.dp.Engine.Basket().Lines); n != 0 {
		t.Fatalf("basket lines = %d, want 0", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log WHERE entity_type='pos_reset' AND blocked_actor_id=?`, e.cashierID); n != 1 {
		t.Fatalf("pos_reset audit rows = %d, want 1", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM cleared_baskets`); n != 1 {
		t.Fatalf("cleared_baskets = %d, want 1", n)
	}
}

func TestElevationBind7096_Reset_SwappedBasketSameCountAndTotalRefused(t *testing.T) {
	e := bindEnv(t)
	key := e.seedLine(t, 2)
	retry := promptRetry(t, e, "/api/pos/reset", url.Values{}, e.cashier)
	e.dp.Engine.RemoveLine(key)
	if _, err := e.dp.Engine.ScanQty("ABC", 2); err != nil {
		t.Fatal(err)
	}
	rec := e.post("/api/pos/reset", retry)
	if !refused(rec) {
		t.Fatalf("swapped basket not refused: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(e.dp.Engine.Basket().Lines); n != 1 {
		t.Fatalf("basket lines = %d, want 1", n)
	}
	e.noResetRows(t)
}

func TestElevationBind7096_Reset_TableChangedRefused(t *testing.T) {
	e := bindEnv(t)
	e.seedLine(t, 2)
	retry := promptRetry(t, e, "/api/pos/reset", url.Values{}, e.cashier)
	e.dp.Engine.SetTable("tbl-x", "T9")
	rec := e.post("/api/pos/reset", retry)
	if !refused(rec) {
		t.Fatalf("table change not refused: %d %s", rec.Code, rec.Body.String())
	}
	if len(e.dp.Engine.Basket().Lines) != 1 || e.dp.Engine.TableID() != "tbl-x" {
		t.Fatalf("basket or table touched")
	}
	e.noResetRows(t)
}

func TestElevationBind7096_Reset_ReplayRefused(t *testing.T) {
	e := bindEnv(t)
	e.seedLine(t, 2)
	retry := promptRetry(t, e, "/api/pos/reset", url.Values{}, e.cashier)
	if rec := e.post("/api/pos/reset", retry); isElevationPrompt(rec) {
		t.Fatalf("first retry prompted: %s", rec.Body.String())
	}
	if _, err := e.dp.Engine.ScanQty("ABC", 2); err != nil {
		t.Fatal(err)
	}
	rec := e.post("/api/pos/reset", retry)
	if !refused(rec) {
		t.Fatalf("replay not refused: %d %s", rec.Code, rec.Body.String())
	}
	if len(e.dp.Engine.Basket().Lines) != 1 {
		t.Fatalf("replay cleared the basket")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log WHERE entity_type='pos_reset'`); n != 1 {
		t.Fatalf("pos_reset rows = %d, want 1", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM cleared_baskets`); n != 1 {
		t.Fatalf("cleared_baskets = %d, want 1", n)
	}
}

func TestElevationBind7096_Reset_ApprovalFromOtherPromptRefused(t *testing.T) {
	e := bindEnv(t)
	key := e.seedLine(t, 2)
	a := promptRetry(t, e, "/api/pos/reset", url.Values{}, e.cashier)
	b := promptRetry(t, e, "/api/pos/remove", url.Values{"key": {key}, "reason": {"void"}}, e.cashier)
	a.Set("approval", b.Get("approval"))
	if rec := e.post("/api/pos/reset", a); !refused(rec) {
		t.Fatalf("swapped approval not refused: %s", rec.Body.String())
	}
	e.noResetRows(t)
}

func TestElevationBind7096_Reset_MissingOrForgedApprovalRefusedBeforePIN(t *testing.T) {
	e := bindEnv(t)
	e.seedLine(t, 2)
	snap := e.dp.Engine.Snapshot()
	form := func(pin, approval string) url.Values {
		v := url.Values{"from_lines": {"1"}, "from_total": {strconv.FormatInt(snap.Total.Minor(), 10)}, "override_pin": {pin}}
		if approval != "" {
			v.Set("approval", approval)
		}
		return v
	}
	if rec := e.post("/api/pos/reset", form(gatePIN, "")); !refused(rec) {
		t.Fatalf("missing approval not refused: %s", rec.Body.String())
	}
	if rec := e.post("/api/pos/reset", form(gatePIN, "forged")); !refused(rec) {
		t.Fatalf("forged approval not refused: %s", rec.Body.String())
	}
	e.noResetRows(t)
	for i := 0; i < 6; i++ {
		if rec := e.post("/api/pos/reset", form("000000", "")); !refused(rec) {
			t.Fatalf("wrong pin w/o token not refused: %s", rec.Body.String())
		}
	}
	retry := promptRetry(t, e, "/api/pos/reset", url.Values{}, e.cashier)
	if rec := e.post("/api/pos/reset", retry); isElevationPrompt(rec) {
		t.Fatalf("lockout ticked or prompt again: %s", rec.Body.String())
	}
	if len(e.dp.Engine.Basket().Lines) != 0 {
		t.Fatalf("basket not cleared")
	}
}

func TestElevationBind7096_Reset_OtherSessionUserRefused(t *testing.T) {
	e := bindEnv(t)
	e.seedLine(t, 2)
	retry := promptRetry(t, e, "/api/pos/reset", url.Values{}, e.cashier)
	other := &auth.User{ID: "cashier-other", Role: "cashier"}
	rec := postFormHtmx(e.mux, "/api/pos/reset", retry, other)
	if !refused(rec) {
		t.Fatalf("other user not refused: %d %s", rec.Code, rec.Body.String())
	}
	if len(e.dp.Engine.Basket().Lines) != 1 {
		t.Fatalf("basket cleared")
	}
}

// ---- held-order cancel ----

func heldCancelForm(id string) url.Values {
	return url.Values{"id": {id}, "view": {"parked-orders"}, "tab": {"hold"}}
}

func TestElevationBind7096_HeldCancel_UnchangedRetryCancels(t *testing.T) {
	e := bindEnv(t)
	seedCancelHeldOrder(t, e.dp, "hold-b1", "Sarah", "")
	retry := promptRetry(t, e, "/api/pos/held/cancel", heldCancelForm("hold-b1"), e.cashier)
	for _, k := range []string{"approval", "id", "view", "tab"} {
		if retry.Get(k) == "" {
			t.Fatalf("prompt lacks %s: %v", k, retry)
		}
	}
	if retry.Get("offline") != "0" {
		t.Fatalf("offline = %q, want 0", retry.Get("offline"))
	}
	if rec := e.post("/api/pos/held/cancel", retry); isElevationPrompt(rec) {
		t.Fatalf("retry prompted: %s", rec.Body.String())
	}
	if heldSaleExists(t, e.dp, "hold-b1") {
		t.Fatal("order not cancelled")
	}
	got := cancelAuditRows(t, e.dp)
	if len(got) != 1 || got[0].actor.String != e.mgrID || got[0].blocked.String != e.cashierID {
		t.Fatalf("audit = %+v", got)
	}
}

func TestElevationBind7096_HeldCancel_RepackedSameIdRefused(t *testing.T) {
	e := bindEnv(t)
	seedCancelHeldOrder(t, e.dp, "hold-b2", "Sarah", "")
	retry := promptRetry(t, e, "/api/pos/held/cancel", heldCancelForm("hold-b2"), e.cashier)
	if err := data.NewHeldSalesRepo(e.dp.Db).Upsert(context.Background(), data.HeldSale{
		ID: "hold-b2", Label: "Sarah", TotalMinor: 8000, LineCount: 1, Payload: `{"other":1}`,
	}); err != nil {
		t.Fatal(err)
	}
	if rec := e.post("/api/pos/held/cancel", retry); !refused(rec) {
		t.Fatalf("repacked order not refused: %s", rec.Body.String())
	}
	if !heldSaleExists(t, e.dp, "hold-b2") || len(cancelAuditRows(t, e.dp)) != 0 {
		t.Fatal("order cancelled or audited")
	}
}

func TestElevationBind7096_HeldCancel_SamePayloadFieldsChangedOnlyPayloadRefused(t *testing.T) {
	e := bindEnv(t)
	seedCancelHeldOrder(t, e.dp, "hold-b3", "Sarah", "")
	repo := data.NewHeldSalesRepo(e.dp.Db)
	cur, _, err := repo.Get(context.Background(), "hold-b3")
	if err != nil {
		t.Fatal(err)
	}
	retry := promptRetry(t, e, "/api/pos/held/cancel", heldCancelForm("hold-b3"), e.cashier)
	cur.Payload += " "
	if err := repo.Upsert(context.Background(), cur); err != nil {
		t.Fatal(err)
	}
	if rec := e.post("/api/pos/held/cancel", retry); !refused(rec) {
		t.Fatalf("payload-only change not refused: %s", rec.Body.String())
	}
	if !heldSaleExists(t, e.dp, "hold-b3") {
		t.Fatal("order cancelled")
	}
}

func TestElevationBind7096_HeldCancel_OtherIdRefused(t *testing.T) {
	e := bindEnv(t)
	seedCancelHeldOrder(t, e.dp, "hold-x", "X", "")
	seedCancelHeldOrder(t, e.dp, "hold-y", "Y", "")
	retry := promptRetry(t, e, "/api/pos/held/cancel", heldCancelForm("hold-x"), e.cashier)
	retry.Set("id", "hold-y")
	if rec := e.post("/api/pos/held/cancel", retry); !refused(rec) {
		t.Fatalf("other id not refused: %s", rec.Body.String())
	}
	if !heldSaleExists(t, e.dp, "hold-x") || !heldSaleExists(t, e.dp, "hold-y") {
		t.Fatal("an order was cancelled")
	}
}

func TestElevationBind7096_HeldCancel_OfflineFlipRefused(t *testing.T) {
	e := bindEnv(t)
	seedCancelHeldOrder(t, e.dp, "hold-b5", "Sarah", "")
	retry := promptRetry(t, e, "/api/pos/held/cancel", heldCancelForm("hold-b5"), e.cashier)
	flip := cloneVals(retry)
	flip.Set("offline", "1")
	if rec := e.post("/api/pos/held/cancel", flip); !refused(rec) {
		t.Fatalf("offline flip not refused: %s", rec.Body.String())
	}
	ov := cloneVals(retry)
	ov.Set("offline_override", "1")
	if rec := e.post("/api/pos/held/cancel", ov); !refused(rec) {
		t.Fatalf("offline_override not refused: %s", rec.Body.String())
	}
	if !heldSaleExists(t, e.dp, "hold-b5") {
		t.Fatal("order cancelled")
	}
}

func TestElevationBind7096_HeldCancel_ReplayRefused(t *testing.T) {
	e := bindEnv(t)
	seedCancelHeldOrder(t, e.dp, "hold-b6", "Sarah", "")
	retry := promptRetry(t, e, "/api/pos/held/cancel", heldCancelForm("hold-b6"), e.cashier)
	var updatedAt string
	if err := e.dp.Db.QueryRow(`SELECT updated_at FROM held_sales WHERE id = ?`, "hold-b6").Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	if rec := e.post("/api/pos/held/cancel", retry); isElevationPrompt(rec) {
		t.Fatalf("first retry prompted")
	}
	if heldSaleExists(t, e.dp, "hold-b6") {
		t.Fatal("first retry did not cancel")
	}
	// Put a byte-identical row back (same id, payload and updated_at), so
	// the binding still matches and only the approval's single use can
	// refuse the replay (review M1: without this the replay failed only
	// because the order was gone).
	seedCancelHeldOrder(t, e.dp, "hold-b6", "Sarah", "")
	if _, err := e.dp.Db.Exec(`UPDATE held_sales SET updated_at = ? WHERE id = ?`, updatedAt, "hold-b6"); err != nil {
		t.Fatal(err)
	}
	if rec := e.post("/api/pos/held/cancel", retry); !refused(rec) {
		t.Fatalf("replayed approval not refused: %s", rec.Body.String())
	}
	if !heldSaleExists(t, e.dp, "hold-b6") {
		t.Fatal("replayed approval cancelled the re-held order")
	}
	if n := len(cancelAuditRows(t, e.dp)); n != 1 {
		t.Fatalf("cancel audit rows = %d, want 1", n)
	}
}

func TestElevationBind7096_HeldCancel_MissingApprovalRefusedBeforePIN(t *testing.T) {
	e := bindEnv(t)
	seedCancelHeldOrder(t, e.dp, "hold-b7", "Sarah", "")
	f := heldCancelForm("hold-b7")
	f.Set("override_pin", gatePIN)
	if rec := e.post("/api/pos/held/cancel", f); !refused(rec) {
		t.Fatalf("missing approval not refused: %s", rec.Body.String())
	}
	if !heldSaleExists(t, e.dp, "hold-b7") || e.count(t, `SELECT COUNT(*) FROM audit_log`) != 0 {
		t.Fatal("cancelled or audited")
	}
	for i := 0; i < 6; i++ {
		w := heldCancelForm("hold-b7")
		w.Set("override_pin", "000000")
		if rec := e.post("/api/pos/held/cancel", w); !refused(rec) {
			t.Fatalf("wrong pin w/o token not refused: %s", rec.Body.String())
		}
	}
	retry := promptRetry(t, e, "/api/pos/held/cancel", heldCancelForm("hold-b7"), e.cashier)
	e.post("/api/pos/held/cancel", retry)
	if heldSaleExists(t, e.dp, "hold-b7") {
		t.Fatal("proper prompt+PIN did not cancel (lockout ticked?)")
	}
}

func TestElevationBind7096_HeldCancel_UnknownOrderCashierNoPrompt(t *testing.T) {
	e := bindEnv(t)
	rec := e.post("/api/pos/held/cancel", heldCancelForm("hold-none"))
	if isElevationPrompt(rec) {
		t.Fatalf("unknown order prompted: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not found") {
		t.Fatalf("popup answer is not \"not found\": %s", rec.Body.String())
	}
	if e.count(t, `SELECT COUNT(*) FROM audit_log`) != 0 {
		t.Fatal("audit rows written")
	}
	page := heldCancelForm("hold-none")
	page.Set("view", "page")
	rec = e.post("/api/pos/held/cancel", page)
	if got := rec.Header().Get("HX-Redirect"); got != "/open-orders?tab=hold&err=hold.error.not_found" {
		t.Fatalf("HX-Redirect = %q", got)
	}
}

// ---- F1 / F2 ----

func TestElevationBind7096_LineCodeAddressed_ReplacedLineRefused(t *testing.T) {
	e := bindEnv(t)
	key := e.seedLine(t, 3)
	retry := promptRetry(t, e, "/api/pos/line", url.Values{"code": {"ABC"}, "qty": {"1"}}, e.cashier)
	e.dp.Engine.RemoveLine(key)
	if _, err := e.dp.Engine.ScanQty("ABC", 3); err != nil {
		t.Fatal(err)
	}
	rec := e.post("/api/pos/line", retry)
	if !refused(rec) {
		t.Fatalf("replaced line not refused: %d %s", rec.Code, rec.Body.String())
	}
	if q := e.dp.Engine.Basket().Lines[0].Qty; q != 3 {
		t.Fatalf("qty = %v, want 3", q)
	}
	e.noRows(t)
}

func (e gateEnv) lastAuditDetail(t *testing.T, entity string) map[string]any {
	t.Helper()
	var raw string
	if err := e.dp.Db.QueryRow(`SELECT data_json FROM audit_log WHERE entity_type=? ORDER BY rowid DESC LIMIT 1`, entity).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestElevationBind7096_AuditDetailOmitsUnboundCode(t *testing.T) {
	t.Run("remove", func(t *testing.T) {
		e := bindEnv(t)
		key := e.seedLine(t, 2)
		retry := promptRetry(t, e, "/api/pos/remove", url.Values{"key": {key}, "code": {"junk"}, "reason": {"void"}}, e.cashier)
		retry.Set("code", "junk")
		e.post("/api/pos/remove", retry)
		d := e.lastAuditDetail(t, "pos_line")
		if _, has := d["code"]; has || d["key"] != key {
			t.Fatalf("detail = %v", d)
		}
	})
	t.Run("line reduce", func(t *testing.T) {
		e := bindEnv(t)
		key := e.seedLine(t, 3)
		retry := promptRetry(t, e, "/api/pos/line", url.Values{"key": {key}, "code": {"junk"}, "qty": {"1"}}, e.cashier)
		retry.Set("code", "junk")
		e.post("/api/pos/line", retry)
		d := e.lastAuditDetail(t, "pos_line")
		if _, has := d["code"]; has || d["key"] != key {
			t.Fatalf("detail = %v", d)
		}
	})
	t.Run("code addressed keeps code", func(t *testing.T) {
		e := bindEnv(t)
		e.seedLine(t, 3)
		retry := promptRetry(t, e, "/api/pos/line", url.Values{"code": {"ABC"}, "qty": {"1"}}, e.cashier)
		e.post("/api/pos/line", retry)
		d := e.lastAuditDetail(t, "pos_line")
		if d["code"] != "ABC" {
			t.Fatalf("detail = %v", d)
		}
	})
}

func cloneVals(v url.Values) url.Values {
	o := url.Values{}
	for k, vs := range v {
		o[k] = append([]string(nil), vs...)
	}
	return o
}

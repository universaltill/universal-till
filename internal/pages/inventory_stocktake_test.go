package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// Till stock-take on /inventory (ut-docs#5852). Items are invented.

func stockTakeFixture(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, dp := newInventoryAPITestDeps(t)
	registerInventoryPage(mux, dp)
	for _, q := range []string{
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itmB', 'BAN-1', 'Bananas', 100, 1)`,
		`INSERT INTO items (id, sku, name, base_price, is_active, is_weighed) VALUES ('itmW', 'CHE-1', 'Cheese', 900, 1, 1)`,
		`INSERT INTO items (id, sku, name, base_price, is_active, stock_untracked) VALUES ('itmU', 'SRV-1', 'Gift wrap', 100, 1, 1)`,
		`INSERT INTO inventory (id, item_id, variant_id, location_id, quantity, updated_at) VALUES ('invB', 'itmB', NULL, 'loc_main', 8, datetime('now'))`,
	} {
		if _, err := dp.Db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return mux, dp
}

func stockTakeBody(started time.Time, note string, lines ...string) string {
	return fmt.Sprintf(`{"location_id":"loc_main","started_at":%q,"note":%q,"lines":[%s]}`,
		started.UTC().Format(time.RFC3339), note, strings.Join(lines, ","))
}

func stLine(item, variant string, target any) string {
	return fmt.Sprintf(`{"item_id":%q,"variant_id":%q,"target":%v}`, item, variant, target)
}

func countMovementRows(t *testing.T, dp *common.Deps) int {
	t.Helper()
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM stock_movements WHERE type = 'count'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

type stockTakeErr struct {
	Error struct {
		Code  string `json:"code"`
		Lines []struct {
			Index int    `json:"index"`
			Code  string `json:"code"`
		} `json:"lines"`
	} `json:"error"`
}

func TestStockTake_AppliesAllLinesInOneGo(t *testing.T) {
	mux, dp := stockTakeFixture(t)
	start := time.Now().Add(-time.Minute)
	rec := postInvJSON(t, mux, "/api/inventory/stock-take",
		stockTakeBody(start, "", stLine("itmB", "", 5), stLine("", "var1", 30), stLine("itmW", "", 2.5)))
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Trigger") != "stock-updated" {
		t.Fatal("want HX-Trigger: stock-updated")
	}
	data, _, errVal, _ := envelopeOf(t, rec.Body.Bytes())
	var got struct{ Counted, Changed, Unchanged int }
	if err := json.Unmarshal(data, &got); err != nil || string(errVal) != "null" {
		t.Fatalf("bad envelope %s (%v)", rec.Body.String(), err)
	}
	if got.Counted != 3 || got.Changed != 2 || got.Unchanged != 1 {
		t.Fatalf("summary = %+v, want counted 3 changed 2 unchanged 1", got)
	}
	if q := inventoryQty(t, dp.Db, "itmB", "", "loc_main"); q != 5 {
		t.Fatalf("bananas = %v, want 5", q)
	}
	if q := inventoryQty(t, dp.Db, "itmW", "", "loc_main"); q != 2.5 {
		t.Fatalf("cheese = %v, want 2.5", q)
	}
	if n := countMovementRows(t, dp); n != 2 {
		t.Fatalf("%d count movements, want 2 (zero-delta line writes none)", n)
	}
	var actor, payload string
	if err := dp.Db.QueryRow(`SELECT COALESCE(actor_id,''), data_json FROM audit_log WHERE action = 'count' ORDER BY created_at LIMIT 1`).Scan(&actor, &payload); err != nil {
		t.Fatal(err)
	}
	if actor != "system" || !strings.Contains(payload, `"reason":"stock take"`) {
		t.Fatalf("actor=%q payload=%s", actor, payload)
	}
}

func TestStockTake_NoteBecomesReason(t *testing.T) {
	mux, dp := stockTakeFixture(t)
	rec := postInvJSON(t, mux, "/api/inventory/stock-take", stockTakeBody(time.Now().Add(-time.Minute), "  year end ", stLine("itmB", "", 1)))
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	var payload string
	if err := dp.Db.QueryRow(`SELECT data_json FROM audit_log WHERE action = 'count'`).Scan(&payload); err != nil || !strings.Contains(payload, `"reason":"year end"`) {
		t.Fatalf("payload=%s err=%v", payload, err)
	}
}

func TestStockTake_BadLinesRefuseTheWholeTake(t *testing.T) {
	start := time.Now().Add(-time.Minute)
	cases := map[string]struct{ line, code string }{
		"negative":          {stLine("itmB", "", -1), "target_invalid"},
		"too many decimals": {stLine("itmW", "", 1.2345), "too_many_decimals"},
		"fractional":        {stLine("itmB", "", 1.5), "whole_number_required"},
		"too large":         {stLine("itmB", "", 1000001), "target_too_large"},
		"untracked":         {stLine("itmU", "", 1), "not_countable"},
		"variant item":      {stLine("itm1", "", 1), "not_countable"},
		"unknown":           {stLine("nope", "", 1), "not_countable"},
		"variant of other":  {stLine("itmB", "var1", 1), "not_countable"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			mux, dp := stockTakeFixture(t)
			// Line 0 is fine; the bad one is index 1.
			rec := postInvJSON(t, mux, "/api/inventory/stock-take", stockTakeBody(start, "", stLine("", "var1", 3), c.line))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
			}
			var e stockTakeErr
			if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
				t.Fatal(err)
			}
			if e.Error.Code != "invalid_lines" || len(e.Error.Lines) != 1 || e.Error.Lines[0].Index != 1 || e.Error.Lines[0].Code != c.code {
				t.Fatalf("error = %+v, want invalid_lines [1 %s]", e.Error, c.code)
			}
			if n := countMovementRows(t, dp); n != 0 {
				t.Fatalf("%d movements written by a refused take", n)
			}
			if q := inventoryQty(t, dp.Db, "itmB", "", "loc_main"); q != 8 {
				t.Fatalf("bananas = %v, want untouched 8", q)
			}
		})
	}
}

func TestStockTake_DuplicateLine(t *testing.T) {
	mux, dp := stockTakeFixture(t)
	rec := postInvJSON(t, mux, "/api/inventory/stock-take", stockTakeBody(time.Now().Add(-time.Minute), "", stLine("itmB", "", 3), stLine("itmB", "", 4)))
	var e stockTakeErr
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if rec.Code != 400 || len(e.Error.Lines) != 1 || e.Error.Lines[0].Index != 1 || e.Error.Lines[0].Code != "duplicate" {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
	if countMovementRows(t, dp) != 0 {
		t.Fatal("refused take wrote movements")
	}
}

func TestStockTake_RequestLevelRefusals(t *testing.T) {
	ok := stLine("itmB", "", 3)
	many := make([]string, 501)
	for i := range many {
		many[i] = ok
	}
	cases := map[string]struct{ body, code string }{
		"no lines":     {stockTakeBody(time.Now().Add(-time.Minute), ""), "no_lines"},
		"501 lines":    {stockTakeBody(time.Now().Add(-time.Minute), "", many...), "too_many_lines"},
		"future start": {stockTakeBody(time.Now().Add(time.Hour), "", ok), "started_at_invalid"},
		"stale start":  {stockTakeBody(time.Now().Add(-49*time.Hour), "", ok), "started_at_invalid"},
		"long note":    {stockTakeBody(time.Now().Add(-time.Minute), strings.Repeat("x", 201), ok), "note_invalid"},
		"control note": {stockTakeBody(time.Now().Add(-time.Minute), "a\nb", ok), "note_invalid"},
		"bad location": {strings.Replace(stockTakeBody(time.Now().Add(-time.Minute), "", ok), "loc_main", "loc_nope", 1), "location_invalid"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			mux, dp := stockTakeFixture(t)
			rec := postInvJSON(t, mux, "/api/inventory/stock-take", c.body)
			var e stockTakeErr
			_ = json.Unmarshal(rec.Body.Bytes(), &e)
			if rec.Code != http.StatusBadRequest || e.Error.Code != c.code {
				t.Fatalf("code %d body %.300s, want 400 %s", rec.Code, rec.Body.String(), c.code)
			}
			if countMovementRows(t, dp) != 0 {
				t.Fatal("refused take wrote movements")
			}
		})
	}
}

func TestStockTake_RejectsNonJSONAndBadJSON(t *testing.T) {
	mux, _ := stockTakeFixture(t)
	if rec := postInvForm(t, mux, "/api/inventory/stock-take", "location_id=loc_main", "application/json"); rec.Code != http.StatusBadRequest {
		t.Fatalf("form body: code %d", rec.Code)
	}
	if rec := postInvJSON(t, mux, "/api/inventory/stock-take", `{nope`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json: code %d", rec.Code)
	}
}

func TestStockTake_ReplicaIsRefused(t *testing.T) {
	mux, dp := stockTakeFixture(t)
	if err := dp.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatal(err)
	}
	rec := postInvJSON(t, mux, "/api/inventory/stock-take", stockTakeBody(time.Now().Add(-time.Minute), "", stLine("itmB", "", 1)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	if countMovementRows(t, dp) != 0 {
		t.Fatal("replica wrote movements")
	}
	req := httptest.NewRequest(http.MethodGet, "/ui/inventory/stock-take?location_id=loc_main", nil)
	g := httptest.NewRecorder()
	mux.ServeHTTP(g, req)
	if g.Code != http.StatusConflict {
		t.Fatalf("GET on replica: code %d", g.Code)
	}
	page := httptest.NewRecorder()
	mux.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/inventory", nil))
	if strings.Contains(page.Body.String(), `id="stocktake-start"`) {
		t.Fatal("replica page still shows the Stock-take button")
	}
	if !strings.Contains(page.Body.String(), "Stock-takes are done on the main till.") {
		t.Fatal("replica page lacks the replica hint")
	}
}

func TestStockTake_PartialIsSalesSafe(t *testing.T) {
	mux, dp := stockTakeFixture(t)
	start := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	if _, err := dp.Db.Exec(`INSERT INTO stock_movements(id,item_id,variant_id,location_id,type,quantity,created_at) VALUES('mv-s',?,NULL,'loc_main','sale',-3,?)`,
		"itmB", start.Add(10*time.Minute).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`UPDATE inventory SET quantity = 5 WHERE id = 'invB'`); err != nil {
		t.Fatal(err)
	}
	rec := postInvJSON(t, mux, "/api/inventory/stock-take", stockTakeBody(start, "", stLine("itmB", "", 8)))
	if rec.Code != 200 {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	if q := inventoryQty(t, dp.Db, "itmB", "", "loc_main"); q != 5 {
		t.Fatalf("bananas = %v, want 8 counted - 3 sold = 5", q)
	}
}

func TestStockTakeFragment(t *testing.T) {
	mux, _ := stockTakeFixture(t)
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	rec := get("/ui/inventory/stock-take?location_id=loc_main")
	if rec.Code != 200 {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`data-started-at="`, `data-key="itmB|"`, `data-key="|var1"`, `data-key="itmW|"`, `inputmode="decimal"`, `data-weighed="1"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("fragment lacks %q:\n%s", want, body)
		}
	}
	for _, not := range []string{`data-key="itm1|"`, `data-key="itmU|"`, "showModal", "<form"} {
		if strings.Contains(body, not) {
			t.Fatalf("fragment must not contain %q", not)
		}
	}
	if c := get("/ui/inventory/stock-take?location_id=loc_nope").Code; c != http.StatusBadRequest {
		t.Fatalf("unknown location: code %d, want 400", c)
	}
}

func TestInventoryPage_StockTakeButtonAndDefaultLocation(t *testing.T) {
	mux, _ := stockTakeFixture(t)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/inventory", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `id="stocktake-start"`) || !strings.Contains(body, `data-default-location="loc_main"`) {
		t.Fatalf("page lacks the Stock-take button / default location")
	}
}

// A changed line publishes stock.adjusted (reason "count", the location, the
// delta); an unchanged line publishes nothing.
func TestStockTake_PublishesStockAdjustedForChangedLinesOnly(t *testing.T) {
	mux, dp := stockTakeFixture(t)
	ctx := t.Context()
	m := &plugins.Manifest{
		ID: "com.example.stocktake-5852", Name: "Stock 5852", Version: "1.0.0", Entrypoint: "./c",
		Hooks:       []plugins.ManifestHook{{Event: "stock.adjusted", Action: "sync"}},
		Permissions: []string{"events:receive"},
	}
	if err := plugins.PersistManifest(ctx, dp.Db, m, plugins.InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := plugins.GrantPermission(ctx, dp.Db, m.ID, "events:receive"); err != nil {
		t.Fatal(err)
	}
	bus := plugins.SharedBus(dp.Db)
	ch, err := bus.Subscribe(context.Background(), m.ID, []string{"stock.adjusted"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bus.Unsubscribe(m.ID) })

	// Bananas 8 -> 5 (changed); cheese 0 -> 0 (unchanged).
	rec := postInvJSON(t, mux, "/api/inventory/stock-take",
		stockTakeBody(time.Now().Add(-time.Minute), "", stLine("itmB", "", 5), stLine("itmW", "", 0)))
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	var got []plugins.StockAdjustedEvent
	deadline := time.After(time.Second)
collect:
	for {
		select {
		case ev := <-ch:
			var e plugins.StockAdjustedEvent
			if err := json.Unmarshal(ev.Payload, &e); err != nil {
				t.Fatal(err)
			}
			got = append(got, e)
		case <-deadline:
			break collect
		}
	}
	if len(got) != 1 {
		t.Fatalf("events = %+v, want exactly one (the changed line)", got)
	}
	if e := got[0]; e.ItemID != "itmB" || e.DeltaQty != -3 || e.Location != "loc_main" || e.Reason != stockMovementReason(data.StockCountMovementType) {
		t.Fatalf("event = %+v", e)
	}
}

// A take that changes stock nudges linked tills to pull stock.
func TestStockTake_NudgesLinkedTillsStock(t *testing.T) {
	t.Setenv("UT_AUTH", "off") // this fixture seeds users; the gate is covered elsewhere
	f := newSyncLinkFixture(t)
	registerInventoryStockTake(f.mux, f.dp)
	if _, err := f.dp.Db.Exec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itmN', 'NUD-1', 'Nuts', 100, 1)`); err != nil {
		t.Fatal(err)
	}
	f.enrol(t, "Till 2", "bearer-t2")
	c, _, err := f.dial(t, "bearer-t2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nextLinkFrame(t, c); err != nil {
		t.Fatal(err)
	}
	rec := postInvJSON(t, f.mux, "/api/inventory/stock-take", stockTakeBody(time.Now().Add(-time.Minute), "", stLine("itmN", "", 4)))
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	waitSync(t, c, fleetlink.ScopeStock)
}

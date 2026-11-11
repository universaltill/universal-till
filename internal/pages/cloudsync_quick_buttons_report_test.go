package pages

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/ui"
)

// ut-docs#2562: the heartbeat quick_buttons report and set_quick_button_layout
// cover the whole visible sale-screen grid, implicit tiles included.

func qbExec(t *testing.T, dp *common.Deps, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := dp.Db.Exec(s); err != nil {
			t.Fatalf("exec %q: %v", s, err)
		}
	}
}

// reportCodes is the reported barcodes in order. itm1 (Apple, sku ABC) has no
// shortcut_buttons row unless seedQuickButtons ran, so it is an implicit tile.
func reportCodes(t *testing.T, dp *common.Deps) []string {
	t.Helper()
	list, _, ok := remoteQuickButtonsReport(t.Context(), dp)
	if !ok {
		t.Fatal("report not ok")
	}
	out := make([]string, len(list))
	for i, b := range list {
		out[i], _ = b["barcode"].(string)
	}
	return out
}

func shortcutSnapshot(t *testing.T, dp *common.Deps) string {
	t.Helper()
	var s string
	if err := dp.Db.QueryRow(`SELECT COALESCE(group_concat(barcode||':'||sort_order||':'||label||':'||COALESCE(item_id,''),'|'),'') FROM (SELECT * FROM shortcut_buttons ORDER BY barcode)`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRemoteQuickButtonsReport_ImplicitTilesWithNoRows(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	qbExec(t, dp,
		`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('itmB','BAN','Banana',100,'tax_std',1)`,
		`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('itmC','CHE','Cherry',100,'tax_std',1)`)
	list, total, ok := remoteQuickButtonsReport(t.Context(), dp)
	if !ok || total != 3 || len(list) != 3 {
		t.Fatalf("ok=%v total=%d len=%d", ok, total, len(list))
	}
	qbExec(t, dp, `UPDATE items SET color='#112233' WHERE id='itmB'`)
	list, _, _ = remoteQuickButtonsReport(t.Context(), dp)
	want := []struct{ code, label, item, color string }{{"ABC", "Apple", "itm1", ""}, {"BAN", "Banana", "itmB", "#112233"}, {"CHE", "Cherry", "itmC", ""}}
	for i, w := range want {
		b := list[i]
		if b["barcode"] != w.code || b["label"] != w.label || b["sort_order"] != i {
			t.Fatalf("entry %d = %+v, want %+v", i, b, w)
		}
		if b["item_id"] != w.item {
			t.Fatalf("entry %d item_id = %#v, want %q", i, b["item_id"], w.item)
		}
		if c, p := b["color"]; !p || c != w.color {
			t.Fatalf("entry %d color = %#v (present=%v), want %q", i, c, p, w.color)
		}
	}
}

func TestRemoteQuickButtonsReport_MixedExplicitImplicitHiddenRemoved(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	qbExec(t, dp,
		`DELETE FROM item_barcodes`,
		`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('iA','A','Aaa',100,'tax_std',1),('iB','B','Bbb',100,'tax_std',1),('iC','C','Ccc',100,'tax_std',1),('iD','D','Ddd',100,'tax_std',1)`,
		`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active,sell_screen_hidden) VALUES('iH1','H1','Hid1',100,'tax_std',1,1),('iH2','H2','Hid2',100,'tax_std',1,1)`,
		`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active,sell_screen_removed) VALUES('iR','R','Rem',100,'tax_std',1,1)`,
		`INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('B','Bbb','iB',0),('A','Aaa','iA',1),('H1','Hid1','iH1',2)`)
	// itm1 (Apple, ABC) is also an implicit tile; it sorts first by name but
	// implicit tiles come after the explicit rows.
	got := strings.Join(reportCodes(t, dp), ",")
	if got != "B,A,ABC,C,D" {
		t.Fatalf("report = %s, want B,A,ABC,C,D", got)
	}
}

func TestRemoteQuickButtonsReport_5kCatalogBounded(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	tx, err := dp.Db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	st, err := tx.Prepare(`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES (?,?,?,100,'tax_std',1)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4999; i++ {
		name := fmt.Sprintf("Item %05d %s", i, strings.Repeat("n", 24))
		if i == 7 {
			// "A" prefix sorts it into the reported head (an all-"é" name
			// sorts after "Item…" and would fall past the cap untested).
			name = "A" + strings.Repeat("é", 300)
		}
		if _, err := st.Exec(fmt.Sprintf("big%05d", i), fmt.Sprintf("S%05d", i), name); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	list, total, ok := remoteQuickButtonsReport(t.Context(), dp)
	if !ok || len(list) != 500 || total != 5000 {
		t.Fatalf("ok=%v len=%d total=%d, want 500/5000", ok, len(list), total)
	}
	raw, _ := json.Marshal(list)
	if len(raw) > 128<<10 {
		t.Fatalf("report is %d bytes, want <= 128 KiB", len(raw))
	}
	grid, _, err := ui.NewButtonStore(dp.Db).LoadGrid(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500; i++ {
		if list[i]["barcode"] != grid[i].Code {
			t.Fatalf("entry %d = %v, grid has %s", i, list[i]["barcode"], grid[i].Code)
		}
	}
	cut := 0
	for _, b := range list {
		l := b["label"].(string)
		if utf8.RuneCountInString(l) > 200 {
			t.Fatalf("label of %d runes not cut", utf8.RuneCountInString(l))
		}
		if b["barcode"] == "S00007" {
			if !utf8.ValidString(l) || utf8.RuneCountInString(l) != 200 {
				t.Fatalf("long label = %d runes (valid=%v), want 200", utf8.RuneCountInString(l), utf8.ValidString(l))
			}
			cut++
		}
	}
	if cut != 1 {
		t.Fatal("the long-label item must be in the reported head")
	}
}

func TestRemoteQuickButtonsReport_OverBudgetOrReadErrorOmits(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	prev := quickButtonsReportByteBudget
	quickButtonsReportByteBudget = 8
	extra := buildCloudHooks(dp, nil).DeviceExtra(t.Context())
	quickButtonsReportByteBudget = prev
	for _, k := range []string{"quick_buttons", "quick_buttons_total"} {
		if _, p := extra[k]; p {
			t.Fatalf("%s sent over budget", k)
		}
	}
	if _, p := extra["theme"]; !p {
		t.Fatal("the rest of the heartbeat must still be sent")
	}

	qbExec(t, dp, `DROP TABLE shortcut_buttons`)
	extra = buildCloudHooks(dp, nil).DeviceExtra(t.Context())
	for _, k := range []string{"quick_buttons", "quick_buttons_total"} {
		if _, p := extra[k]; p {
			t.Fatalf("%s sent after a read error", k)
		}
	}
	if _, p := extra["theme"]; !p {
		t.Fatal("heartbeat must still build")
	}
}

func TestQuickButtonsReportGate_SendsOnChangeCommitsOnSuccess(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	hooks := buildCloudHooks(dp, nil)
	has := func(e map[string]any) bool {
		_, a := e["quick_buttons"]
		_, b := e["quick_buttons_total"]
		if a != b {
			t.Fatalf("keys disagree: quick_buttons=%v total=%v", a, b)
		}
		return a
	}
	if !has(hooks.DeviceExtra(ctx)) {
		t.Fatal("first beat must carry the report")
	}
	hooks.AfterTick(ctx, true, nil)
	if has(hooks.DeviceExtra(ctx)) {
		t.Fatal("unchanged sell screen after a committed beat must omit the report")
	}
	qbExec(t, dp, `INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('itmN','NEW','New',100,'tax_std',1)`)
	if !has(hooks.DeviceExtra(ctx)) {
		t.Fatal("a catalog change must bring the report back")
	}
	hooks.AfterTick(ctx, false, nil) // did not get through
	if !has(hooks.DeviceExtra(ctx)) {
		t.Fatal("an uncommitted report must be re-sent")
	}
	hooks.AfterTick(ctx, true, nil)
	prev := quickButtonsReportRefresh
	quickButtonsReportRefresh = 0
	t.Cleanup(func() { quickButtonsReportRefresh = prev })
	if !has(hooks.DeviceExtra(ctx)) {
		t.Fatal("refresh period elapsed: report must be sent")
	}
}

func TestCloudSetQuickButtonLayout_ImplicitTileMovesAheadOfExplicit(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	seedQuickButtons(t, dp)
	qbExec(t, dp, `INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('itmB','BAN','Banana',100,'tax_std',1)`)
	if got := strings.Join(reportCodes(t, dp), ","); got != "b1,b2,b3,BAN" {
		t.Fatalf("report = %s", got)
	}
	if _, err := cloudSetQuickButtonLayout(ctx, dp, []string{"BAN", "b1", "b2", "b3"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var label, itemID string
	if err := dp.Db.QueryRow(`SELECT label, item_id FROM shortcut_buttons WHERE barcode='BAN'`).Scan(&label, &itemID); err != nil {
		t.Fatalf("no row materialised for BAN: %v", err)
	}
	if label != "" || itemID != "itmB" {
		t.Fatalf("row = %q/%q", label, itemID)
	}
	fresh, err := ui.NewButtonStore(dp.Db).Load()
	if err != nil {
		t.Fatal(err)
	}
	if fresh[0].Code != "BAN" {
		t.Fatalf("fresh Load first = %s", fresh[0].Code)
	}
	list, _, _ := remoteQuickButtonsReport(ctx, dp)
	if list[0]["barcode"] != "BAN" || list[0]["label"] != "Banana" {
		t.Fatalf("report first = %+v", list[0])
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='quick_button_layout_set'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit rows = %d err=%v", n, err)
	}
}

func TestCloudSetQuickButtonLayout_RefusalsWriteNothing(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	seedQuickButtons(t, dp)
	qbExec(t, dp,
		`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('itmB','BAN','Banana',100,'tax_std',1)`,
		`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active,sell_screen_hidden) VALUES('itmH','HID','Hidden',100,'tax_std',1,1)`)
	before := shortcutSnapshot(t, dp)
	for _, c := range []struct {
		name  string
		codes []string
		want  string
	}{
		{"hidden", []string{"HID", "BAN", "b1", "b2", "b3"}, "HID"},
		{"unknown", []string{"nope", "BAN", "b1", "b2", "b3"}, "nope"},
		{"duplicate", []string{"BAN", "BAN", "b1", "b2", "b3"}, "BAN"},
		{"missing implicit", []string{"b3", "b2", "b1"}, "BAN"},
	} {
		_, err := cloudSetQuickButtonLayout(ctx, dp, c.codes)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want containing %q", c.name, err, c.want)
		}
		if got := shortcutSnapshot(t, dp); got != before {
			t.Errorf("%s: wrote rows\nbefore %s\nafter  %s", c.name, before, got)
		}
	}
}

func TestCloudSetQuickButtonLayout_PastTheCapRefused(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	tx, _ := dp.Db.Begin()
	for i := 0; i < 600; i++ {
		if _, err := tx.Exec(`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES (?,?,?,100,'tax_std',1)`, fmt.Sprintf("p%04d", i), fmt.Sprintf("P%04d", i), fmt.Sprintf("Pitem %04d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	codes := reportCodes(t, dp)
	if len(codes) != 500 {
		t.Fatalf("report len = %d", len(codes))
	}
	tailCodes := func() string {
		g, err := ui.NewButtonStore(dp.Db).Load()
		if err != nil || len(g) != 601 {
			t.Fatalf("Load: %d tiles err=%v", len(g), err)
		}
		return codesOfButtons(g[500:])
	}
	tailBefore := tailCodes()
	before := shortcutSnapshot(t, dp)
	// P0599 is tile 601 (past the cap).
	bad := append([]string{"P0599"}, codes[1:]...)
	if _, err := cloudSetQuickButtonLayout(ctx, dp, bad); err == nil || !strings.Contains(err.Error(), "P0599") {
		t.Fatalf("err = %v", err)
	}
	if shortcutSnapshot(t, dp) != before {
		t.Fatal("refusal wrote rows")
	}
	// A permutation of the reported 500 applies; the 100 past the cap stay implicit.
	codes[0], codes[1] = codes[1], codes[0]
	if _, err := cloudSetQuickButtonLayout(ctx, dp, codes); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var n int
	_ = dp.Db.QueryRow(`SELECT COUNT(*) FROM shortcut_buttons`).Scan(&n)
	if n != 2 {
		t.Fatalf("rows = %d, want 2 (only the touched head)", n)
	}
	// Tiles past the cap stay after the head, in their previous order.
	if got := tailCodes(); got != tailBefore {
		t.Fatalf("tail past the cap moved:\nbefore %s\nafter  %s", tailBefore, got)
	}
	if got := strings.Join(reportCodes(t, dp), ","); got != strings.Join(codes, ",") {
		t.Fatal("next report must show the applied head order")
	}
}

func TestCloudSetQuickButtonLayout_SharedCodeRefused(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	qbExec(t, dp,
		`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('iX','DUP','Xray',100,'tax_std',1),('iY','YSKU','Yankee',100,'tax_std',1)`,
		// Y's barcode equals X's SKU: both tiles carry the code DUP.
		`INSERT INTO item_barcodes(barcode,item_id,is_primary) VALUES('DUP','iY',1)`)
	codes := reportCodes(t, dp)
	if strings.Join(codes, ",") != "ABC,DUP" {
		t.Fatalf("report = %v (shared code listed once)", codes)
	}
	before := shortcutSnapshot(t, dp)
	_, err := cloudSetQuickButtonLayout(ctx, dp, []string{"DUP", "ABC"})
	if err == nil || !strings.Contains(err.Error(), "two sale-screen tiles") {
		t.Fatalf("err = %v", err)
	}
	if shortcutSnapshot(t, dp) != before {
		t.Fatal("refusal wrote rows")
	}
}

func TestCloudSetQuickButtonLayout_DegradedGridRefused(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	qbExec(t, dp, `INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('itmB','BAN','Banana',100,'tax_std',1)`)
	before := shortcutSnapshot(t, dp)
	qbExec(t, dp, `DROP TABLE item_barcodes`)
	if _, _, ok := remoteQuickButtonsReport(ctx, dp); ok {
		t.Fatal("a degraded grid must not be reported")
	}
	_, err := cloudSetQuickButtonLayout(ctx, dp, []string{"BAN", "ABC"})
	if err == nil || !strings.Contains(err.Error(), "try the layout again") {
		t.Fatalf("err = %v", err)
	}
	if shortcutSnapshot(t, dp) != before {
		t.Fatal("refusal wrote rows")
	}
}

func codesOfButtons(bs []ui.Button) string {
	c := make([]string, len(bs))
	for i, b := range bs {
		c[i] = b.Code
	}
	return strings.Join(c, ",")
}

// D1 (Tester, ut-docs#2562): cacheEntitlement rewrites settings on every
// answered sync, and the settings trigger bumps sync_admin_version every
// time, so a gate keyed on it never suppressed. A settings write that does
// not touch a grid input must not bring the report back; one that does
// (the enabled barcode symbologies pick each implicit tile's code) must.
func TestQuickButtonsReportGate_SurvivesPerTickSettingsWrites(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	hooks := buildCloudHooks(dp, nil)
	sent := func() bool { _, ok := hooks.DeviceExtra(ctx)["quick_buttons"]; return ok }
	if !sent() {
		t.Fatal("first beat must carry the report")
	}
	hooks.AfterTick(ctx, true, nil)
	for i := 0; i < 3; i++ {
		qbExec(t, dp, `INSERT INTO settings(key,value,updated_at) VALUES('cloud.link_tier','periodic','x') ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at='y'`)
		if sent() {
			t.Fatalf("beat %d: report re-sent although only an unrelated setting was rewritten", i)
		}
	}
	if err := data.NewSettingsRepo(dp.Db).Set(ctx, data.BarcodeEnabledSymbologiesKey, `["ean13"]`); err != nil {
		t.Fatal(err)
	}
	if !sent() {
		t.Fatal("a change of the enabled barcode symbologies changes tile codes: the report must come back")
	}
}

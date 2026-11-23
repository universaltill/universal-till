package ui

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func qbBtns(specs ...string) []Button {
	// "A" visible, "!H" hidden.
	out := make([]Button, 0, len(specs))
	for _, s := range specs {
		if strings.HasPrefix(s, "!") {
			out = append(out, Button{Code: s[1:], Hidden: true})
			continue
		}
		out = append(out, Button{Code: s})
	}
	return out
}

func codesOf(bs []Button) string {
	c := make([]string, len(bs))
	for i, b := range bs {
		c[i] = b.Code
	}
	return strings.Join(c, ",")
}

func TestReportableTiles_SkipsHiddenDupLongCapsAndCounts(t *testing.T) {
	long := strings.Repeat("x", MaxReportedQuickButtonCodeBytes+1)
	ok64 := strings.Repeat("y", MaxReportedQuickButtonCodeBytes)
	// hidden skipped; dup keeps first; hidden-first dup code drops the later visible one;
	// long skipped but counted; empty code skipped but counted.
	grid := qbBtns("A", "!H", "B", "A", "!S", "S", long, ok64, "")
	head, total := ReportableTiles(grid)
	if got := codesOf(head); got != "A,B,"+ok64 {
		t.Fatalf("head = %q", got)
	}
	// visible tiles: A B A S long ok64 "" = 7
	if total != 7 {
		t.Fatalf("total = %d, want 7", total)
	}

	many := make([]Button, 600)
	for i := range many {
		many[i] = Button{Code: fmt.Sprintf("c%03d", i)}
	}
	head, total = ReportableTiles(many)
	if len(head) != MaxReportedQuickButtons || total != 600 {
		t.Fatalf("len(head)=%d total=%d, want 500/600", len(head), total)
	}
	if head[0].Code != "c000" || head[499].Code != "c499" {
		t.Fatalf("head not the first 500 in order")
	}
}

func TestReportLabel_CutsWholeRunes(t *testing.T) {
	s := strings.Repeat("é", 300)
	got := ReportLabel(s)
	if n := len([]rune(got)); n != MaxReportedQuickButtonLabelRunes {
		t.Fatalf("runes = %d", n)
	}
	if ReportLabel("short") != "short" {
		t.Fatal("short label changed")
	}
}

func TestLoadGrid_ErrorsAndDegradedAreSurfaced(t *testing.T) {
	db := setupFullTestDB(t)
	t.Cleanup(func() { db.Close() })
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES ('i1','S1','One',100,1)`)
	store := NewButtonStore(db)
	grid, degraded, err := store.LoadGrid(context.Background())
	if err != nil || degraded || len(grid) != 1 {
		t.Fatalf("healthy: grid=%d degraded=%v err=%v", len(grid), degraded, err)
	}

	mustExec(t, db, `DROP TABLE item_barcodes`)
	_, degraded, err = store.LoadGrid(context.Background())
	if err != nil || !degraded {
		t.Fatalf("barcodes lookup down: degraded=%v err=%v, want degraded", degraded, err)
	}
	if _, err := store.ApplyReportedLayout(context.Background(), []string{"S1"}); !errors.Is(err, ErrLayoutGridDegraded) {
		t.Fatalf("ApplyReportedLayout on degraded grid = %v, want ErrLayoutGridDegraded", err)
	}

	mustExec(t, db, `DROP TABLE items`)
	if _, _, err := store.LoadGrid(context.Background()); err == nil {
		t.Fatal("items table gone: LoadGrid must return the error, not swallow it")
	}
}

func seedManyItems(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	st, err := tx.Prepare(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES (?,?,?,100,1)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := st.Exec(fmt.Sprintf("id%05d", i), fmt.Sprintf("S%05d", i), fmt.Sprintf("Item %05d", i)); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyReportedLayout_5kCatalogMaterialisesOnlyTheTouchedHead(t *testing.T) {
	db := setupFullTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedManyItems(t, db, 5000)
	store := NewButtonStore(db)
	ctx := context.Background()

	before, _, err := store.LoadGrid(ctx)
	if err != nil {
		t.Fatal(err)
	}
	head, total := ReportableTiles(before)
	if len(head) != 500 || total != 5000 {
		t.Fatalf("head=%d total=%d", len(head), total)
	}
	codes := make([]string, len(head))
	for i, b := range head {
		codes[i] = b.Code
	}
	codes[0], codes[9] = codes[9], codes[0]
	n, err := store.ApplyReportedLayout(ctx, codes)
	if err != nil || n != 500 {
		t.Fatalf("apply: n=%d err=%v", n, err)
	}

	var rows, nonEmpty, distinct int
	if err := db.QueryRow(`SELECT COUNT(*), SUM(label<>'' OR item_id IS NULL OR item_id=''), COUNT(DISTINCT sort_order) FROM shortcut_buttons`).Scan(&rows, &nonEmpty, &distinct); err != nil {
		t.Fatal(err)
	}
	if rows != 10 || nonEmpty != 0 || distinct != 10 {
		t.Fatalf("rows=%d bad=%d distinctSort=%d, want 10/0/10", rows, nonEmpty, distinct)
	}
	after, _, err := store.LoadGrid(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after[0].Code != before[9].Code || after[9].Code != before[0].Code {
		t.Fatalf("swap not applied: %s %s", after[0].Code, after[9].Code)
	}
	for i := 10; i < 5000; i++ {
		if after[i].Code != before[i].Code {
			t.Fatalf("position %d moved: %s != %s", i, after[i].Code, before[i].Code)
		}
	}
}

func TestApplyReportedLayout_Refusals(t *testing.T) {
	db := setupFullTestDB(t)
	t.Cleanup(func() { db.Close() })
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active, sell_screen_hidden) VALUES ('h','HID','Hidden',100,1,1)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES ('a','A','Apple',100,1),('b','B','Banana',100,1),('c1','DUP','Cherry',100,1),('c2','DUP','Cherry2',100,1)`)
	store := NewButtonStore(db)
	ctx := context.Background()
	snap := func() string {
		var s string
		_ = db.QueryRow(`SELECT COALESCE(group_concat(barcode||':'||sort_order||':'||label,'|'),'') FROM shortcut_buttons`).Scan(&s)
		return s
	}
	for _, c := range []struct {
		name  string
		codes []string
		want  string
	}{
		{"hidden", []string{"HID", "B", "A", "DUP"}, "is hidden"},
		{"unknown", []string{"ZZZ", "B", "A", "DUP"}, "ZZZ"},
		{"duplicate", []string{"A", "A", "B", "DUP"}, "duplicate barcode"},
		{"missing", []string{"B", "A"}, "missing barcode"},
		{"shared", []string{"DUP", "A", "B"}, "two sale-screen tiles"},
	} {
		_, err := store.ApplyReportedLayout(ctx, c.codes)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want containing %q", c.name, err, c.want)
		}
		if got := snap(); got != "" {
			t.Errorf("%s: wrote rows: %s", c.name, got)
		}
	}
	// Shared code refusal wraps ErrSharedTileCode.
	if _, err := store.ApplyReportedLayout(ctx, []string{"DUP", "A", "B"}); !errors.Is(err, ErrSharedTileCode) {
		t.Errorf("shared: err = %v, want ErrSharedTileCode", err)
	}
}

// ut-docs#6708: the till's own reorder (UpdateOrder / UpdateOrderSubset) keys
// rows by code, so a shared code in the written prefix is refused, like
// ApplyReportedLayout and UpdateCategoryOrder.
func TestUpdateOrder_SharedCode6708(t *testing.T) {
	db := setupFullTestDB(t)
	t.Cleanup(func() { db.Close() })
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES ('a','A','Apple',100,1),('b','B','Banana',100,1),('c1','DUP','Cherry',100,1),('c2','DUP','Cherry2',100,1)`)
	store := NewButtonStore(db)
	ctx := context.Background()
	snap := func() string {
		var s string
		_ = db.QueryRow(`SELECT COALESCE(group_concat(barcode||':'||sort_order,'|'),'') FROM shortcut_buttons`).Scan(&s)
		return s
	}
	cur, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := codesOf(cur); got != "A,B,DUP,DUP" {
		t.Fatalf("fixture order = %s", got)
	}

	t.Run("full reorder moving a shared code is refused", func(t *testing.T) {
		err := store.UpdateOrder(ctx, []string{"DUP", "A", "B", "DUP"})
		if !errors.Is(err, ErrSharedTileCode) || !strings.Contains(err.Error(), "DUP") {
			t.Fatalf("err = %v, want ErrSharedTileCode naming DUP", err)
		}
		if got := snap(); got != "" {
			t.Fatalf("wrote rows: %s", got)
		}
	})
	t.Run("subset reorder moving a shared code is refused", func(t *testing.T) {
		err := store.UpdateOrderSubset(ctx, []string{"DUP", "A"})
		if !errors.Is(err, ErrSharedTileCode) {
			t.Fatalf("err = %v, want ErrSharedTileCode", err)
		}
		if got := snap(); got != "" {
			t.Fatalf("wrote rows: %s", got)
		}
	})
	t.Run("shared codes only in the untouched tail still reorder", func(t *testing.T) {
		if err := store.UpdateOrder(ctx, []string{"B", "A", "DUP", "DUP"}); err != nil {
			t.Fatalf("err = %v", err)
		}
		after, _ := store.Load()
		if got := codesOf(after); got != "B,A,DUP,DUP" {
			t.Fatalf("order = %s", got)
		}
	})
	t.Run("no-op reorder is fine", func(t *testing.T) {
		if err := store.UpdateOrder(ctx, []string{"B", "A", "DUP", "DUP"}); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
}

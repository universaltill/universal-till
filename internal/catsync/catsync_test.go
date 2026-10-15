package catsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pos"
)

const testSource = "com.example.import.api"

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "catsync-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	d, err := db.Open(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d.DB
}

func rows(t *testing.T, rr ...string) []json.RawMessage {
	t.Helper()
	out := make([]json.RawMessage, len(rr))
	for i, r := range rr {
		out[i] = json.RawMessage(r)
	}
	return out
}

func opts(t *testing.T, sqlDB *sql.DB, commit bool) Options {
	t.Helper()
	loc, err := pos.ResolveStockLocationID(context.Background(), sqlDB, "")
	if err != nil {
		t.Fatalf("location: %v", err)
	}
	return Options{Source: testSource, Commit: commit, LocationID: loc, ActorID: "", Stock: true}
}

func run(t *testing.T, sqlDB *sql.DB, commit bool, rr ...string) Result {
	t.Helper()
	res, err := Run(context.Background(), sqlDB, rows(t, rr...), opts(t, sqlDB, commit))
	if err != nil {
		t.Fatalf("Run(commit=%v): %v", commit, err)
	}
	return res
}

func count(t *testing.T, sqlDB *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := sqlDB.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func wantCounts(t *testing.T, got Result, newN, updated, unchanged, issues int) {
	t.Helper()
	if got.New != newN || got.Updated != updated || got.Unchanged != unchanged || got.Issues != issues {
		t.Fatalf("counts new/updated/unchanged/issues = %d/%d/%d/%d, want %d/%d/%d/%d (samples %+v)",
			got.New, got.Updated, got.Unchanged, got.Issues, newN, updated, unchanged, issues, got.Samples)
	}
}

func linkedItem(t *testing.T, sqlDB *sql.DB, ext string) string {
	t.Helper()
	id, ok, err := data.NewImportSourceLinkRepo().LocalID(context.Background(), sqlDB, testSource, "items", ext)
	if err != nil || !ok {
		t.Fatalf("no link for %s: %v", ext, err)
	}
	return id
}

const coffee = `{"external_id":"p-1","name":"Flat white","sku":"FW-1","barcodes":["5000000000017"],"price_minor":320,"category_path":["Drinks","Hot"],"vat_rate_bp":2000,"stock_qty":12,"active":true}`

// Create: an unlinked row with no match creates the item with its nested
// category, tax code, barcode and opening stock, and records the link.
func TestRun_CreateNewItem(t *testing.T) {
	sqlDB := openDB(t)
	wantCounts(t, run(t, sqlDB, true, coffee), 1, 0, 0, 0)

	id := linkedItem(t, sqlDB, "p-1")
	cat := data.NewCatalogRepo(sqlDB)
	st, ok, err := cat.SyncItemState(context.Background(), sqlDB, id)
	if err != nil || !ok {
		t.Fatalf("item: %v %v", ok, err)
	}
	if st.Name != "Flat white" || st.BasePrice != 320 || st.SKU != "FW-1" || !st.IsActive {
		t.Fatalf("item = %+v", st)
	}
	if !st.HasTax || st.TaxRateBP != 2000 {
		t.Fatalf("tax = %v %d, want 2000", st.HasTax, st.TaxRateBP)
	}
	if len(st.Barcodes) != 1 || st.Barcodes[0] != "5000000000017" {
		t.Fatalf("barcodes = %v", st.Barcodes)
	}
	if n := count(t, sqlDB, `SELECT COUNT(*) FROM categories c JOIN categories p ON p.id = c.parent_id WHERE c.id = ? AND c.name = 'Hot' AND p.name = 'Drinks' AND p.parent_id IS NULL`, st.CategoryID); n != 1 {
		t.Fatalf("category is not Drinks > Hot")
	}
	qty, _, err := data.NewPOSRepo(sqlDB).CurrentQty(context.Background(), nil, opts(t, sqlDB, true).LocationID, id, "")
	if err != nil || qty != 12 {
		t.Fatalf("stock = %v (%v), want 12", qty, err)
	}
	if n := count(t, sqlDB, `SELECT COUNT(*) FROM audit_log WHERE entity_type = 'inventory' AND data_json LIKE '%import_sync%'`); n != 1 {
		t.Fatalf("opening stock movements with reason import_sync = %d, want 1", n)
	}
}

// Re-running the same batch changes nothing and writes nothing.
func TestRun_ReRunIsUnchanged(t *testing.T) {
	sqlDB := openDB(t)
	run(t, sqlDB, true, coffee)
	before := count(t, sqlDB, `SELECT COUNT(*) FROM stock_movements`)
	updatedAt := count(t, sqlDB, `SELECT COUNT(*) FROM import_source_links`)
	wantCounts(t, run(t, sqlDB, false, coffee), 0, 0, 1, 0)
	wantCounts(t, run(t, sqlDB, true, coffee), 0, 0, 1, 0)
	if n := count(t, sqlDB, `SELECT COUNT(*) FROM items`); n != 1 {
		t.Fatalf("items = %d, want 1 (no duplicate)", n)
	}
	if n := count(t, sqlDB, `SELECT COUNT(*) FROM stock_movements`); n != before {
		t.Fatalf("stock movements %d -> %d on an unchanged re-run", before, n)
	}
	if n := count(t, sqlDB, `SELECT COUNT(*) FROM import_source_links`); n != updatedAt {
		t.Fatalf("links changed on re-run")
	}
}

// A linked row updates the item: price (with price history), name,
// active, and stock through an import_sync adjust movement.
func TestRun_UpdateLinkedItem(t *testing.T) {
	sqlDB := openDB(t)
	run(t, sqlDB, true, coffee)
	id := linkedItem(t, sqlDB, "p-1")
	changed := strings.NewReplacer(`"price_minor":320`, `"price_minor":350`, `"stock_qty":12`, `"stock_qty":7`, `"Flat white"`, `"Flat White"`).Replace(coffee)

	prev := run(t, sqlDB, false, changed)
	wantCounts(t, prev, 0, 1, 0, 0)
	if st, _, _ := data.NewCatalogRepo(sqlDB).SyncItemState(context.Background(), sqlDB, id); st.BasePrice != 320 {
		t.Fatalf("preview wrote the price: %d", st.BasePrice)
	}

	res := run(t, sqlDB, true, changed)
	wantCounts(t, res, 0, 1, 0, 0)
	if got := strings.Join(res.Samples[0].Changes, ","); got != "name,price,stock" {
		t.Fatalf("changes = %q, want name,price,stock", got)
	}
	st, _, _ := data.NewCatalogRepo(sqlDB).SyncItemState(context.Background(), sqlDB, id)
	if st.BasePrice != 350 || st.Name != "Flat White" {
		t.Fatalf("item after update = %+v", st)
	}
	if n := count(t, sqlDB, `SELECT COUNT(*) FROM price_history WHERE item_id = ? AND price = 350`, id); n != 1 {
		t.Fatalf("price change not in price_history")
	}
	qty, _, _ := data.NewPOSRepo(sqlDB).CurrentQty(context.Background(), nil, opts(t, sqlDB, true).LocationID, id, "")
	if qty != 7 {
		t.Fatalf("stock = %v, want 7", qty)
	}
	if n := count(t, sqlDB, `SELECT COUNT(*) FROM stock_movements WHERE item_id = ? AND type = 'adjust' AND quantity = -5`, id); n != 1 {
		t.Fatalf("expected one adjust movement of -5")
	}
	// Deactivation follows the source.
	wantCounts(t, run(t, sqlDB, true, strings.Replace(changed, `"active":true`, `"active":false`, 1)), 0, 1, 0, 0)
	if st, _, _ := data.NewCatalogRepo(sqlDB).SyncItemState(context.Background(), sqlDB, id); st.IsActive {
		t.Fatal("item still active")
	}
}

func seedItem(t *testing.T, sqlDB *sql.DB, name, sku, barcode string) string {
	t.Helper()
	cat := data.NewCatalogRepo(sqlDB)
	id, err := cat.CreateItem(context.Background(), catalogtypes.ItemInput{Name: name, SKU: sku, BasePrice: 100, IsActive: true})
	if err != nil {
		t.Fatal(err)
	}
	if barcode != "" {
		if err := cat.AddBarcode(context.Background(), catalogtypes.BarcodeInput{ItemID: id, Barcode: barcode, IsPrimary: true}); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func TestRun_LinkByBarcode(t *testing.T) {
	sqlDB := openDB(t)
	existing := seedItem(t, sqlDB, "Local name", "LOCAL-1", "5000000000017")
	res := run(t, sqlDB, true, coffee)
	wantCounts(t, res, 0, 1, 0, 0)
	if got := linkedItem(t, sqlDB, "p-1"); got != existing {
		t.Fatalf("linked to %s, want the barcode match %s", got, existing)
	}
	if n := count(t, sqlDB, `SELECT COUNT(*) FROM items`); n != 1 {
		t.Fatalf("items = %d, want 1", n)
	}
	if st, _, _ := data.NewCatalogRepo(sqlDB).SyncItemState(context.Background(), sqlDB, existing); st.Name != "Flat white" || st.BasePrice != 320 {
		t.Fatalf("matched item not updated: %+v", st)
	}
	wantCounts(t, run(t, sqlDB, true, coffee), 0, 0, 1, 0)
}

func TestRun_LinkBySKU(t *testing.T) {
	sqlDB := openDB(t)
	existing := seedItem(t, sqlDB, "Local name", "FW-1", "")
	wantCounts(t, run(t, sqlDB, true, coffee), 0, 1, 0, 0)
	if got := linkedItem(t, sqlDB, "p-1"); got != existing {
		t.Fatalf("linked to %s, want the SKU match %s", got, existing)
	}
	if n := count(t, sqlDB, `SELECT COUNT(*) FROM items`); n != 1 {
		t.Fatalf("items = %d, want 1", n)
	}
}

// A link whose local item is gone is recreated as new and re-pointed.
func TestRun_DeletedLocalItemIsRecreated(t *testing.T) {
	sqlDB := openDB(t)
	tx, _ := sqlDB.Begin()
	if err := data.NewImportSourceLinkRepo().Upsert(context.Background(), tx, testSource, "items", "p-1", "gone-item"); err != nil {
		t.Fatal(err)
	}
	_ = tx.Commit()
	wantCounts(t, run(t, sqlDB, true, coffee), 1, 0, 0, 0)
	if got := linkedItem(t, sqlDB, "p-1"); got == "gone-item" {
		t.Fatal("link still points at the deleted item")
	}
}

// Bad rows become per-row issues; good rows in the same batch still apply.
func TestRun_BadRowsAreIssues(t *testing.T) {
	sqlDB := openDB(t)
	res := run(t, sqlDB, true,
		`{"external_id":"ok","name":"Tea","price_minor":200}`,
		`{"name":"No id","price_minor":1}`,
		`{"external_id":"x2","price_minor":1}`,
		`{"external_id":"x3","name":"Negative","price_minor":-1}`,
		`{"external_id":"x4","name":"No price"}`,
		`{"external_id":"x5","name":"VAT","price_minor":1,"vat_rate_bp":10001}`,
		`{"external_id":"ok","name":"Duplicate id","price_minor":1}`,
		`not json`,
		`{"external_id":"x6","name":"Bad barcode","price_minor":1,"barcodes":["no spaces allowed"]}`,
	)
	wantCounts(t, res, 1, 0, 0, 8)
	if n := count(t, sqlDB, `SELECT COUNT(*) FROM items`); n != 1 {
		t.Fatalf("items = %d, want 1", n)
	}
	for _, s := range res.Samples {
		if s.Action == ActionIssue && s.Issue == "" {
			t.Fatalf("issue sample without a reason: %+v", s)
		}
	}
}

// A linked row naming a barcode another item carries is an issue, not a
// silent re-assignment.
func TestRun_BarcodeOwnedByAnotherItemIsIssue(t *testing.T) {
	sqlDB := openDB(t)
	run(t, sqlDB, true, coffee)
	seedItem(t, sqlDB, "Other", "OTHER", "4006381333931")
	res := run(t, sqlDB, true, strings.Replace(coffee, `["5000000000017"]`, `["5000000000017","4006381333931"]`, 1))
	wantCounts(t, res, 0, 0, 0, 1)
}

// Preview writes nothing, even when a commit would create categories,
// tax codes, items, links and stock.
func TestRun_PreviewWritesNothing(t *testing.T) {
	sqlDB := openDB(t)
	tables := []string{"items", "categories", "tax_codes", "item_barcodes", "import_source_links", "stock_movements", "inventory", "audit_log", "price_history"}
	before := map[string]int{}
	for _, tb := range tables {
		before[tb] = count(t, sqlDB, `SELECT COUNT(*) FROM `+tb)
	}
	res := run(t, sqlDB, false, coffee, `{"external_id":"p-2","name":"Scone","price_minor":250,"category_path":["Bakery"],"vat_rate_bp":1234,"stock_qty":3}`)
	wantCounts(t, res, 2, 0, 0, 0)
	for _, tb := range tables {
		if n := count(t, sqlDB, `SELECT COUNT(*) FROM `+tb); n != before[tb] {
			t.Fatalf("preview changed %s: %d -> %d", tb, before[tb], n)
		}
	}
}

// Without the stock entity resolved, stock_qty is not applied.
func TestRun_StockNotAppliedWithoutStockEntity(t *testing.T) {
	sqlDB := openDB(t)
	o := opts(t, sqlDB, true)
	o.Stock = false
	if _, err := Run(context.Background(), sqlDB, rows(t, coffee), o); err != nil {
		t.Fatal(err)
	}
	if n := count(t, sqlDB, `SELECT COUNT(*) FROM stock_movements`); n != 0 {
		t.Fatalf("stock movements = %d, want 0", n)
	}
}

func TestRun_SamplesAreBounded(t *testing.T) {
	sqlDB := openDB(t)
	var rr []string
	for i := 0; i < MaxSamples+20; i++ {
		rr = append(rr, `{"external_id":"e`+strings.Repeat("x", i+1)+`","name":"N","price_minor":1}`)
	}
	res := run(t, sqlDB, false, rr...)
	if res.New != MaxSamples+20 || len(res.Samples) != MaxSamples {
		t.Fatalf("new=%d samples=%d", res.New, len(res.Samples))
	}
}

func TestRun_RequiresSource(t *testing.T) {
	sqlDB := openDB(t)
	o := opts(t, sqlDB, false)
	o.Source = ""
	if _, err := Run(context.Background(), sqlDB, rows(t, coffee), o); err == nil {
		t.Fatal("a run without a core-stamped source must be refused")
	}
}

func chunkRows(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf(`{"external_id":"c-%d","name":"Item %d","sku":"CH-%d","price_minor":100,"active":true}`, i, i, i)
	}
	return out
}

// A commit holds SQLite's write lock only per chunk, never for the whole
// run, so a sale on the same till never waits behind a big import (a
// 20k-row single transaction would outlast the 5 s busy timeout). Each
// chunk is visible to other connections as soon as it commits.
func TestRun_CommitsInChunks(t *testing.T) {
	sqlDB := openDB(t)
	defer func(n int, h func()) { commitChunk, afterChunkCommit = n, h }(commitChunk, afterChunkCommit)
	commitChunk = 2
	var seen []int
	afterChunkCommit = func() {
		seen = append(seen, count(t, sqlDB, `SELECT COUNT(*) FROM import_source_links`))
	}
	wantCounts(t, run(t, sqlDB, true, chunkRows(5)...), 5, 0, 0, 0)
	if len(seen) != 3 || seen[0] != 2 || seen[1] != 4 || seen[2] != 5 {
		t.Fatalf("links visible after each chunk commit = %v, want [2 4 5]", seen)
	}
}

// A run that fails part-way keeps the chunks it already committed and
// reports their counts; because every applied row is linked, the next
// run finishes the job without duplicating anything.
func TestRun_FailedRunKeepsCommittedChunksAndHealsOnReRun(t *testing.T) {
	sqlDB := openDB(t)
	defer func(n int, h func()) { commitChunk, afterChunkCommit = n, h }(commitChunk, afterChunkCommit)
	commitChunk = 2
	ctx, cancel := context.WithCancel(context.Background())
	afterChunkCommit = cancel
	rr := chunkRows(5)
	res, err := Run(ctx, sqlDB, rows(t, rr...), opts(t, sqlDB, true))
	if err == nil {
		t.Fatal("cancelled run returned no error")
	}
	wantCounts(t, res, 2, 0, 0, 0)
	if n := count(t, sqlDB, `SELECT COUNT(*) FROM import_source_links`); n != 2 {
		t.Fatalf("links after failed run = %d, want 2 (the committed chunk)", n)
	}
	afterChunkCommit = nil
	wantCounts(t, run(t, sqlDB, true, rr...), 3, 0, 2, 0)
	if n := count(t, sqlDB, `SELECT COUNT(*) FROM import_source_links`); n != 5 {
		t.Fatalf("links after re-run = %d, want 5", n)
	}
}

// A row that fails mid-apply rolls back its own savepoint — including a
// category and tax code it just created — so the run's caches must not
// keep pointing at them, or every later row in that category fails too.
func TestRun_FailedRowDoesNotPoisonCategoryOrTaxCache(t *testing.T) {
	sqlDB := openDB(t)
	defer func(h func(string) error) { applyFault = h }(applyFault)
	applyFault = func(ext string) error {
		if ext == "f-1" {
			return fmt.Errorf("injected apply failure")
		}
		return nil
	}
	first := `{"external_id":"f-1","name":"Clipper A","sku":"CL-A","price_minor":5000,"category_path":["Clippers","Cordless"],"vat_rate_bp":1750,"active":true}`
	second := `{"external_id":"f-2","name":"Clipper B","sku":"CL-B","price_minor":6000,"category_path":["Clippers","Cordless"],"vat_rate_bp":1750,"active":true}`
	wantCounts(t, run(t, sqlDB, true, first, second), 1, 0, 0, 1)
	st, ok, err := data.NewCatalogRepo(sqlDB).SyncItemState(context.Background(), sqlDB, linkedItem(t, sqlDB, "f-2"))
	if err != nil || !ok {
		t.Fatalf("item f-2: %v %v", ok, err)
	}
	if n := count(t, sqlDB, `SELECT COUNT(*) FROM categories WHERE id = ? AND name = 'Cordless'`, st.CategoryID); n != 1 {
		t.Fatalf("f-2 category %q does not exist (stale cache after a rolled-back row)", st.CategoryID)
	}
	if !st.HasTax || st.TaxRateBP != 1750 {
		t.Fatalf("f-2 tax = %v %d, want 1750", st.HasTax, st.TaxRateBP)
	}
}

// The write lock is per chunk of *processed* rows, not applied ones: a
// steady-state re-run where every row is unchanged must still release it,
// or one long BEGIN IMMEDIATE outlasts the busy timeout and fails a sale.
func TestRun_UnchangedReRunStillCommitsInChunks(t *testing.T) {
	sqlDB := openDB(t)
	rr := chunkRows(5)
	wantCounts(t, run(t, sqlDB, true, rr...), 5, 0, 0, 0)
	defer func(n int, h func()) { commitChunk, afterChunkCommit = n, h }(commitChunk, afterChunkCommit)
	commitChunk = 2
	chunks := 0
	afterChunkCommit = func() { chunks++ }
	wantCounts(t, run(t, sqlDB, true, rr...), 0, 0, 5, 0)
	if chunks < 3 {
		t.Fatalf("unchanged re-run committed %d chunks, want 3 (lock held for the whole run)", chunks)
	}
}

// A row rolled back in apply must give back its in-run claims, or a later
// valid row carrying the same barcode is a false "more than one row" issue.
func TestRun_FailedRowReleasesItsClaims(t *testing.T) {
	sqlDB := openDB(t)
	defer func(h func(string) error) { applyFault = h }(applyFault)
	applyFault = func(ext string) error {
		if ext == "a" {
			return fmt.Errorf("injected apply failure")
		}
		return nil
	}
	first := `{"external_id":"a","name":"Trimmer","sku":"TR-1","barcodes":["5000000000017"],"price_minor":2500,"active":true}`
	retry := `{"external_id":"a2","name":"Trimmer","sku":"TR-1","barcodes":["5000000000017"],"price_minor":2500,"active":true}`
	wantCounts(t, run(t, sqlDB, true, first, retry), 1, 0, 0, 1)
	linkedItem(t, sqlDB, "a2")
}

// A barcode added to an item that already has one never becomes a second
// primary (lookups ORDER BY is_primary would then pick arbitrarily).
func TestRun_AddedBarcodeOnUpdateIsNotASecondPrimary(t *testing.T) {
	sqlDB := openDB(t)
	wantCounts(t, run(t, sqlDB, true, coffee), 1, 0, 0, 0)
	more := strings.Replace(coffee, `"barcodes":["5000000000017"]`, `"barcodes":["5000000000024"]`, 1)
	wantCounts(t, run(t, sqlDB, true, more), 0, 1, 0, 0)
	if n := count(t, sqlDB, `SELECT COUNT(*) FROM item_barcodes WHERE item_id = ? AND is_primary = 1`, linkedItem(t, sqlDB, "p-1")); n != 1 {
		t.Fatalf("primary barcodes = %d, want 1", n)
	}
}

// ZWNJ is part of correct Persian spelling; bidi overrides stay refused.
func TestRun_NameAllowsZWNJButNotBidiOverride(t *testing.T) {
	sqlDB := openDB(t)
	zwnj := `{"external_id":"z-1","name":"می‌خواهم","price_minor":100,"active":true}`
	bidi := `{"external_id":"z-2","name":"abc‮def","price_minor":100,"active":true}`
	wantCounts(t, run(t, sqlDB, true, zwnj, bidi), 1, 0, 0, 1)
}

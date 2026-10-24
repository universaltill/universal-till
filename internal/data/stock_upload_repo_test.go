package data

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
)

// ADR-0169 §8, ut-docs#4763: the main till's stock upload bookkeeping
// (migration 091). Real migrated database: the triggers are the contract.

type stockUploadFixture struct {
	d    *db.DB
	repo *StockUploadRepo
	loc  string
}

func newStockUploadFixture(t *testing.T) *stockUploadFixture {
	t.Helper()
	d := openMigratedDB(t, "stock-upload.db")
	f := &stockUploadFixture{d: d, repo: NewStockUploadRepo(d.DB)}
	if err := d.QueryRow(`SELECT id FROM stock_locations ORDER BY id LIMIT 1`).Scan(&f.loc); err != nil {
		t.Fatal(err)
	}
	// The migration backfills nothing on a fresh database; start clean anyway.
	mustExec(t, d, `DELETE FROM stock_upload_outbox`)
	return f
}

func (f *stockUploadFixture) item(t *testing.T, id, cloudID string) {
	t.Helper()
	mustExec(t, f.d, `INSERT INTO items (id, name, base_price) VALUES (?, ?, 100)`, id, id)
	if cloudID != "" {
		mustExec(t, f.d, `INSERT INTO cloud_links (entity, cloud_id, local_id, version, updated_at) VALUES ('item', ?, ?, 1, 'x')`, cloudID, id)
	}
}

func (f *stockUploadFixture) variant(t *testing.T, id, itemID, cloudID string) {
	t.Helper()
	mustExec(t, f.d, `INSERT INTO item_variants (id, item_id, name, price) VALUES (?, ?, ?, 100)`, id, itemID, id)
	if cloudID != "" {
		mustExec(t, f.d, `INSERT INTO cloud_links (entity, cloud_id, local_id, version, updated_at) VALUES ('item_variant', ?, ?, 1, 'x')`, cloudID, id)
	}
}

func (f *stockUploadFixture) setStock(t *testing.T, itemID, variantID string, qty float64) {
	t.Helper()
	res, err := f.d.Exec(`UPDATE inventory SET quantity = ? WHERE location_id = ? AND COALESCE(item_id,'') = ? AND COALESCE(variant_id,'') = ?`,
		qty, f.loc, itemID, variantID)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		mustExec(t, f.d, `INSERT INTO inventory (id, item_id, variant_id, location_id, quantity) VALUES (?, ?, ?, ?, ?)`,
			itemID+variantID+"-inv", nullIfEmpty(itemID), nullIfEmpty(variantID), f.loc, qty)
	}
}

func (f *stockUploadFixture) marks(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.d.QueryRow(`SELECT COUNT(*) FROM stock_upload_outbox`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *stockUploadFixture) pending(t *testing.T) []StockUploadRow {
	t.Helper()
	rows, err := f.repo.Pending(context.Background(), 0, time.Now(), 100)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

const (
	cloudItemA = "11111111-1111-4111-8111-111111111111"
	cloudItemB = "22222222-2222-4222-8222-222222222222"
	cloudVarB1 = "33333333-3333-4333-8333-333333333333"
)

func TestStockUploadTriggers_MarkEveryQuantityChangeOnceAndOnlyRealChanges(t *testing.T) {
	f := newStockUploadFixture(t)
	f.item(t, "a", cloudItemA)
	f.setStock(t, "a", "", 5)
	if f.marks(t) != 1 {
		t.Fatalf("inserting inventory left %d marks, want 1", f.marks(t))
	}
	first := f.pending(t)
	if len(first) != 1 || first[0].OnHandMilli != 5000 || first[0].ItemCloudID != cloudItemA || first[0].VariantCloudID != "" {
		t.Fatalf("pending = %+v", first)
	}
	// Same quantity written again: no new mark, same seq.
	f.setStock(t, "a", "", 5)
	if again := f.pending(t); len(again) != 1 || again[0].Seq != first[0].Seq {
		t.Fatalf("a no-op write re-marked: %+v then %+v", first, again)
	}
	// A change replaces the mark under a higher seq; the table stays one row per inventory row.
	f.setStock(t, "a", "", 4.25)
	f.setStock(t, "a", "", 3.5)
	got := f.pending(t)
	if f.marks(t) != 1 || len(got) != 1 || got[0].Seq <= first[0].Seq || got[0].OnHandMilli != 3500 {
		t.Fatalf("after two changes: marks=%d pending=%+v (first seq %d)", f.marks(t), got, first[0].Seq)
	}
	// An update of another column of the inventory row is not a stock change.
	mustExec(t, f.d, `UPDATE inventory SET reorder_level = 9 WHERE location_id = ?`, f.loc)
	if again := f.pending(t); again[0].Seq != got[0].Seq {
		t.Fatalf("a reorder-level edit re-marked the row")
	}
}

func TestStockUploadPending_VariantRowsCarryTheirItemAndNeedBothLinks(t *testing.T) {
	f := newStockUploadFixture(t)
	f.item(t, "b", cloudItemB)
	f.variant(t, "b1", "b", cloudVarB1)
	f.variant(t, "b2", "b", "") // no cloud link yet
	f.item(t, "c", "")          // no cloud link yet
	f.setStock(t, "", "b1", 2)
	f.setStock(t, "", "b2", 3)
	f.setStock(t, "c", "", 4)
	got := f.pending(t)
	if len(got) != 1 {
		t.Fatalf("pending = %+v, want only the fully linked variant row", got)
	}
	if got[0].ItemCloudID != cloudItemB || got[0].VariantCloudID != cloudVarB1 || got[0].OnHandMilli != 2000 {
		t.Fatalf("variant row = %+v", got[0])
	}
	if f.marks(t) != 3 {
		t.Fatalf("unlinked rows must keep their marks, have %d", f.marks(t))
	}
	// The link appears (the catalogue upload answered): the waiting row is now owed.
	mustExec(t, f.d, `INSERT INTO cloud_links (entity, cloud_id, local_id, version, updated_at) VALUES ('item', ?, 'c', 1, 'x')`, "44444444-4444-4444-8444-444444444444")
	if got := f.pending(t); len(got) != 2 {
		t.Fatalf("after linking: %+v", got)
	}
}

func TestStockUploadPending_QuantitiesAreIntegerThousandthsAndClamped(t *testing.T) {
	f := newStockUploadFixture(t)
	f.item(t, "a", cloudItemA)
	for _, tc := range []struct {
		qty  float64
		want int64
	}{
		{12.5, 12500}, {0.001, 1}, {0.0004, 0}, {-2, -2000}, {1.0005, 1001}, {0.1 + 0.2, 300},
		{1e18, StockUploadMaxMilli}, {-1e18, -StockUploadMaxMilli},
	} {
		f.setStock(t, "a", "", tc.qty)
		if got := f.pending(t); len(got) != 1 || got[0].OnHandMilli != tc.want {
			t.Errorf("quantity %v -> %+v, want %d", tc.qty, got, tc.want)
		}
	}
}

func TestStockUploadAck_RemovesOnlyTheAnsweredSequence(t *testing.T) {
	f := newStockUploadFixture(t)
	f.item(t, "a", cloudItemA)
	f.setStock(t, "a", "", 1)
	sent := f.pending(t)[0]
	f.setStock(t, "a", "", 2) // sold between the read and the answer
	if err := f.repo.Ack(context.Background(), []int64{sent.Seq}); err != nil {
		t.Fatal(err)
	}
	got := f.pending(t)
	if len(got) != 1 || got[0].OnHandMilli != 2000 || got[0].Seq <= sent.Seq {
		t.Fatalf("the newer mark was lost by the ack: %+v", got)
	}
	if err := f.repo.Ack(context.Background(), []int64{got[0].Seq}); err != nil {
		t.Fatal(err)
	}
	if f.marks(t) != 0 {
		t.Fatal("ack left the mark")
	}
	if err := f.repo.Ack(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestStockUploadRotateStream_NewIDAndEverythingOwedAgain(t *testing.T) {
	f := newStockUploadFixture(t)
	f.item(t, "a", cloudItemA)
	f.item(t, "b", cloudItemB)
	f.setStock(t, "a", "", 1)
	f.setStock(t, "b", "", 2)
	beforeS, err := f.repo.Stream(context.Background())
	before := beforeS.ID
	if err != nil || !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-a[0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(before) || beforeS.StartedAt.IsZero() {
		t.Fatalf("stream id %q, %v", before, err)
	}
	for _, p := range f.pending(t) {
		_ = f.repo.Ack(context.Background(), []int64{p.Seq})
	}
	if f.marks(t) != 0 {
		t.Fatal("setup")
	}
	afterS, err := f.repo.RotateStream(context.Background())
	after := afterS.ID
	if err != nil || after == before {
		t.Fatalf("rotate: %q -> %q, %v", before, after, err)
	}
	if !afterS.StartedAt.After(beforeS.StartedAt) {
		t.Fatalf("a rotated stream must start strictly after the previous one: %v then %v", beforeS.StartedAt, afterS.StartedAt)
	}
	if got, _ := f.repo.Stream(context.Background()); got.ID != after || !got.StartedAt.Equal(afterS.StartedAt) {
		t.Fatalf("stream not stored: %+v", got)
	}
	if len(f.pending(t)) != 2 {
		t.Fatalf("after a rotation both rows must be owed again: %+v", f.pending(t))
	}
}

func TestStockUploadPurgeOrphans_DropsMarksOfDeletedInventory(t *testing.T) {
	f := newStockUploadFixture(t)
	f.item(t, "a", cloudItemA)
	f.setStock(t, "a", "", 1)
	mustExec(t, f.d, `DELETE FROM inventory WHERE item_id = 'a'`)
	if f.marks(t) != 1 {
		t.Fatal("setup: a delete leaves the mark")
	}
	n, err := f.repo.PurgeOrphans(context.Background(), 100)
	if err != nil || n != 1 || f.marks(t) != 0 {
		t.Fatalf("purge = %d, %v, marks %d", n, err, f.marks(t))
	}
}

// The migration owes every existing inventory row to the cloud once: its
// statements are idempotent, so running them again over a database whose
// marks were lost proves the backfill.
func TestStockUploadMigration_BackfillsExistingInventory(t *testing.T) {
	f := newStockUploadFixture(t)
	f.item(t, "a", cloudItemA)
	f.item(t, "b", cloudItemB)
	f.setStock(t, "a", "", 1)
	f.setStock(t, "b", "", 2)
	mustExec(t, f.d, `DELETE FROM stock_upload_outbox`)
	// Not a relative path: another test in this package may change directory.
	_, here, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(here), "..", "db", "migrations", "091_stock_upload_outbox.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Exec(string(raw)); err != nil {
		t.Fatalf("re-running the migration: %v", err)
	}
	if f.marks(t) != 2 {
		t.Fatalf("backfill left %d marks, want 2", f.marks(t))
	}
	if !strings.Contains(string(raw), "stock_upload_state") {
		t.Fatal("wrong file")
	}
	// A second run changes nothing (idempotent), and keeps the stream id.
	s1, _ := f.repo.Stream(context.Background())
	id1 := s1.ID
	if _, err := f.d.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	if s2, _ := f.repo.Stream(context.Background()); id1 != s2.ID || f.marks(t) != 2 {
		t.Fatalf("migration re-run changed state: %q %q marks %d", id1, "(changed)", f.marks(t))
	}
}

// A rotation with rows still owed replaces every mark (no duplicate key),
// under fresh, higher sequences.
func TestStockUploadRotateStream_ReplacesMarksStillOwed(t *testing.T) {
	f := newStockUploadFixture(t)
	f.item(t, "a", cloudItemA)
	f.setStock(t, "a", "", 1)
	before := f.pending(t)[0].Seq
	if _, err := f.repo.RotateStream(context.Background()); err != nil {
		t.Fatalf("rotate with a mark outstanding: %v", err)
	}
	got := f.pending(t)
	if f.marks(t) != 1 || len(got) != 1 || got[0].Seq <= before {
		t.Fatalf("after rotate: marks %d pending %+v (was seq %d)", f.marks(t), got, before)
	}
}

// ---- review fixes (ut-docs#4763, Fable review) ----

// seedBulk inserts n items, n inventory rows (so n marks, by trigger) and
// optionally a cloud link per item, set-based so 20,000 rows seed fast.
func (f *stockUploadFixture) seedBulk(t *testing.T, n int, linked bool) {
	t.Helper()
	mustExec(t, f.d, `WITH RECURSIVE s(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM s WHERE i < ?)
INSERT INTO items (id, name, base_price) SELECT 'bulk'||i, 'bulk'||i, 100 FROM s`, n)
	if linked {
		mustExec(t, f.d, `WITH RECURSIVE s(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM s WHERE i < ?)
INSERT INTO cloud_links (entity, cloud_id, local_id, version, updated_at)
SELECT 'item', lower(substr(hex(randomblob(16)),1,8)||'-'||substr(hex(randomblob(16)),1,4)||'-4'||substr(hex(randomblob(16)),1,3)||'-a'||substr(hex(randomblob(16)),1,3)||'-'||substr(hex(randomblob(16)),1,12)), 'bulk'||i, 1, 'x' FROM s`, n)
	}
	mustExec(t, f.d, `WITH RECURSIVE s(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM s WHERE i < ?)
INSERT INTO inventory (id, item_id, variant_id, location_id, quantity) SELECT 'bulkinv'||i, 'bulk'||i, NULL, ?, 5 FROM s`, n, f.loc)
}

// seedUnlinked adds n items with stock and NO cloud link (n marks that can
// never be sent). Called before seedBulk they hold the lowest sequences, the
// worst case for HasSendable, which scans from the start.
func (f *stockUploadFixture) seedUnlinked(t *testing.T, n int) {
	t.Helper()
	mustExec(t, f.d, `WITH RECURSIVE s(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM s WHERE i < ?)
INSERT INTO items (id, name, base_price) SELECT 'unl'||i, 'unl'||i, 100 FROM s`, n)
	mustExec(t, f.d, `WITH RECURSIVE s(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM s WHERE i < ?)
INSERT INTO inventory (id, item_id, variant_id, location_id, quantity) SELECT 'unlinv'||i, 'unl'||i, NULL, ?, 5 FROM s`, n, f.loc)
}

func explain(t *testing.T, f *stockUploadFixture, q string, args ...any) string {
	t.Helper()
	rows, err := f.d.Query("EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, detail)
	}
	return strings.Join(out, "\n")
}

// The review's blocker: the inventory join must be an index probe, never a
// scan of inventory per mark (O(marks x inventory)).
func TestStockUploadQueriesUseTheInventoryIndex(t *testing.T) {
	f := newStockUploadFixture(t)
	for name, plan := range map[string]string{
		"pending": explain(t, f, pendingSQL, 1.0, 1.0, 0, "x", 10),
		"purge":   explain(t, f, purgeSQL, 10),
	} {
		if regexp.MustCompile(`SCAN inv\b`).MatchString(plan) {
			t.Errorf("%s scans inventory per mark:\n%s", name, plan)
		}
		if !strings.Contains(plan, "ux_inventory_item") {
			t.Errorf("%s does not use ux_inventory_item:\n%s", name, plan)
		}
	}
}

// 20,000 inventory rows and marks is what the migration backfill creates on
// upgrade. Pending and a purge chunk are fast, and a sale's UPDATE running
// beside the purge never waits anywhere near the 5 s busy timeout (it was
// 12.9 s of lock before).
func TestStockUploadAtTwentyThousandRows_FastAndSaleNeverBlocked(t *testing.T) {
	if testing.Short() {
		t.Skip("20,000-row timing test")
	}
	f := newStockUploadFixture(t)
	f.seedBulk(t, 20000, true)
	mustExec(t, f.d, `DELETE FROM inventory WHERE item_id IN ('bulk1','bulk2','bulk3')`) // three orphan marks
	ctx := context.Background()

	start := time.Now()
	rows, err := f.repo.Pending(ctx, 0, time.Now(), 500)
	if err != nil || len(rows) != 500 {
		t.Fatalf("pending = %d, %v", len(rows), err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Pending took %v at 20,000 rows", d)
	}

	var worst time.Duration
	done := make(chan struct{})
	go func() { // a sale, repeatedly, while the purge runs
		defer close(done)
		for i := 0; i < 20; i++ {
			s := time.Now()
			if _, err := f.d.Exec(`UPDATE inventory SET quantity = quantity - 1 WHERE item_id = 'bulk15000'`); err != nil {
				t.Errorf("a sale failed while the purge ran: %v", err)
				return
			}
			worst = max(worst, time.Since(s))
			time.Sleep(5 * time.Millisecond)
		}
	}()
	start = time.Now()
	n, err := f.repo.PurgeOrphans(ctx, 1000)
	<-done
	if err != nil || n != 3 {
		t.Fatalf("purge = %d, %v", n, err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("PurgeOrphans took %v at 20,000 rows", d)
	}
	if worst > time.Second {
		t.Fatalf("a sale waited %v behind the purge's write lock", worst)
	}
}

func TestStockUploadPurgeOrphans_IsBoundedByTheLimit(t *testing.T) {
	f := newStockUploadFixture(t)
	f.seedBulk(t, 30, true)
	mustExec(t, f.d, `DELETE FROM inventory`)
	n, err := f.repo.PurgeOrphans(context.Background(), 10)
	if err != nil || n != 10 || f.marks(t) != 20 {
		t.Fatalf("a chunk of 10 removed %d (marks left %d), %v", n, f.marks(t), err)
	}
}

func TestStockUploadHasSendable_UnlinkedMarksAreNotSendable(t *testing.T) {
	f := newStockUploadFixture(t)
	f.item(t, "c", "") // never linked
	f.setStock(t, "c", "", 4)
	ctx := context.Background()
	if ok, err := f.repo.HasSendable(ctx, time.Now()); err != nil || ok {
		t.Fatalf("an unlinked mark must not be sendable: %v %v", ok, err)
	}
	f.item(t, "a", cloudItemA)
	f.setStock(t, "a", "", 1)
	if ok, _ := f.repo.HasSendable(ctx, time.Now()); !ok {
		t.Fatal("a linked mark must be sendable")
	}
}

func TestStockUploadPending_CursorSkipsEarlierSequences(t *testing.T) {
	f := newStockUploadFixture(t)
	f.item(t, "a", cloudItemA)
	f.item(t, "b", cloudItemB)
	f.setStock(t, "a", "", 1)
	f.setStock(t, "b", "", 1)
	all := f.pending(t)
	got, _ := f.repo.Pending(context.Background(), all[0].Seq, time.Now(), 10)
	if len(got) != 1 || got[0].Seq != all[1].Seq {
		t.Fatalf("cursor: %+v", got)
	}
}

func TestStockUploadDefer_BacksOffThenDropsAndAChangeResets(t *testing.T) {
	f := newStockUploadFixture(t)
	f.item(t, "a", cloudItemA)
	f.setStock(t, "a", "", 1)
	ctx := context.Background()
	now := time.Now()
	seq := f.pending(t)[0].Seq
	dropped, err := f.repo.Defer(ctx, []int64{seq}, now)
	if err != nil || dropped != 0 {
		t.Fatalf("defer: %d %v", dropped, err)
	}
	if got, _ := f.repo.Pending(ctx, 0, now, 10); len(got) != 0 {
		t.Fatalf("a deferred mark must wait out its delay: %+v", got)
	}
	if got, _ := f.repo.Pending(ctx, 0, now.Add(16*time.Minute), 10); len(got) != 1 {
		t.Fatalf("the mark must come back after the delay: %+v", got)
	}
	// The delay grows: after the second deferral 16 minutes is not enough.
	_, _ = f.repo.Defer(ctx, []int64{seq}, now)
	if got, _ := f.repo.Pending(ctx, 0, now.Add(16*time.Minute), 10); len(got) != 0 {
		t.Fatalf("the second delay must be longer: %+v", got)
	}
	// A stock change re-marks the row: attempts and delay reset.
	f.setStock(t, "a", "", 2)
	if got, _ := f.repo.Pending(ctx, 0, now, 10); len(got) != 1 {
		t.Fatalf("a stock change must make the row sendable at once: %+v", got)
	}
	// Bounded: the fifth deferral drops the mark.
	cur := f.pending(t)[0].Seq
	total := 0
	for i := 0; i < maxAttemptsT; i++ {
		d, err := f.repo.Defer(ctx, []int64{cur}, now)
		if err != nil {
			t.Fatal(err)
		}
		total += d
	}
	if total != 1 || f.marks(t) != 0 {
		t.Fatalf("after %d deferrals dropped=%d marks=%d", maxAttemptsT, total, f.marks(t))
	}
}

const maxAttemptsT = StockUploadMaxAttempts

// Review round 2 (ut-docs#4763): the cloud_links lookups must probe
// idx_cloud_links_local (entity, local_id). Without it SQLite picked the
// primary key (entity, cloud_id) and walked the whole entity range per mark
// (4.5 s per page at 20,000 random-UUID links; tills never run ANALYZE).
func TestStockUploadPendingPlan_NeverScansCloudLinks(t *testing.T) {
	f := newStockUploadFixture(t)
	plan := explain(t, f, pendingSQL, 1.0, 1.0, 0, "x", 10)
	for _, line := range strings.Split(plan, "\n") {
		if strings.Contains(line, "cloud_links") && (strings.Contains(line, "SCAN") || !strings.Contains(line, "USING INDEX idx_cloud_links_local (entity=? AND local_id=?)")) {
			t.Errorf("a cloud_links step that is not a (entity, local_id) probe:\n%s", plan)
		}
	}
	if !strings.Contains(plan, "idx_cloud_links_local") {
		t.Errorf("the cloud link lookups do not use idx_cloud_links_local:\n%s", plan)
	}
}

// The measured failure: 20,000 linked items with RANDOM cloud ids (sequential
// ids hid it) and 2,000 unlinked marks ahead of them. Pending and HasSendable
// (every 5 s on the realtime loop) stay fast, and a sale beside them never
// waits.
func TestStockUploadPendingAndHasSendable_FastWithRandomCloudIDsAndUnlinkedMarks(t *testing.T) {
	if testing.Short() {
		t.Skip("20,000-row timing test")
	}
	f := newStockUploadFixture(t)
	f.seedUnlinked(t, 2000)
	f.seedBulk(t, 20000, true)
	ctx := context.Background()

	start := time.Now()
	rows, err := f.repo.Pending(ctx, 0, time.Now(), 500)
	if err != nil || len(rows) != 500 {
		t.Fatalf("pending = %d, %v", len(rows), err)
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("Pending(500) took %v at 20,000 linked items", d)
	}
	start = time.Now()
	ok, err := f.repo.HasSendable(ctx, time.Now())
	if err != nil || !ok {
		t.Fatalf("HasSendable = %v, %v", ok, err)
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("HasSendable took %v with 2,000 unlinked marks ahead", d)
	}
	// All 40 pages, as one Run does (with a cursor).
	start = time.Now()
	var cursor int64
	total := 0
	for {
		page, err := f.repo.Pending(ctx, cursor, time.Now(), 500)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		total += len(page)
		cursor = page[len(page)-1].Seq
	}
	if total != 20000 {
		t.Fatalf("paged %d rows, want 20000", total)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("paging all 40 pages took %v", d)
	}
}

// ---- review round 2: stream start and rotation bookkeeping ----

// Two rotations (even within the same millisecond) give strictly increasing
// starts: the cloud takes a different stream only if it started LATER. Also
// when the stored start is slightly ahead of the clock.
func TestStockUploadRotateStream_StartsAreStrictlyIncreasing(t *testing.T) {
	f := newStockUploadFixture(t)
	ctx := context.Background()
	prev, err := f.repo.Stream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		next, err := f.repo.RotateStream(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !next.StartedAt.After(prev.StartedAt) {
			t.Fatalf("rotation %d: start %v not after %v", i, next.StartedAt, prev.StartedAt)
		}
		prev = next
	}
	// The stored start is 10 s ahead of the clock (inside the tolerance):
	// the next one is still after it.
	ahead := time.Now().Add(10 * time.Second).UTC()
	mustExec(t, f.d, `UPDATE stock_upload_state SET started_at = ?`, ahead.Format(StreamStartLayout))
	next, err := f.repo.RotateStream(ctx)
	if err != nil || !next.StartedAt.After(ahead) {
		t.Fatalf("rotation after an ahead start: %v (prev %v) %v", next.StartedAt, ahead, err)
	}
}

// A start far in the future (the clock was wrong when it was written) is
// reset to now on read, persisted, and a rotation does not chain from it.
func TestStockUploadStream_AFutureStartIsResetToNow(t *testing.T) {
	f := newStockUploadFixture(t)
	ctx := context.Background()
	mustExec(t, f.d, `UPDATE stock_upload_state SET started_at = ?`, time.Now().Add(20*time.Hour).UTC().Format(StreamStartLayout))
	s, err := f.repo.Stream(ctx)
	if err != nil || s.StartedAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("read start %v (%v)", s.StartedAt, err)
	}
	var stored string
	if err := f.d.QueryRow(`SELECT started_at FROM stock_upload_state`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if p, _ := time.Parse(StreamStartLayout, stored); p.After(time.Now().Add(time.Second)) {
		t.Fatalf("the reset was not persisted: %s", stored)
	}
	// And a rotation from a future stored start lands at now, not past it.
	mustExec(t, f.d, `UPDATE stock_upload_state SET started_at = ?`, time.Now().Add(20*time.Hour).UTC().Format(StreamStartLayout))
	n, err := f.repo.RotateStream(ctx)
	if err != nil || n.StartedAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("rotation chained from a future start: %v %v", n.StartedAt, err)
	}
	// ResetStreamStart is the answer to the cloud's 400.
	mustExec(t, f.d, `UPDATE stock_upload_state SET started_at = ?`, time.Now().Add(30*time.Second).UTC().Format(StreamStartLayout))
	if err := f.repo.ResetStreamStart(ctx); err != nil {
		t.Fatal(err)
	}
	if s, _ := f.repo.Stream(ctx); s.StartedAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("ResetStreamStart left %v", s.StartedAt)
	}
}

func TestStockUploadRotationAllowed_RateLimited(t *testing.T) {
	f := newStockUploadFixture(t)
	ctx := context.Background()
	now := time.Now()
	if ok, err := f.repo.RotationAllowed(ctx, now); err != nil || !ok {
		t.Fatalf("never rotated: %v %v", ok, err)
	}
	if _, err := f.repo.RotateStream(ctx); err != nil {
		t.Fatal(err)
	}
	if ok, _ := f.repo.RotationAllowed(ctx, time.Now().Add(time.Minute)); ok {
		t.Fatal("a second rotation a minute later must be refused")
	}
	if ok, _ := f.repo.RotationAllowed(ctx, time.Now().Add(StockUploadRotateMinInterval+time.Second)); !ok {
		t.Fatal("after the interval a rotation is allowed")
	}
	// A clock that went backwards (rotated_at ahead of now) must not wedge it.
	mustExec(t, f.d, `UPDATE stock_upload_state SET rotated_at = ?`, time.Now().Add(48*time.Hour).UTC().Format(time.RFC3339))
	if ok, _ := f.repo.RotationAllowed(ctx, time.Now()); !ok {
		t.Fatal("a rotated_at in the future must not block rotations forever")
	}
}

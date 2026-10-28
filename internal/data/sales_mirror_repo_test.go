package data

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// SalesMirrorRepo, main-till side (ut-docs#5074 M2): the change log, its
// acknowledgements and pruning, against the real migrated schema.

func openMirrorLogDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(testsupport.MigratedDBFile(t, "mirror_log.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d.DB
}

// --- main till: log, acks, prune ---

func seedLog(t *testing.T, sqlDB *sql.DB, n int, loggedAt string) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := sqlDB.Exec(`INSERT INTO sales_mirror_log (op, ref_id, origin_till_id, logged_at) VALUES ('sale', ?, '', ?)`, fmt.Sprintf("r%d", i), loggedAt); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSalesMirrorRepo_HeadHorizonAndRead(t *testing.T) {
	sqlDB := openMirrorLogDB(t)
	r := NewSalesMirrorRepo(sqlDB)
	ctx := context.Background()
	if h, _ := r.LogHead(ctx); h != 0 {
		t.Fatalf("empty head = %d", h)
	}
	seedLog(t, sqlDB, 10, "2026-10-26T10:00:00Z")
	entries, err := r.ReadLog(ctx, 3, 4)
	if err != nil || len(entries) != 4 || entries[0].Seq != 4 || entries[3].Seq != 7 {
		t.Fatalf("read = %+v err=%v", entries, err)
	}
	if _, err := sqlDB.Exec(`DELETE FROM sales_mirror_log WHERE seq <= 6`); err != nil {
		t.Fatal(err)
	}
	if h, _ := r.LogHorizon(ctx); h != 6 {
		t.Fatalf("horizon = %d, want 6", h)
	}
	if _, err := sqlDB.Exec(`DELETE FROM sales_mirror_log`); err != nil {
		t.Fatal(err)
	}
	head, _ := r.LogHead(ctx)
	hz, _ := r.LogHorizon(ctx)
	if head != 10 || hz != 10 {
		t.Fatalf("emptied log: head=%d horizon=%d, want 10/10", head, hz)
	}
}

func TestSalesMirrorRepo_AckNeverMovesBackInEpoch(t *testing.T) {
	sqlDB := openMirrorLogDB(t)
	r := NewSalesMirrorRepo(sqlDB)
	ctx := context.Background()
	_ = r.UpsertAck(ctx, "t1", "e1", 50, time.Now())
	_ = r.UpsertAck(ctx, "t1", "e1", 20, time.Now())
	acks, _ := r.Acks(ctx)
	if acks[0].AckedSeq != 50 {
		t.Fatalf("ack moved back: %+v", acks)
	}
	_ = r.UpsertAck(ctx, "t1", "e2", 3, time.Now())
	acks, _ = r.Acks(ctx)
	if acks[0].AckedSeq != 3 || acks[0].Epoch != "e2" {
		t.Fatalf("new epoch ack: %+v", acks)
	}
}

// Prune never deletes an entry a registered till still needs while under
// the caps (AC10); satellites and other epochs do not count; a cap overrides
// and names the till.
func TestSalesMirrorRepo_PruneLog(t *testing.T) {
	sqlDB := openMirrorLogDB(t)
	r := NewSalesMirrorRepo(sqlDB)
	ctx := context.Background()
	tills := NewTillsRepo(sqlDB)
	b, _ := tills.InsertTill(ctx, "Till 2", "hb")
	c, _ := tills.InsertTill(ctx, "Till 3", "hc")
	sat, _, _ := tills.EnrolTill(ctx, "Kiosk", "hs", TillRoleSatellite, "")
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	seedLog(t, sqlDB, 100, now.Format("2006-01-02T15:04:05Z"))
	_ = r.UpsertAck(ctx, b, "e1", 70, now)
	_ = r.UpsertAck(ctx, c, "e1", 40, now)
	_ = r.UpsertAck(ctx, sat, "e1", 1, now)         // satellites never count
	_ = r.UpsertAck(ctx, "gone-till", "e1", 1, now) // not enrolled: never counts
	opts := MirrorLogPruneOpts{Epoch: "e1", MaxDays: 30, MaxRows: 250000, Now: now}
	for range 3 {
		res, err := r.PruneLog(ctx, opts)
		if err != nil {
			t.Fatal(err)
		}
		if res.AckHorizon != 40 || len(res.CapPassed) != 0 {
			t.Fatalf("prune = %+v", res)
		}
	}
	if hz, _ := r.LogHorizon(ctx); hz != 40 {
		t.Fatalf("horizon = %d, want 40 (Till 3's ack)", hz)
	}
	// Row cap 20 overrides Till 3 (40) and Till 2 (70): keeps 81..100.
	opts.MaxRows = 20
	res, err := r.PruneLog(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if hz, _ := r.LogHorizon(ctx); hz != 80 {
		t.Fatalf("horizon after row cap = %d, want 80", hz)
	}
	if len(res.CapPassed) != 2 {
		t.Fatalf("cap passed = %v, want both tills", res.CapPassed)
	}
	// No registered till in the epoch: everything goes.
	res, err = r.PruneLog(ctx, MirrorLogPruneOpts{Epoch: "e-other", Now: now})
	if err != nil || res.AckHorizon != 100 {
		t.Fatalf("no registered: %+v err=%v", res, err)
	}
	if n, _ := r.LogCount(ctx); n != 0 {
		t.Fatalf("log rows = %d, want 0", n)
	}
}

func TestSalesMirrorRepo_PruneLogAgeCap(t *testing.T) {
	sqlDB := openMirrorLogDB(t)
	r := NewSalesMirrorRepo(sqlDB)
	ctx := context.Background()
	tills := NewTillsRepo(sqlDB)
	b, _ := tills.InsertTill(ctx, "Till 2", "hb")
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	seedLog(t, sqlDB, 5, "2026-09-01T00:00:00Z") // 1..5, older than 30 days
	seedLog(t, sqlDB, 5, "2026-10-25T00:00:00Z") // 6..10
	_ = r.UpsertAck(ctx, b, "e1", 2, now)
	res, err := r.PruneLog(ctx, MirrorLogPruneOpts{Epoch: "e1", MaxDays: 30, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if hz, _ := r.LogHorizon(ctx); hz != 5 || len(res.CapPassed) != 1 || res.CapPassed[0] != b {
		t.Fatalf("age cap: horizon=%d res=%+v", hz, res)
	}
}

// The chunked delete handles more than one chunk.
func TestSalesMirrorRepo_PruneLogChunks(t *testing.T) {
	sqlDB := openMirrorLogDB(t)
	r := NewSalesMirrorRepo(sqlDB)
	ctx := context.Background()
	if _, err := sqlDB.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 12000)
INSERT INTO sales_mirror_log (op, ref_id) SELECT 'sale', 'r' || i FROM n`); err != nil {
		t.Fatal(err)
	}
	res, err := r.PruneLog(ctx, MirrorLogPruneOpts{Epoch: "e1", Now: time.Now()})
	if err != nil || res.Deleted != 12000 {
		t.Fatalf("deleted %d err=%v", res.Deleted, err)
	}
}

// An empty epoch is refused (M1 review finding 3): it would prune the whole
// log and send every till into a state re-sync.
func TestSalesMirrorRepo_PruneLogRefusesEmptyEpoch(t *testing.T) {
	sqlDB := openMirrorLogDB(t)
	r := NewSalesMirrorRepo(sqlDB)
	seedLog(t, sqlDB, 3, "2026-10-26T10:00:00Z")
	if _, err := r.PruneLog(context.Background(), MirrorLogPruneOpts{Now: time.Now()}); err == nil {
		t.Fatal("PruneLog with no epoch did not fail")
	}
	if n, _ := r.LogCount(context.Background()); n != 3 {
		t.Fatalf("log rows = %d, want 3", n)
	}
}

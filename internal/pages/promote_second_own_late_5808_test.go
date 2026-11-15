package pages

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/secrets"
)

// ut-docs#5808 (ADR-0066 amendment rule 1, second promote): B is promoted and
// holds an own-late sale S (recorded_at stamped at B's promote), B sells S2 as
// main, a third till C re-joins B (the real join snapshot of B's DB), copies
// B's state (S arrives as one of B's own sales: no recorded_at in the mirror
// record), B dies before any Z and C is promoted. Every sale must land in
// exactly one Z: the join snapshot carries sales.recorded_at and the Z floor,
// and AdoptCopy never overwrites an existing sales row. Times derive from
// time.Now() (guard-fixed-dates.sh).

func TestPromote_SecondPromote_OwnLateSale_ExactlyOneZ_5808(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	shop := newMirrorShop(t, "B")
	shop.enable()
	b := shop.tills["B"]
	ctx := t.Context()

	if _, err := data.NewPOSRepo(b.d.Db).ArchiveReport(ctx, "eod", "b-old", []byte(`{"sales_count":0}`), "", "", time.Now().Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := data.NewPOSRepo(shop.main.d.Db).ArchiveReport(ctx, "eod", "m-close", []byte(`{"sales_count":0}`), "", "", time.Now().Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	shop.pull(b)
	shop.pull(b)
	s := shop.sell(b, 250)
	shop.push(b)
	shop.pull(b)
	shop.pull(b)
	// M stamped S at its apply; the Z's upper bound is second-truncated, so put
	// it well back (still after M's close).
	if _, err := b.d.Db.Exec(`UPDATE mirror_sale_arrivals SET completed_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-90*time.Second).Format(time.RFC3339), s); err != nil {
		t.Fatal(err)
	}
	promote(t, b)
	if n := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM sales WHERE id = ? AND recorded_at IS NOT NULL`, s); n != 1 {
		t.Fatal("promote did not mark S with the old main till's recording time")
	}
	// B, now main, sells S2; keep it inside C's Z window too.
	s2 := shop.sell(b, 70)
	if _, err := b.d.Db.Exec(`UPDATE sales SET created_at = datetime('now','-60 seconds'), completed_at = datetime('now','-60 seconds') WHERE id = ?`, s2); err != nil {
		t.Fatal(err)
	}
	// A sale dated before M's close (-2 min) sits under the inherited Z floor:
	// C's Z must leave it out, so a lost floor double-counts it (3 / 1320).
	pre := shop.sell(b, 1000)
	if _, err := b.d.Db.Exec(`UPDATE sales SET created_at = datetime('now','-180 seconds'), completed_at = datetime('now','-180 seconds') WHERE id = ?`, pre); err != nil {
		t.Fatal(err)
	}
	origin := func(d *sql.DB) string {
		var c, cp, l, r string
		if err := d.QueryRow(`SELECT created_at, completed_at, local_date, receipt_no FROM sales WHERE id = ?`, s).Scan(&c, &cp, &l, &r); err != nil {
			t.Fatal(err)
		}
		return c + "|" + cp + "|" + l + "|" + r
	}
	bOrigin := origin(b.d.Db)

	// C re-joins B: the real redacted snapshot of B's DB, then the real join
	// identity step.
	var bPath string
	if err := b.d.Db.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&bPath); err != nil || bPath == "" {
		t.Fatalf("B db path %q: %v", bPath, err)
	}
	snap, cleanup, err := db.RedactedJoinSnapshot(b.d.Db, bPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	raw, err := os.ReadFile(snap)
	if err != nil {
		t.Fatal(err)
	}
	cDir := t.TempDir()
	cPath := filepath.Join(cDir, "c.db")
	if err := os.WriteFile(cPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	// ApplyReplicaIdentity clears the local secrets key file under the data
	// dir: point that at a scratch dir, not the repo's ./data.
	prevData := paths.DataDir()
	paths.Init(filepath.Join(cDir, "data"))
	t.Cleanup(func() { paths.Init(prevData) })
	cOpen, err := db.Open(cPath)
	if err != nil {
		t.Fatal(err)
	}
	cdb := cOpen.DB
	t.Cleanup(func() { cdb.Close() })
	cdb.SetMaxOpenConns(1)
	cdb.SetMaxIdleConns(1)
	if _, err := cdb.Exec(`PRAGMA journal_mode = MEMORY`); err != nil {
		t.Fatal(err)
	}
	if _, err := cdb.Exec(`PRAGMA synchronous = OFF`); err != nil {
		t.Fatal(err)
	}
	const cID = "c-till-id"
	if err := db.StageReplicaIdentity(cPath, db.ReplicaIdentity{PrimaryURL: "https://b.invalid", TillID: cID,
		Bearer: "x", ReceiptPrefix: "T3-", TillName: "Till C", DeviceID: "c-device"}); err != nil {
		t.Fatal(err)
	}
	// ApplyReplicaIdentity resets the process-wide key store; restore the one
	// TestMain registered so later tests in the package keep it.
	prevKS := secrets.Default()
	t.Cleanup(func() { secrets.SetDefault(prevKS) })
	if applied, err := db.ApplyReplicaIdentity(cdb, cPath); err != nil || !applied {
		t.Fatalf("ApplyReplicaIdentity: applied=%v err=%v", applied, err)
	}
	_, cd := newSyncSalesTestDepsOn(t, cdb)

	// The join snapshot carries S's recording time and the Z floor.
	if n := harnessCount(t, cdb, `SELECT COUNT(*) FROM sales WHERE id = ? AND recorded_at IS NOT NULL`, s); n != 1 {
		t.Fatal("C's snapshot copy of S has no recorded_at")
	}
	bFloor, _, _ := b.d.Settings.Get(ctx, data.EODWindowFloorKey)
	if cFloor, _, _ := cd.Settings.Get(ctx, data.EODWindowFloorKey); cFloor == "" || cFloor != bFloor {
		t.Fatalf("C's Z floor = %q, want B's %q", cFloor, bFloor)
	}

	// C's first state re-sync from B, through B's real loaders: S travels as
	// B's own sale (the mirror record has no recorded_at), plus B's close.
	const bID = "b-main-id"
	src := data.MirrorSource{MainTillID: bID, MainTillName: "Till B"}
	brepo := data.NewSalesMirrorRepo(b.d.Db)
	sales, err := brepo.LoadMirrorSales(ctx, []string{s, s2}, src)
	if err != nil || len(sales) != 2 {
		t.Fatalf("load B's sales: %v (%d)", err, len(sales))
	}
	closes, err := brepo.LoadMirrorCloses(ctx, []string{"b-old"}, src)
	if err != nil {
		t.Fatal(err)
	}
	recs := []data.MirrorRecord{{Seq: 9, Op: data.MirrorOpSale, Sale: sales[s]}, {Seq: 9, Op: data.MirrorOpSale, Sale: sales[s2]}}
	for _, c := range closes {
		recs = append(recs, data.MirrorRecord{Seq: 9, Op: data.MirrorOpClose, Close: c})
	}
	if _, err := data.NewSalesMirrorRepo(cdb).ApplyStatePage(ctx, "", data.MirrorStatePage{Epoch: "e2", MainTillID: bID, ResumeSeq: 9, Records: recs},
		data.MirrorApplyOpts{SelfTillID: cID, Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	var copyDone, copyCreated string
	if err := cdb.QueryRow(`SELECT completed_at, created_at FROM mirror_sales WHERE id = ?`, s).Scan(&copyDone, &copyCreated); err != nil || copyDone != copyCreated {
		t.Fatalf("C's mirror copy of S: completed_at=%q created_at=%q (%v), want equal (no recorded_at travels)", copyDone, copyCreated, err)
	}

	// B dies before any Z; C is promoted and closes.
	promote(t, &mirrorTill{name: "C", d: cd})
	rep, _, err := generateEOD(ctx, cd, "system", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if rep.SalesCount != 2 || rep.Gross != 320 {
		t.Fatalf("C's first Z = %d sales / %d gross, want S and S2 exactly once (2 / 320)", rep.SalesCount, rep.Gross)
	}
	if got := origin(cd.Db); got != bOrigin {
		t.Fatalf("S origin fields moved on C: %s -> %s", bOrigin, got)
	}
	// The next window (a preview; one archived Z per day) counts neither again.
	rep2, _, err := generateEOD(ctx, cd, "system", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if rep2.SalesCount != 0 || rep2.Gross != 0 {
		t.Fatalf("a later Z counted %d/%d again", rep2.SalesCount, rep2.Gross)
	}
}

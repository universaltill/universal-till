package pages

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#5777 (ADR-0066 amendment rule 1), end to end: a till's own sale
// that the old main till applied only after its last close reaches the
// promoted till's first Z exactly once. Times derive from time.Now()
// (guard-fixed-dates.sh).

func TestPromote_OwnLateSale_ExactlyOneZ_5777(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	shop := newMirrorShop(t, "B")
	shop.enable()
	b := shop.tills["B"]
	ctx := t.Context()
	bRepo := data.NewPOSRepo(b.d.Db)

	// B's own Z from yesterday: archived, and must stay byte-identical.
	if _, err := bRepo.ArchiveReport(ctx, "eod", "b-old", []byte(`{"sales_count":0}`), "", "", time.Now().Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	preZ := archivedZ(t, b.d)

	// M closes a minute ago; B's sale S (made two hours ago) reaches M only now.
	if _, err := data.NewPOSRepo(shop.main.d.Db).ArchiveReport(ctx, "eod", "m-close", []byte(`{"sales_count":0}`), "", "", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	// B is already following M's feed (its first pull is a state re-sync that
	// holds M's close), so S's apply reaches B as a feed record.
	shop.pull(b)
	shop.pull(b)
	s := shop.sell(b, 250)
	var created, completed, local, rcpt string
	q := `SELECT created_at, completed_at, local_date, receipt_no FROM sales WHERE id = ?`
	if err := b.d.Db.QueryRow(q, s).Scan(&created, &completed, &local, &rcpt); err != nil {
		t.Fatal(err)
	}
	shop.push(b)
	shop.pull(b)
	shop.pull(b)
	if n := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM mirror_sale_arrivals WHERE id = ?`, s); n != 1 {
		t.Fatalf("the pull kept %d arrival rows for S, want 1", n)
	}

	// M stamped S at its apply, i.e. now; the Z's upper bound is second-
	// truncated, so a recording inside the Z's own second would (rightly) wait
	// for the next Z. Put it half a minute back, still after M's close.
	if _, err := b.d.Db.Exec(`UPDATE mirror_sale_arrivals SET completed_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-30*time.Second).Format(time.RFC3339), s); err != nil {
		t.Fatal(err)
	}
	promote(t, b)
	if n := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM sales WHERE id = ? AND recorded_at IS NOT NULL`, s); n != 1 {
		t.Fatal("promote did not mark S with the old main till's recording time")
	}
	if n := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM mirror_sale_arrivals`); n != 0 {
		t.Fatalf("%d arrival rows left after promote, want 0", n)
	}
	var auditJSON string
	if err := b.d.Db.QueryRow(`SELECT data_json FROM audit_log WHERE action = 'sales_adopted'`).Scan(&auditJSON); err != nil {
		t.Fatal(err)
	}
	var audit map[string]any
	_ = json.Unmarshal([]byte(auditJSON), &audit)
	if audit["own_late_counted"] != float64(1) {
		t.Fatalf("sales_adopted audit = %s, want own_late_counted 1", auditJSON)
	}

	rep, _, err := generateEOD(ctx, b.d, "system", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if rep.SalesCount != 1 || rep.Gross != 250 {
		t.Fatalf("B's first Z after promote = %d/%d, want S exactly once (1/250)", rep.SalesCount, rep.Gross)
	}
	if rep.LateEntries == nil || len(rep.LateEntries.Tills) != 1 || rep.LateEntries.Tills[0].TillID != b.tillID ||
		rep.LateEntries.Tills[0].SalesCount != 1 || rep.LateEntries.Tills[0].Gross != 250 {
		t.Fatalf("late note = %+v, want B's own entry with 1 sale / 250", rep.LateEntries)
	}
	if rep.LateEntries.EarliestOrigin == "" {
		t.Fatal("late note has no earliest origin")
	}
	// M's copied Z counted nothing of S; sum across M's and B's Z is 1.
	// B's earlier Z is untouched, and so are S's origin fields.
	if got := archivedZ(t, b.d); got != preZ {
		t.Fatalf("pre-promote Z changed:\n%s\n%s", preZ, got)
	}
	var c2, cp2, l2, r2 string
	if err := b.d.Db.QueryRow(q, s).Scan(&c2, &cp2, &l2, &r2); err != nil || c2 != created || cp2 != completed || l2 != local || r2 != rcpt {
		t.Fatalf("S origin fields moved: %s|%s|%s|%s -> %s|%s|%s|%s (%v)", created, completed, local, rcpt, c2, cp2, l2, r2, err)
	}

	// A resumed adoption / second mark does not change what a later Z counts.
	// The first Z above was archived (one per local day), so this second
	// generateEOD is a PREVIEW of the next window [first Z close, now), not a
	// second archived Z; it shows S is not counted again.
	if _, err := data.NewSalesMirrorRepo(b.d.Db).MarkOwnLateArrivals(ctx, "2000-01-01 00:00:00", ""); err != nil { // fixed-date:ok bound far outside any real window
		t.Fatal(err)
	}
	rep2, _, err := generateEOD(ctx, b.d, "system", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if rep2.SalesCount != 0 || rep2.Gross != 0 {
		t.Fatalf("a later Z counted S again: %d/%d", rep2.SalesCount, rep2.Gross)
	}
}

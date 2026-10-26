package pages

import (
	"context"
	"testing"
)

// ut-docs#5074 S0b: a journaled sale is written with its source till and its
// own creation time in the SAME transaction as the sale itself. Before, the
// main till inserted it as its own (till_id "", created_at = replay time)
// and stamped provenance in a later statement, so a reader between the two
// (a report, a Z, the cloud link, a future mirror trigger) saw the sale as
// the main till's own, at the wrong time. A probe trigger records what the
// INSERT itself carried, and any later change of till_id/created_at.
func TestS0b_JournaledSaleCarriesItsTillAndTimeInTheInsert(t *testing.T) {
	_, dp := newSyncSalesTestDeps(t)
	ctx := context.Background()
	for _, q := range []string{
		`CREATE TABLE probe_sales (op TEXT, id TEXT, till_id TEXT, created_at TEXT, local_date TEXT)`,
		`CREATE TRIGGER probe_sales_ins AFTER INSERT ON sales BEGIN
		   INSERT INTO probe_sales VALUES ('ins', NEW.id, NEW.till_id, NEW.created_at, NEW.local_date); END`,
		`CREATE TRIGGER probe_sales_upd AFTER UPDATE OF till_id, created_at ON sales
		 WHEN OLD.till_id IS NOT NEW.till_id OR OLD.created_at IS NOT NEW.created_at BEGIN
		   INSERT INTO probe_sales VALUES ('upd', NEW.id, NEW.till_id, NEW.created_at, NEW.local_date); END`,
	} {
		if _, err := dp.Db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	j := seedJournalSale("remote-prov-1", "T2-P001", "sale", "", "itm1", 1, 100)
	if applied, reason, err := applyJournal(ctx, dp, "till-2", j); err != nil || !applied {
		t.Fatalf("apply: applied=%v reason=%q err=%v", applied, reason, err)
	}
	var wantLocal string
	if err := dp.Db.QueryRowContext(ctx, `SELECT date(?, 'localtime')`, j.Sale.CreatedAt).Scan(&wantLocal); err != nil {
		t.Fatal(err)
	}
	rows, err := dp.Db.QueryContext(ctx, `SELECT op, till_id, created_at, local_date FROM probe_sales WHERE id = 'remote-prov-1' ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var op, till, created, local string
		if err := rows.Scan(&op, &till, &created, &local); err != nil {
			t.Fatal(err)
		}
		got = append(got, op+" "+till+" "+created+" "+local)
	}
	want := "ins till-2 " + j.Sale.CreatedAt + " " + wantLocal
	if len(got) != 1 || got[0] != want {
		t.Fatalf("writes of the journaled sale = %q, want exactly [%q]: provenance must ride the insert, not a later update", got, want)
	}
}

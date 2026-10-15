package db

import (
	"strings"
	"testing"
)

// voucherAdjustmentsMigrationVersion is 074_voucher_adjustments.sql
// (ut-docs#4253): voucher_transactions is rebuilt to admit the 'adjustment'
// type and gains actor_user_id / reason.
const voucherAdjustmentsMigrationVersion = 74

func TestMigration074_IsOnDisk(t *testing.T) {
	m := loadMigrationVersion(t, voucherAdjustmentsMigrationVersion)
	if m.Name != "074_voucher_adjustments.sql" {
		t.Fatalf("migration %d on disk is %q", voucherAdjustmentsMigrationVersion, m.Name)
	}
}

// TestMigration074_RebuildKeepsLedgerAndIndexes seeds a pre-074 ledger
// (issue + redemption rows, one with a NULL sale_id), migrates, and proves
// the rebuild kept every row and column value, recreated both indexes —
// the partial unique index still refusing a duplicate redemption — and
// widened the CHECK to 'adjustment' only.
func TestMigration074_RebuildKeepsLedgerAndIndexes(t *testing.T) {
	d, path := openAtPreMigrationSchema(t, voucherAdjustmentsMigrationVersion, "m074.db")
	exec := func(db *DB, q string, args ...any) {
		t.Helper()
		if _, err := db.DB.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	exec(d, `INSERT INTO vouchers (id, original_amount, balance, currency) VALUES ('GS-1', 5000, 3000, 'EUR')`)
	exec(d, `INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount, created_at) VALUES ('tx-issue', 'GS-1', 'sale-A', 'issue', 5000, '2026-01-02T10:00:00Z')`)
	exec(d, `INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount, created_at) VALUES ('tx-red', 'GS-1', 'sale-B', 'redemption', 2000, '2026-01-03T10:00:00Z')`)
	exec(d, `INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount, created_at) VALUES ('tx-imp', 'GS-1', NULL, 'issue', 1, '2026-01-01T10:00:00Z')`)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open (runs 074): %v", err)
	}
	defer d.Close()

	type row struct {
		id, voucher, sale, typ, created string
		amount                          int64
		actorNull, reasonNull           bool
	}
	rows, err := d.DB.Query(`SELECT id, voucher_id, COALESCE(sale_id, '<null>'), type, amount, created_at, actor_user_id IS NULL, reason IS NULL FROM voucher_transactions ORDER BY id`)
	if err != nil {
		t.Fatalf("select after 074 (new columns must exist): %v", err)
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.voucher, &r.sale, &r.typ, &r.amount, &r.created, &r.actorNull, &r.reasonNull); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	rows.Close()
	want := []row{
		{"tx-imp", "GS-1", "<null>", "issue", "2026-01-01T10:00:00Z", 1, true, true},
		{"tx-issue", "GS-1", "sale-A", "issue", "2026-01-02T10:00:00Z", 5000, true, true},
		{"tx-red", "GS-1", "sale-B", "redemption", "2026-01-03T10:00:00Z", 2000, true, true},
	}
	if len(got) != len(want) {
		t.Fatalf("rows after 074 = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d after 074 = %+v, want %+v", i, got[i], want[i])
		}
	}

	for _, idx := range []string{"idx_voucher_tx_voucher", "ux_voucher_tx_redemption_once"} {
		var n int
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ? AND tbl_name = 'voucher_transactions'`, idx).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("index %s missing after 074", idx)
		}
	}

	// The partial unique index still refuses a second redemption for the
	// same (voucher, sale) …
	_, err = d.DB.Exec(`INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount) VALUES ('tx-dup', 'GS-1', 'sale-B', 'redemption', 2000)`)
	if err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed: voucher_transactions.voucher_id, voucher_transactions.sale_id") {
		t.Fatalf("duplicate redemption after 074: err = %v, want the ux_voucher_tx_redemption_once refusal", err)
	}
	// … and is still partial: an adjustment (NULL sale) and an issue for
	// the same pair are fine.
	exec(d, `INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount, actor_user_id, reason) VALUES ('tx-adj', 'GS-1', NULL, 'adjustment', -500, 'u-1', 'damaged card')`)
	exec(d, `INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount) VALUES ('tx-issue2', 'GS-1', 'sale-B', 'issue', 100)`)

	// The CHECK is widened to 'adjustment' only.
	if _, err := d.DB.Exec(`INSERT INTO voucher_transactions (id, voucher_id, type, amount) VALUES ('tx-bad', 'GS-1', 'refund', 1)`); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("unknown type after 074: err = %v, want a CHECK refusal", err)
	}
	// The voucher FK survives the rebuild.
	if _, err := d.DB.Exec(`INSERT INTO voucher_transactions (id, voucher_id, type, amount) VALUES ('tx-orphan', 'NO-SUCH', 'issue', 1)`); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("orphan voucher_id after 074: err = %v, want a FOREIGN KEY refusal", err)
	}
	// The vouchers parent is untouched.
	var bal int64
	if err := d.DB.QueryRow(`SELECT balance FROM vouchers WHERE id = 'GS-1'`).Scan(&bal); err != nil || bal != 3000 {
		t.Fatalf("vouchers row after 074: balance %d err %v", bal, err)
	}
}

package db

import (
	"path/filepath"
	"strings"
	"testing"
)

// voucherTopUpMigrationVersion is 080_voucher_top_up.sql (ut-docs#4252):
// voucher_transactions is rebuilt again so its CHECK admits 'top_up' — a
// customer adding value to an existing multi-purpose voucher as a sale.
const voucherTopUpMigrationVersion = 80

func TestMigration080_IsOnDisk(t *testing.T) {
	m := loadMigrationVersion(t, voucherTopUpMigrationVersion)
	if m.Name != "080_voucher_top_up.sql" {
		t.Fatalf("migration %d on disk is %q", voucherTopUpMigrationVersion, m.Name)
	}
}

// TestMigration080_RebuildKeepsLedgerAndIndexes seeds a pre-080 ledger
// (issue, redemption, an imported issue with a NULL sale and a manager
// adjustment with its actor/reason), migrates, and proves the rebuild kept
// every row and column value — actor_user_id/reason included — recreated
// both indexes (the partial unique index still refusing a duplicate
// redemption), kept the voucher FK, and widened the CHECK to 'top_up' only.
func TestMigration080_RebuildKeepsLedgerAndIndexes(t *testing.T) {
	// Migrated to the highest version below this one (robust to a gap).
	loadMigrationVersion(t, voucherTopUpMigrationVersion)
	migs, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	prev := 0
	for _, m := range migs {
		if m.Version < voucherTopUpMigrationVersion && m.Version > prev {
			prev = m.Version
		}
	}
	path := filepath.Join(t.TempDir(), "m080.db")
	d := openMigratedTo(t, path, prev)
	exec := func(db *DB, q string, args ...any) {
		t.Helper()
		if _, err := db.DB.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	exec(d, `INSERT INTO vouchers (id, original_amount, balance, currency) VALUES ('GS-1', 5000, 2500, 'EUR')`)
	exec(d, `INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount, created_at) VALUES ('tx-issue', 'GS-1', 'sale-A', 'issue', 5000, '2026-01-02T10:00:00Z')`)
	exec(d, `INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount, created_at) VALUES ('tx-red', 'GS-1', 'sale-B', 'redemption', 2000, '2026-01-03T10:00:00Z')`)
	exec(d, `INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount, created_at) VALUES ('tx-imp', 'GS-1', NULL, 'issue', 1, '2026-01-01T10:00:00Z')`)
	exec(d, `INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount, created_at, actor_user_id, reason) VALUES ('tx-adj', 'GS-1', NULL, 'adjustment', -501, '2026-01-04T10:00:00Z', 'u-1', 'damaged card')`)
	// A pre-080 schema refuses a top-up row.
	if _, err := d.DB.Exec(`INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount) VALUES ('tx-pre', 'GS-1', 'sale-C', 'top_up', 100)`); err == nil {
		t.Fatal("pre-080 schema accepted a top_up row")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d, err = Open(path)
	if err != nil {
		t.Fatalf("Open (runs 080): %v", err)
	}
	defer d.Close()

	type row struct {
		id, voucher, sale, typ, created, actor, reason string
		amount                                         int64
	}
	rows, err := d.DB.Query(`SELECT id, voucher_id, COALESCE(sale_id, '<null>'), type, created_at, COALESCE(actor_user_id, '<null>'), COALESCE(reason, '<null>'), amount FROM voucher_transactions ORDER BY id`)
	if err != nil {
		t.Fatalf("select after 080: %v", err)
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.voucher, &r.sale, &r.typ, &r.created, &r.actor, &r.reason, &r.amount); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	rows.Close()
	want := []row{
		{"tx-adj", "GS-1", "<null>", "adjustment", "2026-01-04T10:00:00Z", "u-1", "damaged card", -501},
		{"tx-imp", "GS-1", "<null>", "issue", "2026-01-01T10:00:00Z", "<null>", "<null>", 1},
		{"tx-issue", "GS-1", "sale-A", "issue", "2026-01-02T10:00:00Z", "<null>", "<null>", 5000},
		{"tx-red", "GS-1", "sale-B", "redemption", "2026-01-03T10:00:00Z", "<null>", "<null>", 2000},
	}
	if len(got) != len(want) {
		t.Fatalf("rows after 080 = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d after 080 = %+v, want %+v", i, got[i], want[i])
		}
	}

	for _, idx := range []string{"idx_voucher_tx_voucher", "ux_voucher_tx_redemption_once"} {
		var n int
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ? AND tbl_name = 'voucher_transactions'`, idx).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("index %s missing after 080", idx)
		}
	}
	_, err = d.DB.Exec(`INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount) VALUES ('tx-dup', 'GS-1', 'sale-B', 'redemption', 2000)`)
	if err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed: voucher_transactions.voucher_id, voucher_transactions.sale_id") {
		t.Fatalf("duplicate redemption after 080: err = %v, want the ux_voucher_tx_redemption_once refusal", err)
	}

	// The CHECK now admits 'top_up' …
	exec(d, `INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount) VALUES ('tx-top', 'GS-1', 'sale-C', 'top_up', 1500)`)
	// … and still nothing else.
	if _, err := d.DB.Exec(`INSERT INTO voucher_transactions (id, voucher_id, type, amount) VALUES ('tx-bad', 'GS-1', 'refund', 1)`); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("unknown type after 080: err = %v, want a CHECK refusal", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO voucher_transactions (id, voucher_id, type, amount) VALUES ('tx-orphan', 'NO-SUCH', 'top_up', 1)`); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("orphan voucher_id after 080: err = %v, want a FOREIGN KEY refusal", err)
	}
	var bal int64
	if err := d.DB.QueryRow(`SELECT balance FROM vouchers WHERE id = 'GS-1'`).Scan(&bal); err != nil || bal != 2500 {
		t.Fatalf("vouchers row after 080: balance %d err %v", bal, err)
	}
}

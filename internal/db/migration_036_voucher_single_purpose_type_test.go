package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// voucherSinglePurposeMigrationVersion is
// 036_voucher_single_purpose_type.sql (ADR-0105, ut-docs#1037). If the
// file is ever renumbered, this constant moves with it.
const voucherSinglePurposeMigrationVersion = 36

type voucherRow struct {
	id                string
	holder            sql.NullString
	original, balance int64
	currency, vtype   string
	status            string
	issuedSale        sql.NullString
	createdAt         string
}

type voucherTxRow struct {
	id, voucherID string
	saleID        sql.NullString
	txType        string
	amount        int64
	createdAt     string
}

// pre036Seed is a real shop's voucher ledger in the pre-036 shape: two
// multi-purpose vouchers (one with no holder label, one already partly
// redeemed) plus their issue rows, one redemption row and one imported
// opening-balance row with a NULL sale_id (ut-docs#1834's shape).
var (
	seedVouchers = []voucherRow{
		{"GS-1", sql.NullString{String: "Anna", Valid: true}, 5000, 3000, "EUR", "multi_purpose", "active", sql.NullString{String: "sale-a", Valid: true}, "2026-09-01T10:00:00Z"},
		{"GS-2", sql.NullString{}, 2500, 2500, "EUR", "multi_purpose", "active", sql.NullString{}, "2026-09-02T11:00:00Z"},
	}
	seedVoucherTxs = []voucherTxRow{
		{"tx-1", "GS-1", sql.NullString{String: "sale-a", Valid: true}, "issue", 5000, "2026-09-01T10:00:00Z"},
		{"tx-2", "GS-1", sql.NullString{String: "sale-b", Valid: true}, "redemption", 2000, "2026-09-03T12:00:00Z"},
		{"tx-3", "GS-2", sql.NullString{}, "issue", 2500, "2026-09-02T11:00:00Z"},
	}
)

func readVouchers(t *testing.T, d *DB) []voucherRow {
	t.Helper()
	rows, err := d.DB.Query(`SELECT id, holder_label, original_amount, balance, currency, voucher_type, status, issued_sale_id, created_at FROM vouchers ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []voucherRow
	for rows.Next() {
		var v voucherRow
		if err := rows.Scan(&v.id, &v.holder, &v.original, &v.balance, &v.currency, &v.vtype, &v.status, &v.issuedSale, &v.createdAt); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func readVoucherTxs(t *testing.T, d *DB) []voucherTxRow {
	t.Helper()
	rows, err := d.DB.Query(`SELECT id, voucher_id, sale_id, type, amount, created_at FROM voucher_transactions ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []voucherTxRow
	for rows.Next() {
		var v voucherTxRow
		if err := rows.Scan(&v.id, &v.voucherID, &v.saleID, &v.txType, &v.amount, &v.createdAt); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertVoucherSeedSurvived(t *testing.T, d *DB, when string) {
	t.Helper()
	got := readVouchers(t, d)
	if len(got) != len(seedVouchers) {
		t.Fatalf("%s: vouchers = %+v, want %+v", when, got, seedVouchers)
	}
	for i := range seedVouchers {
		if got[i] != seedVouchers[i] {
			t.Fatalf("%s: voucher[%d] = %+v, want %+v", when, i, got[i], seedVouchers[i])
		}
	}
	gotTx := readVoucherTxs(t, d)
	if len(gotTx) != len(seedVoucherTxs) {
		t.Fatalf("%s: voucher_transactions = %+v, want %+v", when, gotTx, seedVoucherTxs)
	}
	for i := range seedVoucherTxs {
		if gotTx[i] != seedVoucherTxs[i] {
			t.Fatalf("%s: voucher_transaction[%d] = %+v, want %+v", when, i, gotTx[i], seedVoucherTxs[i])
		}
	}
}

// assertPost036Shape pins ADR-0105 Decision 2's structural contract on any
// database that has run 036: vouchers.tax_rate_bp exists (nullable), the
// voucher_type CHECK admits single_purpose and still rejects anything
// else, voucher_transactions.voucher_id points at `vouchers` (never the
// _new name), both of its indexes are back (001's plain index and 012's
// partial unique redemption-once index, still enforced), nothing *_new is
// left behind, and PRAGMA foreign_key_check is clean.
func assertPost036Shape(t *testing.T, d *DB) {
	t.Helper()
	if n := columnCount(t, d, "vouchers", "tax_rate_bp"); n != 1 {
		t.Fatalf("vouchers.tax_rate_bp count = %d, want 1", n)
	}
	for _, col := range []string{"id", "holder_label", "original_amount", "balance", "currency", "voucher_type", "status", "issued_sale_id", "created_at"} {
		if n := columnCount(t, d, "vouchers", col); n != 1 {
			t.Fatalf("vouchers.%s count = %d, want 1", col, n)
		}
	}
	var notNull int
	if err := d.DB.QueryRow(`SELECT "notnull" FROM pragma_table_info('vouchers') WHERE name = 'tax_rate_bp'`).Scan(&notNull); err != nil {
		t.Fatal(err)
	}
	if notNull != 0 {
		t.Fatalf("vouchers.tax_rate_bp must be nullable (NULL for every multi-purpose voucher)")
	}
	var leftover int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE '%\_new' ESCAPE '\'`).Scan(&leftover); err != nil {
		t.Fatal(err)
	}
	if leftover != 0 {
		t.Fatalf("%d *_new object(s) left behind by the rebuild", leftover)
	}
	var parent string
	if err := d.DB.QueryRow(`SELECT "table" FROM pragma_foreign_key_list('voucher_transactions') WHERE "from" = 'voucher_id'`).Scan(&parent); err != nil {
		t.Fatalf("voucher_transactions: no voucher_id foreign key: %v", err)
	}
	if parent != "vouchers" {
		t.Fatalf("voucher_transactions.voucher_id REFERENCES %s, want vouchers", parent)
	}
	for _, idx := range []string{"idx_voucher_tx_voucher", "ux_voucher_tx_redemption_once"} {
		var n int
		if err := d.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ? AND tbl_name = 'voucher_transactions'`, idx).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("index %s missing after the rebuild", idx)
		}
	}
	rows, err := d.DB.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	violations := 0
	for rows.Next() {
		violations++
	}
	if violations != 0 {
		t.Fatalf("PRAGMA foreign_key_check reports %d violation(s) after 036", violations)
	}
}

// assertPost036Constraints exercises the rebuilt constraints for real: the
// widened CHECK, the FK, and 012's partial unique index. Leaves the ledger
// as it found it (every probe row is rolled back or deleted).
func assertPost036Constraints(t *testing.T, d *DB) {
	t.Helper()
	if _, err := d.DB.Exec(`INSERT INTO vouchers (id, original_amount, balance, voucher_type, tax_rate_bp) VALUES ('SP-probe', 1900, 1900, 'single_purpose', 1900)`); err != nil {
		t.Fatalf("a single_purpose voucher must be insertable after 036: %v", err)
	}
	var rate sql.NullInt64
	if err := d.DB.QueryRow(`SELECT tax_rate_bp FROM vouchers WHERE id = 'SP-probe'`).Scan(&rate); err != nil || !rate.Valid || rate.Int64 != 1900 {
		t.Fatalf("tax_rate_bp round trip = %+v err=%v, want 1900", rate, err)
	}
	if _, err := d.DB.Exec(`INSERT INTO vouchers (id, original_amount, balance, voucher_type) VALUES ('BAD-probe', 1, 1, 'three_purpose')`); err == nil {
		t.Fatalf("CHECK (voucher_type IN ('multi_purpose','single_purpose')) must reject an unknown type")
	}
	if _, err := d.DB.Exec(`INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount) VALUES ('tx-orphan', 'NO-SUCH', 's', 'issue', 1)`); err == nil {
		t.Fatalf("voucher_transactions.voucher_id FK must still be enforced after the rebuild")
	}
	if _, err := d.DB.Exec(`INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount) VALUES ('tx-r-a', 'SP-probe', 'sale-r', 'redemption', 1)`); err != nil {
		t.Fatalf("first redemption row: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount) VALUES ('tx-r-b', 'SP-probe', 'sale-r', 'redemption', 1)`); err == nil {
		t.Fatalf("ux_voucher_tx_redemption_once (migration 012) must survive the rebuild")
	}
	if _, err := d.DB.Exec(`DELETE FROM voucher_transactions WHERE voucher_id = 'SP-probe'`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`DELETE FROM vouchers WHERE id = 'SP-probe'`); err != nil {
		t.Fatal(err)
	}
}

// TestMigration036_UpgradesRealPre036Database is the upgrade path every
// installed till takes on its first boot after ADR-0105: a database built
// by 001..035 (vouchers.voucher_type CHECK admitting only multi_purpose,
// voucher_transactions pointing at it), seeded with a real voucher ledger,
// opened normally so 036 runs through the real runner inside its own
// transaction with foreign_keys=ON. Every row must survive byte-for-byte
// (the PARENT rebuild must not cascade its children away — 034's ordering,
// 003's proven trap), the new column and widened CHECK must be there, and
// the schema must be whole again.
func TestMigration036_UpgradesRealPre036Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre036.db")
	old := openMigratedTo(t, path, voucherSinglePurposeMigrationVersion-1)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := old.DB.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if n := columnCount(t, old, "vouchers", "tax_rate_bp"); n != 0 {
		t.Fatalf("pre-036 schema must not carry tax_rate_bp (count=%d) — is the seam building the wrong version?", n)
	}
	if _, err := old.DB.Exec(`INSERT INTO vouchers (id, original_amount, balance, voucher_type) VALUES ('SP-early', 1, 1, 'single_purpose')`); err == nil {
		t.Fatalf("pre-036 CHECK must still reject single_purpose — otherwise this test proves nothing about the widening")
	}
	for _, v := range seedVouchers {
		exec(`INSERT INTO vouchers (id, holder_label, original_amount, balance, currency, voucher_type, status, issued_sale_id, created_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			v.id, v.holder, v.original, v.balance, v.currency, v.vtype, v.status, v.issuedSale, v.createdAt)
	}
	for _, tx := range seedVoucherTxs {
		exec(`INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount, created_at) VALUES (?,?,?,?,?,?)`,
			tx.id, tx.voucherID, tx.saleID, tx.txType, tx.amount, tx.createdAt)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open (upgrade through 036): %v", err)
	}
	defer d.Close()
	var applied int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, voucherSinglePurposeMigrationVersion).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("036 not recorded as applied after the upgrade")
	}
	assertVoucherSeedSurvived(t, d, "after upgrade")
	// Every pre-existing voucher is multi-purpose and so has NO fixed rate.
	var nonNull int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM vouchers WHERE tax_rate_bp IS NOT NULL`).Scan(&nonNull); err != nil {
		t.Fatal(err)
	}
	if nonNull != 0 {
		t.Fatalf("%d pre-existing voucher(s) gained a tax_rate_bp — a multi-purpose voucher must stay NULL", nonNull)
	}
	assertPost036ShapeAndReplay(t, d, path)
}

// assertPost036ShapeAndReplay is the second half of the upgrade test, shared
// with the fresh-install test: shape, constraints, then replay safety —
// including the one property 034's replay could not need: a value written
// to the ADDED column survives a replay (the copy names tax_rate_bp, and
// the runner skips the now-redundant ADD COLUMN — ut-docs#1412).
func assertPost036ShapeAndReplay(t *testing.T, d *DB, path string) {
	t.Helper()
	assertPost036Shape(t, d)
	assertPost036Constraints(t, d)

	if _, err := d.DB.Exec(`INSERT INTO vouchers (id, original_amount, balance, voucher_type, tax_rate_bp, created_at) VALUES ('SP-kept', 700, 700, 'single_purpose', 700, '2026-09-19T09:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount, created_at) VALUES ('tx-kept', 'SP-kept', 'sale-k', 'issue', 700, '2026-09-19T09:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version >= ?`, voucherSinglePurposeMigrationVersion); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	replayed, err := Open(path)
	if err != nil {
		t.Fatalf("re-open (replay 036 against an already-migrated file): %v", err)
	}
	defer replayed.Close()
	assertPost036Shape(t, replayed)
	var rate sql.NullInt64
	var vtype string
	if err := replayed.DB.QueryRow(`SELECT voucher_type, tax_rate_bp FROM vouchers WHERE id = 'SP-kept'`).Scan(&vtype, &rate); err != nil {
		t.Fatalf("single-purpose voucher lost in replay: %v", err)
	}
	if vtype != "single_purpose" || !rate.Valid || rate.Int64 != 700 {
		t.Fatalf("after replay: type=%q rate=%+v, want single_purpose/700 — the rebuild's copy must carry tax_rate_bp", vtype, rate)
	}
	var kept int
	if err := replayed.DB.QueryRow(`SELECT COUNT(*) FROM voucher_transactions WHERE id = 'tx-kept'`).Scan(&kept); err != nil || kept != 1 {
		t.Fatalf("issue row lost in replay (count=%d err=%v)", kept, err)
	}
	if _, err := replayed.DB.Exec(`DELETE FROM voucher_transactions WHERE id = 'tx-kept'`); err != nil {
		t.Fatal(err)
	}
	if _, err := replayed.DB.Exec(`DELETE FROM vouchers WHERE id = 'SP-kept'`); err != nil {
		t.Fatal(err)
	}
	assertVoucherSeedSurvived(t, replayed, "after replay")
}

// TestMigration036_FreshInstallHasSameShape: a brand-new database (every
// migration in order, 036 rebuilding empty tables) ends in exactly the
// same shape the upgrade path produces, and the same seed survives a
// replay identically.
func TestMigration036_FreshInstallHasSameShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh036.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.DB.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, v := range seedVouchers {
		exec(`INSERT INTO vouchers (id, holder_label, original_amount, balance, currency, voucher_type, status, issued_sale_id, created_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			v.id, v.holder, v.original, v.balance, v.currency, v.vtype, v.status, v.issuedSale, v.createdAt)
	}
	for _, tx := range seedVoucherTxs {
		exec(`INSERT INTO voucher_transactions (id, voucher_id, sale_id, type, amount, created_at) VALUES (?,?,?,?,?,?)`,
			tx.id, tx.voucherID, tx.saleID, tx.txType, tx.amount, tx.createdAt)
	}
	assertVoucherSeedSurvived(t, d, "fresh install")
	assertPost036ShapeAndReplay(t, d, path)
}

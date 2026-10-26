package pos

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/money"
)

// Sales mirror change log (ut-docs#5074, migration 093). These run the REAL
// migrated schema and the real pos.CompleteSale / void / Z archive paths.

// mirrorTables are every table migration 093 adds. With the setting off,
// none of them may gain a row from anything the sale path does.
var mirrorTables = []string{
	"mirror_sales", "mirror_sale_lines", "mirror_sale_line_modifiers", "mirror_payments",
	"mirror_sale_charges", "mirror_sale_discounts", "mirror_sale_links", "mirror_closes",
	"mirror_state", "sales_mirror_log", "sales_mirror_acks",
}

func setupMirrorLogDB(t testing.TB) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "mirror.db"))
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	for _, s := range []string{
		`INSERT INTO stock_locations (id, name) VALUES ('loc1', 'Main')`,
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm1', 'SKU1', 'Coffee Beans', 1000, 1)`,
		`INSERT INTO inventory (id, item_id, variant_id, location_id, quantity, updated_at) VALUES ('inv1', 'itm1', NULL, 'loc1', 10000000, datetime('now'))`,
		`INSERT OR IGNORE INTO payment_methods (id, name, type, is_active) VALUES ('cash', 'Cash', 'cash', 1)`,
	} {
		if _, err := d.DB.Exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}
	return d.DB
}

func setSetting(t testing.TB, sqlDB *sql.DB, k, v string) {
	t.Helper()
	if _, err := sqlDB.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
		t.Fatalf("set %s: %v", k, err)
	}
}

func mirrorSale(t testing.TB, sqlDB *sql.DB) string {
	t.Helper()
	id, err := CompleteSale(context.Background(), sqlDB, SaleInput{
		SaleType: "sale", Currency: "GBP", TaxInclusive: true,
		Lines:    []SaleLineInput{articleLine()},
		Payments: []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1000)}},
	})
	if err != nil {
		t.Fatalf("CompleteSale: %v", err)
	}
	return id
}

func countRows(t testing.TB, sqlDB *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

type logRow struct{ op, ref, origin string }

func mirrorLog(t testing.TB, sqlDB *sql.DB) []logRow {
	t.Helper()
	rows, err := sqlDB.Query(`SELECT op, ref_id, origin_till_id FROM sales_mirror_log ORDER BY seq`)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	defer rows.Close()
	var out []logRow
	for rows.Next() {
		var r logRow
		if err := rows.Scan(&r.op, &r.ref, &r.origin); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// workload is the sale path the mirror observes: sales, a void, a
// provenance stamp (journal replay), a kitchen status write, a customer
// erasure and a Z archive.
func workload(t testing.TB, sqlDB *sql.DB) (saleIDs []string) {
	t.Helper()
	ctx := context.Background()
	repo := data.NewPOSRepo(sqlDB)
	for range 3 {
		saleIDs = append(saleIDs, mirrorSale(t, sqlDB))
	}
	if err := repo.UpdateSaleStatus(ctx, nil, saleIDs[0], "completed", "voided"); err != nil {
		t.Fatalf("void: %v", err)
	}
	if err := repo.SetSaleProvenance(ctx, saleIDs[1], "till-a", "2026-10-26T09:00:00Z"); err != nil {
		t.Fatalf("provenance: %v", err)
	}
	if _, err := sqlDB.Exec(`UPDATE sales SET order_status = 'ready' WHERE id = ?`, saleIDs[2]); err != nil {
		t.Fatalf("order status: %v", err)
	}
	if _, err := repo.ArchiveReport(ctx, "eod", "2026-10-26T18:00:00Z", []byte(`{"total":3000}`), "", "", time.Time{}); err != nil {
		t.Fatalf("archive: %v", err)
	}
	return saleIDs
}

// Off is INERT (the owner's hard rule for shipping dark): with the setting
// absent (every till today) the triggers write nothing — not one row in any
// table migration 093 added — across sales, a void, a provenance stamp and a
// Z archive.
func TestSalesMirrorLog_OffWritesNothing(t *testing.T) {
	sqlDB := setupMirrorLogDB(t)
	workload(t, sqlDB)
	setSetting(t, sqlDB, data.SalesMirrorEnabledKey, "0")
	workload(t, sqlDB)
	for _, tb := range mirrorTables {
		if n := countRows(t, sqlDB, tb); n != 0 {
			t.Errorf("%s has %d rows with the sales mirror off, want 0", tb, n)
		}
	}
}

// The stronger inert proof: the same workload on a database WITHOUT the
// triggers (dropped) leaves every table with exactly the same row count as
// on one with them, setting off. Any write the triggers added on the sale
// path, to any table, would show here.
func TestSalesMirrorLog_OffEqualsNoTriggers(t *testing.T) {
	with := setupMirrorLogDB(t)
	without := setupMirrorLogDB(t)
	for _, trg := range []string{"trg_sales_mirror_log_sale_ins", "trg_sales_mirror_log_sale_upd", "trg_sales_mirror_log_close_ins"} {
		if _, err := without.Exec(`DROP TRIGGER ` + trg); err != nil {
			t.Fatalf("drop %s: %v", trg, err)
		}
	}
	workload(t, with)
	workload(t, without)
	rows, err := with.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	for _, tb := range tables {
		if a, b := countRows(t, with, tb), countRows(t, without, tb); a != b {
			t.Errorf("table %s: %d rows with the triggers (off) vs %d without", tb, a, b)
		}
	}
}

// On, on the main till: one pointer per observed write, in seq order — the
// insert, the void, the provenance stamp, the close; never the kitchen
// status write.
func TestSalesMirrorLog_OnLogsEachChange(t *testing.T) {
	sqlDB := setupMirrorLogDB(t)
	setSetting(t, sqlDB, data.SalesMirrorEnabledKey, "1")
	ids := workload(t, sqlDB)
	got := mirrorLog(t, sqlDB)
	var archiveID string
	if err := sqlDB.QueryRow(`SELECT id FROM report_archive WHERE kind = 'eod'`).Scan(&archiveID); err != nil {
		t.Fatal(err)
	}
	want := []logRow{
		{"sale", ids[0], ""}, {"sale", ids[1], ""}, {"sale", ids[2], ""},
		{"sale", ids[0], ""},       // void
		{"sale", ids[1], "till-a"}, // provenance
		{"close", archiveID, ""},
	}
	if len(got) != len(want) {
		t.Fatalf("log = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("log[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	// Logging never touches the copy tables on the main till.
	for _, tb := range mirrorTables[:9] {
		if n := countRows(t, sqlDB, tb); n != 0 {
			t.Errorf("%s has %d rows on the main till, want 0", tb, n)
		}
	}
}

// A replica (sync.primary_url set) and a till whose own kill switch is on
// log nothing, even with the shop setting on.
func TestSalesMirrorLog_ReplicaAndKillSwitchLogNothing(t *testing.T) {
	for name, kv := range map[string][2]string{
		"replica":     {"sync.primary_url", "https://192.0.2.10:8090"},
		"kill switch": {data.SalesMirrorTillOffKey, "1"},
	} {
		t.Run(name, func(t *testing.T) {
			sqlDB := setupMirrorLogDB(t)
			setSetting(t, sqlDB, data.SalesMirrorEnabledKey, "1")
			setSetting(t, sqlDB, kv[0], kv[1])
			workload(t, sqlDB)
			if n := countRows(t, sqlDB, "sales_mirror_log"); n != 0 {
				t.Fatalf("log has %d rows, want 0", n)
			}
		})
	}
}

// The log row is in the SALE'S OWN transaction, and so are the sale's
// children (design §3.2): aborting the sale on its last child write (the
// payment) rolls back the header AND the log row. If a later change wrote a
// child of a completed sale outside CompleteSale's transaction, a header
// trigger could serve a half-written sale; that change must touch the
// header's status/total or add its own trigger.
func TestSalesMirrorLog_RollsBackWithTheSale(t *testing.T) {
	for _, child := range []string{"sale_lines", "payments"} {
		t.Run(child, func(t *testing.T) {
			sqlDB := setupMirrorLogDB(t)
			setSetting(t, sqlDB, data.SalesMirrorEnabledKey, "1")
			sqlDB.SetMaxOpenConns(1) // the TEMP trigger lives on one connection
			if _, err := sqlDB.Exec(`CREATE TEMP TRIGGER abort_child BEFORE INSERT ON main.` + child + ` BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
				t.Fatal(err)
			}
			_, err := CompleteSale(context.Background(), sqlDB, SaleInput{
				SaleType: "sale", Currency: "GBP", TaxInclusive: true,
				Lines:    []SaleLineInput{articleLine()},
				Payments: []PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1000)}},
			})
			if err == nil || !strings.Contains(err.Error(), "injected") {
				t.Fatalf("CompleteSale err = %v, want the injected abort", err)
			}
			if n := countRows(t, sqlDB, "sales"); n != 0 {
				t.Fatalf("sales has %d rows after the aborted sale: children are not in the sale's transaction", n)
			}
			if n := countRows(t, sqlDB, "sales_mirror_log"); n != 0 {
				t.Fatalf("log has %d rows after the aborted sale", n)
			}
		})
	}
}

// Revoking (deleting) a till deletes its acknowledgement in the same
// statement, so it leaves the prune minimum at once (design §4.3).
func TestSalesMirrorLog_TillDeleteDropsAck(t *testing.T) {
	sqlDB := setupMirrorLogDB(t)
	ctx := context.Background()
	tills := data.NewTillsRepo(sqlDB)
	a, err := tills.InsertTill(ctx, "Till 2", "h1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := tills.InsertTill(ctx, "Till 3", "h2")
	if err != nil {
		t.Fatal(err)
	}
	for id, seq := range map[string]int{a: 5, b: 9} {
		if _, err := sqlDB.Exec(`INSERT INTO sales_mirror_acks (till_id, epoch, acked_seq, acked_at) VALUES (?, 'e1', ?, 'x')`, id, seq); err != nil {
			t.Fatal(err)
		}
	}
	if err := tills.DeleteTill(ctx, a); err != nil {
		t.Fatal(err)
	}
	var acks []string
	rows, err := sqlDB.Query(`SELECT till_id FROM sales_mirror_acks`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		acks = append(acks, id)
	}
	rows.Close()
	if len(acks) != 1 || acks[0] != b {
		t.Fatalf("acks after revoking %s = %+v, want only %s", a, acks, b)
	}
}

// Overhead of the triggers on a sale (performance skill; reported in the
// review record): off vs on vs no triggers at all.
func BenchmarkCompleteSale_SalesMirror(b *testing.B) {
	for _, mode := range []string{"no_triggers", "off", "on"} {
		b.Run(mode, func(b *testing.B) {
			sqlDB := setupMirrorLogDB(b)
			switch mode {
			case "no_triggers":
				for _, trg := range []string{"trg_sales_mirror_log_sale_ins", "trg_sales_mirror_log_sale_upd", "trg_sales_mirror_log_close_ins"} {
					if _, err := sqlDB.Exec(`DROP TRIGGER ` + trg); err != nil {
						b.Fatal(err)
					}
				}
			case "on":
				setSetting(b, sqlDB, data.SalesMirrorEnabledKey, "1")
			}
			b.ResetTimer()
			for b.Loop() {
				mirrorSale(b, sqlDB)
			}
		})
	}
}

// AC3 (design §13 "Isolation"): 10,000 copied sales in the mirror_* tables
// change nothing this till reports or numbers: its Z-report figures, its
// sales aggregate, its next receipt number, its journal push queue, its
// stock and its vouchers are identical before and after.
func TestSalesMirrorCopy_NeverCountsOnThisTill(t *testing.T) {
	sqlDB := setupMirrorLogDB(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(sqlDB)
	for range 3 {
		mirrorSale(t, sqlDB)
	}
	today := time.Now().Format("2006-01-02")
	from, to := time.Now().Add(-24*time.Hour), time.Now().Add(time.Hour)
	snapshot := func() []any {
		eod, err := repo.EndOfDayInstant(ctx, from, to)
		if err != nil {
			t.Fatal(err)
		}
		eod.GeneratedAt = "" // stamped with the wall clock on every call
		agg, err := repo.SalesAggregateForTill(ctx, today, "", "", true)
		if err != nil {
			t.Fatal(err)
		}
		next, err := repo.NextReceiptNo(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		queue, err := repo.LocalSalesSince(ctx, "", 1000)
		if err != nil {
			t.Fatal(err)
		}
		queued, err := repo.CountLocalSalesSince(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		var stock float64
		var vouchers, sales int
		_ = sqlDB.QueryRow(`SELECT COALESCE(SUM(quantity), 0) FROM inventory`).Scan(&stock)
		_ = sqlDB.QueryRow(`SELECT COUNT(*) FROM vouchers`).Scan(&vouchers)
		_ = sqlDB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&sales)
		return []any{eod, agg, next, queue, queued, stock, vouchers, sales}
	}
	before := snapshot()
	if _, err := sqlDB.Exec(`
WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 10000)
INSERT INTO mirror_sales (id, src_seq, epoch, origin_till_id, receipt_no, status, sale_type, tender_type, currency,
  subtotal, discount_total, tax_total, total, rounding, service_charge_amount, service_charge_tax_basis_bp,
  voucher_issue_total, offline, created_at, completed_at, local_date, received_at)
SELECT 'copy-' || i, i, 'e1', 'till-2', 'T2-' || printf('%09d', i), 'completed', 'sale', 'cash', 'GBP',
  1000, 0, 167, 1000, 0, 0, 0, 0, 0, strftime('%Y-%m-%dT%H:%M:%SZ', 'now'), strftime('%Y-%m-%dT%H:%M:%SZ', 'now'), ?, 'x'
FROM n`, today); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec(`
INSERT INTO mirror_sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp,
  tax_amount, total_before_tax, total_after_tax)
SELECT id || '-l', id, 1, 'itm1', 'Coffee Beans', 1, 1000, 0, 2000, 167, 833, 1000 FROM mirror_sales`); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec(`
INSERT INTO mirror_payments (id, sale_id, method_id, amount, currency, change_given, tip_amount, paid_at, local_date)
SELECT id || '-p', id, 'cash', 1000, 'GBP', 0, 0, created_at, local_date FROM mirror_sales`); err != nil {
		t.Fatal(err)
	}
	after := snapshot()
	names := []string{"Z-report figures", "sales aggregate", "next receipt number", "journal push queue", "queued count", "stock", "vouchers", "sales rows"}
	for i := range before {
		if fmt.Sprintf("%#v", before[i]) != fmt.Sprintf("%#v", after[i]) {
			t.Errorf("%s changed after 10,000 copied sales:\n before %#v\n after  %#v", names[i], before[i], after[i])
		}
	}
}

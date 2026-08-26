package pos

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
	_ "modernc.org/sqlite"
)

func setupSaleDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), fmt.Sprintf("pos_%d.db", time.Now().UnixNano()))
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		t.Fatalf("set busy_timeout: %v", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("enable fks: %v", err)
	}
	stmts := []string{
		`PRAGMA foreign_keys = ON;`,
		`CREATE TABLE stock_locations (id TEXT PRIMARY KEY, name TEXT);`,
		// ut-docs#820: GetSaleDetail LEFT JOINs tables to resolve a sale's
		// TableLabel -- needed even though no test row sets sales.table_id.
		`CREATE TABLE tables (id TEXT PRIMARY KEY, label TEXT NOT NULL);`,
		`CREATE TABLE items (id TEXT PRIMARY KEY, sku TEXT, name TEXT, base_price INTEGER NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE item_variants (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, price INTEGER NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE sales (id TEXT PRIMARY KEY, receipt_no TEXT NOT NULL UNIQUE, status TEXT NOT NULL, sale_type TEXT NOT NULL, tender_type TEXT NOT NULL DEFAULT 'unknown', order_type TEXT NOT NULL DEFAULT '', table_id TEXT, offline INTEGER NOT NULL DEFAULT 0, sync_status TEXT NOT NULL DEFAULT 'queued', sync_attempts INTEGER NOT NULL DEFAULT 0, sync_next_attempt_at TEXT, sync_last_error TEXT, register_id TEXT, cashier_id TEXT, customer_id TEXT, currency TEXT NOT NULL, subtotal INTEGER NOT NULL, discount_total INTEGER NOT NULL, tax_total INTEGER NOT NULL, total INTEGER NOT NULL, service_charge_amount INTEGER NOT NULL DEFAULT 0, service_charge_tax_basis_bp INTEGER NOT NULL DEFAULT 0, voucher_issue_total INTEGER NOT NULL DEFAULT 0, rounding INTEGER NOT NULL DEFAULT 0, note TEXT, created_at TEXT NOT NULL, completed_at TEXT, voided_at TEXT);`,
		`CREATE TABLE sale_lines (id TEXT PRIMARY KEY, sale_id TEXT NOT NULL, line_no INTEGER NOT NULL, item_id TEXT, variant_id TEXT, name_snapshot TEXT NOT NULL, sku_snapshot TEXT, barcode_snapshot TEXT, quantity REAL NOT NULL, unit_price INTEGER NOT NULL, line_discount INTEGER NOT NULL DEFAULT 0, tax_rate_bp INTEGER NOT NULL, tax_amount INTEGER NOT NULL, total_before_tax INTEGER NOT NULL, total_after_tax INTEGER NOT NULL, FOREIGN KEY (sale_id) REFERENCES sales(id));`,
		`CREATE TABLE sale_line_modifiers (id TEXT PRIMARY KEY, sale_line_id TEXT NOT NULL, group_id TEXT, option_id TEXT, group_name_snapshot TEXT NOT NULL, option_name_snapshot TEXT NOT NULL, price_delta_minor INTEGER NOT NULL, FOREIGN KEY (sale_line_id) REFERENCES sale_lines(id));`,
		`CREATE TABLE sale_discounts (id TEXT PRIMARY KEY, sale_id TEXT NOT NULL, line_id TEXT, type TEXT NOT NULL, value INTEGER NOT NULL, amount INTEGER NOT NULL, reason TEXT);`,
		// ADR-0062/ut-docs#984: GetSaleDetail now always reads sale_charges,
		// even for a sale with none -- needed here even though no test row
		// writes to it yet (step 2/3 of that ADR is what starts writing).
		`CREATE TABLE sale_charges (sale_id TEXT NOT NULL, seq INTEGER NOT NULL, key TEXT NOT NULL, label TEXT NOT NULL DEFAULT '', amount_minor INTEGER NOT NULL, tax_basis_bp INTEGER NOT NULL DEFAULT 0, base TEXT NOT NULL DEFAULT 'net_lines', PRIMARY KEY (sale_id, seq));`,
		`CREATE TABLE payments (id TEXT PRIMARY KEY, sale_id TEXT NOT NULL, method_id TEXT NOT NULL, amount INTEGER NOT NULL, currency TEXT NOT NULL, reference TEXT, change_given INTEGER NOT NULL DEFAULT 0, tip_amount INTEGER NOT NULL DEFAULT 0, tip_recipient TEXT NOT NULL DEFAULT 'employee', masked_pan TEXT, auth_code TEXT, terminal_id TEXT, trace_id TEXT, voucher_id TEXT, paid_at TEXT NOT NULL, FOREIGN KEY (sale_id) REFERENCES sales(id));`,
		`CREATE TABLE sale_links (id TEXT PRIMARY KEY, sale_id TEXT NOT NULL, original_sale_id TEXT NOT NULL, reason TEXT);`,
		`CREATE TABLE stock_movements (id TEXT PRIMARY KEY, item_id TEXT, variant_id TEXT, location_id TEXT NOT NULL, sale_line_id TEXT, type TEXT NOT NULL, quantity REAL NOT NULL, created_at TEXT NOT NULL);`,
		`CREATE TABLE inventory (id TEXT PRIMARY KEY, item_id TEXT, variant_id TEXT, location_id TEXT NOT NULL, quantity REAL NOT NULL, updated_at TEXT NOT NULL, UNIQUE(item_id, variant_id, location_id));`,
		`CREATE TABLE payment_methods (id TEXT PRIMARY KEY, name TEXT, type TEXT, is_active INTEGER DEFAULT 1);`,
		`CREATE TABLE audit_log (id TEXT PRIMARY KEY, actor_id TEXT, entity_type TEXT NOT NULL, entity_id TEXT NOT NULL, action TEXT NOT NULL, data_json TEXT, created_at TEXT NOT NULL, blocked_actor_id TEXT);`,
		`CREATE TABLE plugins (id TEXT PRIMARY KEY, name TEXT, version TEXT, author TEXT, is_active INTEGER DEFAULT 1);`,
		// vouchers/voucher_transactions: column-identical to migration 068
		// (ut-docs#1008) -- UpdateSaleStatus's void path now always checks
		// voucher_transactions for issues to cascade to, even when the sale
		// issued none.
		`CREATE TABLE vouchers (id TEXT PRIMARY KEY, holder_label TEXT, original_amount INTEGER NOT NULL, balance INTEGER NOT NULL, currency TEXT NOT NULL DEFAULT 'EUR', voucher_type TEXT NOT NULL DEFAULT 'multi_purpose' CHECK (voucher_type IN ('multi_purpose')), status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','redeemed','void')), issued_sale_id TEXT, created_at TEXT NOT NULL DEFAULT (datetime('now')));`,
		`CREATE TABLE voucher_transactions (id TEXT PRIMARY KEY, voucher_id TEXT NOT NULL REFERENCES vouchers (id), sale_id TEXT, type TEXT NOT NULL CHECK (type IN ('issue','redemption')), amount INTEGER NOT NULL, created_at TEXT NOT NULL DEFAULT (datetime('now')));`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup stmt failed: %v", err)
		}
	}
	return db
}

func TestCompleteSale_SucceedsAndWritesRows(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	in := SaleInput{
		SaleType:     "sale",
		RegisterID:   "reg1",
		CashierID:    "user1",
		Currency:     "GBP",
		TaxInclusive: false,
		Lines: []SaleLineInput{
			{
				ItemID:             "itm1",
				SKU:                "SKU1",
				Name:               "Apple",
				Qty:                2,
				UnitPrice:          500,
				TaxRateBasisPoints: 2000,
				LineDiscount:       0,
				LocationID:         "loc1",
			},
		},
		Payments: []PaymentInput{
			{MethodID: "cash", Amount: 1200, Currency: "GBP"},
		},
		AllowNegativeInventory: false,
	}

	saleID, err := CompleteSale(ctx, db, in)
	if err != nil {
		t.Fatalf("CompleteSale error: %v", err)
	}
	if saleID == "" {
		t.Fatalf("expected saleID")
	}

	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 sale, got %d", count)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM sale_lines`).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 sale line, got %d", count)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM payments`).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 payment, got %d", count)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM stock_movements`).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 stock movement, got %d", count)
	}
	var qty float64
	_ = db.QueryRow(`SELECT quantity FROM inventory WHERE item_id='itm1' AND location_id='loc1'`).Scan(&qty)
	if qty != 3 {
		t.Fatalf("expected inventory 3, got %v", qty)
	}
}

// ut-docs#744: a variant scanned by barcode resolves through
// ui.PriceResolverAdapter with BOTH ItemID and VariantID set on the
// BasketLine (ItemID is kept deliberately — see ui.TestResolve_VariantBarcode
// — because tax_hook.go's tax.rate.ask payload still needs it even for a
// variant line), and pos_api.go/self_order_shop.go copy both verbatim into
// SaleLineInput. Before the fix, validateLine rejected any line with both
// set ("line cannot have both item_id and variant_id"), so a variant could
// be scanned into the basket but never tendered — this reproduces that
// exact SaleLineInput shape and asserts CompleteSale now succeeds, with
// only variant_id (not item_id) persisted on the sale_lines row, matching
// the same-shaped CHECK constraint on sale_lines/inventory/stock_movements.
func TestCompleteSale_VariantLineWithBothIDsSetIsTenderable(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm-cof','COF','Coffee', 300, 1)`)
	_, _ = db.Exec(`INSERT INTO item_variants(id, item_id, price, is_active) VALUES('var-lg','itm-cof', 400, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1',NULL,'var-lg','loc1',5,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	in := SaleInput{
		SaleType:     "sale",
		RegisterID:   "reg1",
		CashierID:    "user1",
		Currency:     "GBP",
		TaxInclusive: false,
		Lines: []SaleLineInput{
			{
				// This is exactly what ResolveShortcutLine/PriceResolverAdapter
				// produce for a variant barcode scan today: both set.
				ItemID:             "itm-cof",
				VariantID:          "var-lg",
				SKU:                "VB-1",
				Name:               "Coffee - Large",
				Qty:                1,
				UnitPrice:          400,
				TaxRateBasisPoints: 0,
				LocationID:         "loc1",
			},
		},
		Payments: []PaymentInput{
			{MethodID: "cash", Amount: 400, Currency: "GBP"},
		},
	}

	saleID, err := CompleteSale(ctx, db, in)
	if err != nil {
		t.Fatalf("CompleteSale error: %v (variant barcode scan must be tenderable)", err)
	}

	var itemID, variantID sql.NullString
	if err := db.QueryRow(`SELECT item_id, variant_id FROM sale_lines WHERE sale_id = ?`, saleID).Scan(&itemID, &variantID); err != nil {
		t.Fatalf("query sale_line: %v", err)
	}
	if itemID.Valid {
		t.Fatalf("expected item_id NULL for a variant line, got %q", itemID.String)
	}
	if !variantID.Valid || variantID.String != "var-lg" {
		t.Fatalf("expected variant_id = var-lg, got %+v", variantID)
	}

	// Stock deducted against the variant's own inventory row, not a
	// nonexistent item-level row (CurrentQty's query requires exactly one
	// of item_id/variant_id to match, mirroring the sale_lines CHECK).
	var qty float64
	if err := db.QueryRow(`SELECT quantity FROM inventory WHERE variant_id='var-lg' AND location_id='loc1'`).Scan(&qty); err != nil {
		t.Fatal(err)
	}
	if qty != 4 {
		t.Fatalf("expected inventory 4 after selling 1, got %v", qty)
	}
}

// ADR-0020: a sale line's chosen modifiers persist as their own rows,
// snapshotted (name + delta) rather than FK'd to the live option, and the
// line's unit_price already reflects the folded-in delta — no separate
// modifier-total column, no double-counting.
func TestCompleteSale_PersistsModifiers(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','COFFEE','Flat White', 320, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	in := SaleInput{
		SaleType:     "sale",
		RegisterID:   "reg1",
		CashierID:    "user1",
		Currency:     "GBP",
		TaxInclusive: false,
		Lines: []SaleLineInput{
			{
				ItemID:             "itm1",
				SKU:                "COFFEE",
				Name:               "Flat White",
				Qty:                1,
				UnitPrice:          370, // 320 base + 50 extra-shot delta, already folded in by the engine
				TaxRateBasisPoints: 2000,
				LocationID:         "loc1",
				Modifiers: []data.SelectedModifier{
					{GroupID: "g1", OptionID: "opt1", GroupName: "Extras", OptionName: "Extra shot", PriceDeltaMinor: 50},
				},
			},
		},
		Payments: []PaymentInput{
			{MethodID: "cash", Amount: 500, Currency: "GBP"},
		},
	}

	saleID, err := CompleteSale(ctx, db, in)
	if err != nil {
		t.Fatalf("CompleteSale error: %v", err)
	}

	var lineID string
	if err := db.QueryRow(`SELECT id FROM sale_lines WHERE sale_id = ?`, saleID).Scan(&lineID); err != nil {
		t.Fatalf("query sale_line id: %v", err)
	}

	var groupName, optionName string
	var delta int64
	if err := db.QueryRow(`SELECT group_name_snapshot, option_name_snapshot, price_delta_minor FROM sale_line_modifiers WHERE sale_line_id = ?`, lineID).
		Scan(&groupName, &optionName, &delta); err != nil {
		t.Fatalf("expected 1 sale_line_modifiers row: %v", err)
	}
	if groupName != "Extras" || optionName != "Extra shot" || delta != 50 {
		t.Fatalf("unexpected modifier row: group=%q option=%q delta=%d", groupName, optionName, delta)
	}

	// No double-counting: the persisted unit_price (and before-tax total,
	// exclusive-tax mode) reflect the already-folded 370, not 320 with the
	// +50 delta counted again anywhere else.
	var unitPrice, totalBeforeTax int64
	if err := db.QueryRow(`SELECT unit_price, total_before_tax FROM sale_lines WHERE id = ?`, lineID).Scan(&unitPrice, &totalBeforeTax); err != nil {
		t.Fatal(err)
	}
	if unitPrice != 370 || totalBeforeTax != 370 {
		t.Fatalf("expected unit_price/total_before_tax 370, got unit_price=%d total_before_tax=%d", unitPrice, totalBeforeTax)
	}
}

func TestCompleteSale_RejectsUnderpayment(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`)

	in := SaleInput{
		SaleType:               "sale",
		Currency:               "GBP",
		AllowNegativeInventory: false,
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 1, UnitPrice: 500, TaxRateBasisPoints: 0, LocationID: "loc1"},
		},
		Payments: []PaymentInput{
			{MethodID: "cash", Amount: 100, Currency: "GBP"},
		},
	}

	if _, err := CompleteSale(ctx, db, in); err == nil {
		t.Fatalf("expected underpayment to fail")
	}
}

// ut-docs#543: card-present reconciliation fields (masked PAN, auth code,
// terminal/trace ID) are optional metadata on a payment, same shape as
// TipAmount -- they must persist when a caller (a future card-terminal
// plugin, e.g. #515's ZVT integration) supplies them.
func TestCompleteSale_PersistsCardPresentFields(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Coffee', 370, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',20,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('card','Card','card',1)`)

	in := SaleInput{
		SaleType: "sale",
		Currency: "GBP",
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Coffee", Qty: 1, UnitPrice: 370, TaxRateBasisPoints: 0, LocationID: "loc1"},
		},
		Payments: []PaymentInput{
			{MethodID: "card", Amount: 370, MaskedPAN: "VISA •••• 4242", AuthCode: "013579", TerminalID: "TERM-01", TraceID: "TRACE-99"},
		},
	}

	saleID, err := CompleteSale(ctx, db, in)
	if err != nil {
		t.Fatalf("complete sale: %v", err)
	}
	var maskedPAN, authCode, terminalID, traceID string
	if err := db.QueryRow(`SELECT masked_pan, auth_code, terminal_id, trace_id FROM payments WHERE sale_id=?`, saleID).
		Scan(&maskedPAN, &authCode, &terminalID, &traceID); err != nil {
		t.Fatalf("read payment: %v", err)
	}
	if maskedPAN != "VISA •••• 4242" || authCode != "013579" || terminalID != "TERM-01" || traceID != "TRACE-99" {
		t.Fatalf("card-present fields not persisted correctly: %q %q %q %q", maskedPAN, authCode, terminalID, traceID)
	}
}

// The masked-PAN field must never accept anything that looks like an
// unmasked PAN -- masking happens at the boundary (here), not just at
// render time, since nothing downstream re-validates it before printing
// on a receipt or exposing it via GetSaleDetail.
func TestCompleteSale_RejectsUnmaskedPAN(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Coffee', 370, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',20,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('card','Card','card',1)`)

	in := SaleInput{
		SaleType: "sale",
		Currency: "GBP",
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Coffee", Qty: 1, UnitPrice: 370, TaxRateBasisPoints: 0, LocationID: "loc1"},
		},
		Payments: []PaymentInput{
			// A real, unmasked 16-digit PAN -- must be rejected outright.
			{MethodID: "card", Amount: 370, MaskedPAN: "4242424242424242"},
		},
	}

	if _, err := CompleteSale(ctx, db, in); err == nil {
		t.Fatalf("expected an unmasked-looking PAN to be rejected")
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&count); err != nil {
		t.Fatalf("count sales: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected the sale to roll back entirely, found %d sale rows", count)
	}
}

// Independent review, ut-docs#543: validateMaskedPAN's original digit check
// used a bare ASCII range ('0'-'9'), so a full PAN written in non-ASCII
// digits -- Arabic-Indic (used by the shipped fa/ar locales) or fullwidth
// -- bypassed the guard entirely. unicode.IsDigit must catch both.
func TestCompleteSale_RejectsUnmaskedPAN_NonASCIIDigits(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Coffee', 370, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',20,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('card','Card','card',1)`)

	for _, tc := range []struct {
		name string
		pan  string
	}{
		// A real, unmasked 16-digit PAN in Arabic-Indic digits.
		{"arabic-indic", "۴۲۴۲۴۲۴۲۴۲۴۲۴۲۴۲"},
		// Same, in fullwidth digits.
		{"fullwidth", "４２４２４２４２４２４２４２４２"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := SaleInput{
				SaleType: "sale",
				Currency: "GBP",
				Lines: []SaleLineInput{
					{ItemID: "itm1", SKU: "SKU1", Name: "Coffee", Qty: 1, UnitPrice: 370, TaxRateBasisPoints: 0, LocationID: "loc1"},
				},
				Payments: []PaymentInput{
					{MethodID: "card", Amount: 370, MaskedPAN: tc.pan},
				},
			}
			if _, err := CompleteSale(ctx, db, in); err == nil {
				t.Fatalf("expected a non-ASCII-digit unmasked PAN (%q) to be rejected", tc.pan)
			}
		})
	}
}

func TestCompleteSale_OfflineSyncFlagsAndAuditPlugins(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)
	_, _ = db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('p1','Test Plugin','1.2.3',1)`)

	in := SaleInput{
		SaleType:     "sale",
		RegisterID:   "reg1",
		CashierID:    "user1",
		Currency:     "GBP",
		TaxInclusive: false,
		Lines: []SaleLineInput{
			{
				ItemID:             "itm1",
				SKU:                "SKU1",
				Name:               "Apple",
				Qty:                1,
				UnitPrice:          500,
				TaxRateBasisPoints: 2000,
				LineDiscount:       0,
				LocationID:         "loc1",
			},
		},
		Payments: []PaymentInput{
			{MethodID: "cash", Amount: 600, Currency: "GBP"},
		},
		AllowNegativeInventory: false,
		Offline:                true,
	}

	saleID, err := CompleteSale(ctx, db, in)
	if err != nil {
		t.Fatalf("CompleteSale error: %v", err)
	}

	var offline int
	var syncStatus string
	var syncAttempts int
	var tenderType string
	if err := db.QueryRow(`SELECT offline, sync_status, sync_attempts, tender_type FROM sales WHERE id = ?`, saleID).Scan(&offline, &syncStatus, &syncAttempts, &tenderType); err != nil {
		t.Fatalf("read sale flags: %v", err)
	}
	if offline != 1 {
		t.Fatalf("expected offline=1, got %d", offline)
	}
	if syncStatus != "queued" {
		t.Fatalf("expected sync_status queued, got %s", syncStatus)
	}
	if syncAttempts != 0 {
		t.Fatalf("expected sync_attempts 0, got %d", syncAttempts)
	}
	if tenderType != "cash" {
		t.Fatalf("expected tender_type cash, got %s", tenderType)
	}

	var dataJSON string
	if err := db.QueryRow(`SELECT data_json FROM audit_log WHERE entity_id = ? LIMIT 1`, saleID).Scan(&dataJSON); err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(dataJSON), &payload); err != nil {
		t.Fatalf("decode audit log: %v", err)
	}
	pluginsRaw, ok := payload["plugins"].(map[string]any)
	if !ok {
		t.Fatalf("expected plugins in audit payload")
	}
	if pluginsRaw["p1"] != "1.2.3" {
		t.Fatalf("expected plugin version in audit payload, got %v", pluginsRaw["p1"])
	}
}

func TestCompleteSale_InclusiveTaxNoDoubleCount(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 120, 1)`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)

	in := SaleInput{
		SaleType:     "sale",
		Currency:     "GBP",
		TaxInclusive: true,
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 1, UnitPrice: 120, TaxRateBasisPoints: 2000, LocationID: "loc1"},
		},
		Payments: []PaymentInput{
			{MethodID: "cash", Amount: 120, Currency: "GBP"},
		},
	}

	saleID, err := CompleteSale(ctx, db, in)
	if err != nil {
		t.Fatalf("CompleteSale error: %v", err)
	}
	var storedTotal int64
	var taxTotal int64
	if err := db.QueryRow(`SELECT total, tax_total FROM sales WHERE id = ?`, saleID).Scan(&storedTotal, &taxTotal); err != nil {
		t.Fatalf("read sale: %v", err)
	}
	if storedTotal != 120 {
		t.Fatalf("expected total 120 (inclusive), got %d", storedTotal)
	}
	if taxTotal <= 0 {
		t.Fatalf("expected tax_total > 0 for inclusive tax, got %d", taxTotal)
	}
	// The LINE must agree with the header: with inclusive pricing the
	// after-tax total IS the ticket price; before-tax is net of the tax.
	// (Regression: after-tax used to get the tax added ON TOP → 140.)
	var before, after, lineTax int64
	if err := db.QueryRow(`SELECT total_before_tax, total_after_tax, tax_amount FROM sale_lines WHERE sale_id = ?`, saleID).
		Scan(&before, &after, &lineTax); err != nil {
		t.Fatalf("read line: %v", err)
	}
	if after != 120 {
		t.Fatalf("inclusive line total_after_tax = %d, want 120 (the ticket price)", after)
	}
	if before != 120-lineTax {
		t.Fatalf("inclusive line total_before_tax = %d, want %d", before, 120-lineTax)
	}
}

func TestCompleteSale_ExclusiveLineTotalsUnchanged(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 100, 1)`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)

	in := SaleInput{
		SaleType: "sale", Currency: "GBP", TaxInclusive: false,
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 1, UnitPrice: 100, TaxRateBasisPoints: 2000, LocationID: "loc1"},
		},
		Payments: []PaymentInput{{MethodID: "cash", Amount: 120, Currency: "GBP"}},
	}
	saleID, err := CompleteSale(ctx, db, in)
	if err != nil {
		t.Fatalf("CompleteSale: %v", err)
	}
	var before, after int64
	if err := db.QueryRow(`SELECT total_before_tax, total_after_tax FROM sale_lines WHERE sale_id = ?`, saleID).
		Scan(&before, &after); err != nil {
		t.Fatalf("read line: %v", err)
	}
	if before != 100 || after != 120 {
		t.Fatalf("exclusive line totals = %d/%d, want 100/120", before, after)
	}
}

// ut-docs#1035: computeSaleTotals used to accumulate taxTotal as a flat
// per-line sum, never adjusted for in.SaleDiscount (a whole-sale discount) --
// but VATBandsForSale (the shared function invoice_page.go/eod_tax_bands.go
// use to reconstruct a sale's VAT bands) DOES apportion the discount and
// re-derive Tax for inclusive-priced sales, so the two disagreed. This is
// the ticket's own reproduction: €11.90 inclusive @19% with a €1.90
// whole-sale discount -- pre-fix taxTotal was 190 (undiscounted); the
// correct, VATBandsForSale-consistent figure is 160.
func TestComputeSaleTotals_InclusiveSaleDiscountReducesTaxTotal(t *testing.T) {
	in := SaleInput{
		TaxInclusive: true,
		SaleDiscount: 190,
		Lines: []SaleLineInput{
			{ItemID: "itm1", Name: "Widget", Qty: 1, UnitPrice: 1190, TaxRateBasisPoints: 1900, LocationID: "loc1"},
		},
	}
	_, taxTotal, _, _, _, err := computeSaleTotals(in)
	if err != nil {
		t.Fatalf("computeSaleTotals: %v", err)
	}
	if taxTotal != 160 {
		t.Fatalf("taxTotal = %d, want 160 (was 190 before ut-docs#1035's fix)", taxTotal)
	}
}

// Multi-rate-band inclusive sale + a whole-sale discount: taxTotal must
// equal sum(VATBandsForSale(...).Tax) computed independently here from the
// same per-line facts -- a parity assertion rather than a hardcoded number,
// so it stays correct regardless of the exact largest-remainder rounding
// rule, and it proves computeSaleTotals is actually feeding VATBandsForSale
// the right per-line bands (not just correct by coincidence for one rate).
func TestComputeSaleTotals_InclusiveMultiRateDiscountMatchesVATBands(t *testing.T) {
	in := SaleInput{
		TaxInclusive: true,
		SaleDiscount: 300,
		Lines: []SaleLineInput{
			// Qty=2 + a line discount here (not just Qty=1/no-line-discount,
			// independent review F7): the reconstruction below must derive
			// the same lineNet computeSaleTotals does -- AmountForQuantity
			// then Sub(LineDiscount) -- or a qty/line-discount mistake in
			// production wouldn't be caught by this parity check.
			{ItemID: "itm1", Name: "Wine", Qty: 2, UnitPrice: 500, LineDiscount: 100, TaxRateBasisPoints: 1900, LocationID: "loc1"},
			{ItemID: "itm2", Name: "Bread", Qty: 1, UnitPrice: 500, TaxRateBasisPoints: 700, LocationID: "loc1"},
		},
	}
	_, taxTotal, _, _, _, err := computeSaleTotals(in)
	if err != nil {
		t.Fatalf("computeSaleTotals: %v", err)
	}

	// Independently reconstruct the same per-line VAT facts (via the same
	// AmountForQuantity + LineDiscount path computeSaleTotals uses, not a
	// shortcut off UnitPrice alone) and re-run the shared banding function,
	// exactly as invoice_page.go's vatBreakdown does from persisted
	// sale_lines rows.
	var vatLines []VATLine
	for _, l := range in.Lines {
		lineNet := AmountForQuantity(l.UnitPrice, l.Qty).Sub(l.LineDiscount)
		tax, gross := ComputeTaxBasisPoints(lineNet, l.TaxRateBasisPoints, in.TaxInclusive)
		vatLines = append(vatLines, VATLine{RateBP: l.TaxRateBasisPoints, LineTotal: gross.Minor(), TaxAmount: tax.Minor()})
	}
	var wantTax money.Money
	for _, b := range VATBandsForSale(vatLines, in.SaleDiscount.Minor(), true, 0, 0) {
		wantTax = wantTax.Add(money.FromMinor(b.Tax))
	}
	if taxTotal != wantTax {
		t.Fatalf("taxTotal = %d, want %d (sum of VATBandsForSale bands)", taxTotal, wantTax)
	}
	if wantTax != 139 {
		t.Fatalf("test's own reference figure = %d, want 139 -- recheck the hand-derived expectation", wantTax)
	}
}

// Exclusive pricing must be completely unaffected by this fix: VATBandsForSale
// leaves Tax untouched under a discount for the exclusive path (only Net/Gross
// move), so computeSaleTotals's taxTotal for an exclusive sale + a whole-sale
// discount must be identical before and after ut-docs#1035.
func TestComputeSaleTotals_ExclusiveSaleDiscountTaxUnchanged(t *testing.T) {
	in := SaleInput{
		TaxInclusive: false,
		SaleDiscount: 200,
		Lines: []SaleLineInput{
			{ItemID: "itm1", Name: "Widget", Qty: 1, UnitPrice: 1000, TaxRateBasisPoints: 1900, LocationID: "loc1"},
		},
	}
	_, taxTotal, _, _, total, err := computeSaleTotals(in)
	if err != nil {
		t.Fatalf("computeSaleTotals: %v", err)
	}
	if taxTotal != 190 {
		t.Fatalf("taxTotal = %d, want 190 (exclusive tax must not move with the sale discount)", taxTotal)
	}
	if total != 990 {
		t.Fatalf("total = %d, want 990 (1000-200 discounted subtotal + 190 tax)", total)
	}
}

// Independent review (F4): an over-discount (SaleDiscount exceeding the
// sale's subtotal) drives a VATBandsForSale band's Gross negative for an
// inclusive-priced sale, which -- without a clamp -- would persist a
// NEGATIVE sales.tax_total. total is already floored at 0 for the same
// over-discount case (see the IsNegative check right after this call in
// computeSaleTotals); taxTotal must be floored the same way.
func TestComputeSaleTotals_OverDiscountClampsTaxTotalNonNegative(t *testing.T) {
	in := SaleInput{
		TaxInclusive: true,
		SaleDiscount: 500, // exceeds the sale's 100-minor-unit gross
		Lines: []SaleLineInput{
			{ItemID: "itm1", Name: "Widget", Qty: 1, UnitPrice: 100, TaxRateBasisPoints: 1900, LocationID: "loc1"},
		},
	}
	_, taxTotal, _, _, total, err := computeSaleTotals(in)
	if err != nil {
		t.Fatalf("computeSaleTotals: %v", err)
	}
	if taxTotal.IsNegative() {
		t.Fatalf("taxTotal = %d, must never be negative", taxTotal)
	}
	if taxTotal != 0 {
		t.Fatalf("taxTotal = %d, want 0 for a discount that consumes the whole sale", taxTotal)
	}
	if total != 0 {
		t.Fatalf("total = %d, want 0 (already-floored case)", total)
	}
}

// Integration-level: the fix must reach the persisted sales.tax_total column
// through the real CompleteSale path, not just the pure function.
func TestCompleteSale_InclusiveSaleDiscountReducesPersistedTaxTotal(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Widget', 1190, 1)`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)

	in := SaleInput{
		SaleType:     "sale",
		Currency:     "EUR",
		TaxInclusive: true,
		SaleDiscount: 190, // €1.90 whole-sale discount
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Widget", Qty: 1, UnitPrice: 1190, TaxRateBasisPoints: 1900, LocationID: "loc1"},
		},
		Payments: []PaymentInput{
			{MethodID: "cash", Amount: 1000, Currency: "EUR"},
		},
	}

	saleID, err := CompleteSale(ctx, db, in)
	if err != nil {
		t.Fatalf("CompleteSale error: %v", err)
	}
	var storedTotal, taxTotal int64
	if err := db.QueryRow(`SELECT total, tax_total FROM sales WHERE id = ?`, saleID).Scan(&storedTotal, &taxTotal); err != nil {
		t.Fatalf("read sale: %v", err)
	}
	if storedTotal != 1000 {
		t.Fatalf("stored total = %d, want 1000 (1190 - 190 discount)", storedTotal)
	}
	if taxTotal != 160 {
		t.Fatalf("persisted sales.tax_total = %d, want 160 (was 190 before ut-docs#1035's fix)", taxTotal)
	}
}

func TestCompleteSale_RollsBackOnPaymentFailure(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	// create trigger to force payment insert failure
	_, _ = db.Exec(`CREATE TRIGGER payments_fail BEFORE INSERT ON payments WHEN NEW.reference = 'FAIL' BEGIN SELECT RAISE(ABORT, 'payment failure'); END;`)

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	in := SaleInput{
		SaleType:     "sale",
		Currency:     "GBP",
		TaxInclusive: false,
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 1, UnitPrice: 500, TaxRateBasisPoints: 0, LocationID: "loc1"},
		},
		Payments: []PaymentInput{
			{MethodID: "cash", Amount: 500, Currency: "GBP", Reference: "FAIL"},
		},
	}

	if _, err := CompleteSale(ctx, db, in); err == nil {
		t.Fatalf("expected payment failure to bubble")
	}

	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&count)
	if count != 0 {
		t.Fatalf("expected no sales persisted, got %d", count)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM sale_lines`).Scan(&count)
	if count != 0 {
		t.Fatalf("expected no sale_lines, got %d", count)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM payments`).Scan(&count)
	if count != 0 {
		t.Fatalf("expected no payments, got %d", count)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM stock_movements`).Scan(&count)
	if count != 0 {
		t.Fatalf("expected no stock_movements, got %d", count)
	}
}

func TestUpdateSaleStatus_Void(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 120, 1)`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)

	saleID, err := CompleteSale(ctx, db, SaleInput{
		SaleType:     "sale",
		Currency:     "GBP",
		TaxInclusive: true,
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 1, UnitPrice: 120, TaxRateBasisPoints: 2000, LocationID: "loc1"},
		},
		Payments: []PaymentInput{{MethodID: "cash", Amount: 120}},
	})
	if err != nil {
		t.Fatalf("complete sale: %v", err)
	}

	if err := UpdateSaleStatus(ctx, db, saleID, "voided", "actor1", "test-void"); err != nil {
		t.Fatalf("update status: %v", err)
	}
	var status string
	_ = db.QueryRow(`SELECT status FROM sales WHERE id=?`, saleID).Scan(&status)
	if status != "voided" {
		t.Fatalf("expected voided, got %s", status)
	}
}

func TestUpdateSaleStatus_UnknownSaleErrorsAndWritesNoAudit(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	// Voiding a sale that doesn't exist must fail loudly — and the whole
	// transaction must roll back, leaving NO "voided" audit row for a sale
	// that was never voided (audit-log poisoning, batch 8 review).
	err := UpdateSaleStatus(ctx, db, "no-such-sale", "voided", "actor1", "phantom")
	if err == nil {
		t.Fatal("UpdateSaleStatus(unknown sale) = nil, want error")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_id='no-such-sale'`).Scan(&count); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("phantom void left %d audit row(s), want 0", count)
	}
}

func TestCompleteSale_AllowsChangeAcrossPayments(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',20,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('card','Card','card',1)`)

	in := SaleInput{
		SaleType: "sale",
		Currency: "GBP",
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 2, UnitPrice: 500, TaxRateBasisPoints: 0, LocationID: "loc1"},
		},
		Payments: []PaymentInput{
			{MethodID: "cash", Amount: 600},
			{MethodID: "card", Amount: 500, ChangeGiven: 100},
		},
	}

	saleID, err := CompleteSale(ctx, db, in)
	if err != nil {
		t.Fatalf("complete sale: %v", err)
	}
	var change int64
	if err := db.QueryRow(`SELECT change_given FROM payments WHERE sale_id=? AND method_id='card'`, saleID).Scan(&change); err != nil {
		t.Fatalf("read payment: %v", err)
	}
	if change != 100 {
		t.Fatalf("expected change 100, got %d", change)
	}
}

func TestCompleteSale_RejectsInvalidChange(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	in := SaleInput{
		SaleType: "sale",
		Currency: "GBP",
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 1, UnitPrice: 500, TaxRateBasisPoints: 0, LocationID: "loc1"},
		},
		Payments: []PaymentInput{
			{MethodID: "cash", Amount: 500, ChangeGiven: 600},
		},
	}

	if _, err := CompleteSale(ctx, db, in); err == nil {
		t.Fatalf("expected change validation error")
	}
}

func TestCompleteSale_PersistsTipAmountWithoutAffectingTotal(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Coffee', 370, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',20,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('card','Card','card',1)`)

	in := SaleInput{
		SaleType: "sale",
		Currency: "GBP",
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Coffee", Qty: 1, UnitPrice: 370, TaxRateBasisPoints: 0, LocationID: "loc1"},
		},
		// 420 = 370 sale total + 50 tip, charged as one card transaction --
		// the shape a SumUp reader's Cloud API result would report
		// (docs/germany-pos-parity-backlog.md tip-flow gap).
		Payments: []PaymentInput{
			{MethodID: "card", Amount: 420, TipAmount: 50},
		},
	}

	saleID, err := CompleteSale(ctx, db, in)
	if err != nil {
		t.Fatalf("complete sale: %v", err)
	}
	var tip, total, saleTotal int64
	if err := db.QueryRow(`SELECT tip_amount, amount FROM payments WHERE sale_id=? AND method_id='card'`, saleID).Scan(&tip, &total); err != nil {
		t.Fatalf("read payment: %v", err)
	}
	if tip != 50 {
		t.Fatalf("expected tip_amount 50, got %d", tip)
	}
	if total != 420 {
		t.Fatalf("expected amount 420, got %d", total)
	}
	if err := db.QueryRow(`SELECT total FROM sales WHERE id=?`, saleID).Scan(&saleTotal); err != nil {
		t.Fatalf("read sale: %v", err)
	}
	if saleTotal != 370 {
		t.Fatalf("tip must not inflate sale total; expected 370, got %d", saleTotal)
	}
}

// ut-docs#72: service charge is a till-set percentage ADDED to the sale
// total -- unlike tip (metadata, excluded from total), payments must cover
// the inflated total. A payment that only covers the pre-service-charge
// subtotal must be rejected as underpayment.
func TestCompleteSale_ServiceChargeInflatesTotalAndRequiresPayment(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Steak', 1000, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	baseIn := SaleInput{
		SaleType:      "sale",
		Currency:      "GBP",
		ServiceCharge: 100, // 10% of the 1000 subtotal, pre-computed by the caller
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Steak", Qty: 1, UnitPrice: 1000, TaxRateBasisPoints: 0, LocationID: "loc1"},
		},
	}

	// A payment covering only the pre-service-charge subtotal (1000) must
	// be rejected: total is 1000 + 10% = 1100.
	underpaid := baseIn
	underpaid.Payments = []PaymentInput{{MethodID: "cash", Amount: 1000}}
	if _, err := CompleteSale(ctx, db, underpaid); err == nil {
		t.Fatalf("expected payment covering only pre-service-charge subtotal to be rejected as underpayment")
	}

	// A payment covering the inflated total succeeds and persists the
	// computed service charge amount alongside the inflated total.
	paid := baseIn
	paid.Payments = []PaymentInput{{MethodID: "cash", Amount: 1100}}
	saleID, err := CompleteSale(ctx, db, paid)
	if err != nil {
		t.Fatalf("CompleteSale with sufficient payment: %v", err)
	}
	var total, serviceCharge int64
	if err := db.QueryRow(`SELECT total, service_charge_amount FROM sales WHERE id=?`, saleID).Scan(&total, &serviceCharge); err != nil {
		t.Fatalf("read sale: %v", err)
	}
	if serviceCharge != 100 {
		t.Fatalf("expected service_charge_amount 100, got %d", serviceCharge)
	}
	if total != 1100 {
		t.Fatalf("expected total 1100 (1000 + 10%% service charge), got %d", total)
	}
}

// ut-docs#72: a sale's service charge and a payment's tip must never
// cross-contaminate -- each lands in its own column with its own
// semantics (service charge inflates the total; tip does not).
func TestCompleteSale_ServiceChargeAndTipDoNotCrossContaminate(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Steak', 1000, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('card','Card','card',1)`)

	in := SaleInput{
		SaleType:      "sale",
		Currency:      "GBP",
		ServiceCharge: 100, // 10% of the 1000 subtotal -> total 1100
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Steak", Qty: 1, UnitPrice: 1000, TaxRateBasisPoints: 0, LocationID: "loc1"},
		},
		// 1150 = 1100 total (incl. service charge) + 50 discretionary tip on
		// top, charged as one card transaction.
		Payments: []PaymentInput{
			{MethodID: "card", Amount: 1150, TipAmount: 50},
		},
	}

	saleID, err := CompleteSale(ctx, db, in)
	if err != nil {
		t.Fatalf("CompleteSale: %v", err)
	}

	var saleTotal, serviceCharge int64
	if err := db.QueryRow(`SELECT total, service_charge_amount FROM sales WHERE id=?`, saleID).Scan(&saleTotal, &serviceCharge); err != nil {
		t.Fatalf("read sale: %v", err)
	}
	if serviceCharge != 100 {
		t.Fatalf("expected service_charge_amount 100, got %d", serviceCharge)
	}
	if saleTotal != 1100 {
		t.Fatalf("expected sale total 1100 (tip must not inflate it), got %d", saleTotal)
	}

	var tip int64
	if err := db.QueryRow(`SELECT tip_amount FROM payments WHERE sale_id=?`, saleID).Scan(&tip); err != nil {
		t.Fatalf("read payment: %v", err)
	}
	if tip != 50 {
		t.Fatalf("expected tip_amount 50 on the payment, got %d", tip)
	}
}

func TestCompleteSale_RejectsNegativeTip(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Coffee', 370, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('card','Card','card',1)`)

	in := SaleInput{
		SaleType: "sale",
		Currency: "GBP",
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Coffee", Qty: 1, UnitPrice: 370, TaxRateBasisPoints: 0, LocationID: "loc1"},
		},
		Payments: []PaymentInput{
			{MethodID: "card", Amount: 370, TipAmount: -10},
		},
	}

	if _, err := CompleteSale(ctx, db, in); err == nil {
		t.Fatalf("expected negative tip validation error")
	}
}

func TestRecordPaymentFailure_PersistsAuditLog(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	failureID, err := RecordPaymentFailure(ctx, db, PaymentFailure{
		Reason:   "gateway timeout",
		Payments: []PaymentInput{{MethodID: "card", Amount: 1000}},
		Lines:    []SaleLineInput{{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 1, UnitPrice: 1000}},
		Total:    1000,
		Currency: "GBP",
	})
	if err != nil {
		t.Fatalf("record failure: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_id = ? AND action = 'payment_failed'`, failureID).Scan(&count); err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 failure audit, got %d", count)
	}
}

func TestReceiptNoGenerator_Concurrency(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',100,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	const workers = 6
	var wg sync.WaitGroup
	receipts := make(chan string, workers)
	errs := make(chan error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			saleID, err := CompleteSale(ctx, db, SaleInput{
				SaleType: "sale",
				Currency: "GBP",
				Lines:    []SaleLineInput{{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 1, UnitPrice: 500, TaxRateBasisPoints: 0, LocationID: "loc1"}},
				Payments: []PaymentInput{{MethodID: "cash", Amount: 500}},
			})
			if err != nil {
				errs <- err
				return
			}
			var receipt string
			if err := db.QueryRow(`SELECT receipt_no FROM sales WHERE id = ?`, saleID).Scan(&receipt); err != nil {
				errs <- err
				return
			}
			receipts <- receipt
		}()
	}
	wg.Wait()
	close(receipts)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent sale error: %v", err)
		}
	}
	var receiptVals []int
	for rcpt := range receipts {
		val, err := strconv.Atoi(rcpt)
		if err != nil {
			t.Fatalf("invalid receipt %s: %v", rcpt, err)
		}
		receiptVals = append(receiptVals, val)
	}
	if len(receiptVals) != workers {
		t.Fatalf("expected %d receipts, got %d", workers, len(receiptVals))
	}
	sort.Ints(receiptVals)
	for i := 1; i < len(receiptVals); i++ {
		if receiptVals[i] <= receiptVals[i-1] {
			t.Fatalf("receipts not strictly increasing: %v", receiptVals)
		}
	}
}

func TestCompleteSale_RetriesAfterReceiptConflict(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',10,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`
INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, rounding, created_at)
VALUES ('existing', '000000001', 'completed', 'sale', 'GBP', 500, 0, 0, 500, 0, ?)
`, now); err != nil {
		t.Fatalf("insert existing sale: %v", err)
	}

	origAllocator := receiptAllocator
	t.Cleanup(func() { receiptAllocator = origAllocator })
	var mu sync.Mutex
	allocations := map[*sql.Tx]string{}
	seq := []string{"000000001", "000000002"}
	receiptAllocator = func(ctx context.Context, tx *sql.Tx, repo *data.POSRepo) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if val, ok := allocations[tx]; ok {
			return val, nil
		}
		if len(seq) == 0 {
			return repo.NextReceiptNo(ctx, tx)
		}
		val := seq[0]
		seq = seq[1:]
		allocations[tx] = val
		return val, nil
	}

	if _, err := CompleteSale(ctx, db, SaleInput{
		SaleType: "sale",
		Currency: "GBP",
		Lines:    []SaleLineInput{{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 1, UnitPrice: 500, TaxRateBasisPoints: 0, LocationID: "loc1"}},
		Payments: []PaymentInput{{MethodID: "cash", Amount: 500}},
	}); err != nil {
		t.Fatalf("complete sale: %v", err)
	}

	rows, err := db.Query(`SELECT receipt_no FROM sales ORDER BY receipt_no`)
	if err != nil {
		t.Fatalf("query receipts: %v", err)
	}
	defer rows.Close()
	var receipts []string
	for rows.Next() {
		var rcpt string
		if err := rows.Scan(&rcpt); err != nil {
			t.Fatalf("scan receipt: %v", err)
		}
		receipts = append(receipts, rcpt)
	}
	if len(receipts) != 2 {
		t.Fatalf("expected 2 receipts, got %d", len(receipts))
	}
	if receipts[0] != "000000001" || receipts[1] != "000000002" {
		t.Fatalf("unexpected receipts: %v", receipts)
	}
}

// TestCompleteSale_ClampsUnknownOrderTypeToDineIn covers the independent
// review finding on ut-docs#181: internal/pages/sync_sales.go's journal
// replay passes a remote peer's OrderType straight through, unvalidated,
// unlike the live checkout's own form-parsing which only ever produces ""
// or OrderTypeTakeaway. CompleteSale is the one choke point every caller
// (cashier, kiosk, sync replay) goes through, so it must clamp there.
func TestCompleteSale_ClampsUnknownOrderTypeToDineIn(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	line := SaleLineInput{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 1, UnitPrice: 500, TaxRateBasisPoints: 0, LocationID: "loc1"}
	payment := PaymentInput{MethodID: "cash", Amount: 500}

	saleID, err := CompleteSale(ctx, db, SaleInput{
		SaleType:  "sale",
		Currency:  "GBP",
		OrderType: "'; DROP TABLE sales; --",
		Lines:     []SaleLineInput{line},
		Payments:  []PaymentInput{payment},
	})
	if err != nil {
		t.Fatalf("CompleteSale (garbage order type): %v", err)
	}
	repo := data.NewPOSRepo(db)
	detail, ok, err := repo.GetSaleDetailByID(ctx, saleID)
	if err != nil || !ok {
		t.Fatalf("GetSaleDetailByID: ok=%v err=%v", ok, err)
	}
	if detail.OrderType != "" {
		t.Fatalf("unrecognized OrderType was NOT clamped to dine-in: got %q", detail.OrderType)
	}

	saleID2, err := CompleteSale(ctx, db, SaleInput{
		SaleType:  "sale",
		Currency:  "GBP",
		OrderType: OrderTypeTakeaway,
		Lines:     []SaleLineInput{line},
		Payments:  []PaymentInput{payment},
	})
	if err != nil {
		t.Fatalf("CompleteSale (takeaway): %v", err)
	}
	detail2, ok, err := repo.GetSaleDetailByID(ctx, saleID2)
	if err != nil || !ok {
		t.Fatalf("GetSaleDetailByID: ok=%v err=%v", ok, err)
	}
	if detail2.OrderType != OrderTypeTakeaway {
		t.Fatalf("legitimate OrderTypeTakeaway was clamped away: got %q", detail2.OrderType)
	}
}

package data

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
)

// Coverage batch 8 — POSRepo lookups/ensure/payment/pricing group:
// LookupCustomer, FindActivePromo, EnsurePaymentMethod, EnsureRegister,
// EnsureUser, ListActivePluginVersions, ListActivePaymentMethods,
// ListActiveNonCashPaymentMethods, AppendPriceHistoryItem,
// AppendPriceHistoryVariant, valueOrDefault.

func newBatch8DB(t *testing.T, name string) *db.DB {
	t.Helper()
	dbo, err := db.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbo.Close() })
	return dbo
}

func TestPOSRepo_LookupCustomer(t *testing.T) {
	dbo := newBatch8DB(t, "cust.db")
	ctx := context.Background()
	repo := NewPOSRepo(dbo.DB)

	mustExec(t, dbo, `INSERT INTO customers (id, name, phone, loyalty_no)
VALUES ('cust-b8', 'Dana Vega', '+449900112233', 'LOY-B8-DANA')`)

	// By id, case-insensitively — the scanner emits whatever case the badge
	// was printed in.
	id, name, ok := repo.LookupCustomer(ctx, "CUST-B8")
	if !ok || id != "cust-b8" || name != "Dana Vega" {
		t.Fatalf("by id: got (%q,%q,%v)", id, name, ok)
	}
	// By loyalty number, case-insensitively, with surrounding whitespace.
	id, name, ok = repo.LookupCustomer(ctx, "  loy-b8-dana  ")
	if !ok || id != "cust-b8" || name != "Dana Vega" {
		t.Fatalf("by loyalty: got (%q,%q,%v)", id, name, ok)
	}
	// By exact phone.
	id, _, ok = repo.LookupCustomer(ctx, "+449900112233")
	if !ok || id != "cust-b8" {
		t.Fatalf("by phone: got (%q,%v)", id, ok)
	}
	// Empty / whitespace-only input must short-circuit to not-found.
	if _, _, ok := repo.LookupCustomer(ctx, ""); ok {
		t.Fatal("empty code must not match")
	}
	if _, _, ok := repo.LookupCustomer(ctx, "   "); ok {
		t.Fatal("whitespace-only code must not match")
	}
	// Unknown code.
	if _, _, ok := repo.LookupCustomer(ctx, "no-such-customer"); ok {
		t.Fatal("unknown code must not match")
	}
}

func TestPOSRepo_FindActivePromo(t *testing.T) {
	dbo := newBatch8DB(t, "promo.db")
	ctx := context.Background()
	repo := NewPOSRepo(dbo.DB)

	now := time.Now().UTC()
	past := now.Add(-2 * time.Hour).Format(time.RFC3339)
	future := now.Add(2 * time.Hour).Format(time.RFC3339)

	mustExec(t, dbo, `INSERT INTO promotions (code, type, value, starts_at, ends_at, customer_id, is_active) VALUES
('B8-OPEN',     'amount',  250,  NULL, NULL, NULL, 1),
('B8-PERCENT',  'percent', 1000, ?,    ?,    NULL, 1),
('B8-NOTYET',   'amount',  100,  ?,    NULL, NULL, 1),
('B8-EXPIRED',  'amount',  100,  NULL, ?,    NULL, 1),
('B8-INACTIVE', 'amount',  100,  NULL, NULL, NULL, 0),
('B8-ZERO',     'amount',  0,    NULL, NULL, NULL, 1)`,
		past, future, future, past)
	mustExec(t, dbo, `INSERT INTO customers (id, name) VALUES ('cust-promo', 'Promo Person')`)
	mustExec(t, dbo, `INSERT INTO customers (id, name) VALUES ('cust-other', 'Someone Else')`)
	mustExec(t, dbo, `INSERT INTO promotions (code, type, value, customer_id, is_active)
VALUES ('B8-TARGETED', 'amount', 500, 'cust-promo', 1)`)

	// Open-ended promo (NULL window), no customer attached.
	pType, value, ok := repo.FindActivePromo(ctx, "", "B8-OPEN")
	if !ok || pType != "amount" || value != 250 {
		t.Fatalf("B8-OPEN: got (%q,%d,%v), want (amount,250,true)", pType, value, ok)
	}
	// Inside the active window; percent value is basis points, exact.
	pType, value, ok = repo.FindActivePromo(ctx, "", "B8-PERCENT")
	if !ok || pType != "percent" || value != 1000 {
		t.Fatalf("B8-PERCENT: got (%q,%d,%v), want (percent,1000,true)", pType, value, ok)
	}
	// Just outside the window on either side: not yet started / expired.
	if _, _, ok := repo.FindActivePromo(ctx, "", "B8-NOTYET"); ok {
		t.Fatal("promo starting in the future must not apply")
	}
	if _, _, ok := repo.FindActivePromo(ctx, "", "B8-EXPIRED"); ok {
		t.Fatal("expired promo must not apply")
	}
	// is_active flag beats everything else.
	if _, _, ok := repo.FindActivePromo(ctx, "", "B8-INACTIVE"); ok {
		t.Fatal("inactive promo must not apply")
	}
	// Non-positive value is rejected post-scan (defensive: a 0-value discount
	// is meaningless and could mask data errors).
	if _, _, ok := repo.FindActivePromo(ctx, "", "B8-ZERO"); ok {
		t.Fatal("zero-value promo must not apply")
	}
	// Customer targeting: only the targeted customer gets it.
	pType, value, ok = repo.FindActivePromo(ctx, "cust-promo", "B8-TARGETED")
	if !ok || pType != "amount" || value != 500 {
		t.Fatalf("B8-TARGETED for its customer: got (%q,%d,%v)", pType, value, ok)
	}
	if _, _, ok := repo.FindActivePromo(ctx, "cust-other", "B8-TARGETED"); ok {
		t.Fatal("targeted promo must not apply to a different customer")
	}
	if _, _, ok := repo.FindActivePromo(ctx, "", "B8-TARGETED"); ok {
		t.Fatal("targeted promo must not apply with no customer attached")
	}
	// Code is trimmed before matching.
	if _, _, ok := repo.FindActivePromo(ctx, "", "  B8-OPEN  "); !ok {
		t.Fatal("promo code must be matched after trimming whitespace")
	}
	// Unknown code.
	if _, _, ok := repo.FindActivePromo(ctx, "", "B8-NOPE"); ok {
		t.Fatal("unknown promo code must not apply")
	}
}

func TestPOSRepo_EnsurePaymentMethod(t *testing.T) {
	dbo := newBatch8DB(t, "epm.db")
	ctx := context.Background()
	repo := NewPOSRepo(dbo.DB)

	// New id: creates a minimal row (name=id, type=cash, active).
	if err := repo.EnsurePaymentMethod(ctx, "b8-tender"); err != nil {
		t.Fatalf("EnsurePaymentMethod(new): %v", err)
	}
	var name, typ string
	var active int
	if err := dbo.DB.QueryRow(`SELECT name, type, is_active FROM payment_methods WHERE id = 'b8-tender'`).
		Scan(&name, &typ, &active); err != nil {
		t.Fatalf("row not created: %v", err)
	}
	if name != "b8-tender" || typ != "cash" || active != 1 {
		t.Fatalf("created row wrong: name=%q type=%q active=%d", name, typ, active)
	}

	// Idempotent: second call leaves exactly one row.
	if err := repo.EnsurePaymentMethod(ctx, "b8-tender"); err != nil {
		t.Fatalf("EnsurePaymentMethod(repeat): %v", err)
	}
	var n int
	if err := dbo.DB.QueryRow(`SELECT COUNT(*) FROM payment_methods WHERE id = 'b8-tender'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("repeat call must not duplicate, got %d rows", n)
	}

	// Migration-seeded 'card' is active with type='card' — Ensure must NOT
	// rewrite it to type='cash' (that would leak card tenders into the cash
	// drawer expectation at shift close).
	if err := repo.EnsurePaymentMethod(ctx, "card"); err != nil {
		t.Fatalf("EnsurePaymentMethod(existing active): %v", err)
	}
	if err := dbo.DB.QueryRow(`SELECT type FROM payment_methods WHERE id = 'card'`).Scan(&typ); err != nil {
		t.Fatal(err)
	}
	if typ != "card" {
		t.Fatalf("existing method's type must be untouched, got %q", typ)
	}

	// Existing but INACTIVE row: the FK it exists to satisfy is already
	// satisfied; Ensure must neither error, nor duplicate, nor force it
	// active behind the back-office's deactivation.
	mustExec(t, dbo, `INSERT INTO payment_methods (id, name, type, is_active) VALUES ('b8-retired', 'Retired', 'card', 0)`)
	if err := repo.EnsurePaymentMethod(ctx, "b8-retired"); err != nil {
		t.Fatalf("EnsurePaymentMethod(inactive existing): %v", err)
	}
	if err := dbo.DB.QueryRow(`SELECT COUNT(*), MIN(is_active) FROM payment_methods WHERE id = 'b8-retired'`).Scan(&n, &active); err != nil {
		t.Fatal(err)
	}
	if n != 1 || active != 0 {
		t.Fatalf("inactive method must stay a single inactive row, got n=%d active=%d", n, active)
	}
}

func TestPOSRepo_EnsureRegister(t *testing.T) {
	dbo := newBatch8DB(t, "reg.db")
	ctx := context.Background()
	repo := NewPOSRepo(dbo.DB)

	// Migrations seed no registers: first call creates the default.
	id, err := repo.EnsureRegister(ctx)
	if err != nil {
		t.Fatalf("EnsureRegister(empty): %v", err)
	}
	if id != "reg-default" {
		t.Fatalf("expected reg-default, got %q", id)
	}
	// Idempotent: second call returns the same id and there is still one row.
	id2, err := repo.EnsureRegister(ctx)
	if err != nil || id2 != id {
		t.Fatalf("EnsureRegister(repeat): id=%q err=%v", id2, err)
	}
	var n int
	if err := dbo.DB.QueryRow(`SELECT COUNT(*) FROM registers`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("repeat call must not create another register, got %d", n)
	}

	// With an earlier-sorting active register present, that one wins.
	mustExec(t, dbo, `INSERT INTO registers (id, name, is_active) VALUES ('reg-a-front', 'Front Till', 1)`)
	id3, err := repo.EnsureRegister(ctx)
	if err != nil || id3 != "reg-a-front" {
		t.Fatalf("expected existing active register reg-a-front, got %q err=%v", id3, err)
	}

	// Inactive registers are ignored.
	mustExec(t, dbo, `DELETE FROM registers`)
	mustExec(t, dbo, `INSERT INTO registers (id, name, is_active) VALUES ('reg-a-dead', 'Old Till', 0)`)
	id4, err := repo.EnsureRegister(ctx)
	if err != nil || id4 != "reg-default" {
		t.Fatalf("inactive register must not satisfy Ensure: got %q err=%v", id4, err)
	}
}

func TestPOSRepo_EnsureUser(t *testing.T) {
	dbo := newBatch8DB(t, "usr.db")
	ctx := context.Background()
	repo := NewPOSRepo(dbo.DB)

	// Migrations seed active users ('kiosk' from 018, 'system' from 003);
	// EnsureUser returns the first active one by id — it must NOT create a
	// new user when one exists.
	id, err := repo.EnsureUser(ctx)
	if err != nil {
		t.Fatalf("EnsureUser(seeded): %v", err)
	}
	if id != "kiosk" {
		t.Fatalf("expected first seeded active user 'kiosk', got %q", id)
	}
	var n int
	if err := dbo.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	seeded := n

	// With every user deactivated, a default cashier is created.
	mustExec(t, dbo, `UPDATE users SET is_active = 0`)
	id, err = repo.EnsureUser(ctx)
	if err != nil {
		t.Fatalf("EnsureUser(none active): %v", err)
	}
	if id != "cashier-default" {
		t.Fatalf("expected cashier-default, got %q", id)
	}
	var role string
	var active int
	if err := dbo.DB.QueryRow(`SELECT role, is_active FROM users WHERE id = 'cashier-default'`).Scan(&role, &active); err != nil {
		t.Fatalf("created user missing: %v", err)
	}
	if role != "cashier" || active != 1 {
		t.Fatalf("default user must be an active cashier, got role=%q active=%d", role, active)
	}
	// Idempotent: repeat returns the same id, no extra rows.
	id2, err := repo.EnsureUser(ctx)
	if err != nil || id2 != "cashier-default" {
		t.Fatalf("EnsureUser(repeat): id=%q err=%v", id2, err)
	}
	if err := dbo.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != seeded+1 {
		t.Fatalf("repeat call must not add users: want %d, got %d", seeded+1, n)
	}
}

func TestPOSRepo_ListActivePluginVersions(t *testing.T) {
	dbo := newBatch8DB(t, "plug.db")
	ctx := context.Background()
	repo := NewPOSRepo(dbo.DB)

	// Fresh DB seeds no plugins.
	rows, err := repo.ListActivePluginVersions(ctx, nil)
	if err != nil {
		t.Fatalf("ListActivePluginVersions(empty): %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected no plugins on a fresh DB, got %#v", rows)
	}

	// plugins has an FK to plugin_catalog(id,version) and foreign_keys=ON.
	for _, p := range [][2]string{{"com.example.zeta", "2.0.0"}, {"com.example.alpha", "1.1.0"}, {"com.example.off", "0.9.0"}} {
		mustExec(t, dbo, `INSERT INTO plugin_catalog
(id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
VALUES (?, ?, 'P', 'wasm', 'main.wasm', 'https://example.com/p', 'cafe', '0.1.0', '1', datetime('now'))`, p[0], p[1])
	}
	mustExec(t, dbo, `INSERT INTO plugins (id, name, version, entrypoint, is_active) VALUES ('com.example.zeta', 'Zeta', '2.0.0', 'main.wasm', 1)`)
	mustExec(t, dbo, `INSERT INTO plugins (id, name, version, entrypoint, is_active) VALUES ('com.example.alpha', 'Alpha', '1.1.0', 'main.wasm', 1)`)
	mustExec(t, dbo, `INSERT INTO plugins (id, name, version, entrypoint, is_active) VALUES ('com.example.off', 'Off', '0.9.0', 'main.wasm', 0)`)

	// Active only, ordered by id (alpha before zeta), inactive excluded —
	// this list stamps sale_plugin_versions on every completed sale.
	rows, err = repo.ListActivePluginVersions(ctx, nil)
	if err != nil {
		t.Fatalf("ListActivePluginVersions: %v", err)
	}
	if len(rows) != 2 ||
		rows[0].ID != "com.example.alpha" || rows[0].Version != "1.1.0" ||
		rows[1].ID != "com.example.zeta" || rows[1].Version != "2.0.0" {
		t.Fatalf("wrong rows: %#v", rows)
	}

	// Same result through an explicit transaction (the sale-completion path).
	tx, err := dbo.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	txRows, err := repo.ListActivePluginVersions(ctx, tx)
	if err != nil {
		t.Fatalf("ListActivePluginVersions(tx): %v", err)
	}
	if len(txRows) != 2 || txRows[0] != rows[0] || txRows[1] != rows[1] {
		t.Fatalf("tx path diverged: %#v vs %#v", txRows, rows)
	}
}

func TestPOSRepo_ListActivePaymentMethods(t *testing.T) {
	dbo := newBatch8DB(t, "lpm.db")
	ctx := context.Background()
	repo := NewPOSRepo(dbo.DB)

	// Replace the migration-seeded methods with a controlled set.
	mustExec(t, dbo, `DELETE FROM payment_methods`)
	mustExec(t, dbo, `INSERT INTO payment_methods (id, name, type, is_active, sort_order, plugin_id) VALUES
('cash',   'Cash',        'cash', 1, 1, NULL),
('card',   'Card',        'card', 1, 2, NULL),
('stripe', 'Stripe',      'card', 1, 9, 'com.example.stripe'),
('gift',   'Gift Card',   'voucher', 0, 3, NULL)`)

	got, err := repo.ListActivePaymentMethods(ctx)
	if err != nil {
		t.Fatalf("ListActivePaymentMethods: %v", err)
	}
	want := []PaymentMethod{
		{ID: "cash", Name: "Cash", PluginID: ""},
		{ID: "card", Name: "Card", PluginID: ""},
		{ID: "stripe", Name: "Stripe", PluginID: "com.example.stripe"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d methods, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("method[%d]: got %+v, want %+v", i, got[i], want[i])
		}
	}

	// sort_order beats id: bump cash after card and the order flips.
	mustExec(t, dbo, `UPDATE payment_methods SET sort_order = 5 WHERE id = 'cash'`)
	got, err = repo.ListActivePaymentMethods(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != "card" || got[1].ID != "cash" || got[2].ID != "stripe" {
		t.Fatalf("sort_order not respected: %#v", got)
	}
}

func TestPOSRepo_ListActiveNonCashPaymentMethods(t *testing.T) {
	dbo := newBatch8DB(t, "kiosk.db")
	ctx := context.Background()
	repo := NewPOSRepo(dbo.DB)

	mustExec(t, dbo, `DELETE FROM payment_methods`)
	mustExec(t, dbo, `INSERT INTO payment_methods (id, name, type, is_active, sort_order, plugin_id) VALUES
('cash',      'Cash',          'cash',    1, 1, NULL),
('cash2',     'Petty Cash',    'cash',    1, 2, NULL),
('card',      'Card',          'card',    1, 3, NULL),
('deadcard',  'Broken Reader', 'card',    0, 4, NULL),
('stripe',    'Stripe',        'card',    1, 5, 'com.example.stripe'),
('gift',      'Gift Card',     'voucher', 1, 6, NULL)`)

	got, err := repo.ListActiveNonCashPaymentMethods(ctx)
	if err != nil {
		t.Fatalf("ListActiveNonCashPaymentMethods: %v", err)
	}
	// The kiosk has no cash drawer (ADR-0020): EVERY type='cash' row must be
	// excluded no matter how many there are, and inactive methods must not
	// resurface. Non-cash, non-card types (voucher) are allowed through.
	want := []PaymentMethod{
		{ID: "card", Name: "Card", PluginID: ""},
		{ID: "stripe", Name: "Stripe", PluginID: "com.example.stripe"},
		{ID: "gift", Name: "Gift Card", PluginID: ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d methods, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("method[%d]: got %+v, want %+v", i, got[i], want[i])
		}
	}
	for _, m := range got {
		if m.ID == "cash" || m.ID == "cash2" {
			t.Fatalf("cash tender leaked into the kiosk list: %+v", m)
		}
		if m.ID == "deadcard" {
			t.Fatalf("inactive tender leaked into the kiosk list: %+v", m)
		}
	}

	// All-cash shop: the kiosk list is empty (the page must handle that),
	// while the staffed-till list still shows the cash tenders.
	mustExec(t, dbo, `DELETE FROM payment_methods WHERE type != 'cash'`)
	got, err = repo.ListActiveNonCashPaymentMethods(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("all-cash shop must yield an empty kiosk list, got %#v", got)
	}
	staffed, err := repo.ListActivePaymentMethods(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(staffed) != 2 {
		t.Fatalf("staffed list must still include the cash tenders, got %#v", staffed)
	}
}

func TestPOSRepo_AppendPriceHistoryItem(t *testing.T) {
	dbo := newBatch8DB(t, "phi.db")
	ctx := context.Background()
	repo := NewPOSRepo(dbo.DB)

	mustExec(t, dbo, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-b8', 'SKU-B8', 'Batch8 Widget', 500, 1)`)

	if err := repo.AppendPriceHistoryItem(ctx, "", 500, time.Now()); err == nil {
		t.Fatal("empty itemID must error")
	}

	t1 := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	if err := repo.AppendPriceHistoryItem(ctx, "itm-b8", 500, t1); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if err := repo.AppendPriceHistoryItem(ctx, "itm-b8", 725, t2); err != nil {
		t.Fatalf("second append: %v", err)
	}

	type ph struct {
		price  int64
		starts string
		ends   *string
	}
	var rows []ph
	res, err := dbo.DB.Query(`SELECT price, starts_at, ends_at FROM price_history WHERE item_id = 'itm-b8' ORDER BY starts_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	for res.Next() {
		var r ph
		if err := res.Scan(&r.price, &r.starts, &r.ends); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, r)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 price rows, got %d", len(rows))
	}
	// The first price was closed exactly when the second started — the
	// history has no gap and no overlap, in exact minor units.
	if rows[0].price != 500 || rows[0].starts != t1.Format(time.RFC3339) {
		t.Fatalf("first row wrong: %+v", rows[0])
	}
	if rows[0].ends == nil || *rows[0].ends != t2.Format(time.RFC3339) {
		t.Fatalf("first row must be closed at t2: %+v", rows[0])
	}
	if rows[1].price != 725 || rows[1].starts != t2.Format(time.RFC3339) || rows[1].ends != nil {
		t.Fatalf("second row must be the open price: %+v", rows[1])
	}
}

func TestPOSRepo_AppendPriceHistoryVariant(t *testing.T) {
	dbo := newBatch8DB(t, "phv.db")
	ctx := context.Background()
	repo := NewPOSRepo(dbo.DB)

	mustExec(t, dbo, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-b8v', 'SKU-B8V', 'Batch8 Drink', 300, 1)`)
	mustExec(t, dbo, `INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES ('var-b8', 'itm-b8v', 'SKU-B8V-L', 'Large', 350, 1)`)

	if err := repo.AppendPriceHistoryVariant(ctx, "", 350, time.Now()); err == nil {
		t.Fatal("empty variantID must error")
	}

	// An open ITEM price for the parent must not be closed by a VARIANT
	// price change — they are separate price streams (CHECK: exactly one of
	// item_id/variant_id is set).
	t0 := time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC)
	if err := repo.AppendPriceHistoryItem(ctx, "itm-b8v", 300, t0); err != nil {
		t.Fatal(err)
	}

	t1 := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)
	if err := repo.AppendPriceHistoryVariant(ctx, "var-b8", 350, t1); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if err := repo.AppendPriceHistoryVariant(ctx, "var-b8", 395, t2); err != nil {
		t.Fatalf("second append: %v", err)
	}

	var price int64
	var starts string
	var ends *string
	if err := dbo.DB.QueryRow(`SELECT price, starts_at, ends_at FROM price_history WHERE variant_id = 'var-b8' AND ends_at IS NOT NULL`).
		Scan(&price, &starts, &ends); err != nil {
		t.Fatalf("closed variant row: %v", err)
	}
	if price != 350 || starts != t1.Format(time.RFC3339) || ends == nil || *ends != t2.Format(time.RFC3339) {
		t.Fatalf("closed variant row wrong: price=%d starts=%q ends=%v", price, starts, ends)
	}
	if err := dbo.DB.QueryRow(`SELECT price, starts_at FROM price_history WHERE variant_id = 'var-b8' AND ends_at IS NULL`).
		Scan(&price, &starts); err != nil {
		t.Fatalf("open variant row: %v", err)
	}
	if price != 395 || starts != t2.Format(time.RFC3339) {
		t.Fatalf("open variant row wrong: price=%d starts=%q", price, starts)
	}

	// The parent item's own price row is untouched and still open.
	var n int
	if err := dbo.DB.QueryRow(`SELECT COUNT(*) FROM price_history WHERE item_id = 'itm-b8v' AND ends_at IS NULL AND price = 300`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("variant price change must not close the parent item's open price, got %d open item rows", n)
	}
}

func TestPOSRepo_Batch8Lookups_ClosedDBErrorPaths(t *testing.T) {
	dbo := newBatch8DB(t, "closed.db")
	ctx := context.Background()
	repo := NewPOSRepo(dbo.DB)
	dbo.Close()

	// Lookup-style calls swallow the error into a not-found — the scan
	// handler treats any failure as "no match", never a 500 mid-sale.
	if _, _, ok := repo.LookupCustomer(ctx, "cust-b8"); ok {
		t.Fatal("LookupCustomer on a closed DB must be not-found")
	}
	if _, _, ok := repo.FindActivePromo(ctx, "", "B8-OPEN"); ok {
		t.Fatal("FindActivePromo on a closed DB must be not-found")
	}
	// Everything else must surface the error, not fake success.
	if err := repo.EnsurePaymentMethod(ctx, "x"); err == nil {
		t.Fatal("EnsurePaymentMethod on a closed DB must error")
	}
	if _, err := repo.EnsureRegister(ctx); err == nil {
		t.Fatal("EnsureRegister on a closed DB must error")
	}
	if _, err := repo.EnsureUser(ctx); err == nil {
		t.Fatal("EnsureUser on a closed DB must error")
	}
	if _, err := repo.ListActivePluginVersions(ctx, nil); err == nil {
		t.Fatal("ListActivePluginVersions on a closed DB must error")
	}
	if _, err := repo.ListActivePaymentMethods(ctx); err == nil {
		t.Fatal("ListActivePaymentMethods on a closed DB must error")
	}
	if _, err := repo.ListActiveNonCashPaymentMethods(ctx); err == nil {
		t.Fatal("ListActiveNonCashPaymentMethods on a closed DB must error")
	}
	if err := repo.AppendPriceHistoryItem(ctx, "itm-x", 100, time.Now()); err == nil {
		t.Fatal("AppendPriceHistoryItem on a closed DB must error")
	}
	if err := repo.AppendPriceHistoryVariant(ctx, "var-x", 100, time.Now()); err == nil {
		t.Fatal("AppendPriceHistoryVariant on a closed DB must error")
	}
}

func TestValueOrDefault(t *testing.T) {
	cases := []struct {
		val, fallback, want string
	}{
		{"itm-1", "var-1", "itm-1"},
		{"", "var-1", "var-1"},
		{"   ", "var-1", "var-1"}, // blank-only val falls through
		{"", "", ""},
		// Non-blank val is returned as-is (not trimmed).
		{"  itm-1  ", "var-1", "  itm-1  "},
	}
	for _, c := range cases {
		if got := valueOrDefault(c.val, c.fallback); got != c.want {
			t.Fatalf("valueOrDefault(%q,%q) = %q, want %q", c.val, c.fallback, got, c.want)
		}
	}
}

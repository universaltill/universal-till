package pages

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

func newPrintAPITestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })

	cfg := &config.Config{
		Theme:   "default",
		Locales: config.Locales{Currency: "GBP", TaxRate: 20},
		Marketplace: config.MarketplaceConfig{
			EndpointURL: "http://localhost:8081",
		},
	}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(db),
	}
	mux := http.NewServeMux()
	registerPrintAPI(mux, dp)
	return mux, dp
}

func TestPrinterConfig_Defaults(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	cfg := printerConfig(context.Background(), dp)
	if cfg.Mode != "off" {
		t.Fatalf("expected default mode 'off', got %q", cfg.Mode)
	}
	if cfg.Charset != "utf8" {
		t.Fatalf("expected default charset 'utf8', got %q", cfg.Charset)
	}
	if !cfg.AutoPrint {
		t.Fatal("expected auto-print to default to true")
	}
	if cfg.Enabled() {
		t.Fatal("expected a default (unconfigured) printer to report Enabled()=false")
	}
}

// ut-docs#425: printReceiptAsync/printKitchenAsync fire best-effort
// goroutines after tender that read Settings (and, on failure, write an
// audit row) through d.Db, with no caller left holding a handle to them —
// by design, checkout must never block on printer I/O. That is correct for
// callers, but a caller that closes Db and removes the directory its file
// lives in right after tender returns can race that goroutine's still-
// in-flight DB access — reproduced live with
// `go test -race -run TestSelfOrderShop_CheckoutAppliesPluginReportedTipFromAuthorizeResponse -count=N`:
// intermittent "sql: database is closed" from data.settings.get, and (via
// t.TempDir()'s RemoveAll racing SQLite's WAL sidecar files) the actual
// "directory not empty" cleanup failure this card is about.
//
// AsyncWork/WaitForAsyncWork (common.Deps) closes that gap. This test
// proves Wait genuinely blocks until BOTH goroutines have finished, not
// just started — NOT by inferring it from Close()/RemoveAll() succeeding
// (an earlier draft did exactly that and was caught by independent review:
// over 300 iterations under -race it passed every single time even with
// AsyncWork.Add/Done removed entirely, because Close()/RemoveAll() almost
// always succeed regardless — the actual "directory not empty" failure is
// a much rarer sub-case than "a goroutine is still touching a closed db",
// so asserting on the filesystem symptom instead of the goroutine's own
// effect made this a false-pass test). Instead: configure both printers
// pointing at a receipt/ticket that doesn't exist, so each goroutine is
// GUARANTEED to fail (no network I/O involved — buildReceiptDoc/
// buildKitchenTicket error out on the missing sale before ever reaching a
// transport) and write a failure-audit row. Immediately after
// WaitForAsyncWork returns, both rows must already be present — no
// polling, no retry. Pre-fix (Wait is a no-op), the goroutines have
// essentially never finished that fast, so this fails deterministically
// rather than depending on a rare filesystem race window.
func TestAsyncPrintGoroutinesFinishBeforeWaitForAsyncWorkReturns(t *testing.T) {
	for i := 0; i < 3; i++ {
		dir, err := os.MkdirTemp("", "print-async-race-*")
		if err != nil {
			t.Fatalf("iteration %d: MkdirTemp: %v", i, err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) }) // backstop if a t.Fatal below skips the loop's own removal

		d, err := db.Open(filepath.Join(dir, "shop.db"))
		if err != nil {
			t.Fatalf("iteration %d: Open: %v", i, err)
		}
		dp := &common.Deps{
			Cfg:      &config.Config{Theme: "default"},
			Db:       d.DB,
			Settings: settings.NewStore(d.DB),
		}
		if err := dp.Settings.SetMany(context.Background(), map[string]string{
			keyPrinterMode:    "network",
			keyPrinterAddress: "unused.invalid:9100", // never dialed — the sale lookup fails first
			keyPrinterKitchen: "unused.invalid:9100",
		}); err != nil {
			t.Fatalf("iteration %d: seed printer settings: %v", i, err)
		}

		const receiptNo = "receipt-does-not-exist"
		// actorID "" -> audit_log.actor_id NULL (nullIfEmpty), so this
		// doesn't need a seeded users row to satisfy the FK.
		printReceiptAsync(dp, receiptNo, "")
		printKitchenAsync(dp, receiptNo, "")
		dp.WaitForAsyncWork()

		var failureRows int
		if err := d.QueryRow(
			`SELECT COUNT(*) FROM audit_log WHERE entity_id = ? AND action IN ('print_failed', 'kitchen_print_failed')`,
			receiptNo,
		).Scan(&failureRows); err != nil {
			t.Fatalf("iteration %d: query audit_log: %v", i, err)
		}
		if failureRows != 2 {
			t.Fatalf("iteration %d: want 2 failure-audit rows (print_failed + kitchen_print_failed) immediately after WaitForAsyncWork, got %d — WaitForAsyncWork returned before both goroutines actually finished", i, failureRows)
		}

		if err := d.Close(); err != nil {
			t.Fatalf("iteration %d: Close: %v", i, err)
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Fatalf("iteration %d: RemoveAll immediately after Close: %v", i, err)
		}
	}
}

// ut-docs#513: app.Run needs to join Deps.AsyncWork into its own shutdown
// drain, but the *common.Deps app.go can see and the one printReceiptAsync/
// printKitchenAsync actually track their goroutines on are only the SAME
// instance if pages.Init hands the caller back the exact *common.Deps it
// built every handler with — not a copy, not a differently-constructed one.
// A full app.Run()-level test can't observe this cleanly (Run doesn't export
// deps, and forcing a real listen+shutdown cycle just to peek at a private
// field is a bigger, more brittle seam than this bug needs — see the
// Architect's design notes on this card). This is the honest test within
// reach instead: boot through the REAL pages.Init entrypoint (not a
// hand-built *common.Deps + registerPrintAPI like every other test in this
// file), fire a genuine tender through the resulting mux — which internally
// calls printReceiptAsync using ITS OWN dp, not a variable this test
// controls — then call WaitForAsyncWork on the *common.Deps Init returned to
// the caller. If that's a different instance than the one the handler
// closed over, the failure-audit row this test forces (device path in a
// directory that doesn't exist) would not reliably be there yet when this
// returns, and the test would flake/fail. It doesn't, deterministically,
// which is the proof this card's fix (threading Init's Deps back to app.go)
// is wired to the right object.
func TestInit_ReturnedDepsIsTheSameInstanceAsyncPrintGoroutinesTrack(t *testing.T) {
	chdirRoot(t)               // templates resolve relative to repo root
	t.Setenv("UT_AUTH", "off") // no operator session needed to hit /api/pos/*

	// A REAL migrated DB (same reason as newPrintFlagTestDeps above): the
	// resolver Init wires the cashier engine with queries items/item_barcodes
	// joined against item_images, a table this file's OTHER hand-rolled
	// seedForPages fixture doesn't define — fine for tests that hand-build
	// *common.Deps with a stub resolver, but this test needs a genuine
	// /api/pos/scan through the real Init-built mux to resolve, which means
	// the schema has to be the real one.
	dbase, err := db.Open(filepath.Join(t.TempDir(), "wiring.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { dbase.Close() })
	// This item's own tax_code_id is unset, but pos.Service falls back to
	// the config's default tax rate for any line with a zero TaxRateBP
	// (effectiveTaxRateBP) — so the tender below reads the basket's actual
	// computed Total rather than assuming the bare item price.
	if _, err := dbase.DB.Exec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-notax','NOTAX','No-Tax Item',500,1)`); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	if _, err := dbase.DB.Exec(`INSERT INTO item_barcodes (barcode, item_id, is_primary) VALUES ('NOTAX','itm-notax',1)`); err != nil {
		t.Fatalf("seed item barcode: %v", err)
	}
	// Stock at the default location (001_init.sql seeds 'loc_main' itself) —
	// tender rejects the sale outright over insufficient stock otherwise.
	if _, err := dbase.DB.Exec(`INSERT INTO inventory (id, item_id, location_id, quantity, updated_at) VALUES ('inv-notax','itm-notax','loc_main',10,datetime('now'))`); err != nil {
		t.Fatalf("seed inventory: %v", err)
	}
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}}
	// Cancelled on cleanup, same pattern as init_test.go's own Init caller:
	// Init starts several bgCtx-scoped background loops (StartSyncPush/
	// StartSyncPull/StartCloudSync/StartEODScheduler/
	// StartAutoUpdateScheduler) that would otherwise keep running against
	// dbase after this test's own t.Cleanup closes it.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	pm, err := plugins.Init(ctx, cfg, dbase.DB)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	var wg sync.WaitGroup
	mux, dp := Init(ctx, ctx, cfg, pm, dbase.DB, nil, &wg)

	// A printer that's guaranteed to fail fast (unopenable device path, no
	// network dial) so the async goroutine's effect — a print_failed audit
	// row — is deterministic, same technique as
	// TestAsyncPrintFailureSetsFlagAndSuccessClears above.
	missing := filepath.Join(t.TempDir(), "no-such-dir")
	if err := dp.Settings.SetMany(ctx, map[string]string{
		keyPrinterMode:    "device",
		keyPrinterDevice:  filepath.Join(missing, "receipt-printer"),
		keyPrinterKitchen: filepath.Join(missing, "kitchen-printer"),
	}); err != nil {
		t.Fatalf("seed failing printer settings: %v", err)
	}

	// Scan the tax-free item (barcode "NOTAX" -> itm-notax) — a real
	// completed sale through the real mux, which is what fires
	// printReceiptAsync/printKitchenAsync from inside pos_api.go's own dp,
	// not one this test constructed.
	scanReq := httptest.NewRequest(http.MethodPost, "/api/pos/scan", strings.NewReader("code=NOTAX&qty=1"))
	scanReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	scanRec := httptest.NewRecorder()
	mux.ServeHTTP(scanRec, scanReq)
	if scanRec.Code != http.StatusOK {
		t.Fatalf("scan failed: code %d body %s", scanRec.Code, scanRec.Body.String())
	}

	// Tender the basket's ACTUAL total, not the item's bare price: an item
	// with no tax_code_id still falls back to the config-wide default tax
	// rate (pos.Service.effectiveTaxRateBP), so the price alone underpays.
	total := dp.Engine.Basket().Total.Minor()
	if total <= 0 {
		t.Fatalf("expected a positive basket total after scanning NOTAX, got %d", total)
	}
	tenderReq := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(
		fmt.Sprintf(`{"payments":[{"method":"cash","amount":%d}]}`, total)))
	tenderReq.Header.Set("Content-Type", "application/json")
	tenderRec := httptest.NewRecorder()
	mux.ServeHTTP(tenderRec, tenderReq)
	if tenderRec.Code != http.StatusOK {
		t.Fatalf("tender failed: code %d body %s", tenderRec.Code, tenderRec.Body.String())
	}

	var receiptNo string
	if err := dbase.DB.QueryRowContext(ctx, `SELECT receipt_no FROM sales LIMIT 1`).Scan(&receiptNo); err != nil {
		t.Fatalf("read receipt_no: %v", err)
	}
	if receiptNo == "" {
		t.Fatal("expected a receipt number from the completed tender")
	}

	// The crux of the test: wait on the Deps INIT RETURNED TO THE CALLER,
	// not any internal reference. If this were a different *common.Deps than
	// the one pos_api.go's tender handler used, the goroutines it started
	// would be untracked here and this Wait would return immediately with
	// nothing yet written — racing the assertions below exactly the way
	// ut-docs#513 describes graceful shutdown racing them today.
	dp.WaitForAsyncWork()

	var failureRows int
	if err := dbase.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_log WHERE entity_id = ? AND action IN ('print_failed', 'kitchen_print_failed')`,
		receiptNo,
	).Scan(&failureRows); err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	if failureRows != 2 {
		t.Fatalf("want 2 failure-audit rows (print_failed + kitchen_print_failed) immediately after "+
			"WaitForAsyncWork on Init's returned Deps, got %d — the returned *common.Deps is not the same "+
			"instance the tender handler's async print goroutines track", failureRows)
	}
}

// newPrintFlagTestDeps opens a REAL migrated SQLite DB (same harness shape
// as kitchen_print_test.go — the seedForPages fixture's hand-rolled sales
// table predates the migration-033/034 columns, so it can't exercise the
// print-failed flags) and seeds the two FK prerequisites seedReceiptSale
// relies on (sale_lines.item_id → items, payments.method_id →
// payment_methods).
func newPrintFlagTestDeps(t *testing.T) *common.Deps {
	t.Helper()
	dbase, err := db.Open(filepath.Join(t.TempDir(), "printflags.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbase.Close() })
	// The 'cash' payment_methods row is seeded by 001_init.sql already; only
	// the item behind sale_lines.item_id needs adding.
	if _, err := dbase.DB.Exec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm1','ABC','Apple',120,1)`); err != nil {
		t.Fatalf("seed prerequisites: %v", err)
	}
	return &common.Deps{
		Cfg:      &config.Config{Theme: "default"},
		Db:       dbase.DB,
		Settings: settings.NewStore(dbase.DB),
	}
}

// findRecentOrder reads a receipt's row back through the same repo call the
// /ui/orders poll uses — the surface ut-docs#517a exists for.
func findRecentOrder(t *testing.T, dp *common.Deps, receiptNo string) data.OrderListEntry {
	t.Helper()
	orders, err := data.NewPOSRepo(dp.Db).ListRecentOrders(context.Background(), 50)
	if err != nil {
		t.Fatalf("ListRecentOrders: %v", err)
	}
	for _, o := range orders {
		if o.ReceiptNo == receiptNo {
			return o
		}
	}
	t.Fatalf("receipt %s not in recent orders: %+v", receiptNo, orders)
	return data.OrderListEntry{}
}

// ut-docs#517a: a REAL failed print attempt (device transport pointed at a
// path that cannot be opened — deterministic, no network) must set the
// sale's kitchen/receipt print-failed flags, and a subsequent REAL
// successful print (device transport writing to an ordinary temp file —
// the same code path a USB printer takes) must clear them. This drives the
// actual printReceiptAsync/printKitchenAsync goroutines against a real
// migrated DB with a real sales row — not a mocked print result.
func TestAsyncPrintFailureSetsFlagAndSuccessClears(t *testing.T) {
	dp := newPrintFlagTestDeps(t)
	seedReceiptSale(t, dp, "sale-pf", "R-PF1", "sale", "", 120, 0, 0)
	ctx := context.Background()

	missing := filepath.Join(t.TempDir(), "no-such-dir")
	if err := dp.Settings.SetMany(ctx, map[string]string{
		keyPrinterMode:    "device",
		keyPrinterDevice:  filepath.Join(missing, "receipt-printer"),
		keyPrinterKitchen: filepath.Join(missing, "kitchen-printer"),
	}); err != nil {
		t.Fatalf("seed failing printer settings: %v", err)
	}

	printReceiptAsync(dp, "R-PF1", "")
	printKitchenAsync(dp, "R-PF1", "")
	dp.WaitForAsyncWork()

	entry := findRecentOrder(t, dp, "R-PF1")
	if entry.KitchenPrintFailedAt == "" {
		t.Fatal("failed kitchen print must set KitchenPrintFailedAt, got empty")
	}
	if entry.ReceiptPrintFailedAt == "" {
		t.Fatal("failed receipt print must set ReceiptPrintFailedAt, got empty")
	}

	// A later successful attempt clears the flags — the warning must not
	// stay stuck after the paper roll is replaced.
	okDir := t.TempDir()
	receiptDev := filepath.Join(okDir, "receipt-ok")
	kitchenDev := filepath.Join(okDir, "kitchen-ok")
	for _, p := range []string{receiptDev, kitchenDev} {
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatalf("create fake device file: %v", err)
		}
	}
	if err := dp.Settings.SetMany(ctx, map[string]string{
		keyPrinterDevice:  receiptDev,
		keyPrinterKitchen: kitchenDev,
	}); err != nil {
		t.Fatalf("seed working printer settings: %v", err)
	}

	printReceiptAsync(dp, "R-PF1", "")
	printKitchenAsync(dp, "R-PF1", "")
	dp.WaitForAsyncWork()

	// Sanity: the "printer" files actually received bytes, so this was a
	// genuine successful print, not the disabled no-op path.
	for _, p := range []string{receiptDev, kitchenDev} {
		if fi, err := os.Stat(p); err != nil || fi.Size() == 0 {
			t.Fatalf("expected print bytes written to %s (err=%v)", p, err)
		}
	}
	entry = findRecentOrder(t, dp, "R-PF1")
	if entry.KitchenPrintFailedAt != "" {
		t.Fatalf("successful kitchen print must clear the flag, got %q", entry.KitchenPrintFailedAt)
	}
	if entry.ReceiptPrintFailedAt != "" {
		t.Fatalf("successful receipt print must clear the flag, got %q", entry.ReceiptPrintFailedAt)
	}
}

// The MANUAL kitchen reprint (POST /api/print/kitchen) participates too: an
// operator who fixes the printer and reprints from the till must see the
// warning clear, and a manual attempt that fails must set it.
func TestManualKitchenReprintSetsAndClearsPrintFailedFlag(t *testing.T) {
	dp := newPrintFlagTestDeps(t)
	mux := http.NewServeMux()
	registerKitchenPrintAPI(mux, dp)
	seedReceiptSale(t, dp, "sale-mk", "R-MK1", "sale", "", 120, 0, 0)
	ctx := context.Background()

	post := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/print/kitchen", strings.NewReader("receipt_no=R-MK1"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		mux.ServeHTTP(rec, req)
		return rec
	}

	// Failing manual print (unopenable device path) sets the flag.
	if err := dp.Settings.SetMany(ctx, map[string]string{
		keyPrinterKitchen: filepath.Join(t.TempDir(), "no-such-dir", "kitchen-printer"),
	}); err != nil {
		t.Fatal(err)
	}
	if rec := post(); rec.Code != http.StatusBadGateway {
		t.Fatalf("failing manual print: status = %d, want 502 (body %q)", rec.Code, rec.Body.String())
	}
	if entry := findRecentOrder(t, dp, "R-MK1"); entry.KitchenPrintFailedAt == "" {
		t.Fatal("failed manual kitchen print must set KitchenPrintFailedAt")
	}

	// Successful manual print (writable temp-file device) clears it.
	kitchenDev := filepath.Join(t.TempDir(), "kitchen-ok")
	if err := os.WriteFile(kitchenDev, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.SetMany(ctx, map[string]string{keyPrinterKitchen: kitchenDev}); err != nil {
		t.Fatal(err)
	}
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("successful manual print: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if entry := findRecentOrder(t, dp, "R-MK1"); entry.KitchenPrintFailedAt != "" {
		t.Fatalf("successful manual kitchen print must clear the flag, got %q", entry.KitchenPrintFailedAt)
	}
}

// Independent review, ut-docs#517a: the failure this feature exists to
// surface — an out-of-paper / unplugged / hung printer — does NOT fail fast.
// Both transports block until the print context's own deadline (deviceTransport
// selects on ctx.Done(), networkTransport hands the deadline to
// SetWriteDeadline), so printReceipt/printKitchen return their error with that
// context ALREADY EXPIRED. Recording the failure with it made database/sql drop
// the write before it reached the DB: no audit row, no ⚠ on /orders, the paid
// kiosk order silently lost — precisely the reported bug, in the precise case
// it was reported for. The failure must be written on a fresh context.
func TestAsyncPrintFailureIsRecordedWhenPrintCtxExpired(t *testing.T) {
	dp := newPrintFlagTestDeps(t)
	seedReceiptSale(t, dp, "sale-exp", "R-EXP1", "sale", "", 120, 0, 0)
	ctx := context.Background()

	// A configured, enabled printer — the settings read must succeed, so the
	// attempt is really made (this is not the "printing off" no-op path).
	if err := dp.Settings.SetMany(ctx, map[string]string{
		keyPrinterMode:    "device",
		keyPrinterDevice:  filepath.Join(t.TempDir(), "receipt-printer"),
		keyPrinterKitchen: filepath.Join(t.TempDir(), "kitchen-printer"),
	}); err != nil {
		t.Fatalf("seed printer settings: %v", err)
	}

	// Stand in for the hung printer: block until the print budget is spent,
	// then fail with the context error, exactly as the real transports do.
	// Two shapes: printKitchen's signature grew a station fan-out
	// (ut-docs#516, total/failures/err) that printReceipt never had.
	hang := func(ctx context.Context, _ *common.Deps, _ string) error {
		<-ctx.Done()
		return fmt.Errorf("printer write: %w", ctx.Err())
	}
	hangKitchen := func(ctx context.Context, _ *common.Deps, _, _ string) (int, []kitchenSendFailure, error) {
		<-ctx.Done()
		return 0, nil, fmt.Errorf("printer write: %w", ctx.Err())
	}
	restoreTimeout, restoreReceipt, restoreKitchen := printAsyncTimeout, printReceiptFn, printKitchenFn
	t.Cleanup(func() {
		printAsyncTimeout, printReceiptFn, printKitchenFn = restoreTimeout, restoreReceipt, restoreKitchen
	})
	printAsyncTimeout = 50 * time.Millisecond
	printReceiptFn, printKitchenFn = hang, hangKitchen

	printReceiptAsync(dp, "R-EXP1", "")
	printKitchenAsync(dp, "R-EXP1", "")
	dp.WaitForAsyncWork()

	entry := findRecentOrder(t, dp, "R-EXP1")
	if entry.ReceiptPrintFailedAt == "" {
		t.Error("a receipt print that hung until its deadline must still flag the sale, got empty")
	}
	if entry.KitchenPrintFailedAt == "" {
		t.Error("a kitchen print that hung until its deadline must still flag the sale, got empty")
	}
	// The audit trail must survive the same expiry (it did not before).
	audits, err := data.NewPOSRepo(dp.Db).ListAudit(ctx, data.AuditFilters{Limit: 50})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	var gotReceipt, gotKitchen bool
	for _, a := range audits {
		if a.EntityID != "R-EXP1" {
			continue
		}
		switch a.Action {
		case "print_failed":
			gotReceipt = true
		case "kitchen_print_failed":
			gotKitchen = true
		}
	}
	if !gotReceipt || !gotKitchen {
		t.Errorf("both print failures must be audited (receipt=%v kitchen=%v)", gotReceipt, gotKitchen)
	}
}

// Independent review, ut-docs#517a: the receipt warning must be clearable.
// A receipt only auto-prints once (at tender), so the journal's reprint is the
// ONLY later attempt a shop can make — and the shipped manual tells them to
// make it. Without this, a receipt ⚠ set once stayed on /orders forever, and
// a permanent warning is a warning nobody reads.
func TestManualReceiptReprintSetsAndClearsPrintFailedFlag(t *testing.T) {
	dp := newPrintFlagTestDeps(t)
	mux := http.NewServeMux()
	registerPrintAPI(mux, dp)
	seedReceiptSale(t, dp, "sale-mr", "R-MR1", "sale", "", 120, 0, 0)
	ctx := context.Background()

	post := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/print/receipt/R-MR1", nil))
		return rec
	}

	// Failing reprint (unopenable device path) flags the sale.
	if err := dp.Settings.SetMany(ctx, map[string]string{
		keyPrinterMode:   "device",
		keyPrinterDevice: filepath.Join(t.TempDir(), "no-such-dir", "receipt-printer"),
	}); err != nil {
		t.Fatal(err)
	}
	if rec := post(); rec.Code != http.StatusBadGateway {
		t.Fatalf("failing reprint: status = %d, want 502 (body %q)", rec.Code, rec.Body.String())
	}
	if entry := findRecentOrder(t, dp, "R-MR1"); entry.ReceiptPrintFailedAt == "" {
		t.Fatal("failed manual reprint must set ReceiptPrintFailedAt")
	}

	// Successful reprint clears it — the operator fixed the printer.
	dev := filepath.Join(t.TempDir(), "receipt-ok")
	if err := os.WriteFile(dev, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.SetMany(ctx, map[string]string{keyPrinterDevice: dev}); err != nil {
		t.Fatal(err)
	}
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("successful reprint: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if entry := findRecentOrder(t, dp, "R-MR1"); entry.ReceiptPrintFailedAt != "" {
		t.Fatalf("successful manual reprint must clear the flag, got %q", entry.ReceiptPrintFailedAt)
	}
}

func TestReceiptDesignFromSettings_Defaults(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	rd := receiptDesignFromSettings(context.Background(), dp)
	if rd.Footer != "Thank you!" {
		t.Fatalf("expected the friendly default footer, got %q", rd.Footer)
	}
	if rd.ShowSKU {
		t.Fatal("expected ShowSKU to default to false")
	}
	if !rd.ShowTax {
		t.Fatal("expected ShowTax to default to true")
	}
	if !rd.ShowBarcode {
		t.Fatal("expected ShowBarcode to default to true")
	}
	if len(rd.Header) != 0 {
		t.Fatalf("expected no header lines by default, got %+v", rd.Header)
	}
}

func TestReceiptLogoRaster_NoFileReturnsNilNotError(t *testing.T) {
	rd := receiptDesign{ShowLogo: true}
	// No logo file exists at paths.Data(...) in this fresh temp dir —
	// must degrade to no logo, never block a receipt over a missing file.
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })
	if raster := receiptLogoRaster(rd); raster != nil {
		t.Fatalf("expected nil raster when no logo file exists, got %d bytes", len(raster))
	}
	if raster := receiptLogoRaster(receiptDesign{ShowLogo: false}); raster != nil {
		t.Fatal("expected nil raster when ShowLogo is false, regardless of any file")
	}
}

func seedReceiptSale(t *testing.T, dp *common.Deps, id, receiptNo, saleType, originalReceiptFor string, total, tip, changeGiven int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES(?, ?, 'completed', ?, 'GBP', ?, 0, 0, ?, datetime('now'), datetime('now'))`, id, receiptNo, saleType, total, total); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES(?, ?, 1, 'itm1', 'Apple', 'ABC', 1, ?, 0, 0, ?, ?)`, id+"-line1", id, total, total, total); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, tip_amount, paid_at) VALUES(?,?,?,?,'GBP',?,?,datetime('now'))`,
		id+"-pay", id, "cash", total, changeGiven, tip); err != nil {
		t.Fatal(err)
	}
	if originalReceiptFor != "" {
		if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_links(id, sale_id, original_sale_id, reason) VALUES(?,?,?,'test')`, id+"-link", id, originalReceiptFor); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBuildReceiptDoc_NormalSale(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	seedReceiptSale(t, dp, "sale1", "R001", "sale", "", 120, 0, 0)

	doc, err := buildReceiptDoc(context.Background(), dp, "R001")
	if err != nil {
		t.Fatal(err)
	}
	if doc.StoreName == "" {
		t.Fatal("expected a store name")
	}
	if len(doc.Lines) != 1 || doc.Lines[0].Name != "Apple" {
		t.Fatalf("expected the sale's line, got %+v", doc.Lines)
	}
	if doc.Barcode != "R001" {
		t.Fatalf("expected the receipt number as the scan-to-refund barcode (ShowBarcode defaults true), got %q", doc.Barcode)
	}
	if !doc.KickDrawer {
		t.Fatal("expected the drawer to kick for a cash payment")
	}
	for _, m := range doc.Meta {
		if strings.Contains(m, "REFUND") {
			t.Fatalf("expected no REFUND marker on a plain sale, got %+v", doc.Meta)
		}
	}
}

func TestBuildReceiptDoc_RefundReferencesOriginalReceipt(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	seedReceiptSale(t, dp, "sale1", "R001", "sale", "", 120, 0, 0)
	seedReceiptSale(t, dp, "return1", "R002", "return", "sale1", 120, 0, 0)

	doc, err := buildReceiptDoc(context.Background(), dp, "R002")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(doc.Meta, "\n")
	if !strings.Contains(joined, "REFUND") {
		t.Fatalf("expected a REFUND marker, got %+v", doc.Meta)
	}
	if !strings.Contains(joined, "R001") {
		t.Fatalf("expected the original receipt number referenced, got %+v", doc.Meta)
	}
}

func TestBuildReceiptDoc_TipAndChangeShowAsPaymentLines(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	seedReceiptSale(t, dp, "sale1", "R001", "sale", "", 1000, 150, 200)

	doc, err := buildReceiptDoc(context.Background(), dp, "R001")
	if err != nil {
		t.Fatal(err)
	}
	var change, tip *string
	for i, p := range doc.Payments {
		if p.Label == "Change" {
			change = &doc.Payments[i].Amount
		}
		if p.Label == "Tip" {
			tip = &doc.Payments[i].Amount
		}
	}
	if change == nil {
		t.Fatalf("expected a Change payment line, got %+v", doc.Payments)
	}
	if tip == nil {
		t.Fatalf("expected a Tip payment line, got %+v", doc.Payments)
	}
	// Assert the actual printed amounts, not just that a line with the
	// right label exists — a regression that mislabeled or swapped the
	// minor-unit values would still pass a label-only check.
	if *change != "£2.00" {
		t.Fatalf("expected Change £2.00 (change_given=200), got %q", *change)
	}
	if *tip != "£1.50" {
		t.Fatalf("expected Tip £1.50 (tip_amount=150), got %q", *tip)
	}
}

// ut-docs#72: a non-zero service charge shows as its own receipt line,
// distinct from Tax/Discount/TOTAL and from a payment's Tip line.
func TestBuildReceiptDoc_ServiceChargeShowsAsDistinctTotalsLine(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	ctx := context.Background()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, service_charge_amount, created_at, completed_at)
VALUES('sale-sc', 'R-SC1', 'completed', 'sale', 'GBP', 1000, 0, 0, 1100, 100, datetime('now'), datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES('sale-sc-line1', 'sale-sc', 1, 'itm1', 'Apple', 'ABC', 1, 1000, 0, 0, 1000, 1000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, tip_amount, paid_at) VALUES('sale-sc-pay','sale-sc','cash',1100,'GBP',0,0,datetime('now'))`); err != nil {
		t.Fatal(err)
	}

	doc, err := buildReceiptDoc(ctx, dp, "R-SC1")
	if err != nil {
		t.Fatal(err)
	}
	var serviceCharge *string
	for i, kv := range doc.Totals {
		if kv.Label == "Service Charge" {
			serviceCharge = &doc.Totals[i].Amount
		}
	}
	if serviceCharge == nil {
		t.Fatalf("expected a Service Charge totals line, got %+v", doc.Totals)
	}
	if *serviceCharge != "£1.00" {
		t.Fatalf("expected Service Charge £1.00 (service_charge_amount=100), got %q", *serviceCharge)
	}
}

// A sale with no service charge (the common case, service_charge_amount
// defaults to 0) must NOT show a Service Charge line at all.
func TestBuildReceiptDoc_NoServiceChargeLineWhenZero(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	seedReceiptSale(t, dp, "sale1", "R001", "sale", "", 120, 0, 0)

	doc, err := buildReceiptDoc(context.Background(), dp, "R001")
	if err != nil {
		t.Fatal(err)
	}
	for _, kv := range doc.Totals {
		if kv.Label == "Service Charge" {
			t.Fatalf("expected no Service Charge line when service_charge_amount is 0, got %+v", doc.Totals)
		}
	}
}

func TestBuildReceiptDoc_UnknownReceipt(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	if _, err := buildReceiptDoc(context.Background(), dp, "NO-SUCH-RECEIPT"); err == nil {
		t.Fatal("expected an error for an unknown receipt number")
	}
}

// --- HTTP handlers ---

func TestPostSettingsPrinter_ValidatesModeAndCharset(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPrintAPITestDeps(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/settings/printer", strings.NewReader("mode=bogus"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an invalid mode, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/settings/printer", strings.NewReader("mode=network&address=192.168.1.50:9100&charset=weird&autoPrint=on"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for a valid mode, got %d: %s", rec.Code, rec.Body.String())
	}
	cfg := printerConfig(context.Background(), dp)
	if cfg.Mode != "network" || cfg.Address != "192.168.1.50:9100" {
		t.Fatalf("expected the settings persisted, got %+v", cfg)
	}
	// An unrecognized charset silently falls back to utf8 rather than
	// storing garbage.
	if cfg.Charset != "utf8" {
		t.Fatalf("expected charset to fall back to utf8, got %q", cfg.Charset)
	}
}

func TestPostSettingsPrinter_RequiresManager(t *testing.T) {
	mux, _ := newPrintAPITestDeps(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/settings/printer", strings.NewReader("mode=off"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager session, got %d", rec.Code)
	}
}

func TestPostPrintTest_FailsCleanlyWithNoPrinterConfigured(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newPrintAPITestDeps(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/print/test", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 with printer off, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPostPrintTest_RequiresManager(t *testing.T) {
	mux, _ := newPrintAPITestDeps(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/print/test", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager session, got %d", rec.Code)
	}
}

func TestPostPrintLabels_NoItemIDFails(t *testing.T) {
	mux, _ := newPrintAPITestDeps(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/print/labels", strings.NewReader("item_id=does-not-exist"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown item, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestClampCopies(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 1}, {-5, 1}, {1, 1}, {50, 50}, {51, 50}, {999, 50}, {3, 3},
	}
	for _, c := range cases {
		if got := clampCopies(c.in); got != c.want {
			t.Errorf("clampCopies(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestPostPrintLabels_HandlesOutOfRangeCopiesGracefully(t *testing.T) {
	// The real clamped value is only observable on the success path (needs
	// a working print transport, not available here) — clampCopies itself
	// is directly unit-tested above. This just confirms an out-of-range
	// copies value never crashes or short-circuits validation before the
	// item-not-found check runs.
	mux, _ := newPrintAPITestDeps(t)
	for _, copies := range []string{"0", "-5", "999", "3"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/print/labels", strings.NewReader("item_id=does-not-exist&copies="+copies))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("copies=%s: expected 404 (item not found), got %d: %s", copies, rec.Code, rec.Body.String())
		}
	}
}

// ut-docs#585: a sale with recorded TSE evidence (fiscal_tse_signatures,
// written at tender time from the fiscal.sign.ask v1.1.0 approved response)
// prints the §6 KassenSichV field values as plain-text Meta lines on the
// ESC/POS path — real values, no QR (raster QR on that path is a declared
// non-goal for #585). A sale with no recorded evidence prints no TSE
// evidence lines at all, never placeholders.
func TestBuildReceiptDoc_TSESignatureLinesWhenRecorded(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	seedReceiptSale(t, dp, "sale1", "R001", "sale", "", 120, 0, 0)
	ctx := context.Background()

	// No evidence recorded: no TSE evidence lines (and no placeholders).
	doc, err := buildReceiptDoc(ctx, dp, "R001")
	if err != nil {
		t.Fatal(err)
	}
	if meta := strings.Join(doc.Meta, "\n"); strings.Contains(meta, "TSE serial") || strings.Contains(meta, "TSE transaction") {
		t.Fatalf("no recorded evidence must mean no TSE evidence lines, got %+v", doc.Meta)
	}

	if err := data.NewPOSRepo(dp.Db).RecordFiscalTSESignature(ctx, data.FiscalTSESignature{
		SaleID:             "sale1",
		TransactionNumber:  4711,
		SignatureCounter:   12345,
		SerialNumber:       "TSE-TEST-SERIAL-1",
		StartTime:          "2026-08-15T10:31:00Z",
		LogTime:            "2026-08-15T10:31:02Z",
		Signature:          "TESTSIGBASE64==",
		SignatureAlgorithm: "ecdsa-plain-SHA256",
	}); err != nil {
		t.Fatal(err)
	}

	doc, err = buildReceiptDoc(ctx, dp, "R001")
	if err != nil {
		t.Fatal(err)
	}
	meta := strings.Join(doc.Meta, "\n")
	for _, want := range []string{"TSE-TEST-SERIAL-1", "4711", "12345", "TESTSIGBASE64==", "ecdsa-plain-SHA256", "2026-08-15T10:31:00Z", "2026-08-15T10:31:02Z"} {
		if !strings.Contains(meta, want) {
			t.Fatalf("ESC/POS Meta must carry TSE evidence value %q, got %+v", want, doc.Meta)
		}
	}
}

func TestPostPrintReceipt_ReprintFailsCleanlyWithNoPrinter(t *testing.T) {
	mux, dp := newPrintAPITestDeps(t)
	seedReceiptSale(t, dp, "sale1", "R001", "sale", "", 120, 0, 0)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/print/receipt/R001", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 with no printer configured, got %d: %s", rec.Code, rec.Body.String())
	}

	// An audit entry is recorded either way (ok=false here).
	var count int
	if err := dp.Db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM audit_log WHERE entity_id = 'R001' AND action = 'receipt_reprint'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one receipt_reprint audit entry, got %d", count)
	}
}

// Review of ut-docs#675 (smaller finding alongside B1-B4): a sale whose
// signing gap was RESOLVED by a pre-1.4.0 build's background retry
// (a historical fiscal_signing_resolved audit row for the same sale — no
// current code path writes that row, ADR-0056/ut-docs#839) must reprint
// clean — the outage notice derives from the unsigned_fiscal_signing marker
// ONLY while no resolution row exists. An old sale whose recovery was
// already recorded back then must not claim on reprint that it is unsigned.
func TestBuildReceiptDoc_ResolvedSigningGapPrintsClean(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	seedReceiptSale(t, dp, "sale1", "R001", "sale", "", 120, 0, 0)
	ctx := context.Background()
	repo := data.NewPOSRepo(dp.Db)
	now := "2026-08-15T10:00:00Z"
	if err := repo.InsertAudit(ctx, nil, "", "sale", "sale1", "unsigned_fiscal_signing", map[string]any{
		"reason": "signing backend declared unreachable by the plugin",
	}, now, ""); err != nil {
		t.Fatal(err)
	}

	// Unresolved: the notice line prints.
	doc, err := buildReceiptDoc(ctx, dp, "R001")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(doc.Meta, "\n"), "TSE unreachable") {
		t.Fatalf("unresolved signing gap must print the outage notice, got %+v", doc.Meta)
	}

	// Resolved by a pre-1.4.0 build's background retry (historical row):
	// reprints are clean.
	if err := repo.InsertAudit(ctx, nil, "", "sale", "sale1", "fiscal_signing_resolved", map[string]any{
		"resolved_at": "2026-08-15T10:02:00Z",
	}, "2026-08-15T10:02:00Z", ""); err != nil {
		t.Fatal(err)
	}
	doc, err = buildReceiptDoc(ctx, dp, "R001")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(doc.Meta, "\n"), "TSE unreachable") {
		t.Fatalf("a resolved sale must reprint clean (no outage notice), got %+v", doc.Meta)
	}
}

// ut-docs#835: a sale whose signer declared "cannot-sign" carries its OWN
// ESC/POS notice line, worded so it never implies a connectivity outage —
// and, same as the outage marker above, prints clean when a historical
// fiscal_signing_resolved row shows a pre-1.4.0 build's background retry
// signed it back then (no current code writes that row — ADR-0056,
// ut-docs#839). Regression test for a gap the independent review of this card
// caught: the ESC/POS path had no coverage at all — deleting its
// cannot-sign case passed the full suite silently, which on this path means
// a printed customer receipt that looks identical to a signed sale.
func TestBuildReceiptDoc_CannotSignPrintsDistinctNoticeAndResolvesClean(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	seedReceiptSale(t, dp, "sale1", "R001", "sale", "", 120, 0, 0)
	ctx := context.Background()
	repo := data.NewPOSRepo(dp.Db)
	now := "2026-08-15T10:00:00Z"
	if err := repo.InsertAudit(ctx, nil, "", "sale", "sale1", "unsigned_fiscal_cannot_sign", map[string]any{
		"reason": "signing plugin declared this sale cannot be signed as presented",
	}, now, ""); err != nil {
		t.Fatal(err)
	}

	// Unresolved: the cannot-sign notice prints — and the outage wording
	// must NOT also appear (the two are mutually exclusive).
	doc, err := buildReceiptDoc(ctx, dp, "R001")
	if err != nil {
		t.Fatal(err)
	}
	meta := strings.Join(doc.Meta, "\n")
	if !strings.Contains(meta, "could not be signed as presented") {
		t.Fatalf("unresolved cannot-sign gap must print its own notice, got %+v", doc.Meta)
	}
	if strings.Contains(meta, "TSE unreachable") {
		t.Fatalf("cannot-sign must not also print the outage wording, got %+v", doc.Meta)
	}

	// Resolved by a pre-1.4.0 build's background retry (historical row):
	// reprints are clean.
	if err := repo.InsertAudit(ctx, nil, "", "sale", "sale1", "fiscal_signing_resolved", map[string]any{
		"resolved_at": "2026-08-15T10:02:00Z",
	}, "2026-08-15T10:02:00Z", ""); err != nil {
		t.Fatal(err)
	}
	doc, err = buildReceiptDoc(ctx, dp, "R001")
	if err != nil {
		t.Fatal(err)
	}
	meta = strings.Join(doc.Meta, "\n")
	if strings.Contains(meta, "could not be signed as presented") || strings.Contains(meta, "TSE unreachable") {
		t.Fatalf("a resolved cannot-sign sale must reprint clean (no notice), got %+v", doc.Meta)
	}
}

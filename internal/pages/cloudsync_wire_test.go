package pages

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

func newCloudSyncTestDeps(t *testing.T) *common.Deps {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	cfg := &config.Config{
		Theme:   "default",
		Locales: config.Locales{Currency: "GBP", TaxRate: 20},
		Marketplace: config.MarketplaceConfig{
			EndpointURL: "http://marketplace.test",
			ClientID:    "merchant-1",
			StoreID:     "store-1",
			DeviceID:    "device-1",
		},
	}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	return &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		BaseMenu: []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(db),
	}
}

// --- cloudInstallPlugin ---

// TestCloudInstallPlugin_ConfigurationFailurePersistsOperatorVisibleStatus
// mirrors TestInstallFromMarketplaceFailurePersistsOperatorVisibleStatus
// (plugins_status_test.go) but drives cloudInstallPlugin directly, since a
// cloud directive install goes through this function rather than the HTTP
// handler. With no PublicKey configured, NewMarketplaceInstaller fails
// before any network call — the same failure a misconfigured till would hit
// for either an operator-driven or cloud-driven install.
func TestCloudInstallPlugin_ConfigurationFailurePersistsOperatorVisibleStatus(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	_, err := cloudInstallPlugin(ctx, dp, "listing-failure")
	if err == nil {
		t.Fatalf("expected an error when the marketplace signing key isn't configured")
	}

	record, ok, err := plugins.NewInstallStatusStore(dp.Db).Get(ctx, "listing-failure")
	if err != nil {
		t.Fatalf("load persisted status: %v", err)
	}
	if !ok {
		t.Fatalf("expected a persisted failed install status")
	}
	if record.State != plugins.InstallStateFailed {
		t.Fatalf("state = %q, want %q", record.State, plugins.InstallStateFailed)
	}
	if record.MessageKey != "plugins.install.error.configuration" {
		t.Fatalf("message key = %q, want configuration failure", record.MessageKey)
	}
}

// --- cloudAdjustStock ---

func TestCloudAdjustStock_UsesExistingTrackedLocation(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	msg, err := cloudAdjustStock(ctx, dp, "itm1", 5, "restock")
	if err != nil {
		t.Fatalf("cloudAdjustStock: %v", err)
	}
	if !strings.Contains(msg, "5") {
		t.Fatalf("expected message to mention the delta, got %q", msg)
	}

	levels, err := data.NewPOSRepo(dp.Db).ListStockLevels(ctx)
	if err != nil {
		t.Fatalf("list stock levels: %v", err)
	}
	found := false
	for _, l := range levels {
		// ut-docs#2082: ListStockLevels now also returns itm1's variant
		// (var1/Large) as its own additive row — this adjustment only ever
		// targets the item's own item-scoped row, so skip the variant row
		// rather than asserting the item's post-adjustment qty against it.
		if l.ItemID == "itm1" && l.VariantID == "" {
			found = true
			if l.LocationID != "loc_main" {
				t.Fatalf("expected movement recorded at loc_main, got %q", l.LocationID)
			}
			if l.CurrentQty != 55 {
				t.Fatalf("expected qty 50+5=55, got %v", l.CurrentQty)
			}
		}
	}
	if !found {
		t.Fatalf("expected itm1's item-level row in stock levels")
	}
}

func TestCloudAdjustStock_FallsBackToFirstStockLocationWhenUntracked(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	repo := data.NewCatalogRepo(dp.Db)
	id, err := repo.CreateItem(ctx, catalogtypes.ItemInput{Name: "Untracked Widget", BasePrice: 250, IsActive: true})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}

	msg, err := cloudAdjustStock(ctx, dp, id, 3, "initial stock")
	if err != nil {
		t.Fatalf("cloudAdjustStock: %v", err)
	}
	if !strings.Contains(msg, "3") {
		t.Fatalf("expected message to mention the delta, got %q", msg)
	}

	var locationID string
	if err := dp.Db.QueryRowContext(ctx, `SELECT location_id FROM stock_movements WHERE item_id = ?`, id).Scan(&locationID); err != nil {
		t.Fatalf("query stock movement: %v", err)
	}
	if locationID != "loc_main" {
		t.Fatalf("expected fallback to the shop's only stock location, got %q", locationID)
	}
}

func TestCloudAdjustStock_NoStockLocationConfiguredReturnsError(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	if _, err := dp.Db.ExecContext(ctx, `DELETE FROM inventory`); err != nil {
		t.Fatalf("clear inventory: %v", err)
	}
	if _, err := dp.Db.ExecContext(ctx, `DELETE FROM stock_locations`); err != nil {
		t.Fatalf("clear stock_locations: %v", err)
	}

	_, err := cloudAdjustStock(ctx, dp, "itm1", 1, "restock")
	if err == nil {
		t.Fatalf("expected an error when no stock location is configured")
	}
	if !strings.Contains(err.Error(), "no stock location configured") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCloudAdjustStock_DefaultsReasonWhenEmpty(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	if _, err := cloudAdjustStock(ctx, dp, "itm1", 2, ""); err != nil {
		t.Fatalf("cloudAdjustStock: %v", err)
	}

	// stock_movements has no reason column; pos.RecordStockMovement writes
	// the reason into the audit log instead, keyed by the movement id.
	var auditData string
	if err := dp.Db.QueryRowContext(ctx, `SELECT data_json FROM audit_log WHERE entity_type = 'inventory' ORDER BY created_at DESC LIMIT 1`).Scan(&auditData); err != nil {
		t.Fatalf("query audit log: %v", err)
	}
	if !strings.Contains(auditData, "cloud adjustment") {
		t.Fatalf("expected default reason %q in audit trail, got %q", "cloud adjustment", auditData)
	}
}

// --- cloudCreateItem ---

func TestCloudCreateItem_CreatesNewItemWithBarcode(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	msg, err := cloudCreateItem(ctx, dp, "Cloud Widget", 500, "9999999999")
	if err != nil {
		t.Fatalf("cloudCreateItem: %v", err)
	}
	if !strings.Contains(msg, "Cloud Widget") {
		t.Fatalf("unexpected message: %q", msg)
	}

	repo := data.NewCatalogRepo(dp.Db)
	_, exists, err := repo.FindActiveItemByName(ctx, "Cloud Widget")
	if err != nil || !exists {
		t.Fatalf("expected item to exist: exists=%v err=%v", exists, err)
	}
	taken, err := repo.BarcodeExists(ctx, "9999999999")
	if err != nil || !taken {
		t.Fatalf("expected barcode attached: taken=%v err=%v", taken, err)
	}
}

func TestCloudCreateItem_IdempotentOnExistingActiveItemName(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	if _, err := cloudCreateItem(ctx, dp, "Repeat Widget", 500, ""); err != nil {
		t.Fatalf("first create: %v", err)
	}
	msg, err := cloudCreateItem(ctx, dp, "Repeat Widget", 999, "")
	if err != nil {
		t.Fatalf("second create (retry): %v", err)
	}
	if msg != "item already exists" {
		t.Fatalf("expected idempotent retry message, got %q", msg)
	}

	var count int
	if err := dp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM items WHERE name = 'Repeat Widget'`).Scan(&count); err != nil {
		t.Fatalf("count items: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one item, got %d (directive retry must not duplicate)", count)
	}
}

func TestCloudCreateItem_BarcodeAlreadyTakenFails(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	// "ABC" is itm1's primary barcode, seeded by seedForPages.
	_, err := cloudCreateItem(ctx, dp, "Barcode Clash Widget", 500, "ABC")
	if err == nil {
		t.Fatalf("expected an error for an already-used barcode")
	}
	if !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("unexpected error: %v", err)
	}

	repo := data.NewCatalogRepo(dp.Db)
	if _, exists, _ := repo.FindActiveItemByName(ctx, "Barcode Clash Widget"); exists {
		t.Fatalf("item must not be created when its barcode is already taken")
	}
}

// TestCloudCreateItem_RefusedOnReplica: items is an admin-synced table
// (primary-wins pull, sync_admin_repo.go's adminTables) — same reasoning
// as the local admin item-create handler's own requirePrimary gate
// (catalog/handlers.go). A directive landing on a replica till must be
// refused the same way, or the created row silently vanishes on the next
// admin pull (ut-docs#2353).
func TestCloudCreateItem_RefusedOnReplica(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("seed sync.primary_url: %v", err)
	}

	_, err := cloudCreateItem(ctx, dp, "Replica Widget", 500, "")
	if err == nil {
		t.Fatalf("expected cloudCreateItem to refuse on a replica till")
	}

	repo := data.NewCatalogRepo(dp.Db)
	_, exists, err := repo.FindActiveItemByName(ctx, "Replica Widget")
	if err != nil {
		t.Fatalf("FindActiveItemByName: %v", err)
	}
	if exists {
		t.Fatalf("item must not be created on a replica till")
	}
}

// TestCloudCreateItem_WritesAuditRow: the local admin item-create path has
// no audit call of its own to mirror (verified: catalog/handlers.go's
// POST /api/catalog/item never calls InsertAudit), but a cloud-originated
// mutation still needs a "the merchant changed this from the cloud portal"
// trail distinct from an operator's own actions — same "system"-actor
// pattern as cloudAdjustStock/sync_admin.go's admin_pulled (ut-docs#2353).
func TestCloudCreateItem_WritesAuditRow(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	if _, err := cloudCreateItem(ctx, dp, "Audited Widget", 500, ""); err != nil {
		t.Fatalf("cloudCreateItem: %v", err)
	}

	repo := data.NewCatalogRepo(dp.Db)
	id, exists, err := repo.FindActiveItemByName(ctx, "Audited Widget")
	if err != nil || !exists {
		t.Fatalf("expected item to exist: exists=%v err=%v", exists, err)
	}

	var actorID, action string
	if err := dp.Db.QueryRowContext(ctx,
		`SELECT actor_id, action FROM audit_log WHERE entity_type = 'item' AND entity_id = ? AND action = 'cloud_item_created'`,
		id,
	).Scan(&actorID, &action); err != nil {
		t.Fatalf("expected an audit row for the cloud-originated create: %v", err)
	}
	if actorID != "system" {
		t.Fatalf("expected actor_id 'system', got %q", actorID)
	}
}

// TestCloudCreateItem_IdempotentRetryWritesNoSecondAuditRow: a retry of an
// already-created item (the at-least-once directive replay this hook's own
// idempotency handles) must not add a second audit row for a no-op.
func TestCloudCreateItem_IdempotentRetryWritesNoSecondAuditRow(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	if _, err := cloudCreateItem(ctx, dp, "Retry Widget", 500, ""); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := cloudCreateItem(ctx, dp, "Retry Widget", 500, ""); err != nil {
		t.Fatalf("retry: %v", err)
	}

	repo := data.NewCatalogRepo(dp.Db)
	id, exists, err := repo.FindActiveItemByName(ctx, "Retry Widget")
	if err != nil || !exists {
		t.Fatalf("expected item to exist: exists=%v err=%v", exists, err)
	}

	var count int
	if err := dp.Db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_log WHERE entity_type = 'item' AND entity_id = ? AND action = 'cloud_item_created'`,
		id,
	).Scan(&count); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one audit row across the create + retry, got %d", count)
	}
}

// --- cloudSetPrice ---

func TestCloudSetPrice_RefusedOnReplica(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("seed sync.primary_url: %v", err)
	}

	if _, err := cloudSetPrice(ctx, dp, "itm1", 999); err == nil {
		t.Fatalf("expected cloudSetPrice to refuse on a replica till")
	}

	var price int64
	if err := dp.Db.QueryRowContext(ctx, `SELECT base_price FROM items WHERE id = 'itm1'`).Scan(&price); err != nil {
		t.Fatalf("read base_price: %v", err)
	}
	if price == 999 {
		t.Fatalf("price must not change on a replica till")
	}
}

func TestCloudSetPrice_WritesAuditRow(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	if _, err := cloudSetPrice(ctx, dp, "itm1", 777); err != nil {
		t.Fatalf("cloudSetPrice: %v", err)
	}

	var actorID string
	if err := dp.Db.QueryRowContext(ctx,
		`SELECT actor_id FROM audit_log WHERE entity_type = 'item' AND entity_id = 'itm1' AND action = 'cloud_price_set'`,
	).Scan(&actorID); err != nil {
		t.Fatalf("expected an audit row: %v", err)
	}
	if actorID != "system" {
		t.Fatalf("expected actor_id 'system', got %q", actorID)
	}
}

// --- cloudRenameItem ---

func TestCloudRenameItem_RefusedOnReplica(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("seed sync.primary_url: %v", err)
	}

	if _, err := cloudRenameItem(ctx, dp, "itm1", "Replica Name"); err == nil {
		t.Fatalf("expected cloudRenameItem to refuse on a replica till")
	}

	var name string
	if err := dp.Db.QueryRowContext(ctx, `SELECT name FROM items WHERE id = 'itm1'`).Scan(&name); err != nil {
		t.Fatalf("read name: %v", err)
	}
	if name == "Replica Name" {
		t.Fatalf("name must not change on a replica till")
	}
}

func TestCloudRenameItem_WritesAuditRow(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	if _, err := cloudRenameItem(ctx, dp, "itm1", "Renamed Apple"); err != nil {
		t.Fatalf("cloudRenameItem: %v", err)
	}

	var actorID string
	if err := dp.Db.QueryRowContext(ctx,
		`SELECT actor_id FROM audit_log WHERE entity_type = 'item' AND entity_id = 'itm1' AND action = 'cloud_item_renamed'`,
	).Scan(&actorID); err != nil {
		t.Fatalf("expected an audit row: %v", err)
	}
	if actorID != "system" {
		t.Fatalf("expected actor_id 'system', got %q", actorID)
	}
}

// --- cloudAddBarcode ---

func TestCloudAddBarcode_RefusedOnReplica(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("seed sync.primary_url: %v", err)
	}

	if _, err := cloudAddBarcode(ctx, dp, "itm1", "9990001"); err == nil {
		t.Fatalf("expected cloudAddBarcode to refuse on a replica till")
	}

	repo := data.NewCatalogRepo(dp.Db)
	if taken, _ := repo.BarcodeExists(ctx, "9990001"); taken {
		t.Fatalf("barcode must not be attached on a replica till")
	}
}

func TestCloudAddBarcode_WritesAuditRowForItem(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	if _, err := cloudAddBarcode(ctx, dp, "itm1", "9990002"); err != nil {
		t.Fatalf("cloudAddBarcode: %v", err)
	}

	var entityType, actorID string
	if err := dp.Db.QueryRowContext(ctx,
		`SELECT entity_type, actor_id FROM audit_log WHERE entity_id = 'itm1' AND action = 'cloud_barcode_added'`,
	).Scan(&entityType, &actorID); err != nil {
		t.Fatalf("expected an audit row: %v", err)
	}
	if entityType != "item" || actorID != "system" {
		t.Fatalf("unexpected audit row: entity_type=%q actor_id=%q", entityType, actorID)
	}
}

func TestCloudAddBarcode_WritesAuditRowForVariant(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	if _, err := cloudAddBarcode(ctx, dp, "var1", "9990003"); err != nil {
		t.Fatalf("cloudAddBarcode: %v", err)
	}

	var entityType string
	if err := dp.Db.QueryRowContext(ctx,
		`SELECT entity_type FROM audit_log WHERE entity_id = 'var1' AND action = 'cloud_barcode_added'`,
	).Scan(&entityType); err != nil {
		t.Fatalf("expected an audit row: %v", err)
	}
	if entityType != "item_variant" {
		t.Fatalf("expected entity_type 'item_variant', got %q", entityType)
	}
}

// --- cloudDeactivateItem ---

func TestCloudDeactivateItem_RefusedOnReplica(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("seed sync.primary_url: %v", err)
	}

	if _, err := cloudDeactivateItem(ctx, dp, "itm1"); err == nil {
		t.Fatalf("expected cloudDeactivateItem to refuse on a replica till")
	}

	var active int
	if err := dp.Db.QueryRowContext(ctx, `SELECT is_active FROM items WHERE id = 'itm1'`).Scan(&active); err != nil {
		t.Fatalf("read is_active: %v", err)
	}
	if active == 0 {
		t.Fatalf("item must not be deactivated on a replica till")
	}
}

func TestCloudDeactivateItem_WritesAuditRowForItem(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	if _, err := cloudDeactivateItem(ctx, dp, "itm1"); err != nil {
		t.Fatalf("cloudDeactivateItem: %v", err)
	}

	var entityType, actorID string
	if err := dp.Db.QueryRowContext(ctx,
		`SELECT entity_type, actor_id FROM audit_log WHERE entity_id = 'itm1' AND action = 'cloud_item_deactivated'`,
	).Scan(&entityType, &actorID); err != nil {
		t.Fatalf("expected an audit row: %v", err)
	}
	if entityType != "item" || actorID != "system" {
		t.Fatalf("unexpected audit row: entity_type=%q actor_id=%q", entityType, actorID)
	}
}

func TestCloudDeactivateItem_WritesAuditRowForVariant(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	if _, err := cloudDeactivateItem(ctx, dp, "var1"); err != nil {
		t.Fatalf("cloudDeactivateItem: %v", err)
	}

	var entityType string
	if err := dp.Db.QueryRowContext(ctx,
		`SELECT entity_type FROM audit_log WHERE entity_id = 'var1' AND action = 'cloud_variant_deactivated'`,
	).Scan(&entityType); err != nil {
		t.Fatalf("expected an audit row: %v", err)
	}
	if entityType != "item_variant" {
		t.Fatalf("expected entity_type 'item_variant', got %q", entityType)
	}
}

// --- cloudRemovePlugin ---

func TestCloudRemovePlugin_RejectsPathTraversalID(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	for _, id := range []string{"../etc", "a/b", `a\b`, "../../secret"} {
		if _, err := cloudRemovePlugin(ctx, dp, id); err == nil {
			t.Fatalf("expected %q to be rejected as an invalid plugin id", id)
		}
	}
}

func TestCloudRemovePlugin_RemovesInstalledPluginAndClearsStatus(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	statusStore := plugins.NewInstallStatusStore(dp.Db)
	if err := statusStore.Save(ctx, plugins.InstallStatusRecord{
		ListingID: "listing-p1", PluginID: "p1", State: plugins.InstallStateActive,
	}); err != nil {
		t.Fatalf("seed install status: %v", err)
	}

	msg, err := cloudRemovePlugin(ctx, dp, "p1")
	if err != nil {
		t.Fatalf("cloudRemovePlugin: %v", err)
	}
	if !strings.Contains(msg, "p1") {
		t.Fatalf("unexpected message: %q", msg)
	}

	var count int
	if err := dp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM plugins WHERE id = 'p1'`).Scan(&count); err != nil {
		t.Fatalf("count plugins: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected plugin row removed, still present")
	}

	if _, ok, err := statusStore.Get(ctx, "listing-p1"); err != nil {
		t.Fatalf("get status: %v", err)
	} else if ok {
		t.Fatalf("expected install status cleared for the removed plugin")
	}
}

// --- collectProblems ---

func TestCollectProblems_IncludesRecentLogLines(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	logging.L().Warnf("cloudsync-test-marker: disk almost full")

	problems := collectProblems(ctx, dp)
	found := false
	for _, p := range problems {
		if msg, _ := p["msg"].(string); strings.Contains(msg, "cloudsync-test-marker: disk almost full") {
			found = true
			if p["level"] != "WARN" {
				t.Fatalf("expected level WARN, got %v", p["level"])
			}
		}
	}
	if !found {
		t.Fatalf("expected the logged warning to appear in collectProblems output")
	}
}

func TestCollectProblems_IncludesFailedPluginInstalls(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	// collectProblems fills its maxProblems=20 cap from logging.Recent()
	// first, then falls back to failed-install records only while slots
	// remain. logging.Recent() is a process-global ring shared by the whole
	// test binary (ut-docs#404), so under -shuffle=on, other tests' warn/
	// error lines logged earlier in the process can already occupy all 20
	// slots by the time this test runs — silently starving the DB-backed
	// failed-install entry this test asserts on (ut-docs#219).
	// logging.ResetRecent() (test-only) clears the ring right before the
	// scenario under test, same isolation pattern as stock_ownership_test.go.
	logging.ResetRecent()

	if err := plugins.NewInstallStatusStore(dp.Db).Save(ctx, plugins.InstallStatusRecord{
		ListingID:  "listing-broken",
		State:      plugins.InstallStateFailed,
		MessageKey: "plugins.install.error.checksum",
	}); err != nil {
		t.Fatalf("seed failed install: %v", err)
	}

	problems := collectProblems(ctx, dp)
	found := false
	for _, p := range problems {
		msg, _ := p["msg"].(string)
		if strings.Contains(msg, "listing-broken") && strings.Contains(msg, "plugins.install.error.checksum") {
			found = true
			if p["level"] != "ERROR" {
				t.Fatalf("expected level ERROR for a failed install, got %v", p["level"])
			}
		}
	}
	if !found {
		t.Fatalf("expected the failed install to appear in collectProblems output")
	}
}

// TestCollectProblems_TruncationIsUTF8Safe guards against a rune-splitting
// truncation bug: a long, non-ASCII message truncated by raw byte index can
// land mid-rune and emit invalid UTF-8 into the cloud's problems feed.
func TestCollectProblems_TruncationIsUTF8Safe(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	// 199 ASCII bytes then a 2-byte 'é' (0xC3 0xA9) at byte indices 199-200:
	// a naive msg[:200] byte-slice keeps the lead byte 0xC3 (index 199) but
	// drops its continuation byte (index 200), leaving a dangling lead byte.
	long := strings.Repeat("x", 199) + "é" + strings.Repeat("y", 50)
	logging.L().Errorf("%s", long)

	problems := collectProblems(ctx, dp)
	if len(problems) == 0 {
		t.Fatalf("expected at least one problem")
	}
	// Recent() is newest-first, so the message just logged is problems[0].
	msg, _ := problems[0]["msg"].(string)
	if !strings.HasPrefix(msg, "xxx") {
		t.Fatalf("expected the freshly logged message first, got %q", msg)
	}
	if !utf8.ValidString(msg) {
		t.Fatalf("truncated message is not valid UTF-8: %q", msg)
	}
}

// TestCloudAdjustStock_AuditActorSatisfiesRealForeignKey is a REAL-schema
// regression for the "cloud" actor id bug (ut-docs#1676): audit_log.actor_id
// has a genuine FOREIGN KEY to users(id) in internal/db/migrations/001_init.sql,
// and "cloud" was never a seeded user, so this call always violated it in a
// real deployment. This test opens a real migrated database directly
// (appdb.Open) rather than going through openPagesTestDB/seedForPages, so it
// stays a true regression test regardless of that package's own test-fixture
// schema (which historically carried no such FK at all, and is why this bug
// went uncaught for as long as it did).
func TestCloudAdjustStock_AuditActorSatisfiesRealForeignKey(t *testing.T) {
	migrated, err := appdb.Open(filepath.Join(t.TempDir(), "cloudadjust.db"))
	if err != nil {
		t.Fatalf("open+migrate: %v", err)
	}
	t.Cleanup(func() { migrated.Close() })
	db := migrated.DB

	if _, err := db.Exec(`INSERT INTO items (id, name, base_price, is_active) VALUES ('itm1', 'Widget', 500, 1)`); err != nil {
		t.Fatalf("seed item: %v", err)
	}

	dp := &common.Deps{Db: db}
	if _, err := cloudAdjustStock(t.Context(), dp, "itm1", 5, "restock"); err != nil {
		t.Fatalf("cloudAdjustStock against a real migrated schema: %v", err)
	}

	var actorID, dataJSON string
	if err := db.QueryRow(`SELECT actor_id, data_json FROM audit_log WHERE entity_type = 'inventory' ORDER BY created_at DESC LIMIT 1`).Scan(&actorID, &dataJSON); err != nil {
		t.Fatalf("query audit log: %v", err)
	}
	if actorID != "system" {
		t.Fatalf("audit actor_id = %q, want a real seeded user id (\"system\")", actorID)
	}
	if !strings.Contains(dataJSON, "cloud:") {
		t.Fatalf("expected the audit payload to keep the adjustment's cloud origin visible now the actor can't, got %q", dataJSON)
	}
}

// --- rejectRemoteFiscalPostureWrite ---

// ADR-0083 (ut-docs#1767), independent review finding: the cloud
// set_setting directive has no HTTP session to check a role against, so it
// must refuse the two signing-device posture keys outright rather than
// silently writing them (owner-only everywhere else) or, worse, silently
// NOT writing them — the flat wire name written verbatim used to be a dead
// key once the split gave every real reader a per-country row, turning a
// remote revocation ("set false") into a no-op that still reported
// success while the till kept selling on the stale "true" row.
func TestRejectRemoteFiscalPostureWrite(t *testing.T) {
	d := newCloudSyncTestDeps(t)
	if err := rejectRemoteFiscalPostureWrite(d, fiscal.KeySystemOfRecord); err != nil {
		t.Fatalf("rejectRemoteFiscalPostureWrite(%q): unrelated key must not be refused: %v", fiscal.KeySystemOfRecord, err)
	}
	for _, key := range []string{
		"fiscal.signing_device_configured",
		"fiscal.signing_device_failing_since",
		"fiscal.signing_device_configured.de",
		"fiscal.signing_device_configured.TR",
	} {
		if err := rejectRemoteFiscalPostureWrite(d, key); err == nil {
			t.Fatalf("rejectRemoteFiscalPostureWrite(%q): want an error, got nil — a remote directive must not be able to flip this compliance gate", key)
		}
	}
}

// --- set_till_setting (ut-docs#2289, Decision 1 of the ut-docs#2306
// portal-till configuration design; proposed ADR-0095, pending merge) ---

// The remote till-settings hook is DELIBERATELY a separate, stricter path
// from the generic SetSetting hook: only an explicit whitelist of safe,
// non-device-bound keys may be written from the cloud, enforced here on the
// till as well as on the portal, so a stale or compromised portal can't push
// a printer address, a TSE credential, a PIN or a network setting through.
func TestCloudSetTillSetting_WhitelistedKeysWrite(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	cases := []struct{ key, value, want string }{
		{keyPrinterReceiptPolicy, "ask", "ask"},
		{keyPrinterReceiptPolicy, " Never ", "never"}, // case-folded + trimmed like the local form
		{keyReceiptHeader1, " Corner Shop ", "Corner Shop"},
		{keyReceiptHeader2, "High Street 1", "High Street 1"},
		{keyReceiptHeader3, "", ""}, // blank clears a line, same as the local designer
		{keyReceiptFooter, "Thank you!", "Thank you!"},
		{common.KeyKioskIdleReset, "90", "90"},
	}
	covered := map[string]bool{}
	for _, c := range cases {
		msg, err := cloudSetTillSetting(ctx, dp, nil, c.key, c.value)
		if err != nil {
			t.Fatalf("%s=%q: %v", c.key, c.value, err)
		}
		if !strings.Contains(msg, c.key) {
			t.Fatalf("%s: result %q should name the key", c.key, msg)
		}
		got, _, err := dp.Settings.Get(ctx, c.key)
		if err != nil || got != c.want {
			t.Fatalf("%s: stored %q (err %v), want %q", c.key, got, err, c.want)
		}
		covered[c.key] = true
	}
	// Review of ut-docs#2289: the table must exercise EVERY whitelisted key,
	// so a key added to allowedRemoteTillSettingKeys can't ship with no
	// accepted-value coverage at all — the runtime twin of
	// cloudSetTillSetting's fail-closed `default` branch.
	for key := range allowedRemoteTillSettingKeys {
		if !covered[key] {
			t.Fatalf("whitelisted key %q has no accepted-value case in this table — add one (and a validation branch in cloudSetTillSetting)", key)
		}
	}
}

func TestCloudSetTillSetting_RejectsNonWhitelistedKeysWithoutWriting(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	for _, key := range []string{
		keyPrinterAddress,                  // LAN device identifier
		keyPrinterDevice,                   // local device path
		"fiscal.tse_puk",                   // TSE credential
		"fiscal.signing_device_configured", // compliance posture (owner-only)
		"auth.admin_pin",                   // PIN
		"sync.primary_url",                 // network topology
		common.KeyCountry,                  // has fiscal side-effects; set_setting's job
		"theme",                            // legitimately remote-settable, but only via set_setting
		"",                                 // blank
		" " + keyReceiptFooter,             // exact names only, no trimming games
	} {
		if _, err := cloudSetTillSetting(ctx, dp, nil, key, "x"); err == nil {
			t.Fatalf("key %q: want a refusal, got nil", key)
		}
		if v, ok, _ := dp.Settings.Get(ctx, key); ok && v == "x" {
			t.Fatalf("key %q: refused key must not be written, found %q", key, v)
		}
	}
}

// Values go through the SAME validation the local settings forms apply
// (ut-docs#2306's design: "the till still validates each payload exactly as
// it would the same action performed by hand") — a remote write can't store
// what the local form would refuse.
func TestCloudSetTillSetting_ValidatesValuesLikeTheLocalForms(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	for _, c := range []struct{ key, value string }{
		{keyPrinterReceiptPolicy, "sometimes"},
		{keyPrinterReceiptPolicy, ""},
		{common.KeyKioskIdleReset, "-1"},
		{common.KeyKioskIdleReset, "601"}, // /api/settings/kiosk-idle-reset's own 0..600 bound
		{common.KeyKioskIdleReset, "ninety"},
	} {
		if _, err := cloudSetTillSetting(ctx, dp, nil, c.key, c.value); err == nil {
			t.Fatalf("%s=%q: want a validation error, got nil", c.key, c.value)
		}
		if v, ok, _ := dp.Settings.Get(ctx, c.key); ok && v == c.value {
			t.Fatalf("%s: rejected value %q must not be written", c.key, c.value)
		}
	}
	// Boundary values the local form accepts.
	for _, v := range []string{"0", "600"} {
		if _, err := cloudSetTillSetting(ctx, dp, nil, common.KeyKioskIdleReset, v); err != nil {
			t.Fatalf("%s=%q: %v", common.KeyKioskIdleReset, v, err)
		}
	}
}

// The ADR-0089 Decision 3 Germany lock was removed core-wide
// (ut-docs#2286/universal-till#1188) — a DE shop now chooses freely among
// always/ask/never via the remote path exactly as the local printer form
// does, with no country-specific branch left in cloudSetTillSetting.
func TestCloudSetTillSetting_ReceiptPolicyFreeForGermany(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	if err := dp.Settings.Set(ctx, keyStoreCountry, "DE"); err != nil {
		t.Fatalf("set country: %v", err)
	}
	if _, err := cloudSetTillSetting(ctx, dp, nil, keyPrinterReceiptPolicy, "ask"); err != nil {
		t.Fatalf("DE shop: 'ask' must save now the lock is removed: %v", err)
	}
	if v, ok, _ := dp.Settings.Get(ctx, keyPrinterReceiptPolicy); !ok || v != "ask" {
		t.Fatalf("DE shop: 'ask' must actually be written, got %q (ok=%v)", v, ok)
	}
	if _, err := cloudSetTillSetting(ctx, dp, nil, keyPrinterReceiptPolicy, "always"); err != nil {
		t.Fatalf("DE shop: 'always' must still save: %v", err)
	}
}

// The kiosk idle-reset lives in the derived State (common.LoadState), so the
// hook re-derives after a write the same way the generic SetSetting hook
// does — otherwise the new window wouldn't apply until the next restart.
func TestCloudSetTillSetting_RederivesState(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	rederived := 0
	rederive := func(context.Context) { rederived++ }
	if _, err := cloudSetTillSetting(ctx, dp, rederive, common.KeyKioskIdleReset, "75"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if rederived != 1 {
		t.Fatalf("rederive called %d times, want 1", rederived)
	}
	// A refused write must NOT re-derive (nothing changed).
	if _, err := cloudSetTillSetting(ctx, dp, rederive, keyPrinterAddress, "x"); err == nil {
		t.Fatalf("want refusal")
	}
	if rederived != 1 {
		t.Fatalf("rederive after a refused write: %d, want still 1", rederived)
	}
}

// Read side (ut-docs#2306 Decision 2, proposed ADR-0095, pending merge): the
// heartbeat reports the current value of every whitelisted key — unset keys
// as "" so the cloud form shows the blank rather than a stale value — and
// nothing else (no key outside the whitelist rides along, whatever else is
// in the settings table).
func TestRemoteTillSettingsReport(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	for k, v := range map[string]string{
		keyPrinterReceiptPolicy:  "ask",
		keyReceiptHeader1:        "Corner Shop",
		keyReceiptFooter:         "Thank you!",
		common.KeyKioskIdleReset: "90",
		keyPrinterAddress:        "192.168.1.50:9100", // must NOT be reported
	} {
		if err := dp.Settings.Set(ctx, k, v); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}

	got := remoteTillSettingsReport(ctx, dp)
	if len(got) != len(allowedRemoteTillSettingKeys) {
		t.Fatalf("report has %d keys, want exactly the %d whitelisted: %+v", len(got), len(allowedRemoteTillSettingKeys), got)
	}
	for key := range allowedRemoteTillSettingKeys {
		if _, ok := got[key]; !ok {
			t.Fatalf("whitelisted key %q missing from report: %+v", key, got)
		}
	}
	if got[keyPrinterReceiptPolicy] != "ask" || got[keyReceiptHeader1] != "Corner Shop" ||
		got[keyReceiptFooter] != "Thank you!" || got[common.KeyKioskIdleReset] != "90" {
		t.Fatalf("reported values: %+v", got)
	}
	if got[keyReceiptHeader2] != "" || got[keyReceiptHeader3] != "" {
		t.Fatalf("unset keys should report blank: %+v", got)
	}
	if _, leaked := got[keyPrinterAddress]; leaked {
		t.Fatalf("printer.address must never be reported: %+v", got)
	}
}

// The hooks StartCloudSync wires carry both halves: the DeviceExtra report
// includes till_settings, and SetTillSetting is the whitelisted hook (not the
// generic one).
func TestBuildCloudHooks_WiresTillSettings(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	if err := dp.Settings.Set(ctx, keyReceiptFooter, "Bye!"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	hooks := buildCloudHooks(dp, nil)

	extra := hooks.DeviceExtra(ctx)
	ts, ok := extra["till_settings"].(map[string]string)
	if !ok {
		t.Fatalf("till_settings missing or wrong type in DeviceExtra: %#v", extra["till_settings"])
	}
	if ts[keyReceiptFooter] != "Bye!" {
		t.Fatalf("till_settings = %+v", ts)
	}
	for _, k := range []string{"theme", "themes", "problems"} {
		if _, present := extra[k]; !present {
			t.Fatalf("existing DeviceExtra field %q lost", k)
		}
	}

	if hooks.SetTillSetting == nil {
		t.Fatalf("SetTillSetting hook not wired")
	}
	if _, err := hooks.SetTillSetting(ctx, keyPrinterAddress, "x"); err == nil {
		t.Fatalf("wired SetTillSetting must enforce the whitelist")
	}
	if _, err := hooks.SetTillSetting(ctx, keyReceiptFooter, "Thanks"); err != nil {
		t.Fatalf("wired SetTillSetting: %v", err)
	}
	if v, _, _ := dp.Settings.Get(ctx, keyReceiptFooter); v != "Thanks" {
		t.Fatalf("stored footer = %q", v)
	}
}

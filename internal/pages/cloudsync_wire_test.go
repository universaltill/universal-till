package pages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

func newCloudSyncTestDeps(t *testing.T) *common.Deps {
	t.Helper()
	chdirRoot(t)
	// DeviceExtra creates the main till's directive key under paths.Data
	// (ut-docs#2810): keep it out of the repo root.
	origData := paths.DataDir()
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init(origData) })
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

// ut-docs#2891 M2 sweep: the "/, \\ or .." check let "." (and "") through,
// and RemoveAll(paths.Plugins()/".") wipes every installed plugin — reachable
// from a cloud directive or the LAN primary's plugin set.
func TestCloudRemovePlugin_DotIDKeepsOtherPlugins(t *testing.T) {
	isolatePluginsDir(t)
	dp := newCloudSyncTestDeps(t)
	keep := filepath.Join(paths.Plugins(), "com.test.keep", "1.0.0", "manifest.json")
	if err := os.MkdirAll(filepath.Dir(keep), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keep, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{".", "", "COM.TEST.KEEP"} {
		if _, err := cloudRemovePlugin(t.Context(), dp, id); err == nil {
			t.Errorf("expected %q to be rejected as an invalid plugin id", id)
		}
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("cloudRemovePlugin(%q) removed another plugin's files: %v", id, err)
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
// real deployment. This test opens a real migrated database (openPagesTestDB,
// ut-docs#2219's cloned-template version of the same internal/db.Open
// migration chain) rather than this package's simplified seedForPages
// fixture, so it stays a true regression test regardless of that fixture's
// own schema (which historically carried no such FK at all, and is why this
// bug went uncaught for as long as it did).
func TestCloudAdjustStock_AuditActorSatisfiesRealForeignKey(t *testing.T) {
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })

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
		{data.OrderTypePromptModeKey, data.OrderTypePromptModeBeforeItem, data.OrderTypePromptModeBeforeItem},
		{common.KeyBrowsingMode, common.BrowsingModeStripOverflow, common.BrowsingModeStripOverflow}, // ut-docs#2499: verbatim, one of the three modes
		{common.KeyBrowsingMode, common.BrowsingModeAllFilterChips, common.BrowsingModeAllFilterChips},
		{common.KeyBrowsingMode, common.BrowsingModeCategoryTabs, common.BrowsingModeCategoryTabs},
		{data.SaleDisplayNoSchemeKey, data.DisplayNoSchemeLifetimeNoReset, data.DisplayNoSchemeLifetimeNoReset},
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
		{data.OrderTypePromptModeKey, "sometime"},
		{data.OrderTypePromptModeKey, ""},
		{common.KeyBrowsingMode, "maybe"},
		{common.KeyBrowsingMode, ""},
		{common.KeyBrowsingMode, "CATEGORY_TABS"}, // refused, never case-folded or clamped — a remote result column should say why
		{data.SaleDisplayNoSchemeKey, "monthly"},
		{data.SaleDisplayNoSchemeKey, ""},
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

// order_type_prompt lives OUTSIDE RuntimeState (same ut-docs#2121 class of
// gap as display.mode, settings_page.go's generic /api/settings door
// already documents) — the generic rederive callback above never touches
// it, so cloudSetTillSetting's own case must make the identical
// httpx.InitOrderTypePromptMode call the dedicated
// /api/settings/order-type-prompt handler makes, or a remote directive
// leaves the sale screen showing the OLD placement until the till
// restarts. Same render-and-check-the-attribute shape as
// TestOrderTypePromptModeReachesThePage.
func TestCloudSetTillSetting_OrderTypePromptModeLiveRepublishes(t *testing.T) {
	chdirRoot(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")
	httpx.InitOrderTypePromptMode("top")
	t.Cleanup(func() { httpx.InitOrderTypePromptMode("top") })

	cfg := &config.Config{Theme: "default"}
	dp := &common.Deps{Cfg: cfg, Db: db, State: common.LoadState(t.Context(), settings.NewStore(db), cfg),
		Menu: []common.MenuItem{}, Settings: settings.NewStore(db)}
	mux := http.NewServeMux()
	registerHelp(mux, dp)
	get := func() string {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/help", nil))
		return rec.Body.String()
	}

	if _, err := cloudSetTillSetting(t.Context(), dp, nil, data.OrderTypePromptModeKey, data.OrderTypePromptModeAtPay); err != nil {
		t.Fatalf("set: %v", err)
	}
	if !strings.Contains(get(), `data-order-type-prompt-mode="at_pay"`) {
		t.Fatal("cloud directive did not live-republish the prompt mode")
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

// --- upsert_category (ut-docs#2323, ADR-0095 Decision 1) ---

// findCategoryByName returns the admin row for an exact-name category, or
// ok=false. Test-only lookup over the same repo read the categories admin
// page uses.
func findCategoryByName(t *testing.T, dp *common.Deps, name string) (data.CategoryAdminRow, bool) {
	t.Helper()
	rows, err := data.NewCatalogRepo(dp.Db).ListCategoriesForAdmin(t.Context())
	if err != nil {
		t.Fatalf("list categories: %v", err)
	}
	for _, r := range rows {
		if r.Name == name {
			return r, true
		}
	}
	return data.CategoryAdminRow{}, false
}

func countCategories(t *testing.T, dp *common.Deps) int {
	t.Helper()
	rows, err := data.NewCatalogRepo(dp.Db).ListCategoriesForAdmin(t.Context())
	if err != nil {
		t.Fatalf("list categories: %v", err)
	}
	return len(rows)
}

// Empty id creates through CreateCategoryWithColor (active, colour stored);
// a present id updates name AND colour through UpdateCategory — the same two
// repo calls categories_page.go's POST handlers make.
func TestCloudUpsertCategory_CreateThenUpdate(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	before := countCategories(t, dp)

	msg, err := cloudUpsertCategory(ctx, dp, "", "Drinks", "#0f172a")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if msg != "created category Drinks" {
		t.Fatalf("create msg = %q", msg)
	}
	row, ok := findCategoryByName(t, dp, "Drinks")
	if !ok {
		t.Fatalf("Drinks not created")
	}
	if row.Color != "#0f172a" || !row.IsActive || row.ParentID != "" {
		t.Fatalf("created row = %+v", row)
	}
	if got := countCategories(t, dp); got != before+1 {
		t.Fatalf("category count = %d, want %d", got, before+1)
	}

	msg, err = cloudUpsertCategory(ctx, dp, row.ID, "Hot drinks", "#4338ca")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if msg != "updated category Hot drinks" {
		t.Fatalf("update msg = %q", msg)
	}
	if _, still := findCategoryByName(t, dp, "Drinks"); still {
		t.Fatalf("old name still present after rename")
	}
	updated, ok := findCategoryByName(t, dp, "Hot drinks")
	if !ok || updated.ID != row.ID {
		t.Fatalf("renamed row = %+v (ok=%v), want same id %s", updated, ok, row.ID)
	}
	if updated.Color != "#4338ca" || updated.SortOrder != row.SortOrder || !updated.IsActive {
		t.Fatalf("updated row = %+v (sort order and active flag must be untouched)", updated)
	}

	// An empty colour on update clears it ("No colour"), exactly like the
	// picker's blank tile — not "leave as is".
	if _, err := cloudUpsertCategory(ctx, dp, row.ID, "Hot drinks", ""); err != nil {
		t.Fatalf("clear colour: %v", err)
	}
	cleared, _ := findCategoryByName(t, dp, "Hot drinks")
	if cleared.Color != "" {
		t.Fatalf("colour after clear = %q, want empty", cleared.Color)
	}
	if got := countCategories(t, dp); got != before+1 {
		t.Fatalf("updates must not add rows: count = %d, want %d", got, before+1)
	}
}

// Colour is checked against the SAME fixed palette the local dialog checks
// (catalogtypes.ValidItemColor — a real allowlist, the value lands in a CSS
// custom property). A refused colour writes nothing, on create or update.
func TestCloudUpsertCategory_RefusesOffPaletteColourWithoutWriting(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	if _, err := cloudUpsertCategory(ctx, dp, "", "Bakery", "#0f766e"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	seeded, _ := findCategoryByName(t, dp, "Bakery")
	before := countCategories(t, dp)

	for _, bad := range []string{"#ff0000", "red", "#0f172a;--x:1", "0f172a"} {
		if _, err := cloudUpsertCategory(ctx, dp, "", "Evil", bad); err == nil {
			t.Fatalf("create with colour %q: want refusal", bad)
		}
		if _, err := cloudUpsertCategory(ctx, dp, seeded.ID, "Bakery renamed", bad); err == nil {
			t.Fatalf("update with colour %q: want refusal", bad)
		}
	}
	if _, created := findCategoryByName(t, dp, "Evil"); created {
		t.Fatalf("refused create must not write a row")
	}
	if got := countCategories(t, dp); got != before {
		t.Fatalf("category count changed on refusal: %d -> %d", before, got)
	}
	after, _ := findCategoryByName(t, dp, "Bakery")
	if after.ID != seeded.ID || after.Color != "#0f766e" {
		t.Fatalf("refused update must not touch the row: %+v", after)
	}
}

// Every palette colour, and blank, is accepted on create.
func TestCloudUpsertCategory_AcceptsEveryPaletteColour(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	for i, c := range catalogtypes.ItemColors() {
		name := "Palette " + c.Key
		if _, err := cloudUpsertCategory(ctx, dp, "", name, c.Hex); err != nil {
			t.Fatalf("colour %d %q: %v", i, c.Hex, err)
		}
		row, ok := findCategoryByName(t, dp, name)
		if !ok || row.Color != c.Hex {
			t.Fatalf("colour %q stored as %+v", c.Hex, row)
		}
	}
	if _, err := cloudUpsertCategory(ctx, dp, "", "No colour", ""); err != nil {
		t.Fatalf("blank colour: %v", err)
	}
}

// Directives are at-least-once: a retried CREATE (same name, still no id)
// must not produce a duplicate category — same "already exists counts as
// success" rule cloudCreateItem applies. Name match is case-insensitive,
// like the import path's EnsureCategory. A retried UPDATE is naturally
// idempotent (same row, same values).
func TestCloudUpsertCategory_CreateRetryDoesNotDuplicate(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	if _, err := cloudUpsertCategory(ctx, dp, "", "Drinks", "#0f172a"); err != nil {
		t.Fatalf("first: %v", err)
	}
	before := countCategories(t, dp)
	first, _ := findCategoryByName(t, dp, "Drinks")

	for _, name := range []string{"Drinks", "drinks", " DRINKS "} {
		msg, err := cloudUpsertCategory(ctx, dp, "", name, "#4338ca")
		if err != nil {
			t.Fatalf("retry %q: %v", name, err)
		}
		if !strings.Contains(msg, "already exists") {
			t.Fatalf("retry %q msg = %q, want an 'already exists' note", name, msg)
		}
	}
	if got := countCategories(t, dp); got != before {
		t.Fatalf("retry duplicated: %d -> %d categories", before, got)
	}
	// The retry is a no-op, not a silent recolour of the existing row.
	same, _ := findCategoryByName(t, dp, "Drinks")
	if same.ID != first.ID || same.Color != "#0f172a" {
		t.Fatalf("existing row changed by retried create: %+v", same)
	}

	// Update retry: same call twice, one row, same values.
	for i := 0; i < 2; i++ {
		if _, err := cloudUpsertCategory(ctx, dp, first.ID, "Drinks", "#0f766e"); err != nil {
			t.Fatalf("update retry %d: %v", i, err)
		}
	}
	if got := countCategories(t, dp); got != before {
		t.Fatalf("update retry duplicated: %d -> %d", before, got)
	}
}

// Unknown id and blank name surface the repo's own errors as the directive
// result (visible in the cloud's result column), writing nothing.
func TestCloudUpsertCategory_UnknownIDAndBlankName(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	before := countCategories(t, dp)

	if _, err := cloudUpsertCategory(ctx, dp, "no-such-category", "Ghost", "#0f172a"); !errors.Is(err, data.ErrCategoryNotFound) {
		t.Fatalf("unknown id: err = %v, want ErrCategoryNotFound", err)
	}
	if _, err := cloudUpsertCategory(ctx, dp, "", "   ", "#0f172a"); !errors.Is(err, data.ErrCategoryNameRequired) {
		t.Fatalf("blank name on create: err = %v, want ErrCategoryNameRequired", err)
	}
	if got := countCategories(t, dp); got != before {
		t.Fatalf("failed calls wrote rows: %d -> %d", before, got)
	}
	if _, ghost := findCategoryByName(t, dp, "Ghost"); ghost {
		t.Fatalf("unknown-id update must not fall back to creating")
	}
}

func TestCloudUpsertCategory_CreateRefusedOnReplica(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("seed sync.primary_url: %v", err)
	}

	if _, err := cloudUpsertCategory(ctx, dp, "", "Replica Category", "#0f172a"); err == nil {
		t.Fatalf("expected cloudUpsertCategory to refuse creating on a replica till")
	}
	if _, exists := findCategoryByName(t, dp, "Replica Category"); exists {
		t.Fatalf("category must not be created on a replica till")
	}
}

func TestCloudUpsertCategory_UpdateRefusedOnReplica(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	// Create while still primary, then simulate the till becoming a replica.
	if _, err := cloudUpsertCategory(ctx, dp, "", "Drinks", "#0f172a"); err != nil {
		t.Fatalf("create: %v", err)
	}
	row, exists := findCategoryByName(t, dp, "Drinks")
	if !exists {
		t.Fatalf("expected category to exist")
	}
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("seed sync.primary_url: %v", err)
	}

	if _, err := cloudUpsertCategory(ctx, dp, row.ID, "Renamed on replica", "#4338ca"); err == nil {
		t.Fatalf("expected cloudUpsertCategory to refuse updating on a replica till")
	}
	if got, _ := findCategoryByName(t, dp, "Renamed on replica"); got.ID != "" {
		t.Fatalf("category must not be renamed on a replica till")
	}
}

func TestCloudUpsertCategory_CreateWritesAuditRow(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	if _, err := cloudUpsertCategory(ctx, dp, "", "Audited Category", "#0f172a"); err != nil {
		t.Fatalf("cloudUpsertCategory: %v", err)
	}
	row, exists := findCategoryByName(t, dp, "Audited Category")
	if !exists {
		t.Fatalf("expected category to exist")
	}

	var actorID string
	if err := dp.Db.QueryRowContext(ctx,
		`SELECT actor_id FROM audit_log WHERE entity_type = 'category' AND entity_id = ? AND action = 'cloud_category_created'`,
		row.ID,
	).Scan(&actorID); err != nil {
		t.Fatalf("expected an audit row: %v", err)
	}
	if actorID != "system" {
		t.Fatalf("expected actor_id 'system', got %q", actorID)
	}
}

func TestCloudUpsertCategory_UpdateWritesAuditRow(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	if _, err := cloudUpsertCategory(ctx, dp, "", "Drinks", "#0f172a"); err != nil {
		t.Fatalf("create: %v", err)
	}
	row, exists := findCategoryByName(t, dp, "Drinks")
	if !exists {
		t.Fatalf("expected category to exist")
	}

	if _, err := cloudUpsertCategory(ctx, dp, row.ID, "Hot drinks", "#4338ca"); err != nil {
		t.Fatalf("update: %v", err)
	}

	var actorID string
	if err := dp.Db.QueryRowContext(ctx,
		`SELECT actor_id FROM audit_log WHERE entity_type = 'category' AND entity_id = ? AND action = 'cloud_category_updated'`,
		row.ID,
	).Scan(&actorID); err != nil {
		t.Fatalf("expected an audit row: %v", err)
	}
	if actorID != "system" {
		t.Fatalf("expected actor_id 'system', got %q", actorID)
	}
}

// The hook set StartCloudSync wires carries UpsertCategory, and it is the
// palette-checked hook (not a bare repo call).
func TestBuildCloudHooks_WiresUpsertCategory(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	hooks := buildCloudHooks(dp, nil)
	if hooks.UpsertCategory == nil {
		t.Fatalf("UpsertCategory hook not wired")
	}
	if _, err := hooks.UpsertCategory(ctx, "", "Wired", "#123456"); err == nil {
		t.Fatalf("wired UpsertCategory must enforce the colour palette")
	}
	msg, err := hooks.UpsertCategory(ctx, "", "Wired", "#be185d")
	if err != nil {
		t.Fatalf("wired UpsertCategory create: %v", err)
	}
	if msg != "created category Wired" {
		t.Fatalf("msg = %q", msg)
	}
	row, ok := findCategoryByName(t, dp, "Wired")
	if !ok || row.Color != "#be185d" {
		t.Fatalf("wired create stored %+v (ok=%v)", row, ok)
	}
	if _, err := hooks.UpsertCategory(ctx, row.ID, "Wired 2", ""); err != nil {
		t.Fatalf("wired UpsertCategory update: %v", err)
	}
	if _, ok := findCategoryByName(t, dp, "Wired 2"); !ok {
		t.Fatalf("wired update did not rename")
	}
}

// --- set_quick_button_layout ---

// seedQuickButtons inserts three shortcut_buttons rows, all pointing at
// seedForPages' itm1 (their only FK requirement), in barcode order b1,b2,b3
// (sort_order 0,1,2) — the starting layout each test below reorders away
// from.
func seedQuickButtons(t *testing.T, dp *common.Deps) {
	t.Helper()
	for _, s := range []string{
		`INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('b1','Alpha','itm1',0)`,
		`INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('b2','Beta','itm1',1)`,
		`INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('b3','Gamma','itm1',2)`,
	} {
		if _, err := dp.Db.Exec(s); err != nil {
			t.Fatalf("seed shortcut_buttons: %v", err)
		}
	}
}

// quickButtonOrder returns the button barcodes in persisted sort order.
func quickButtonOrder(t *testing.T, dp *common.Deps) []string {
	t.Helper()
	rows, err := dp.Db.Query(`SELECT barcode FROM shortcut_buttons ORDER BY sort_order`)
	if err != nil {
		t.Fatalf("query shortcut_buttons: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, b)
	}
	return out
}

// On a primary (or standalone) till, cloudSetQuickButtonLayout applies the
// new order — the same UpdateOrder call the Designer's own reorder makes —
// and records one audit_log row so the change is traceable back to a cloud
// directive rather than a local operator action.
func TestCloudSetQuickButtonLayout_AppliesOrderAndAudits(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	seedQuickButtons(t, dp)

	msg, err := cloudSetQuickButtonLayout(ctx, dp, []string{"b3", "b1", "b2"})
	if err != nil {
		t.Fatalf("cloudSetQuickButtonLayout: %v", err)
	}
	if !strings.Contains(msg, "3") {
		t.Fatalf("expected message to mention the button count, got %q", msg)
	}
	got := quickButtonOrder(t, dp)
	want := []string{"b3", "b1", "b2"}
	if len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}

	var actorID, entityType, action string
	row := dp.Db.QueryRow(`SELECT actor_id, entity_type, action FROM audit_log ORDER BY created_at DESC, rowid DESC LIMIT 1`)
	if err := row.Scan(&actorID, &entityType, &action); err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	if actorID != "system" {
		t.Fatalf("audit actor_id = %q, want system", actorID)
	}
	if action == "" || entityType == "" {
		t.Fatalf("audit row incomplete: entity_type=%q action=%q", entityType, action)
	}
}

// A replica till follows shortcut_buttons from the primary via the
// admin-table pull (sync_admin_repo.go) — a write here would just be
// reverted on the next pull with no indication to the cloud operator that
// nothing actually stuck (same class as ut-docs#1697's LAN-route gate).
// The directive is refused before any DB write, matching requirePrimary's
// own refusal shape for the LAN reorder route.
func TestCloudSetQuickButtonLayout_RefusedOnReplica(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	seedQuickButtons(t, dp)
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("seed primary url: %v", err)
	}

	if _, err := cloudSetQuickButtonLayout(ctx, dp, []string{"b3", "b1", "b2"}); err == nil {
		t.Fatalf("expected refusal on a replica till")
	}
	got := quickButtonOrder(t, dp)
	want := []string{"b1", "b2", "b3"} // unchanged
	if len(got) != len(want) {
		t.Fatalf("replica write leaked through: order = %v, want unchanged %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("replica write leaked through: order = %v, want unchanged %v", got, want)
		}
	}
}

// An empty barcode list is refused directly by the hook too (not just by
// cloudsync.apply's own dispatch-level check) -- cloudSetQuickButtonLayout
// is called directly by buildCloudHooks' wiring and by tests, so it must not
// rely solely on the caller having already checked.
func TestCloudSetQuickButtonLayout_EmptyListRefused(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	seedQuickButtons(t, dp)

	if _, err := cloudSetQuickButtonLayout(ctx, dp, nil); err == nil {
		t.Fatalf("expected refusal for an empty barcode list")
	}
	got := quickButtonOrder(t, dp)
	want := []string{"b1", "b2", "b3"}
	if len(got) != len(want) {
		t.Fatalf("order changed on refusal: %v, want unchanged %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order changed on refusal: %v, want unchanged %v", got, want)
		}
	}
}

// A partial list (missing an existing barcode) must be refused, not silently
// applied — buttons_api.go's LAN reorder route documents its own payload as
// "the FULL global list," and UpdateOrder only touches the barcodes it's
// given: applying a partial list leaves the omitted row(s) on a stale
// sort_order that can collide with a listed row's new one (independent
// review's own probe reproduced a real duplicate sort_order this way,
// ut-docs#2321 review).
func TestCloudSetQuickButtonLayout_MissingBarcodeRefused(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	seedQuickButtons(t, dp)

	if _, err := cloudSetQuickButtonLayout(ctx, dp, []string{"b3", "b1"}); err == nil {
		t.Fatalf("expected refusal for a partial list missing b2")
	}
	got := quickButtonOrder(t, dp)
	want := []string{"b1", "b2", "b3"}
	if len(got) != len(want) {
		t.Fatalf("order changed on refusal: %v, want unchanged %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order changed on refusal: %v, want unchanged %v", got, want)
		}
	}
}

// A barcode the till doesn't recognize is refused outright — the till is the
// only thing that can validate a cloud directive's payload before applying
// it, so an unknown barcode must not be a silent, unexplained no-op.
func TestCloudSetQuickButtonLayout_UnknownBarcodeRefused(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	seedQuickButtons(t, dp)

	if _, err := cloudSetQuickButtonLayout(ctx, dp, []string{"b3", "b1", "does-not-exist"}); err == nil {
		t.Fatalf("expected refusal for an unrecognized barcode")
	}
	got := quickButtonOrder(t, dp)
	want := []string{"b1", "b2", "b3"}
	if len(got) != len(want) {
		t.Fatalf("order changed on refusal: %v, want unchanged %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order changed on refusal: %v, want unchanged %v", got, want)
		}
	}
}

// A duplicate barcode in the payload is refused — "the new order" is
// ambiguous once a barcode appears twice.
func TestCloudSetQuickButtonLayout_DuplicateBarcodeRefused(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	seedQuickButtons(t, dp)

	if _, err := cloudSetQuickButtonLayout(ctx, dp, []string{"b1", "b1", "b2"}); err == nil {
		t.Fatalf("expected refusal for a duplicate barcode")
	}
	got := quickButtonOrder(t, dp)
	want := []string{"b1", "b2", "b3"}
	if len(got) != len(want) {
		t.Fatalf("order changed on refusal: %v, want unchanged %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order changed on refusal: %v, want unchanged %v", got, want)
		}
	}
}

// The hook set StartCloudSync wires carries SetQuickButtonLayout, and the
// DeviceExtra report includes the applied layout (barcode + label, in sort
// order) so the cloud's layout panel can pre-fill from real state.
func TestBuildCloudHooks_WiresQuickButtonLayout(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	seedQuickButtons(t, dp)
	hooks := buildCloudHooks(dp, nil)

	if hooks.SetQuickButtonLayout == nil {
		t.Fatalf("SetQuickButtonLayout hook not wired")
	}
	if _, err := hooks.SetQuickButtonLayout(ctx, []string{"b2", "b3", "b1"}); err != nil {
		t.Fatalf("wired SetQuickButtonLayout: %v", err)
	}
	got := quickButtonOrder(t, dp)
	want := []string{"b2", "b3", "b1"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("wired order = %v, want %v", got, want)
		}
	}

	extra := hooks.DeviceExtra(ctx)
	qb, ok := extra["quick_buttons"].([]map[string]any)
	if !ok {
		t.Fatalf("quick_buttons missing or wrong type in DeviceExtra: %#v", extra["quick_buttons"])
	}
	if len(qb) != 3 {
		t.Fatalf("quick_buttons length = %d, want 3", len(qb))
	}
	wantOrder := []string{"b2", "b3", "b1"}
	for i, code := range wantOrder {
		if qb[i]["barcode"] != code {
			t.Fatalf("quick_buttons[%d] = %+v, want barcode %q", i, qb[i], code)
		}
		// sort_order must mirror this entry's actual position — the cloud
		// side decodes it into QuickButtonReport.SortOrder independently of
		// the report's own array order (ut-docs#2321 review).
		if so, ok := qb[i]["sort_order"].(int); !ok || so != i {
			t.Fatalf("quick_buttons[%d][\"sort_order\"] = %#v, want %d", i, qb[i]["sort_order"], i)
		}
	}
	for _, k := range []string{"theme", "themes", "problems", "till_settings"} {
		if _, present := extra[k]; !present {
			t.Fatalf("existing DeviceExtra field %q lost", k)
		}
	}
}

// Each reported quick button carries the item it points at and that item's
// tile colour (ut-docs#2368): a tile's colour IS its item's items.color, so
// the cloud panel needs item_id to queue an update_item_details recolour and
// color to show the applied swatch after the till reports back.
func TestRemoteQuickButtonsReport_CarriesItemIDAndColor(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	seedQuickButtons(t, dp)
	teal := catalogtypes.ItemColors()[2].Hex
	if _, err := dp.Db.Exec(`UPDATE items SET color = ? WHERE id = 'itm1'`, teal); err != nil {
		t.Fatalf("seed item colour: %v", err)
	}

	qb := remoteQuickButtonsReport(ctx, dp)
	if len(qb) != 3 {
		t.Fatalf("quick_buttons length = %d, want 3", len(qb))
	}
	for i, b := range qb {
		if b["item_id"] != "itm1" {
			t.Fatalf("quick_buttons[%d][\"item_id\"] = %#v, want %q", i, b["item_id"], "itm1")
		}
		if b["color"] != teal {
			t.Fatalf("quick_buttons[%d][\"color\"] = %#v, want %q", i, b["color"], teal)
		}
	}

	// An uncoloured item reports "" (not absent), so the panel can tell "no
	// colour" from "an older till that doesn't report colour at all".
	if _, err := dp.Db.Exec(`UPDATE items SET color = NULL WHERE id = 'itm1'`); err != nil {
		t.Fatalf("clear item colour: %v", err)
	}
	qb = remoteQuickButtonsReport(ctx, dp)
	if c, present := qb[0]["color"]; !present || c != "" {
		t.Fatalf("uncoloured item: quick_buttons[0][\"color\"] = %#v (present=%v), want \"\"", c, present)
	}
}

// --- cloudUpdateItemDetails ---

// seedFullDetailItem creates one category and one brand row (satisfying the
// FK columns, PRAGMA foreign_keys is ON for every till DB) and a catalog
// item with every partial-update-eligible field AND every out-of-scope
// field (category/brand/tax code) set to a real, non-default value, so a
// test can prove a single-field update leaves everything else — including
// name/price/category/brand/tax-code — exactly as it was.
func seedFullDetailItem(t *testing.T, dp *common.Deps) catalogtypes.ItemInput {
	t.Helper()
	ctx := t.Context()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO categories(id, name, color) VALUES ('cat-full', 'Full Cat', '#4338ca')`); err != nil {
		t.Fatalf("seed category: %v", err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO brands(id, name) VALUES ('brand-full', 'Full Brand')`); err != nil {
		t.Fatalf("seed brand: %v", err)
	}
	catID, brandID, taxID := "cat-full", "brand-full", "tax_std"
	in := catalogtypes.ItemInput{
		SKU:            "ORIG-SKU",
		Name:           "Original Name",
		BasePrice:      1234,
		Unit:           "kg",
		CategoryID:     &catID,
		BrandID:        &brandID,
		TaxCodeID:      &taxID,
		IsWeighed:      true,
		Description:    "Original description",
		IsActive:       true,
		StockUntracked: true,
		Color:          "#0f766e",
	}
	id, err := data.NewCatalogRepo(dp.Db).CreateItem(ctx, in)
	if err != nil {
		t.Fatalf("seed item: %v", err)
	}
	in.ID = id
	return in
}

func assertItemUnchangedExcept(t *testing.T, dp *common.Deps, before catalogtypes.ItemInput, changed map[string]bool) {
	t.Helper()
	after, ok, err := data.NewCatalogRepo(dp.Db).GetItem(t.Context(), before.ID)
	if err != nil || !ok {
		t.Fatalf("re-read item: ok=%v err=%v", ok, err)
	}
	check := func(field string, want bool, eq bool) {
		if changed[field] {
			return
		}
		if !eq {
			t.Fatalf("field %q changed but was not expected to: before=%+v after=%+v", field, before, after)
		}
	}
	check("sku", false, after.SKU == before.SKU)
	check("name", false, after.Name == before.Name)
	check("base_price", false, after.BasePrice == before.BasePrice)
	check("unit", false, after.Unit == before.Unit)
	check("description", false, after.Description == before.Description)
	check("color", false, after.Color == before.Color)
	check("is_weighed", false, after.IsWeighed == before.IsWeighed)
	check("stock_untracked", false, after.StockUntracked == before.StockUntracked)
	check("is_active", false, after.IsActive == before.IsActive)
	beforeCat, afterCat := "", ""
	if before.CategoryID != nil {
		beforeCat = *before.CategoryID
	}
	if after.CategoryID != nil {
		afterCat = *after.CategoryID
	}
	check("category_id", false, afterCat == beforeCat)
	beforeBrand, afterBrand := "", ""
	if before.BrandID != nil {
		beforeBrand = *before.BrandID
	}
	if after.BrandID != nil {
		afterBrand = *after.BrandID
	}
	check("brand_id", false, afterBrand == beforeBrand)
	beforeTax, afterTax := "", ""
	if before.TaxCodeID != nil {
		beforeTax = *before.TaxCodeID
	}
	if after.TaxCodeID != nil {
		afterTax = *after.TaxCodeID
	}
	check("tax_code_id", false, afterTax == beforeTax)
}

func strp(s string) *string { return &s }
func boolp(b bool) *bool    { return &b }

// TestCloudUpdateItemDetails_SkuOnlyLeavesEverythingElseUnchanged is the
// crux test for this directive's whole reason to exist: a payload that
// names only `sku` must be a TRUE partial update, not a full-item
// overwrite. The seeded item has non-default values in every other
// updatable field, plus the three fields this card explicitly does NOT
// touch (category/brand/tax code, ADR-0095 Decision 2 / ut-docs#2354) — all
// of those must survive the read-modify-write untouched too.
func TestCloudUpdateItemDetails_SkuOnlyLeavesEverythingElseUnchanged(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	before := seedFullDetailItem(t, dp)

	msg, err := cloudUpdateItemDetails(ctx, dp, before.ID, strp("NEW-SKU"), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("cloudUpdateItemDetails: %v", err)
	}
	if msg != "details updated: sku" {
		t.Fatalf("msg = %q, want %q", msg, "details updated: sku")
	}

	after, ok, err := data.NewCatalogRepo(dp.Db).GetItem(ctx, before.ID)
	if err != nil || !ok {
		t.Fatalf("re-read item: ok=%v err=%v", ok, err)
	}
	if after.SKU != "NEW-SKU" {
		t.Fatalf("sku = %q, want NEW-SKU", after.SKU)
	}
	assertItemUnchangedExcept(t, dp, before, map[string]bool{"sku": true})
}

// A multi-field payload updates exactly those fields and nothing else.
func TestCloudUpdateItemDetails_MultipleFieldsUpdateOnlyThose(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	before := seedFullDetailItem(t, dp)

	msg, err := cloudUpdateItemDetails(ctx, dp, before.ID, nil, strp("New description"), nil, strp("#be185d"), boolp(false), nil)
	if err != nil {
		t.Fatalf("cloudUpdateItemDetails: %v", err)
	}
	if msg != "details updated: description, color, is_weighed" {
		t.Fatalf("msg = %q", msg)
	}

	after, ok, err := data.NewCatalogRepo(dp.Db).GetItem(ctx, before.ID)
	if err != nil || !ok {
		t.Fatalf("re-read item: ok=%v err=%v", ok, err)
	}
	if after.Description != "New description" || after.Color != "#be185d" || after.IsWeighed {
		t.Fatalf("after = %+v", after)
	}
	assertItemUnchangedExcept(t, dp, before, map[string]bool{"description": true, "color": true, "is_weighed": true})
}

// An off-palette colour is refused, and — matching cloudUpsertCategory's
// validate-before-write order — writes nothing at all, not even the other
// fields that rode along in the same payload.
func TestCloudUpdateItemDetails_InvalidColorRejectedWritesNothing(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	before := seedFullDetailItem(t, dp)

	_, err := cloudUpdateItemDetails(ctx, dp, before.ID, strp("SHOULD-NOT-STICK"), nil, nil, strp("#ff0000"), nil, nil)
	if err == nil {
		t.Fatalf("expected an error for an off-palette colour")
	}

	after, ok, err := data.NewCatalogRepo(dp.Db).GetItem(ctx, before.ID)
	if err != nil || !ok {
		t.Fatalf("re-read item: ok=%v err=%v", ok, err)
	}
	if after.SKU != before.SKU || after.Color != before.Color {
		t.Fatalf("refused call must write nothing: before=%+v after=%+v", before, after)
	}
}

func TestCloudUpdateItemDetails_UnknownItemIDFailsCleanly(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	if _, err := cloudUpdateItemDetails(ctx, dp, "no-such-item", strp("X"), nil, nil, nil, nil, nil); err == nil {
		t.Fatalf("expected an error for an unknown item_id")
	}
}

func TestCloudUpdateItemDetails_NoFieldsIsANoOp(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	before := seedFullDetailItem(t, dp)

	msg, err := cloudUpdateItemDetails(ctx, dp, before.ID, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("cloudUpdateItemDetails with no fields: %v", err)
	}
	if msg != "no changes" {
		t.Fatalf("msg = %q, want %q", msg, "no changes")
	}
	assertItemUnchangedExcept(t, dp, before, nil)
}

// The genuine concurrent-write regression test for ut-docs#2324 review
// finding S1 (UpdateItemPartial's own BEGIN IMMEDIATE transaction must
// serialize against a concurrent single-column writer) lives at
// internal/data.TestUpdateItemPartialConcurrentRace — a sequential
// same-goroutine test at this layer cannot actually interleave a write
// between cloudUpdateItemDetails's read and write, so it would pass
// identically whether or not the transaction existed (verified directly:
// reverting UpdateItemPartial to a non-transactional GetItem+UpdateItem
// still passed a sequential version of this test). The data-package test
// uses real goroutines against a file-backed database to force the race.

// TestCloudUpdateItemDetails_BlankSkuAndUnitAreNoOps is the regression test
// for ut-docs#2324 review finding S3: updateItemExec's own SQL makes a
// blank sku a true no-op but silently DEFAULTS a blank unit to "each" —
// neither is a meaningful "clear this field" request, so a directive
// carrying only blank sku/unit values must report "no changes" and write
// nothing, never falsely claim (or audit) a change that didn't happen.
func TestCloudUpdateItemDetails_BlankSkuAndUnitAreNoOps(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	before := seedFullDetailItem(t, dp)

	msg, err := cloudUpdateItemDetails(ctx, dp, before.ID, strp("  "), nil, strp(""), nil, nil, nil)
	if err != nil {
		t.Fatalf("cloudUpdateItemDetails: %v", err)
	}
	if msg != "no changes" {
		t.Fatalf("msg = %q, want %q (blank sku/unit must not count as a change)", msg, "no changes")
	}
	assertItemUnchangedExcept(t, dp, before, nil)

	var count int
	if err := dp.Db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_log WHERE entity_type = 'item' AND entity_id = ? AND action = 'cloud_item_details_updated'`,
		before.ID,
	).Scan(&count); err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	if count != 0 {
		t.Fatalf("blank sku/unit must not write an audit row, found %d", count)
	}
}

// items is an admin-synced table (primary-wins pull) — same
// requirePrimaryDirective gate every other catalog-mutating directive has
// (ut-docs#2353).
func TestCloudUpdateItemDetails_RefusedOnReplica(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	before := seedFullDetailItem(t, dp)
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("seed sync.primary_url: %v", err)
	}

	if _, err := cloudUpdateItemDetails(ctx, dp, before.ID, strp("SHOULD-NOT-STICK"), nil, nil, nil, nil, nil); err == nil {
		t.Fatalf("expected cloudUpdateItemDetails to refuse on a replica till")
	}

	after, ok, err := data.NewCatalogRepo(dp.Db).GetItem(ctx, before.ID)
	if err != nil || !ok {
		t.Fatalf("re-read item: ok=%v err=%v", ok, err)
	}
	if after.SKU != before.SKU {
		t.Fatalf("sku must not change on a replica till, got %q", after.SKU)
	}
}

func TestCloudUpdateItemDetails_WritesAuditRowWithOnlyProvidedFields(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	before := seedFullDetailItem(t, dp)

	if _, err := cloudUpdateItemDetails(ctx, dp, before.ID, strp("NEW-SKU"), nil, nil, strp("#be185d"), nil, nil); err != nil {
		t.Fatalf("cloudUpdateItemDetails: %v", err)
	}

	var actorID, payloadJSON string
	if err := dp.Db.QueryRowContext(ctx,
		`SELECT actor_id, data_json FROM audit_log WHERE entity_type = 'item' AND entity_id = ? AND action = 'cloud_item_details_updated'`,
		before.ID,
	).Scan(&actorID, &payloadJSON); err != nil {
		t.Fatalf("expected an audit row: %v", err)
	}
	if actorID != "system" {
		t.Fatalf("expected actor_id 'system', got %q", actorID)
	}
	if !strings.Contains(payloadJSON, `"sku"`) || !strings.Contains(payloadJSON, `"color"`) {
		t.Fatalf("payload missing provided fields: %s", payloadJSON)
	}
	for _, absent := range []string{`"description"`, `"unit"`, `"is_weighed"`, `"stock_untracked"`} {
		if strings.Contains(payloadJSON, absent) {
			t.Fatalf("payload must omit absent fields, found %s: %s", absent, payloadJSON)
		}
	}
}

func TestBuildCloudHooks_WiresUpdateItemDetails(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	before := seedFullDetailItem(t, dp)
	hooks := buildCloudHooks(dp, nil)
	if hooks.UpdateItemDetails == nil {
		t.Fatalf("UpdateItemDetails hook not wired")
	}
	if _, err := hooks.UpdateItemDetails(ctx, before.ID, nil, nil, nil, strp("#ff0000"), nil, nil); err == nil {
		t.Fatalf("wired UpdateItemDetails must enforce the colour palette")
	}
	msg, err := hooks.UpdateItemDetails(ctx, before.ID, strp("WIRED-SKU"), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("wired UpdateItemDetails: %v", err)
	}
	if msg != "details updated: sku" {
		t.Fatalf("msg = %q", msg)
	}
	after, ok, err := data.NewCatalogRepo(dp.Db).GetItem(ctx, before.ID)
	if err != nil || !ok || after.SKU != "WIRED-SKU" {
		t.Fatalf("wired update did not stick: ok=%v err=%v after=%+v", ok, err, after)
	}
}

// --- cloudUpsertModifierGroup ---

// TestCloudUpsertModifierGroup_CreatesGroupWithOptions: CREATE-ONLY (no id
// in the payload at all — ut-docs#2322, ADR-0095 Decision 1, the same scope
// cut cloudUpsertCategory shipped with), so this is the only shape: attach
// a NEW group, with its options, to an existing item — the same
// NextGroupSortOrderForItem -> CreateGroup -> CreateOption sequence a local
// admin creator would use.
func TestCloudUpsertModifierGroup_CreatesGroupWithOptions(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	msg, err := cloudUpsertModifierGroup(ctx, dp, "itm1", "Extras", true, 1, 2, []cloudsync.ModifierGroupOption{
		{Name: "Cheese", PriceDeltaMinor: 150},
		{Name: "Bacon", PriceDeltaMinor: 200},
	})
	if err != nil {
		t.Fatalf("cloudUpsertModifierGroup: %v", err)
	}
	if !strings.Contains(msg, "Extras") {
		t.Fatalf("unexpected message: %q", msg)
	}

	groups, err := data.NewModifierRepo(dp.Db).ListAllGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatalf("ListAllGroupsForItem: %v", err)
	}
	var group *data.ModifierGroup
	for i := range groups {
		if groups[i].Name == "Extras" {
			group = &groups[i]
		}
	}
	if group == nil {
		t.Fatalf("expected an Extras group to exist, got %+v", groups)
	}
	if !group.Required || group.MinSelect != 1 || group.MaxSelect != 2 {
		t.Fatalf("created group = %+v", group)
	}
	if len(group.Options) != 2 {
		t.Fatalf("expected 2 options, got %+v", group.Options)
	}
	byName := map[string]int64{}
	for _, o := range group.Options {
		byName[o.Name] = o.PriceDeltaMinor
	}
	if byName["Cheese"] != 150 || byName["Bacon"] != 200 {
		t.Fatalf("option prices = %+v", byName)
	}
}

// A group may be created with zero options, exactly like the till's own
// data.ModifierRepo.CreateGroup — this must not be treated as an error.
func TestCloudUpsertModifierGroup_ZeroOptionsIsValid(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	if _, err := cloudUpsertModifierGroup(ctx, dp, "itm1", "Bare Group", false, 0, 1, nil); err != nil {
		t.Fatalf("cloudUpsertModifierGroup: %v", err)
	}
	groups, err := data.NewModifierRepo(dp.Db).ListAllGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatalf("ListAllGroupsForItem: %v", err)
	}
	for _, g := range groups {
		if g.Name == "Bare Group" {
			if len(g.Options) != 0 {
				t.Fatalf("expected no options, got %+v", g.Options)
			}
			return
		}
	}
	t.Fatalf("expected a Bare Group group to exist, got %+v", groups)
}

// ADR-0101 (ut-docs#2399): a blank item_id creates a SHOP-WIDE group with
// no assignment at all — listed on /modifiers, linked to nothing, and a
// retry of the same standalone create is a no-op (shop-wide name dedupe).
func TestCloudUpsertModifierGroup_NoItemCreatesStandaloneGroup(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	msg, err := cloudUpsertModifierGroup(ctx, dp, "", "Sauces", false, 0, 2, []cloudsync.ModifierGroupOption{{Name: "Ketchup", PriceDeltaMinor: 0}})
	if err != nil {
		t.Fatalf("cloudUpsertModifierGroup without item_id: %v", err)
	}
	if !strings.Contains(msg, "Sauces") {
		t.Fatalf("unexpected message: %q", msg)
	}
	groups, err := data.NewModifierRepo(dp.Db).ListAllModifierGroupsWithAssignments(ctx)
	if err != nil {
		t.Fatalf("ListAllModifierGroupsWithAssignments: %v", err)
	}
	var found *data.ModifierGroupAdmin
	for i := range groups {
		if groups[i].Name == "Sauces" {
			found = &groups[i]
		}
	}
	if found == nil {
		t.Fatalf("standalone group not created, groups = %+v", groups)
	}
	if len(found.Items) != 0 || len(found.Categories) != 0 {
		t.Fatalf("a standalone create must link nothing, got items=%+v categories=%+v", found.Items, found.Categories)
	}
	if len(found.Options) != 1 || found.Options[0].Name != "Ketchup" {
		t.Fatalf("options = %+v", found.Options)
	}

	// Retry: no duplicate, reported as already existing.
	msg, err = cloudUpsertModifierGroup(ctx, dp, "", "sauces", false, 0, 2, nil)
	if err != nil || !strings.Contains(msg, "already exists") {
		t.Fatalf("retry: msg=%q err=%v", msg, err)
	}
	var n int
	if err := dp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_modifier_groups WHERE name = 'Sauces'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("standalone group count = %d err=%v, want 1", n, err)
	}
}

func TestCloudUpsertModifierGroup_UnknownItemFails(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	if _, err := cloudUpsertModifierGroup(ctx, dp, "no-such-item", "Extras", false, 0, 1, nil); err == nil {
		t.Fatalf("expected an error for an unknown item_id")
	}
	groups, err := data.NewModifierRepo(dp.Db).ListAllModifierGroupsWithAssignments(ctx)
	if err != nil {
		t.Fatalf("ListAllModifierGroupsWithAssignments: %v", err)
	}
	for _, g := range groups {
		if g.Name == "Extras" {
			t.Fatalf("no group must be created for an unknown item")
		}
	}
}

// The till validates min_select/max_select independently of the cloud
// (ut-docs#2322; "validate all external input" — same posture
// cloudUpsertCategory's own colour check takes).
func TestCloudUpsertModifierGroup_InvalidMinMaxFails(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	for _, tc := range []struct {
		name      string
		minSelect int
		maxSelect int
	}{
		{"negative min", -1, 1},
		{"negative max", 0, -1},
		{"min greater than max", 3, 1},
		{"max above cap", 0, maxModifierSelect + 1},
		{"min above cap", maxModifierSelect + 1, maxModifierSelect + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := cloudUpsertModifierGroup(ctx, dp, "itm1", "Bad Range", false, tc.minSelect, tc.maxSelect, nil); err == nil {
				t.Fatalf("expected an error for min=%d max=%d", tc.minSelect, tc.maxSelect)
			}
		})
	}
	groups, err := data.NewModifierRepo(dp.Db).ListAllModifierGroupsWithAssignments(ctx)
	if err != nil {
		t.Fatalf("ListAllModifierGroupsWithAssignments: %v", err)
	}
	for _, g := range groups {
		if g.Name == "Bad Range" {
			t.Fatalf("no group must be created for a refused min/max")
		}
	}
}

// The cap is inclusive — exactly maxModifierSelect must still be accepted,
// not just values below it (ut-docs#2376).
func TestCloudUpsertModifierGroup_AcceptsSelectAtCap(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	if _, err := cloudUpsertModifierGroup(ctx, dp, "itm1", "At Cap", false, 0, maxModifierSelect, nil); err != nil {
		t.Fatalf("expected max_select=%d to be accepted, got: %v", maxModifierSelect, err)
	}

	groups, err := data.NewModifierRepo(dp.Db).ListAllModifierGroupsWithAssignments(ctx)
	if err != nil {
		t.Fatalf("ListAllModifierGroupsWithAssignments: %v", err)
	}
	var found bool
	for _, g := range groups {
		if g.Name == "At Cap" {
			found = true
			if g.MaxSelect != maxModifierSelect {
				t.Fatalf("want max_select=%d, got %d", maxModifierSelect, g.MaxSelect)
			}
		}
	}
	if !found {
		t.Fatal("group 'At Cap' was not created")
	}
}

// A negative option price is refused independently of the cloud's own
// check, matching data.ModifierRepo.CreateOption's own "additive-only"
// rule — and nothing partial is left behind.
func TestCloudUpsertModifierGroup_NegativeOptionPriceFails(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	if _, err := cloudUpsertModifierGroup(ctx, dp, "itm1", "Bad Option", false, 0, 1, []cloudsync.ModifierGroupOption{
		{Name: "Discount", PriceDeltaMinor: -50},
	}); err == nil {
		t.Fatalf("expected an error for a negative price_delta_minor")
	}
	groups, err := data.NewModifierRepo(dp.Db).ListAllModifierGroupsWithAssignments(ctx)
	if err != nil {
		t.Fatalf("ListAllModifierGroupsWithAssignments: %v", err)
	}
	for _, g := range groups {
		if g.Name == "Bad Option" {
			t.Fatalf("no group must be created when an option is refused")
		}
	}
}

// TestCloudUpsertModifierGroup_RefusedOnReplica: item_modifier_groups is an
// admin-synced table (sync_admin_repo.go's adminTables), same reasoning as
// every other catalog-mutating directive (ut-docs#2353) — a directive
// landing on a replica till must be refused, or the created row silently
// vanishes on the next admin pull.
func TestCloudUpsertModifierGroup_RefusedOnReplica(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("seed sync.primary_url: %v", err)
	}

	if _, err := cloudUpsertModifierGroup(ctx, dp, "itm1", "Replica Group", false, 0, 1, nil); err == nil {
		t.Fatalf("expected cloudUpsertModifierGroup to refuse on a replica till")
	}
	groups, err := data.NewModifierRepo(dp.Db).ListAllModifierGroupsWithAssignments(ctx)
	if err != nil {
		t.Fatalf("ListAllModifierGroupsWithAssignments: %v", err)
	}
	for _, g := range groups {
		if g.Name == "Replica Group" {
			t.Fatalf("group must not be created on a replica till")
		}
	}
}

func TestCloudUpsertModifierGroup_WritesAuditRow(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	if _, err := cloudUpsertModifierGroup(ctx, dp, "itm1", "Audited Group", false, 0, 1, nil); err != nil {
		t.Fatalf("cloudUpsertModifierGroup: %v", err)
	}
	groups, err := data.NewModifierRepo(dp.Db).ListAllGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatalf("ListAllGroupsForItem: %v", err)
	}
	var groupID string
	for _, g := range groups {
		if g.Name == "Audited Group" {
			groupID = g.ID
		}
	}
	if groupID == "" {
		t.Fatalf("expected Audited Group to exist")
	}

	var actorID string
	if err := dp.Db.QueryRowContext(ctx,
		`SELECT actor_id FROM audit_log WHERE entity_type = 'modifier_group' AND entity_id = ? AND action = 'cloud_modifier_group_created'`,
		groupID,
	).Scan(&actorID); err != nil {
		t.Fatalf("expected an audit row: %v", err)
	}
	if actorID != "system" {
		t.Fatalf("expected actor_id 'system', got %q", actorID)
	}
}

// Directives are at-least-once: a retried create (same item, same name)
// must not produce a duplicate group — same "already exists counts as
// success" rule cloudCreateItem/cloudUpsertCategory apply. Name match is
// case-insensitive, matching cloudUpsertCategory's own dedupe.
func TestCloudUpsertModifierGroup_CreateRetryDoesNotDuplicate(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	if _, err := cloudUpsertModifierGroup(ctx, dp, "itm1", "Extras", false, 0, 1, []cloudsync.ModifierGroupOption{{Name: "Cheese", PriceDeltaMinor: 100}}); err != nil {
		t.Fatalf("first: %v", err)
	}
	for _, name := range []string{"Extras", "extras", " EXTRAS "} {
		msg, err := cloudUpsertModifierGroup(ctx, dp, "itm1", name, true, 5, 6, []cloudsync.ModifierGroupOption{{Name: "Bacon", PriceDeltaMinor: 999}})
		if err != nil {
			t.Fatalf("retry %q: %v", name, err)
		}
		if !strings.Contains(msg, "already exists") {
			t.Fatalf("retry %q msg = %q, want an 'already exists' note", name, msg)
		}
	}

	groups, err := data.NewModifierRepo(dp.Db).ListAllGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatalf("ListAllGroupsForItem: %v", err)
	}
	n := 0
	var found *data.ModifierGroup
	for i := range groups {
		if strings.EqualFold(groups[i].Name, "Extras") {
			n++
			found = &groups[i]
		}
	}
	if n != 1 {
		t.Fatalf("retry duplicated: %d groups named Extras, want 1", n)
	}
	// The retry is a no-op, not a silent edit of the existing row's rules.
	if found.Required || found.MinSelect != 0 || found.MaxSelect != 1 {
		t.Fatalf("existing group changed by retried create: %+v", found)
	}
	if len(found.Options) != 1 || found.Options[0].Name != "Cheese" {
		t.Fatalf("existing group's options changed by retried create: %+v", found.Options)
	}
}

// The hook set StartCloudSync wires carries UpsertModifierGroup, and it is
// the validating hook (not a bare repo call).
func TestBuildCloudHooks_WiresUpsertModifierGroup(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	hooks := buildCloudHooks(dp, nil)
	if hooks.UpsertModifierGroup == nil {
		t.Fatalf("UpsertModifierGroup hook not wired")
	}
	if _, err := hooks.UpsertModifierGroup(ctx, "itm1", "Wired Group", false, 0, 1, nil); err != nil {
		t.Fatalf("wired UpsertModifierGroup: %v", err)
	}
	groups, err := data.NewModifierRepo(dp.Db).ListAllGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatalf("ListAllGroupsForItem: %v", err)
	}
	for _, g := range groups {
		if g.Name == "Wired Group" {
			return
		}
	}
	t.Fatalf("wired UpsertModifierGroup did not create the group, groups = %+v", groups)
}

// 2026-09-17 review (ut-docs#2322): the dedupe scan reads
// ListAllGroupsForItem, which deliberately INCLUDES deactivated groups. It
// must still only treat an ACTIVE same-named group as "already applied" —
// exactly like cloudUpsertCategory's own `c.IsActive &&` filter. Otherwise
// a group the merchant retired at the till would block every future
// cloud-side create of that name forever, while the portal was told the
// directive applied.
func TestCloudUpsertModifierGroup_DeactivatedGroupDoesNotBlockCreate(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	modRepo := data.NewModifierRepo(dp.Db)

	if _, err := cloudUpsertModifierGroup(ctx, dp, "itm1", "Extras", false, 0, 1, nil); err != nil {
		t.Fatalf("first create: %v", err)
	}
	groups, err := modRepo.ListAllGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatalf("ListAllGroupsForItem: %v", err)
	}
	var oldID string
	for _, g := range groups {
		if g.Name == "Extras" {
			oldID = g.ID
		}
	}
	if oldID == "" {
		t.Fatalf("expected the first Extras group to exist")
	}
	// Retire it at the till, the way the local admin editor does.
	if err := modRepo.UpdateGroup(ctx, oldID, "Extras", false, 0, 1, 0, false); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	msg, err := cloudUpsertModifierGroup(ctx, dp, "itm1", "Extras", false, 0, 1,
		[]cloudsync.ModifierGroupOption{{Name: "Cheese", PriceDeltaMinor: 150}})
	if err != nil {
		t.Fatalf("create after deactivation: %v", err)
	}
	if strings.Contains(msg, "already exists") {
		t.Fatalf("a deactivated group must not count as already applied, msg = %q", msg)
	}
	groups, err = modRepo.ListAllGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatalf("ListAllGroupsForItem: %v", err)
	}
	active := 0
	for _, g := range groups {
		if g.Name == "Extras" && g.IsActive {
			active++
			if len(g.Options) != 1 || g.Options[0].Name != "Cheese" {
				t.Fatalf("new group's options = %+v", g.Options)
			}
		}
	}
	if active != 1 {
		t.Fatalf("expected exactly one ACTIVE Extras group, got %d (all: %+v)", active, groups)
	}
}

// 2026-09-17 review (ut-docs#2322): CreateGroup is transactional in itself,
// but the per-option CreateOption calls after it are separate statements. A
// real DB error partway through must NOT leave a half-created group behind
// — the name dedupe would then report the next at-least-once retry as
// "already exists", cementing the missing options forever. The trigger here
// stands in for that DB error (SQLITE_BUSY, disk I/O) deterministically.
func TestCloudUpsertModifierGroup_OptionFailureRollsBackTheGroup(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	if _, err := dp.Db.ExecContext(ctx, `
CREATE TRIGGER trg_test_boom BEFORE INSERT ON item_modifier_options
WHEN NEW.name = 'BOOM'
BEGIN
    SELECT RAISE(ABORT, 'boom');
END;`); err != nil {
		t.Fatalf("install failure trigger: %v", err)
	}

	_, err := cloudUpsertModifierGroup(ctx, dp, "itm1", "Extras", false, 0, 2, []cloudsync.ModifierGroupOption{
		{Name: "Cheese", PriceDeltaMinor: 150},
		{Name: "BOOM", PriceDeltaMinor: 200},
	})
	if err == nil {
		t.Fatalf("expected the failing option insert to surface as an error")
	}

	var groups, options int
	if err := dp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_modifier_groups WHERE name = 'Extras'`).Scan(&groups); err != nil {
		t.Fatalf("count groups: %v", err)
	}
	if groups != 0 {
		t.Fatalf("half-created group left behind: %d rows", groups)
	}
	if err := dp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_modifier_options WHERE name = 'Cheese'`).Scan(&options); err != nil {
		t.Fatalf("count options: %v", err)
	}
	if options != 0 {
		t.Fatalf("orphaned option rows left behind: %d", options)
	}
	var links int
	if err := dp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_modifier_group_links WHERE item_id = 'itm1'`).Scan(&links); err != nil {
		t.Fatalf("count links: %v", err)
	}
	if links != 0 {
		t.Fatalf("orphaned link rows left behind: %d", links)
	}

	// And the retry (once the fault clears) creates the group cleanly
	// rather than being waved through by the name dedupe.
	if _, err := dp.Db.ExecContext(ctx, `DROP TRIGGER trg_test_boom`); err != nil {
		t.Fatalf("drop failure trigger: %v", err)
	}
	if _, err := cloudUpsertModifierGroup(ctx, dp, "itm1", "Extras", false, 0, 2, []cloudsync.ModifierGroupOption{
		{Name: "Cheese", PriceDeltaMinor: 150},
		{Name: "BOOM", PriceDeltaMinor: 200},
	}); err != nil {
		t.Fatalf("retry after the fault cleared: %v", err)
	}
	all, err := data.NewModifierRepo(dp.Db).ListAllGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatalf("ListAllGroupsForItem: %v", err)
	}
	for _, g := range all {
		if g.Name == "Extras" {
			if len(g.Options) != 2 {
				t.Fatalf("retry left an incomplete group: %+v", g.Options)
			}
			return
		}
	}
	t.Fatalf("retry did not create the group, groups = %+v", all)
}

// 2026-09-17 review (ut-docs#2322): a cloud-created group must land inside
// the same envelope the LOCAL admin creator enforces
// (catalog/handlers.go's POST /api/catalog/modifier-group): max_select at
// least 1, and a "required" group asking for at least one pick. The
// sale-time validator (pos_modifiers_api.go) checks only MinSelect/
// MaxSelect and never Required, so required+min_select=0 would show the
// picker's "*" while still letting the pick be skipped server-side.
func TestCloudUpsertModifierGroup_NormalisesRequiredAndMaxSelect(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	if _, err := cloudUpsertModifierGroup(ctx, dp, "itm1", "Size", true, 0, 1, nil); err != nil {
		t.Fatalf("required group: %v", err)
	}
	if _, err := cloudUpsertModifierGroup(ctx, dp, "itm1", "Sauce", false, 0, 0, nil); err != nil {
		t.Fatalf("zero max_select group: %v", err)
	}
	groups, err := data.NewModifierRepo(dp.Db).ListAllGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatalf("ListAllGroupsForItem: %v", err)
	}
	byName := map[string]data.ModifierGroup{}
	for _, g := range groups {
		byName[g.Name] = g
	}
	if g := byName["Size"]; g.MinSelect != 1 || g.MaxSelect != 1 || !g.Required {
		t.Fatalf("a required group must ask for at least one pick, got %+v", g)
	}
	if g := byName["Sauce"]; g.MinSelect != 0 || g.MaxSelect != 1 {
		t.Fatalf("max_select must be clamped to at least 1, got %+v", g)
	}
}

// The till re-validates option names itself rather than trusting the cloud's
// own check (the cloudsync decoder only TRIMS a name, it does not reject a
// blank one) — and nothing is written when it refuses.
func TestCloudUpsertModifierGroup_BlankOptionNameFails(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()

	if _, err := cloudUpsertModifierGroup(ctx, dp, "itm1", "Blank Option", false, 0, 1, []cloudsync.ModifierGroupOption{
		{Name: "Cheese", PriceDeltaMinor: 100},
		{Name: "   ", PriceDeltaMinor: 0},
	}); err == nil {
		t.Fatalf("expected an error for a blank option name")
	}
	groups, err := data.NewModifierRepo(dp.Db).ListAllModifierGroupsWithAssignments(ctx)
	if err != nil {
		t.Fatalf("ListAllModifierGroupsWithAssignments: %v", err)
	}
	for _, g := range groups {
		if g.Name == "Blank Option" {
			t.Fatalf("no group must be created when an option is refused")
		}
	}
}

// --- config report: categories / modifier_groups / kitchen_stations
// (ut-docs#2472, ADR-0095 Decision 2 read side) ---

// remoteConfigReportJSON runs the wired DeviceExtra and round-trips it
// through encoding/json — the same encoding the heartbeat itself sends —
// so the tests below assert on the wire shape (snake_case keys, `[]` not
// `null`, ints not strings), not on Go types the cloud never sees.
func remoteConfigReportJSON(t *testing.T, dp *common.Deps) (string, map[string]json.RawMessage) {
	t.Helper()
	extra := buildCloudHooks(dp, nil).DeviceExtra(t.Context())
	raw, err := json.Marshal(extra)
	if err != nil {
		t.Fatalf("marshal DeviceExtra: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal DeviceExtra: %v", err)
	}
	return string(raw), decoded
}

// decodeReport decodes one DeviceExtra key into out, failing when the key
// is absent — a missing key would otherwise decode into a zero value and
// pass the wrong assertion.
func decodeReport(t *testing.T, decoded map[string]json.RawMessage, key string, out any) {
	t.Helper()
	raw, ok := decoded[key]
	if !ok {
		t.Fatalf("DeviceExtra key %q missing", key)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode %s (%s): %v", key, raw, err)
	}
}

// assertExactKeys fails unless the JSON object has exactly the given keys
// — the wire contract names every key, and an extra one (a leaked
// printer_address, say) is as wrong as a missing one.
func assertExactKeys(t *testing.T, label string, raw json.RawMessage, want ...string) {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("%s: not a JSON object (%s): %v", label, raw, err)
	}
	if len(obj) != len(want) {
		t.Fatalf("%s: got %d keys, want %d (%v): %s", label, len(obj), len(want), want, raw)
	}
	for _, k := range want {
		if _, ok := obj[k]; !ok {
			t.Fatalf("%s: key %q missing: %s", label, k, raw)
		}
	}
}

type configReportCategory struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	ParentID         string   `json:"parent_id"`
	Color            string   `json:"color"`
	SortOrder        int      `json:"sort_order"`
	Active           bool     `json:"active"`
	ModifierGroupIDs []string `json:"modifier_group_ids"`
	StationIDs       []string `json:"station_ids"`
}

type configReportOption struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	PriceDeltaMinor int64  `json:"price_delta_minor"`
	SortOrder       int    `json:"sort_order"`
	Active          bool   `json:"active"`
}

type configReportGroup struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Required    bool                 `json:"required"`
	MinSelect   int                  `json:"min_select"`
	MaxSelect   int                  `json:"max_select"`
	SortOrder   int                  `json:"sort_order"`
	Active      bool                 `json:"active"`
	Options     []configReportOption `json:"options"`
	CategoryIDs []string             `json:"category_ids"`
	Items       []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"items"`
	ItemsTotal int `json:"items_total"`
}

type configReportStation struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// An empty shop reports `[]` for all three keys — never `null`, which the
// cloud side would decode into a nil slice and render as "loading" or
// "unknown" rather than "nothing configured".
func TestRemoteConfigReport_EmptyShopGivesEmptyArrays(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	_, decoded := remoteConfigReportJSON(t, dp)
	for _, key := range []string{"categories", "modifier_groups", "kitchen_stations"} {
		raw, ok := decoded[key]
		if !ok {
			t.Fatalf("DeviceExtra key %q missing", key)
		}
		if string(raw) != "[]" {
			t.Fatalf("%s on an empty shop = %s, want []", key, raw)
		}
	}
}

// One of everything, linked every way the contract carries: a child
// category with a colour, two modifier groups linked to the parent in an
// explicit order, options with a price delta, a direct item link, and a
// kitchen station (WITH a printer address, which must not travel) routed
// from the parent category.
func TestRemoteConfigReport_CategoriesGroupsStationsLinked(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	cat := data.NewCatalogRepo(dp.Db)
	mods := data.NewModifierRepo(dp.Db)
	posr := data.NewPOSRepo(dp.Db)

	drinksID, err := cat.CreateCategoryWithColor(ctx, "Drinks", catalogtypes.ItemColors()[0].Hex)
	if err != nil {
		t.Fatalf("create Drinks: %v", err)
	}
	hotID, err := cat.CreateCategoryWithColor(ctx, "Hot drinks", "")
	if err != nil {
		t.Fatalf("create Hot drinks: %v", err)
	}
	// No repo setter for parent_id yet (the categories admin screen edits
	// name/colour only); a test-only direct write is the seed pattern
	// seedForPages uses.
	if _, err := dp.Db.ExecContext(ctx, `UPDATE categories SET parent_id = ?, is_active = 0 WHERE id = ?`, drinksID, hotID); err != nil {
		t.Fatalf("set parent: %v", err)
	}
	if _, err := mods.CreateGroup(ctx, "grp_milk", "Milk", true, 1, 1, 3); err != nil {
		t.Fatalf("create Milk: %v", err)
	}
	if _, err := mods.CreateOption(ctx, "opt_oat", "grp_milk", "Oat", 40, 1); err != nil {
		t.Fatalf("create Oat: %v", err)
	}
	if _, err := mods.CreateOption(ctx, "opt_soy", "grp_milk", "Soy", 0, 0); err != nil {
		t.Fatalf("create Soy: %v", err)
	}
	if _, err := mods.CreateGroup(ctx, "grp_size", "Size", false, 0, 1, 1); err != nil {
		t.Fatalf("create Size: %v", err)
	}
	// Linked Size first, Milk second — the report must keep that order.
	if err := mods.SetCategoryModifierGroups(ctx, drinksID, []string{"grp_size", "grp_milk"}); err != nil {
		t.Fatalf("link groups to Drinks: %v", err)
	}
	if err := mods.LinkGroupToItem(ctx, "itm1", "grp_milk", 0); err != nil {
		t.Fatalf("link Milk to itm1: %v", err)
	}
	const printerAddr = "192.168.77.9:9100"
	barID, err := posr.CreateKitchenStation(ctx, "Bar", "printer", printerAddr)
	if err != nil {
		t.Fatalf("create Bar: %v", err)
	}
	if err := posr.SetCategoryStationRoutes(ctx, drinksID, []string{barID}); err != nil {
		t.Fatalf("route Drinks to Bar: %v", err)
	}

	raw, decoded := remoteConfigReportJSON(t, dp)

	// Kitchen stations: id + name, nothing else — never the printer address
	// or destination (that is device/network topology, not menu config).
	if strings.Contains(raw, printerAddr) || strings.Contains(raw, "printer_address") ||
		strings.Contains(raw, "destination") {
		t.Fatalf("printer/destination details leaked into the device report: %s", raw)
	}
	var stations []configReportStation
	decodeReport(t, decoded, "kitchen_stations", &stations)
	if len(stations) != 1 || stations[0].ID != barID || stations[0].Name != "Bar" {
		t.Fatalf("kitchen_stations = %+v", stations)
	}
	var stationObjs []json.RawMessage
	decodeReport(t, decoded, "kitchen_stations", &stationObjs)
	assertExactKeys(t, "kitchen_stations[0]", stationObjs[0], "id", "name")

	// Categories.
	var cats []configReportCategory
	decodeReport(t, decoded, "categories", &cats)
	if len(cats) != 2 {
		t.Fatalf("categories = %+v", cats)
	}
	var catObjs []json.RawMessage
	decodeReport(t, decoded, "categories", &catObjs)
	for i, o := range catObjs {
		assertExactKeys(t, fmt.Sprintf("categories[%d]", i), o,
			"id", "name", "parent_id", "color", "icon", "show_on_sale_screen", "sort_order", "active", "modifier_group_ids", "station_ids")
	}
	byID := map[string]configReportCategory{}
	for _, c := range cats {
		byID[c.ID] = c
	}
	drinks, hot := byID[drinksID], byID[hotID]
	if drinks.Name != "Drinks" || drinks.ParentID != "" || drinks.Color != catalogtypes.ItemColors()[0].Hex ||
		!drinks.Active || drinks.SortOrder != 0 {
		t.Fatalf("Drinks = %+v", drinks)
	}
	if got := strings.Join(drinks.ModifierGroupIDs, ","); got != "grp_size,grp_milk" {
		t.Fatalf("Drinks.modifier_group_ids = %q, want the linked order grp_size,grp_milk", got)
	}
	if len(drinks.StationIDs) != 1 || drinks.StationIDs[0] != barID {
		t.Fatalf("Drinks.station_ids = %v", drinks.StationIDs)
	}
	if hot.Name != "Hot drinks" || hot.ParentID != drinksID || hot.Color != "" || hot.Active || hot.SortOrder != 1 {
		t.Fatalf("Hot drinks = %+v", hot)
	}
	// An unlinked category reports empty lists, not null.
	if !strings.Contains(string(catObjs[1]), `"modifier_group_ids":[]`) || !strings.Contains(string(catObjs[1]), `"station_ids":[]`) {
		t.Fatalf("unlinked category must report [] lists: %s", catObjs[1])
	}

	// Modifier groups (ordered by name: Milk, Size).
	var groups []configReportGroup
	decodeReport(t, decoded, "modifier_groups", &groups)
	if len(groups) != 2 || groups[0].ID != "grp_milk" || groups[1].ID != "grp_size" {
		t.Fatalf("modifier_groups = %+v", groups)
	}
	var groupObjs []json.RawMessage
	decodeReport(t, decoded, "modifier_groups", &groupObjs)
	for i, o := range groupObjs {
		assertExactKeys(t, fmt.Sprintf("modifier_groups[%d]", i), o,
			"id", "name", "required", "min_select", "max_select", "sort_order", "active",
			"options", "category_ids", "items", "items_total")
	}
	milk := groups[0]
	if milk.Name != "Milk" || !milk.Required || milk.MinSelect != 1 || milk.MaxSelect != 1 ||
		milk.SortOrder != 3 || !milk.Active {
		t.Fatalf("Milk = %+v", milk)
	}
	if len(milk.Options) != 2 || milk.Options[0].ID != "opt_soy" || milk.Options[1].ID != "opt_oat" {
		t.Fatalf("Milk.options (want sort order Soy, Oat) = %+v", milk.Options)
	}
	if o := milk.Options[1]; o.Name != "Oat" || o.PriceDeltaMinor != 40 || o.SortOrder != 1 || !o.Active {
		t.Fatalf("Oat = %+v", o)
	}
	var milkObj struct {
		Options []json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal(groupObjs[0], &milkObj); err != nil {
		t.Fatalf("decode Milk options: %v", err)
	}
	assertExactKeys(t, "Milk.options[0]", milkObj.Options[0], "id", "name", "price_delta_minor", "sort_order", "active")
	if len(milk.CategoryIDs) != 1 || milk.CategoryIDs[0] != drinksID {
		t.Fatalf("Milk.category_ids = %v", milk.CategoryIDs)
	}
	if len(milk.Items) != 1 || milk.Items[0].ID != "itm1" || milk.Items[0].Name != "Apple" || milk.ItemsTotal != 1 {
		t.Fatalf("Milk.items = %+v total=%d", milk.Items, milk.ItemsTotal)
	}
	size := groups[1]
	if size.Required || size.MinSelect != 0 || size.MaxSelect != 1 || size.SortOrder != 1 {
		t.Fatalf("Size = %+v", size)
	}
	if len(size.CategoryIDs) != 1 || size.CategoryIDs[0] != drinksID || size.ItemsTotal != 0 {
		t.Fatalf("Size links = %+v", size)
	}
	// A group with no options or items reports [] for both, not null.
	if !strings.Contains(string(groupObjs[1]), `"options":[]`) || !strings.Contains(string(groupObjs[1]), `"items":[]`) {
		t.Fatalf("Size must report [] for options and items: %s", groupObjs[1])
	}

	// The existing fields still ride along.
	for _, k := range []string{"theme", "themes", "problems", "till_settings", "quick_buttons"} {
		if _, present := decoded[k]; !present {
			t.Fatalf("existing DeviceExtra field %q lost", k)
		}
	}
}

// A group directly linked to more items than the cap reports only the first
// remoteReportMaxGroupItems of them but the real total — the heartbeat is a
// status report, not a catalogue sync, and a shop with thousands of items
// on one group must not inflate every heartbeat.
func TestRemoteModifierGroupsReport_CapsItemsAndReportsTotal(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	cat := data.NewCatalogRepo(dp.Db)
	mods := data.NewModifierRepo(dp.Db)
	if _, err := mods.CreateGroup(ctx, "grp_big", "Big", false, 0, 1, 0); err != nil {
		t.Fatalf("create group: %v", err)
	}
	total := remoteReportMaxGroupItems + 5
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("itm_cap_%04d", i)
		if _, err := cat.CreateItem(ctx, catalogtypes.ItemInput{ID: id, SKU: id, Name: "Cap " + id, BasePrice: 100, IsActive: true}); err != nil {
			t.Fatalf("create item %s: %v", id, err)
		}
		if err := mods.LinkGroupToItem(ctx, id, "grp_big", i); err != nil {
			t.Fatalf("link item %s: %v", id, err)
		}
	}

	_, decoded := remoteConfigReportJSON(t, dp)
	var groups []configReportGroup
	decodeReport(t, decoded, "modifier_groups", &groups)
	if len(groups) != 1 {
		t.Fatalf("modifier_groups = %+v", groups)
	}
	if len(groups[0].Items) != remoteReportMaxGroupItems {
		t.Fatalf("items reported = %d, want the cap %d", len(groups[0].Items), remoteReportMaxGroupItems)
	}
	if groups[0].ItemsTotal != total {
		t.Fatalf("items_total = %d, want the real count %d", groups[0].ItemsTotal, total)
	}
}

// The heartbeat carries the till's ISO 4217 currency (ut-docs#2472) so the
// cloud panel can format the reported modifier price deltas with the right
// symbol and scale instead of a bare number.
func TestRemoteConfigReport_CarriesCurrency(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	_, decoded := remoteConfigReportJSON(t, dp)
	var got string
	decodeReport(t, decoded, "currency", &got)
	if want := dp.CurrentState().Currency; got != want || got == "" {
		t.Fatalf("currency = %q, want the till's %q (non-empty)", got, want)
	}
	// The minor-unit exponent travels too: the cloud's own currency data
	// (CLDR) disagrees with the till's for PKR and has no IRT at all
	// (2026-09-23 review), so the till is the authority on scale.
	var decimals int
	decodeReport(t, decoded, "currency_decimals", &decimals)
	if want := httpx.CurrencyByCode(got).Decimals; decimals != want {
		t.Fatalf("currency_decimals = %d, want %d", decimals, want)
	}
}

// A failed read must NOT report an empty list: the cloud treats a present
// empty list as "the till has none" and would wipe its last good copy
// (2026-09-23 review). The keys are left out instead, which the cloud reads
// as "keep what you have".
func TestRemoteConfigReport_ReadErrorOmitsKeys(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	hooks := buildCloudHooks(dp, nil)
	dp.Db.Close()
	extra := hooks.DeviceExtra(t.Context())
	for _, key := range []string{"categories", "modifier_groups", "kitchen_stations"} {
		if v, ok := extra[key]; ok {
			t.Fatalf("%s present (%v) after a read error; must be omitted so the cloud keeps its copy", key, v)
		}
	}
}

// Unchanged config is not re-sent on every 2-minute heartbeat — only when
// it changes, or once per refresh period so a cloud that lost its copy
// recovers (2026-09-23 review: hundreds of KB every tick otherwise).
func TestRemoteConfigReport_SentOnlyWhenChanged(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	hooks := buildCloudHooks(dp, nil)
	ctx := t.Context()
	if _, ok := hooks.DeviceExtra(ctx)["categories"]; !ok {
		t.Fatal("first heartbeat must carry categories")
	}
	if v, ok := hooks.DeviceExtra(ctx)["categories"]; ok {
		t.Fatalf("unchanged config re-sent on the next heartbeat: %v", v)
	}
	if _, err := data.NewCatalogRepo(dp.Db).CreateCategoryWithColor(ctx, "Changed", ""); err != nil {
		t.Fatalf("create category: %v", err)
	}
	if _, ok := hooks.DeviceExtra(ctx)["categories"]; !ok {
		t.Fatal("a changed config must be sent on the next heartbeat")
	}
	prev := configReportRefresh
	configReportRefresh = 0
	t.Cleanup(func() { configReportRefresh = prev })
	if _, ok := hooks.DeviceExtra(ctx)["categories"]; !ok {
		t.Fatal("after the refresh period an unchanged config must be re-sent")
	}
}

// A config too big for the byte budget is left out rather than sent: the
// cloud refuses a sync body over 4 MiB, and a refused sync would also cut
// the till off from its directives (2026-09-23 review).
func TestRemoteConfigReport_OverBudgetOmits(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	prev := configReportByteBudget
	configReportByteBudget = 8
	t.Cleanup(func() { configReportByteBudget = prev })
	if _, err := data.NewCatalogRepo(dp.Db).CreateCategoryWithColor(t.Context(), "Big enough", ""); err != nil {
		t.Fatalf("create category: %v", err)
	}
	extra := buildCloudHooks(dp, nil).DeviceExtra(t.Context())
	for _, key := range []string{"categories", "modifier_groups", "kitchen_stations"} {
		if _, ok := extra[key]; ok {
			t.Fatalf("%s sent although the config exceeds the byte budget", key)
		}
	}
	if _, ok := extra["quick_buttons"]; !ok {
		t.Fatal("the rest of the heartbeat must still be sent")
	}
}

// --- update_category (ut-docs#2354) ---

// The wired hook edits partially (absent = keep), checks the palette,
// writes links, refuses on a replica and audits — the same gates
// cloudUpsertCategory has.
func TestCloudUpdateCategory_PartialEditLinksAuditAndGates(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	hooks := buildCloudHooks(dp, nil)
	if hooks.UpdateCategory == nil {
		t.Fatalf("UpdateCategory hook not wired")
	}
	if _, err := cloudUpsertCategory(ctx, dp, "", "Drinks", "#0f172a"); err != nil {
		t.Fatalf("create: %v", err)
	}
	row, _ := findCategoryByName(t, dp, "Drinks")
	st, err := data.NewPOSRepo(dp.Db).CreateKitchenStation(ctx, "Bar", "printer", "")
	if err != nil {
		t.Fatalf("station: %v", err)
	}
	s := func(v string) *string { return &v }
	ids := func(v ...string) *[]string { return &v }

	// Off-palette colour: refused, nothing written.
	if _, err := hooks.UpdateCategory(ctx, row.ID, s("Hot"), s("#123456"), nil, nil); err == nil {
		t.Fatalf("off-palette colour must be refused")
	}
	if got, _ := findCategoryByName(t, dp, "Drinks"); got.ID != row.ID || got.Color != "#0f172a" {
		t.Fatalf("refused edit wrote: %+v", got)
	}

	// Rename only: colour kept; station link written.
	msg, err := hooks.UpdateCategory(ctx, row.ID, s("Hot drinks"), nil, nil, ids(st))
	if err != nil || msg != "updated category Hot drinks" {
		t.Fatalf("update: msg=%q err=%v", msg, err)
	}
	got, _ := findCategoryByName(t, dp, "Hot drinks")
	if got.ID != row.ID || got.Color != "#0f172a" {
		t.Fatalf("after rename = %+v, colour must be kept", got)
	}
	routes, _ := data.NewPOSRepo(dp.Db).CategoryStationRoutes(ctx, row.ID)
	if len(routes) != 1 || routes[0] != st {
		t.Fatalf("station routes = %v", routes)
	}

	// Group link: written, and the audit records the effective set.
	if _, err := data.NewModifierRepo(dp.Db).CreateGroup(ctx, "grp-wire", "Milk", false, 0, 1, 0); err != nil {
		t.Fatalf("group: %v", err)
	}
	if _, err := hooks.UpdateCategory(ctx, row.ID, nil, nil, ids(" grp-wire ", "grp-wire"), nil); err != nil {
		t.Fatalf("link group: %v", err)
	}
	var detail string
	if err := dp.Db.QueryRowContext(ctx,
		`SELECT COALESCE(data_json, '') FROM audit_log WHERE entity_type = 'category' AND entity_id = ? AND action = 'cloud_category_updated' ORDER BY rowid DESC LIMIT 1`,
		row.ID).Scan(&detail); err != nil {
		t.Fatalf("audit detail: %v", err)
	}
	if !strings.Contains(detail, `"modifier_group_ids":["grp-wire"]`) {
		t.Fatalf("audit detail = %s, want the effective (trimmed, deduped) group set", detail)
	}

	// Unknown group: refused.
	if _, err := hooks.UpdateCategory(ctx, row.ID, nil, nil, ids("no-such-group"), nil); err == nil {
		t.Fatalf("unknown group must be refused")
	}

	var n int
	if err := dp.Db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_log WHERE entity_type = 'category' AND entity_id = ? AND action = 'cloud_category_updated' AND actor_id = 'system'`,
		row.ID).Scan(&n); err != nil || n != 2 {
		t.Fatalf("audit rows = %d err=%v, want exactly 2 (refused edits don't audit)", n, err)
	}

	// Replica: refused, nothing written.
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("seed sync.primary_url: %v", err)
	}
	if _, err := hooks.UpdateCategory(ctx, row.ID, s("On replica"), nil, nil, nil); err == nil {
		t.Fatalf("replica must refuse")
	}
	if got, _ := findCategoryByName(t, dp, "On replica"); got.ID != "" {
		t.Fatalf("replica wrote the rename")
	}
}

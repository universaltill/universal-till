package pages

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
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
		if l.ItemID == "itm1" {
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
		t.Fatalf("expected itm1 in stock levels")
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

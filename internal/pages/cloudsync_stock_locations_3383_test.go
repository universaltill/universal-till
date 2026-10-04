package pages

import (
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// --- stock locations from my. (ut-docs#3383) ---

func stockLocationsByName(t *testing.T, dp *common.Deps) map[string]data.StockLocationAdmin {
	t.Helper()
	locs, err := data.NewPOSRepo(dp.Db).ListStockLocationsForAdmin(t.Context())
	if err != nil {
		t.Fatalf("list stock locations: %v", err)
	}
	out := make(map[string]data.StockLocationAdmin, len(locs))
	for _, l := range locs {
		out[l.Name] = l
	}
	return out
}

func stockLocationAuditCount(t *testing.T, dp *common.Deps, id, action string) int {
	t.Helper()
	var n int
	if err := dp.Db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM audit_log WHERE entity_type = 'stock_location' AND entity_id = ? AND action = ? AND actor_id = 'system'`,
		id, action,
	).Scan(&n); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	return n
}

// Create goes through POSRepo.CreateStockLocation, audited under "system"
// like the /locations page's own audit() call; a retried create (same
// name, any case) is a no-op success, not a second row.
func TestCloudCreateStockLocation_CreatesOnceAndAudits(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	before := len(stockLocationsByName(t, dp))

	msg, err := cloudCreateStockLocation(ctx, dp, "Back room")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if msg != "created stock location Back room" {
		t.Fatalf("create msg = %q", msg)
	}
	locs := stockLocationsByName(t, dp)
	row, ok := locs["Back room"]
	if !ok || !row.IsActive {
		t.Fatalf("created row = %+v (ok=%v)", row, ok)
	}
	if stockLocationAuditCount(t, dp, row.ID, "stock_location_create") != 1 {
		t.Fatalf("create not audited")
	}

	msg, err = cloudCreateStockLocation(ctx, dp, "  back ROOM ")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if msg != "stock location Back room already exists" {
		t.Fatalf("retry msg = %q", msg)
	}
	if got := len(stockLocationsByName(t, dp)); got != before+1 {
		t.Fatalf("retry added a row: count = %d, want %d", got, before+1)
	}

	if _, err := cloudCreateStockLocation(ctx, dp, "   "); err == nil {
		t.Fatal("blank name must be refused")
	}
}

// stock_locations.name is UNIQUE on the till (001_init.sql), inactive rows
// included, so a create whose name a RETIRED location has is refused in the
// /locations page's own words (locations.error.create — what an operator
// sees for the same duplicate at the till), not with the raw constraint
// error, and writes nothing (review of ut-docs#3383).
func TestCloudCreateStockLocation_RefusesRetiredNameInPageWords(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	repo := data.NewPOSRepo(dp.Db)
	id, err := repo.CreateStockLocation(ctx, "Back room")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetStockLocationActive(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	before := len(stockLocationsByName(t, dp))

	_, err = cloudCreateStockLocation(ctx, dp, "back room")
	want := httpx.T("en", "locations.error.create")
	if want == "locations.error.create" || err == nil || err.Error() != want {
		t.Fatalf("create over a retired name: err = %v, want the locations page's text %q", err, want)
	}
	if got := len(stockLocationsByName(t, dp)); got != before {
		t.Fatalf("refused create wrote: count = %d, want %d", got, before)
	}
	if stockLocationAuditCount(t, dp, id, "stock_location_create") != 0 {
		t.Fatal("refused create was audited")
	}
}

// A rename to a name any OTHER location has (active or retired, any case)
// is refused in the page's own words (locations.error.rename) and writes
// nothing; a case-only change of the location's own name is a real rename.
func TestCloudRenameStockLocation_RefusesTakenName(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	repo := data.NewPOSRepo(dp.Db)
	id, err := repo.CreateStockLocation(ctx, "Back room")
	if err != nil {
		t.Fatal(err)
	}
	retired, err := repo.CreateStockLocation(ctx, "Old shed")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetStockLocationActive(ctx, retired, false); err != nil {
		t.Fatal(err)
	}
	want := httpx.T("en", "locations.error.rename")
	if want == "locations.error.rename" {
		t.Fatal("locations.error.rename has no English text")
	}
	mainName := ""
	for name, l := range stockLocationsByName(t, dp) {
		if l.ID == "loc_main" {
			mainName = name
		}
	}
	if mainName == "" {
		t.Fatal("seed has no loc_main")
	}
	for _, taken := range []string{mainName, strings.ToUpper(mainName), "Old shed", "old SHED"} {
		_, err := cloudRenameStockLocation(ctx, dp, id, taken)
		if err == nil || err.Error() != want {
			t.Fatalf("rename to %q: err = %v, want %q", taken, err, want)
		}
	}
	if _, ok := stockLocationsByName(t, dp)["Back room"]; !ok {
		t.Fatal("refused rename wrote")
	}
	if stockLocationAuditCount(t, dp, id, "stock_location_rename") != 0 {
		t.Fatal("refused rename was audited")
	}
	if msg, err := cloudRenameStockLocation(ctx, dp, id, "Back Room"); err != nil || msg != "renamed stock location to Back Room" {
		t.Fatalf("case-only rename of its own name: msg=%q err=%v", msg, err)
	}
}

func TestCloudRenameStockLocation(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	id, err := data.NewPOSRepo(dp.Db).CreateStockLocation(ctx, "Back room")
	if err != nil {
		t.Fatal(err)
	}

	msg, err := cloudRenameStockLocation(ctx, dp, id, "Cellar")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if msg != "renamed stock location to Cellar" {
		t.Fatalf("rename msg = %q", msg)
	}
	if row, ok := stockLocationsByName(t, dp)["Cellar"]; !ok || row.ID != id {
		t.Fatalf("renamed row = %+v (ok=%v)", row, ok)
	}
	if stockLocationAuditCount(t, dp, id, "stock_location_rename") != 1 {
		t.Fatalf("rename not audited")
	}

	// Idempotent: the same name again writes nothing (no second audit row).
	msg, err = cloudRenameStockLocation(ctx, dp, id, "Cellar")
	if err != nil || msg != "stock location Cellar unchanged" {
		t.Fatalf("same-name rename: msg=%q err=%v", msg, err)
	}
	if stockLocationAuditCount(t, dp, id, "stock_location_rename") != 1 {
		t.Fatalf("a no-op rename must not audit")
	}

	if _, err := cloudRenameStockLocation(ctx, dp, "no-such-location", "Ghost"); err == nil {
		t.Fatal("unknown id must be refused")
	}
}

// Deactivating a location that holds stock is refused with the /locations
// page's own wording (locations.error.in_use) and writes nothing.
func TestCloudSetStockLocationActive_RefusesInUseWithLocationsPageText(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	// seedForPages puts 50 units of stock at loc_main.
	_, err := cloudSetStockLocationActive(ctx, dp, "loc_main", false)
	if err == nil {
		t.Fatal("deactivating a location holding stock must be refused")
	}
	want := httpx.T("en", "locations.error.in_use")
	if want == "locations.error.in_use" || err.Error() != want {
		t.Fatalf("refusal = %q, want the locations page's text %q", err.Error(), want)
	}
	locs, _ := data.NewPOSRepo(dp.Db).ListStockLocationsForAdmin(ctx)
	for _, l := range locs {
		if l.ID == "loc_main" && !l.IsActive {
			t.Fatal("refused deactivate still wrote")
		}
	}
	if stockLocationAuditCount(t, dp, "loc_main", "stock_location_deactivate") != 0 {
		t.Fatal("refused deactivate was audited")
	}
}

func TestCloudSetStockLocationActive_DeactivateAndReactivate(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	id, err := data.NewPOSRepo(dp.Db).CreateStockLocation(ctx, "Back room")
	if err != nil {
		t.Fatal(err)
	}

	msg, err := cloudSetStockLocationActive(ctx, dp, id, false)
	if err != nil || msg != "deactivated stock location Back room" {
		t.Fatalf("deactivate: msg=%q err=%v", msg, err)
	}
	if stockLocationsByName(t, dp)["Back room"].IsActive {
		t.Fatal("still active")
	}
	if stockLocationAuditCount(t, dp, id, "stock_location_deactivate") != 1 {
		t.Fatal("deactivate not audited")
	}
	// Idempotent: already inactive is a no-op success.
	msg, err = cloudSetStockLocationActive(ctx, dp, id, false)
	if err != nil || msg != "stock location Back room already inactive" {
		t.Fatalf("repeat deactivate: msg=%q err=%v", msg, err)
	}
	if stockLocationAuditCount(t, dp, id, "stock_location_deactivate") != 1 {
		t.Fatal("a no-op deactivate must not audit")
	}

	msg, err = cloudSetStockLocationActive(ctx, dp, id, true)
	if err != nil || msg != "activated stock location Back room" {
		t.Fatalf("activate: msg=%q err=%v", msg, err)
	}
	if !stockLocationsByName(t, dp)["Back room"].IsActive {
		t.Fatal("not reactivated")
	}
	if stockLocationAuditCount(t, dp, id, "stock_location_activate") != 1 {
		t.Fatal("activate not audited")
	}

	if _, err := cloudSetStockLocationActive(ctx, dp, "no-such-location", false); err == nil {
		t.Fatal("unknown id must be refused")
	}
}

// The last active location can't be deactivated — the /locations page's
// last_location guard, same wording.
func TestCloudSetStockLocationActive_RefusesLastActiveLocation(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	repo := data.NewPOSRepo(dp.Db)
	id, err := repo.CreateStockLocation(ctx, "Back room")
	if err != nil {
		t.Fatal(err)
	}
	locs, _ := repo.ListStockLocationsForAdmin(ctx)
	for _, l := range locs {
		if l.ID != id && l.IsActive {
			if err := repo.SetStockLocationActive(ctx, l.ID, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, err = cloudSetStockLocationActive(ctx, dp, id, false)
	if err == nil || err.Error() != httpx.T("en", "locations.error.last_location") {
		t.Fatalf("last location: err = %v", err)
	}
}

// Every write is refused on a satellite (stock_locations syncs
// primary-wins), with the same message the other catalog hooks use.
func TestCloudStockLocationHooks_RefusedOnSatellite(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	id, err := data.NewPOSRepo(dp.Db).CreateStockLocation(ctx, "Back room")
	if err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatal(err)
	}
	if _, err := cloudCreateStockLocation(ctx, dp, "Shed"); err == nil {
		t.Error("create on a satellite must be refused")
	}
	if _, err := cloudRenameStockLocation(ctx, dp, id, "Cellar"); err == nil {
		t.Error("rename on a satellite must be refused")
	}
	if _, err := cloudSetStockLocationActive(ctx, dp, id, false); err == nil {
		t.Error("deactivate on a satellite must be refused")
	}
}

func TestBuildCloudHooks_WiresStockLocations(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	hooks := buildCloudHooks(dp, nil)
	if hooks.CreateStockLocation == nil || hooks.RenameStockLocation == nil || hooks.SetStockLocationActive == nil {
		t.Fatal("stock location hooks not wired")
	}
	if _, err := hooks.CreateStockLocation(ctx, "Wired"); err != nil {
		t.Fatalf("wired create: %v", err)
	}
	row, ok := stockLocationsByName(t, dp)["Wired"]
	if !ok {
		t.Fatal("wired create wrote nothing")
	}
	if _, err := hooks.RenameStockLocation(ctx, row.ID, "Wired 2"); err != nil {
		t.Fatalf("wired rename: %v", err)
	}
	if _, err := hooks.SetStockLocationActive(ctx, "loc_main", false); err == nil {
		t.Fatal("wired deactivate must keep the in-use guard")
	}
}

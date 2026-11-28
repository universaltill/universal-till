package pages

import (
	"context"
	"encoding/json"
	"github.com/universaltill/universal-till/internal/httpx"
	"net/http"
	"net/http/httptest"
	"os"
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
	"github.com/universaltill/universal-till/internal/shoptemplate"
	"github.com/universaltill/universal-till/internal/uislot"
)

// ut-docs#177: Init's boot-time "persist resolved defaults" call used to
// hand-copy a partial common.RuntimeState{} literal that dropped
// IdleLockMinutes/KioskIdleResetSeconds. SaveState writes those two keys
// unconditionally (unlike UIScale/OSKMode, which are guarded against a zero
// value), so first boot persisted 0 for both, and every boot after that
// LoadState read 0 back — silently disabling the unattended-till auto-lock
// and the self-order kiosk's idle-reset with no error, no log line, and no
// UI indication. This drives Init itself across two real, consecutive boots
// against the same on-disk DB (exactly what a till restart is) and asserts
// the documented defaults still resolve afterward, not 0.
func TestInit_IdleAndKioskDefaultsSurviveTwoConsecutiveBoots(t *testing.T) {
	chdirRoot(t)
	paths.Init(t.TempDir())

	d := &db.DB{DB: openPagesTestDB(t)}
	defer d.Close()

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRateBP: 2000}}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	boot := func(n int) {
		pm, err := plugins.Init(ctx, cfg, d.DB)
		if err != nil {
			t.Fatalf("boot %d: plugins.Init: %v", n, err)
		}
		var wg sync.WaitGroup
		Init(ctx, ctx, cfg, pm, d.DB, nil, &wg)
		t.Cleanup(func() { httpx.InitRailVisibility(nil) }) // ut-docs#3079: Init wires a process-global checker bound to this test's DB
	}

	boot(1) // fresh install: config-derived defaults get persisted for the first time
	boot(2) // restart: must re-resolve the SAME defaults, not zeros left by a lossy persist

	store := settings.NewStore(d.DB)

	// Check the raw persisted rows directly, not just LoadState's resolved
	// view — the bug WROTE "0" to the store, and an absent key would also
	// resolve correctly via LoadState's own default fallback, which alone
	// wouldn't prove the boot-persist step is what's actually correct here.
	if v, ok, err := store.Get(ctx, common.KeyIdleLock); err != nil || !ok || v != "10" {
		t.Errorf("raw store %s = (%q, ok=%v, err=%v), want (\"10\", true, nil)", common.KeyIdleLock, v, ok, err)
	}
	if v, ok, err := store.Get(ctx, common.KeyKioskIdleReset); err != nil || !ok || v != "60" {
		t.Errorf("raw store %s = (%q, ok=%v, err=%v), want (\"60\", true, nil)", common.KeyKioskIdleReset, v, ok, err)
	}

	st := common.LoadState(ctx, store, cfg)
	if st.IdleLockMinutes != common.DefaultIdleLockMinutes {
		t.Errorf("after two boots, IdleLockMinutes = %d, want default %d (auto-lock silently disabled)",
			st.IdleLockMinutes, common.DefaultIdleLockMinutes)
	}
	if st.KioskIdleResetSeconds != common.DefaultKioskIdleResetSeconds {
		t.Errorf("after two boots, KioskIdleResetSeconds = %d, want default %d (kiosk idle-reset silently disabled)",
			st.KioskIdleResetSeconds, common.DefaultKioskIdleResetSeconds)
	}
}

// ut-docs#1390 (independent review finding): a table claimed right before an
// unclean shutdown (crash, kill, power loss) used to stay unbookable
// forever — pos.Service always starts with an empty basket, so nothing ever
// revisited the orphaned table_claims row left behind. This drives Init
// across a simulated crash-restart (a fresh process boots against a DB that
// already carries a claim from a process that never got to release it) and
// asserts the table is free again afterward — the actual boot path, not
// just the repo method in isolation.
func TestInit_ClearsStaleTableClaimLeftByUncleanShutdown(t *testing.T) {
	chdirRoot(t)
	paths.Init(t.TempDir())

	d := &db.DB{DB: openPagesTestDB(t)}
	defer d.Close()

	posRepo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	tableID, err := posRepo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	// Simulates the crashed process's own claim, made and never released —
	// not going through a live pos.Service at all, since the whole point is
	// that no in-memory basket survives to release it.
	if claimed, err := posRepo.ClaimTable(ctx, tableID); err != nil || !claimed {
		t.Fatalf("simulate pre-crash claim: claimed=%v err=%v", claimed, err)
	}
	if ok, err := posRepo.IsTableFree(ctx, tableID, ""); err != nil || ok {
		t.Fatalf("precondition: T1 must read occupied before the restart, got ok=%v err=%v", ok, err)
	}

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRateBP: 2000}}
	pctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	pm, err := plugins.Init(pctx, cfg, d.DB)
	if err != nil {
		t.Fatalf("plugins.Init: %v", err)
	}
	var wg sync.WaitGroup
	Init(pctx, pctx, cfg, pm, d.DB, nil, &wg)           // the restart
	t.Cleanup(func() { httpx.InitRailVisibility(nil) }) // ut-docs#3079: Init wires a process-global checker bound to this test's DB

	if ok, err := posRepo.IsTableFree(ctx, tableID, ""); err != nil || !ok {
		t.Errorf("after restart, T1 must be free again (stale claim swept), got ok=%v err=%v", ok, err)
	}
}

// TestInit_ReclaimsHeldOrdersTableClaimOnBoot (ut-docs#1704, independent
// review 2026-09-07): the sweep above (TestInit_ClearsStaleTableClaimLeftByUncleanShutdown)
// wipes EVERY till_id=” table_claims row unconditionally, including one
// that in fact still belongs to a genuinely parked order -- held_sales
// itself survives a restart by design (offline-first durability), but
// nothing previously re-created its table_claims mirror. Locally this was
// invisible (ListTablesWithState already reads held_sales directly, so the
// table still displayed occupied on THIS till) -- but the mirror is exactly
// what a replica has already write-through'd to the primary, so losing it
// meant a parked order's table read FREE cross-till after every single
// restart, not just after a >tillClaimTTL outage. Init's boot re-claim step
// must restore it.
func TestInit_ReclaimsHeldOrdersTableClaimOnBoot(t *testing.T) {
	chdirRoot(t)
	paths.Init(t.TempDir())

	d := &db.DB{DB: openPagesTestDB(t)}
	defer d.Close()

	posRepo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	tableID, err := posRepo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	if _, err := d.DB.ExecContext(ctx, `INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id) VALUES ('h1','Table 1',100,1,'{}',?)`, tableID); err != nil {
		t.Fatalf("seed held sale: %v", err)
	}
	// No table_claims row seeded here at all -- simulating exactly what a
	// real restart leaves behind: held_sales survived, table_claims did not
	// (this process never even ran the hold handler that would have kept
	// one alive; a real restart wipes it via the sweep regardless).
	var preClaimRows int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM table_claims WHERE table_id = ?`, tableID).Scan(&preClaimRows); err != nil {
		t.Fatalf("count table_claims: %v", err)
	}
	if preClaimRows != 0 {
		t.Fatalf("test setup error: table must start with no live claim, only the held order, got %d claim rows", preClaimRows)
	}

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRateBP: 2000}}
	pctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	pm, err := plugins.Init(pctx, cfg, d.DB)
	if err != nil {
		t.Fatalf("plugins.Init: %v", err)
	}
	var wg sync.WaitGroup
	Init(pctx, pctx, cfg, pm, d.DB, nil, &wg)           // the restart
	t.Cleanup(func() { httpx.InitRailVisibility(nil) }) // ut-docs#3079: Init wires a process-global checker bound to this test's DB

	states, err := posRepo.ListTablesWithState(ctx, time.Now().Add(-tillClaimTTL))
	if err != nil {
		t.Fatalf("ListTablesWithState: %v", err)
	}
	var t1 *data.TableWithState
	for i := range states {
		if states[i].ID == tableID {
			t1 = &states[i]
		}
	}
	if t1 == nil || !t1.Occupied {
		t.Fatalf("after restart, T1 must still read occupied (the parked order never left) -- got %+v", t1)
	}
	var claimRows int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM table_claims WHERE table_id = ?`, tableID).Scan(&claimRows); err != nil {
		t.Fatalf("count table_claims: %v", err)
	}
	if claimRows != 1 {
		t.Fatalf("boot must re-claim the held order's table -- want 1 table_claims row, got %d (this is what a replica write-throughs to the primary; without it the table reads FREE cross-till after every restart)", claimRows)
	}
}

// TestInit_ReleasesOrphanedLiveClaimOnPrimaryAtBootEvenWhenTillStaysOnline
// (ut-docs#1712, the residual left by ut-docs#1703's own review, finding 7):
// a replica's LIVE (not-yet-held) basket claimed a table on the primary,
// then crashed before releasing it. The till reboots and is "seen" by the
// primary again immediately (any sync call refreshes tills.last_seen_at),
// so it is NOT stale by ClaimTableForTill's own staleness rule — and the
// operator never re-picks that exact table again, so nothing else would
// ever revisit that row. Before this card, that claim sat on the primary
// forever, blocking the table from every other till in the shop, precisely
// because the till looked online. Init's new boot-time release-all step
// must clear it anyway, unconditionally, on every restart.
func TestInit_ReleasesOrphanedLiveClaimOnPrimaryAtBootEvenWhenTillStaysOnline(t *testing.T) {
	chdirRoot(t)
	paths.Init(t.TempDir())

	primary, primaryRepo := newHoldCrossTillPrimary(t, "b-123")
	ctx := context.Background()
	tableID, err := primaryRepo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable on primary: %v", err)
	}

	d := &db.DB{DB: openPagesTestDB(t)}
	defer d.Close()

	// Point this till at the primary BEFORE Init ever runs -- exactly like a
	// real replica, already enrolled from a previous boot.
	store := settings.NewStore(d.DB)
	setReplicaSettings(t, store, primary.URL, "b-123")

	// Simulate the pre-crash state: a live basket's table pick, already
	// write-through'd to the primary under this till's bearer -- the real
	// claimTableWriteThrough call this stands in for happens over HTTP in
	// production, so this drives the same primary-side endpoint directly
	// rather than re-deriving a whole pos.Service pick.
	claimResp := postSyncClaimDirect(t, primary.URL, "b-123", tableID, "claim")
	if !claimResp.Data.Claimed {
		t.Fatalf("seed the pre-crash claim: %+v", claimResp)
	}
	if free, err := primaryRepo.IsTableFree(ctx, tableID, ""); err != nil || free {
		t.Fatalf("precondition: T1 must read occupied before the restart, got free=%v err=%v", free, err)
	}

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRateBP: 2000}}
	pctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	pm, err := plugins.Init(pctx, cfg, d.DB)
	if err != nil {
		t.Fatalf("plugins.Init: %v", err)
	}
	var wg sync.WaitGroup
	Init(pctx, pctx, cfg, pm, d.DB, nil, &wg)           // the restart -- till_id is now "seen" again
	t.Cleanup(func() { httpx.InitRailVisibility(nil) }) // ut-docs#3079: Init wires a process-global checker bound to this test's DB

	if free, err := primaryRepo.IsTableFree(ctx, tableID, ""); err != nil || !free {
		t.Fatalf("after restart, T1 must be free on the primary even though this till is online again and never re-picked it, got free=%v err=%v", free, err)
	}
}

// postSyncClaimDirect posts directly to the primary's bearer-authed
// /api/sync/tables/{claim,release} endpoints, bypassing claimTableWriteThrough
// entirely -- used to seed a "claim already write-through'd before the crash"
// precondition without standing up a whole replica pos.Service.
func postSyncClaimDirect(t *testing.T, primaryURL, bearer, tableID, action string) syncTableClaimResp {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, primaryURL+"/api/sync/tables/"+action,
		strings.NewReader("table_id="+tableID))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST /api/sync/tables/%s: %v", action, err)
	}
	defer resp.Body.Close()
	var out syncTableClaimResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// TestInit_HeldOrderClaimSurvivesReleaseAllEvenWhenBootReclaimFails
// (ut-docs#1712, independent review 2026-09-07, blocker 1): the exact
// failure scenario the review's own probe demonstrated pre-fix -- a shop
// power cut, primary and replica both reboot, primary is still warming up
// (or just briefly unreachable) when the replica's boot re-claim tries to
// re-affirm a held order's table. Before the fix, release-all deleted the
// primary's claim UNCONDITIONALLY, so a failed re-claim right after left the
// table genuinely unclaimed on the primary until the next restart -- another
// till could seat a party there in the meantime. With keepTableIDs scoping
// release-all, the primary's claim for a held order is never deleted in the
// first place, so a failed re-claim afterward is a harmless no-op (the boot
// re-claim step is a refresh, not a restore-from-nothing) and the table
// stays correctly occupied on the primary throughout.
func TestInit_HeldOrderClaimSurvivesReleaseAllEvenWhenBootReclaimFails(t *testing.T) {
	chdirRoot(t)
	paths.Init(t.TempDir())

	dbase := &db.DB{DB: openPagesTestDB(t)}
	defer dbase.Close()
	primaryDp := &common.Deps{Db: dbase.DB}
	realMux := http.NewServeMux()
	registerSyncTables(realMux, primaryDp)
	registerSyncTablesClaim(realMux, primaryDp)
	// Everything real EXCEPT /claim, which simulates the primary being
	// unreachable/erroring for just the boot re-claim's own call -- release
	// -all and every other path still hit the real handlers.
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sync/tables/claim" {
			http.Error(w, "simulated primary outage", http.StatusServiceUnavailable)
			return
		}
		realMux.ServeHTTP(w, r)
	})
	primary := httptest.NewServer(wrapped)
	defer primary.Close()

	primaryRepo := data.NewPOSRepo(dbase.DB)
	ctx := context.Background()
	tableID, err := primaryRepo.CreateTable(ctx, "T1", "", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	tillID, err := data.NewTillsRepo(dbase.DB).InsertTill(ctx, "Replica", hashBearer("b-123"))
	if err != nil {
		t.Fatalf("seed till: %v", err)
	}
	// Seed the pre-crash state directly: the primary already holds this
	// replica's claim on T1, write-through'd before the crash -- exactly
	// what ut-docs#1704 keeps alive through a held order's whole park.
	if claimed, err := primaryRepo.ClaimTableForTill(ctx, tableID, tillID, time.Now().Add(-2*time.Minute)); err != nil || !claimed {
		t.Fatalf("seed pre-crash primary claim: claimed=%v err=%v", claimed, err)
	}

	d := &db.DB{DB: openPagesTestDB(t)}
	defer d.Close()
	store := settings.NewStore(d.DB)
	setReplicaSettings(t, store, primary.URL, "b-123")
	if _, err := d.DB.Exec(`INSERT INTO tables (id, label, area_zone, seat_count, shape, pos_x, pos_y, enabled, created_at, updated_at) VALUES (?,?,?,?,?,?,?,1,datetime('now'),datetime('now'))`,
		tableID, "T1", "", 4, "rect", 100, 100); err != nil {
		t.Fatalf("mirror table onto replica: %v", err)
	}
	if _, err := d.DB.Exec(`INSERT INTO held_sales (id, label, total_minor, line_count, payload, table_id) VALUES ('h1','Table 1',100,1,'{}',?)`, tableID); err != nil {
		t.Fatalf("seed held sale: %v", err)
	}

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRateBP: 2000}}
	pctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	pm, err := plugins.Init(pctx, cfg, d.DB)
	if err != nil {
		t.Fatalf("plugins.Init: %v", err)
	}
	var wg sync.WaitGroup
	Init(pctx, pctx, cfg, pm, d.DB, nil, &wg)           // the restart -- re-claim's /claim call fails throughout
	t.Cleanup(func() { httpx.InitRailVisibility(nil) }) // ut-docs#3079: Init wires a process-global checker bound to this test's DB

	if free, err := primaryRepo.IsTableFree(ctx, tableID, ""); err != nil || free {
		t.Fatalf("the held order's claim must survive on the primary even though the boot re-claim failed, free=%v err=%v", free, err)
	}
}

// bootInitForTest runs pages.Init against db and returns the Deps; the
// process-global rail-visibility checker Init wires is reset on cleanup
// (ut-docs#3079: it is bound to this test's DB).
func bootInitForTest(t *testing.T, d *db.DB, cfg *config.Config) *common.Deps {
	t.Helper()
	pctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	pm, err := plugins.Init(pctx, cfg, d.DB)
	if err != nil {
		t.Fatalf("plugins.Init: %v", err)
	}
	var wg sync.WaitGroup
	_, dp := Init(pctx, pctx, cfg, pm, d.DB, nil, &wg)
	t.Cleanup(func() { httpx.InitRailVisibility(nil) })
	return dp
}

func amendmentFor(list []uislot.Amendment, key string) (uislot.Amendment, bool) {
	for _, a := range list {
		if a.Key == key {
			return a, true
		}
	}
	return uislot.Amendment{}, false
}

// TestInit_ServiceShopTypeGetsItsLayoutFromTemplateAtBoot (ut-docs#2001,
// ADR-0190): a shop that already has shop_type="service" persisted — from
// before templates existed, or restored from a backup — must come up with
// its layout on every boot, in every ADR-0088 slot, with no Settings save.
// The type is written straight to the settings store (never through a
// handler), simulating exactly that gap. The layout now comes from the
// service template's legacy_layout (no ledger = legacy mode), not a plugin,
// and a steady-state reboot must populate all four slot lists straight from
// Init, not only after some later reload.
func TestInit_ServiceShopTypeGetsItsLayoutFromTemplateAtBoot(t *testing.T) {
	chdirRoot(t)
	paths.Init(t.TempDir())

	d := &db.DB{DB: openPagesTestDB(t)}
	defer d.Close()
	if err := settings.NewStore(d.DB).Set(context.Background(), common.KeyShopType, "service"); err != nil {
		t.Fatalf("seed pre-existing shop_type: %v", err)
	}
	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRateBP: 2000}}

	for boot := 1; boot <= 2; boot++ { // a first boot and a steady-state reboot
		dp := bootInitForTest(t, d, cfg)
		if st := dp.TemplateState(context.Background()); st.Mode != common.TemplateLegacy {
			t.Fatalf("boot %d: a service shop with no ledger is in legacy mode, got %v", boot, st.Mode)
		}
		if a, ok := amendmentFor(dp.MenuAmendmentsSnapshot(), "/tables"); !ok || !a.Hide || a.PluginID != shoptemplate.PluginID {
			t.Errorf("boot %d: the menu must hide /tables from the template, got %+v", boot, dp.MenuAmendmentsSnapshot())
		}
		if a, ok := amendmentFor(dp.SettingsAmendmentsSnapshot(), "settings-theme"); !ok || a.Order == nil {
			t.Errorf("boot %d: Settings must put Theme first from the template at boot, got %+v", boot, dp.SettingsAmendmentsSnapshot())
		}
		if a, ok := amendmentFor(dp.RailAmendmentsSnapshot(), "/orders"); !ok || a.Order == nil {
			t.Errorf("boot %d: the rail must move Orders from the template, got %+v", boot, dp.RailAmendmentsSnapshot())
		}
		if a, ok := amendmentFor(dp.ItemsAmendmentsSnapshot(), "/catalog"); !ok || a.Order == nil {
			t.Errorf("boot %d: the Items rail must move Catalog from the template, got %+v", boot, dp.ItemsAmendmentsSnapshot())
		}
	}
}

// TestInit_RemovesRetiredSalonLayoutPlugin is the one-time migration: a till
// an earlier build installed the built-in salon layout plugin on (trust level
// system) loses it at boot, DB rows and files, and the amendments it supplied
// are gone with it. A same-id plugin a shop installed itself (any other trust
// level) is left alone.
func TestInit_RemovesRetiredSalonLayoutPlugin(t *testing.T) {
	for _, tc := range []struct {
		trust       string
		wantRemoved bool
	}{{"system", true}, {"verified", false}} {
		t.Run(tc.trust, func(t *testing.T) {
			chdirRoot(t)
			paths.Init(t.TempDir())
			d := &db.DB{DB: openPagesTestDB(t)}
			defer d.Close()
			ctx := context.Background()

			id := retiredLayoutPluginIDs[0]
			m := &plugins.Manifest{
				ID: id, Name: "Retired layout", Version: "0.4.1", Runtime: "none", CanonicalType: "layout",
				Entries: []plugins.ManifestEntry{{Type: "layout", Key: "menu", Label: "Retired", Config: map[string]any{
					"slot": "menu", "amendments": []any{map[string]any{"key": "/tables", "hide": true}},
				}}},
			}
			if err := plugins.PersistManifest(ctx, d.DB, m, plugins.InstallOptions{TrustLevel: tc.trust}); err != nil {
				t.Fatalf("install the retired plugin: %v", err)
			}
			if err := os.MkdirAll(paths.Plugins(id, "0.4.1"), 0o755); err != nil {
				t.Fatal(err)
			}

			cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRateBP: 2000}}
			dp := bootInitForTest(t, d, cfg)

			_, found, err := data.NewPluginRepo(d.DB).GetInstalledPluginVersion(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if found == tc.wantRemoved {
				t.Errorf("trust %q: plugin installed after boot = %v, want %v", tc.trust, found, !tc.wantRemoved)
			}
			_, onDisk := os.Stat(paths.Plugins(id))
			if tc.wantRemoved && onDisk == nil {
				t.Errorf("trust %q: the plugin's files must be removed too", tc.trust)
			}
			hides := false
			for _, a := range dp.MenuAmendmentsSnapshot() {
				hides = hides || (a.Key == "/tables" && a.Hide)
			}
			if hides == tc.wantRemoved {
				t.Errorf("trust %q: /tables hidden after boot = %v, want %v", tc.trust, hides, !tc.wantRemoved)
			}

			// Idempotent: a second boot finds nothing to do and does not fail.
			bootInitForTest(t, d, cfg)
		})
	}
}

// TestInit_DisabledRetiredSalonPluginKeepsFullMenu: a service shop whose
// manager had switched the salon layout plugin off (installed, inactive) is
// not forced into the legacy salon layout when the plugin is retired: the
// opt-out is recorded before removal and the menu stays whole (ADR-0190 Q1).
func TestInit_DisabledRetiredSalonPluginKeepsFullMenu(t *testing.T) {
	for _, active := range []bool{false, true} {
		chdirRoot(t)
		paths.Init(t.TempDir())
		d := &db.DB{DB: openPagesTestDB(t)}
		ctx := context.Background()
		id := retiredLayoutPluginIDs[0]
		m := &plugins.Manifest{ID: id, Name: "Retired layout", Version: "0.4.1", Runtime: "none", CanonicalType: "layout",
			Entries: []plugins.ManifestEntry{{Type: "layout", Key: "menu", Label: "Retired", Config: map[string]any{
				"slot": "menu", "amendments": []any{map[string]any{"key": "/tables", "hide": true}},
			}}}}
		if err := plugins.PersistManifest(ctx, d.DB, m, plugins.InstallOptions{TrustLevel: "system"}); err != nil {
			t.Fatal(err)
		}
		if err := data.NewPluginRepo(d.DB).SetPluginActive(ctx, nil, id, active); err != nil {
			t.Fatal(err)
		}
		if err := settings.NewStore(d.DB).Set(ctx, common.KeyShopType, "service"); err != nil {
			t.Fatal(err)
		}
		cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRateBP: 2000}}
		dp := bootInitForTest(t, d, cfg)
		hidesKitchen := false
		for _, a := range dp.MenuAmendmentsSnapshot() {
			hidesKitchen = hidesKitchen || (a.Key == "/kitchen-stations" && a.Hide)
		}
		if hidesKitchen == !active {
			t.Errorf("plugin active=%v: legacy salon layout applied = %v, want %v", active, hidesKitchen, active)
		}
		d.Close()
	}
}

// TestInit_NoShopTypeGetsNoTemplateLayout confirms a till whose shop type was
// never set renders exactly as before templates: no template amendment in any
// slot just because Init ran.
func TestInit_NoShopTypeGetsNoTemplateLayout(t *testing.T) {
	chdirRoot(t)
	paths.Init(t.TempDir())

	d := &db.DB{DB: openPagesTestDB(t)}
	defer d.Close()
	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRateBP: 2000}}
	dp := bootInitForTest(t, d, cfg) // no shop_type ever set

	if st := dp.TemplateState(context.Background()); st.Mode != common.TemplateNone {
		t.Errorf("no shop type: mode = %v, want none", st.Mode)
	}
	for name, list := range map[string][]uislot.Amendment{
		"menu": dp.MenuAmendmentsSnapshot(), "items": dp.ItemsAmendmentsSnapshot(),
		"rail": dp.RailAmendmentsSnapshot(), "settings": dp.SettingsAmendmentsSnapshot(),
	} {
		if len(list) != 0 {
			t.Errorf("%s slot: %d amendment(s) with no shop type, want none: %+v", name, len(list), list)
		}
	}
}

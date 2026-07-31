package data

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// Regression tests for the plugin payment-key hijack found by coverage
// batch 9's independent review: SyncPluginPaymentMethods took
// payment_methods.id verbatim from plugin_entries.key and its
// ON CONFLICT(id) DO UPDATE stamped plugin_id onto whatever row already
// owned that id — so a plugin declaring {type:"payment", key:"cash"}
// captured the built-in cash tender, and the sync's deactivate step then
// flipped it inactive once the plugin went away: cash silently vanished
// from checkout ("checkout must never be blocked", universal-till
// CLAUDE.md). Written failing-first against the unguarded sync.

// pgPlugin inserts a minimal catalog+plugin pair on the REAL migrated
// schema (plugins has an FK onto plugin_catalog).
func pgPlugin(t *testing.T, d *db.DB, id string, active int) {
	t.Helper()
	mustExec(t, d, `INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
VALUES (?, '1.0.0', ?, 'wasm', 'main.wasm', 'https://example.invalid/pkg', 'deadbeef', '0.0.1', '1', '2026-07-30T00:00:00Z')`, id, "Plugin "+id)
	mustExec(t, d, `INSERT INTO plugins (id, name, version, install_state, runtime, entrypoint, is_active, trust_level)
VALUES (?, ?, '1.0.0', 'installed', 'wasm', 'main.wasm', ?, 'trusted')`, id, "Plugin "+id, active)
}

func pgEntry(t *testing.T, d *db.DB, id, pluginID, key, label string) {
	t.Helper()
	mustExec(t, d, `INSERT INTO plugin_entries (id, plugin_id, type, key, label, sort_order, is_active, created_at, updated_at)
VALUES (?, ?, 'payment', ?, ?, 1, 1, '2026-07-30T00:00:00Z', '2026-07-30T00:00:00Z')`, id, pluginID, key, label)
}

type paymentMethodState struct {
	Name     string
	Type     string
	Active   int
	PluginID string
}

func pgMethod(t *testing.T, d *db.DB, id string) (paymentMethodState, bool) {
	t.Helper()
	var s paymentMethodState
	err := d.DB.QueryRow(`SELECT name, type, is_active, COALESCE(plugin_id,'') FROM payment_methods WHERE id = ?`, id).
		Scan(&s.Name, &s.Type, &s.Active, &s.PluginID)
	if err != nil {
		return s, false
	}
	return s, true
}

func pgOpenDB(t *testing.T, name string) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestSyncPluginPaymentMethods_CannotHijackBuiltins(t *testing.T) {
	d := pgOpenDB(t, "hijack.db")
	ctx := context.Background()
	repo := NewPluginRepo(d.DB)

	pgPlugin(t, d, "com.evil.tender", 1)
	pgEntry(t, d, "pe-evil", "com.evil.tender", "cash", "Evil Cash")

	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	got, ok := pgMethod(t, d, "cash")
	if !ok {
		t.Fatal("built-in cash row vanished")
	}
	if got.PluginID != "" || got.Name != "Cash" || got.Active != 1 {
		t.Fatalf("built-in cash captured by plugin: %+v (want plugin_id '', name Cash, active)", got)
	}

	// The historically fatal half: plugin goes away, deactivate step runs.
	mustExec(t, d, `UPDATE plugins SET is_active = 0 WHERE id = 'com.evil.tender'`)
	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		t.Fatalf("sync after disable: %v", err)
	}
	got, _ = pgMethod(t, d, "cash")
	if got.Active != 1 || got.PluginID != "" {
		t.Fatalf("built-in cash deactivated after plugin disable: %+v — checkout would lose its cash tender", got)
	}
}

func TestSyncPluginPaymentMethods_OwnLifecycleStillWorks(t *testing.T) {
	d := pgOpenDB(t, "lifecycle.db")
	ctx := context.Background()
	repo := NewPluginRepo(d.DB)

	pgPlugin(t, d, "com.good.pay", 1)
	pgEntry(t, d, "pe-good", "com.good.pay", "goodpay", "GoodPay")

	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	got, ok := pgMethod(t, d, "goodpay")
	if !ok || got.PluginID != "com.good.pay" || got.Active != 1 || got.Name != "GoodPay" {
		t.Fatalf("plugin method not materialized: %+v ok=%v", got, ok)
	}

	mustExec(t, d, `UPDATE plugins SET is_active = 0 WHERE id = 'com.good.pay'`)
	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		t.Fatalf("sync after disable: %v", err)
	}
	got, ok = pgMethod(t, d, "goodpay")
	if !ok {
		t.Fatal("goodpay row deleted — rows must never be deleted (payments history references them)")
	}
	if got.Active != 0 {
		t.Fatalf("disabled plugin's method must deactivate: %+v", got)
	}

	mustExec(t, d, `UPDATE plugins SET is_active = 1 WHERE id = 'com.good.pay'`)
	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		t.Fatalf("sync after re-enable: %v", err)
	}
	if got, _ = pgMethod(t, d, "goodpay"); got.Active != 1 {
		t.Fatalf("re-enabled plugin's method must reactivate: %+v", got)
	}
}

func TestSyncPluginPaymentMethods_CrossPluginCollisionFirstOwnerWins(t *testing.T) {
	d := pgOpenDB(t, "crossplugin.db")
	ctx := context.Background()
	repo := NewPluginRepo(d.DB)

	pgPlugin(t, d, "com.first.pay", 1)
	pgEntry(t, d, "pe-first", "com.first.pay", "shared", "First Pay")
	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	pgPlugin(t, d, "com.second.pay", 1)
	pgEntry(t, d, "pe-second", "com.second.pay", "shared", "Second Pay")
	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		t.Fatalf("sync 2: %v", err)
	}

	got, _ := pgMethod(t, d, "shared")
	if got.PluginID != "com.first.pay" || got.Name != "First Pay" {
		t.Fatalf("second plugin captured first plugin's method row: %+v", got)
	}
}

// The review's blocker scenario: a replica repaired by migration 021 can
// re-import a captured built-in over LAN admin sync from a not-yet-upgraded
// primary — and the hijacking plugin may not even exist locally. A one-shot
// migration can't help; the sync must re-assert the built-in invariant on
// EVERY run.
func TestSyncPluginPaymentMethods_SelfHealsDamagedBuiltins(t *testing.T) {
	d := pgOpenDB(t, "selfheal.db")
	ctx := context.Background()
	repo := NewPluginRepo(d.DB)

	// Damage as admin-sync would import it: plugin-stamped + deactivated,
	// with NO matching plugin installed on this till at all.
	mustExec(t, d, `UPDATE payment_methods SET plugin_id = 'com.gone.evil', is_active = 0 WHERE id = 'cash'`)

	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	got, ok := pgMethod(t, d, "cash")
	if !ok || got.PluginID != "" || got.Active != 1 {
		t.Fatalf("sync must self-heal a plugin-stamped built-in every run, got %+v ok=%v", got, ok)
	}
}

// plugin_id on payment_methods is till-local derived state (which plugin on
// THIS till owns the method) — it must never travel over LAN admin sync.
func TestAdminSync_DoesNotImportPaymentMethodPluginOwnership(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "pg-primary.db")
	replica := openMigratedDB(t, "pg-replica.db")

	// A damaged, not-yet-upgraded primary: built-in cash captured.
	mustExec(t, primary, `UPDATE payment_methods SET plugin_id = 'com.evil.tender', name = 'Evil Cash', is_active = 0 WHERE id = 'cash'`)

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	got, ok := pgMethod(t, replica, "cash")
	if !ok {
		t.Fatal("replica cash row missing")
	}
	if got.PluginID != "" {
		t.Fatalf("admin sync imported plugin ownership onto the replica's built-in: %+v", got)
	}
}

// Legacy state (pre-fix capture or a rolled-back colliding manifest): the
// deactivate step must key on the OWNING plugin's entries — an unrelated
// plugin holding an entry with the same key must not keep the row alive.
func TestSyncPluginPaymentMethods_DeactivateIsOwnershipAware(t *testing.T) {
	d := pgOpenDB(t, "deactivate-owner.db")
	ctx := context.Background()
	repo := NewPluginRepo(d.DB)

	pgPlugin(t, d, "com.a.pay", 1)
	pgEntry(t, d, "pe-a", "com.a.pay", "shared", "A Pay")
	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	// Unrelated plugin holds an entry with the same key (legacy state).
	pgPlugin(t, d, "com.b.pay", 1)
	pgEntry(t, d, "pe-b", "com.b.pay", "shared", "B Pay")

	mustExec(t, d, `UPDATE plugins SET is_active = 0 WHERE id = 'com.a.pay'`)
	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		t.Fatalf("sync after disabling owner: %v", err)
	}
	got, ok := pgMethod(t, d, "shared")
	if !ok {
		t.Fatal("shared row vanished — rows must never be deleted")
	}
	if got.Active != 0 || got.PluginID != "com.a.pay" {
		t.Fatalf("owner disabled: row must deactivate and stay A's, got %+v", got)
	}
}

func TestMigration021_RepairsHijackedBuiltins(t *testing.T) {
	d := pgOpenDB(t, "repair.db")

	// Recreate the pre-fix damage on a real schema: built-in captured,
	// renamed, and deactivated by the old sync — with the hijacker's
	// payment entry still installed.
	pgPlugin(t, d, "com.evil.tender", 0)
	pgEntry(t, d, "pe-evil", "com.evil.tender", "cash", "Evil Cash")
	mustExec(t, d, `UPDATE payment_methods SET plugin_id = 'com.evil.tender', name = 'Evil Cash', is_active = 0 WHERE id = 'cash'`)

	sqlBytes, err := os.ReadFile(repairMigrationPath(t))
	if err != nil {
		t.Fatalf("read repair migration: %v", err)
	}
	if _, err := d.DB.Exec(string(sqlBytes)); err != nil {
		t.Fatalf("re-exec repair migration (must be idempotent): %v", err)
	}

	got, _ := pgMethod(t, d, "cash")
	if got.PluginID != "" || got.Active != 1 {
		t.Fatalf("hijacked built-in not repaired: %+v (want plugin ownership cleared + active)", got)
	}
	// Names are deliberately NOT reset (operators may rename tenders) —
	// ADR-0031's explicit promise.
	if got.Name != "Evil Cash" {
		t.Fatalf("repair must not touch names, got %q", got.Name)
	}
	// The hijacking ENTRY must be disarmed too: both payment dispatch
	// gates match on entry key alone, so a live entry with key 'cash'
	// would still receive (and could veto) payment.cash.* events.
	var entryActive int
	if err := d.DB.QueryRow(`SELECT is_active FROM plugin_entries WHERE id = 'pe-evil'`).Scan(&entryActive); err != nil {
		t.Fatalf("read hijacker entry: %v", err)
	}
	if entryActive != 0 {
		t.Fatal("migration must deactivate legacy payment entries squatting on built-in keys")
	}

	// A genuine plugin-backed method must NOT be touched by the repair.
	mustExec(t, d, `INSERT INTO payment_methods (id, name, type, is_active, sort_order, plugin_id) VALUES ('okpay','OK Pay','card',0,100,'com.evil.tender')`)
	if _, err := d.DB.Exec(string(sqlBytes)); err != nil {
		t.Fatalf("repair migration second run: %v", err)
	}
	if got, _ := pgMethod(t, d, "okpay"); got.PluginID != "com.evil.tender" || got.Active != 0 {
		t.Fatalf("repair must only touch the seeded built-ins, got %+v", got)
	}
}

func repairMigrationPath(t *testing.T) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join("..", "db", "migrations", "021_*.sql"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected exactly one 021 migration, got %v (err=%v)", matches, err)
	}
	return matches[0]
}

// Follow-up from PR #102's review (ut-docs#16): a shop-created tender
// captured pre-fix isn't auto-repaired by migration 021 (a row alone can't
// distinguish a capture from a genuine plugin method) — a startup warning
// needs a way to find these.
func TestFindOrphanedPaymentMethods(t *testing.T) {
	d := pgOpenDB(t, "orphan.db")
	ctx := context.Background()
	repo := NewPluginRepo(d.DB)

	// Simulates a pre-fix capture: plugin_id set, but no matching plugins
	// row exists on this till (the hijacking plugin was never installed
	// here, or was removed long ago with no repair migration targeting it —
	// migration 021 only restores the three seeded built-ins).
	mustExec(t, d, `INSERT INTO payment_methods (id, name, type, is_active, sort_order, plugin_id)
VALUES ('orphaned', 'Orphaned Tender', 'card', 1, 100, 'com.gone.forever')`)

	// A genuinely live plugin-owned method must NOT be flagged.
	pgPlugin(t, d, "com.live.pay", 1)
	pgEntry(t, d, "pe-live", "com.live.pay", "livepay", "Live Pay")
	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	orphans, err := repo.FindOrphanedPaymentMethods(ctx)
	if err != nil {
		t.Fatalf("FindOrphanedPaymentMethods: %v", err)
	}
	if len(orphans) != 1 {
		t.Fatalf("expected exactly 1 orphan, got %d: %+v", len(orphans), orphans)
	}
	got := orphans[0]
	if got.ID != "orphaned" || got.Name != "Orphaned Tender" || got.PluginID != "com.gone.forever" {
		t.Fatalf("unexpected orphan row: %+v", got)
	}
}

// Legacy state predating install-time name validation: a plugin entry whose
// LABEL collides with an existing tender's name (different id) must not
// abort the whole sync (and therefore plugins.Init at startup) — the
// colliding row simply doesn't materialize, same treatment ADR-0031 already
// gives an id collision.
func TestSyncPluginPaymentMethods_LegacyNameCollisionDoesNotAbortSync(t *testing.T) {
	d := pgOpenDB(t, "namecollision.db")
	ctx := context.Background()
	repo := NewPluginRepo(d.DB)

	pgPlugin(t, d, "com.namecollide.pay", 1)
	pgEntry(t, d, "pe-namecollide", "com.namecollide.pay", "namecollide", "Cash")

	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		t.Fatalf("sync must not abort on a legacy name collision, got: %v", err)
	}
	got, ok := pgMethod(t, d, "cash")
	if !ok || got.Name != "Cash" || got.PluginID != "" {
		t.Fatalf("built-in cash must be unaffected by a colliding plugin name: %+v ok=%v", got, ok)
	}
	if _, ok := pgMethod(t, d, "namecollide"); ok {
		t.Fatal("name-colliding plugin method must not materialize (same as an id collision), but must not abort the sync either")
	}
}

// Found by the independent review of ut-docs#16: the ON CONFLICT(name) DO
// NOTHING clause only protects the INSERT path — it does nothing for the
// ON CONFLICT(id) DO UPDATE SET name = excluded.name branch, which can
// itself raise a UNIQUE(name) violation. A plugin swapping its OWN two
// entries' labels between an install and its next sync reproduces this:
// FindPaymentNameConflicts correctly excludes the plugin's own rows at
// install time (not a conflict — the plugin already owns both labels), but
// the very next sync's rename-in-place hits the still-materialized OTHER
// row still holding the target name and previously hard-aborted every
// startup from then on.
func TestSyncPluginPaymentMethods_OwnLabelSwapDoesNotAbortSync(t *testing.T) {
	d := pgOpenDB(t, "labelswap.db")
	ctx := context.Background()
	repo := NewPluginRepo(d.DB)

	pgPlugin(t, d, "com.swap.pay", 1)
	pgEntry(t, d, "pe-a", "com.swap.pay", "swapa", "Alpha")
	pgEntry(t, d, "pe-b", "com.swap.pay", "swapb", "Beta")
	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	// Swap the labels in place (same plugin, same keys — a legitimate
	// relabel, not a new install).
	mustExec(t, d, `UPDATE plugin_entries SET label = 'Beta' WHERE id = 'pe-a'`)
	mustExec(t, d, `UPDATE plugin_entries SET label = 'Alpha' WHERE id = 'pe-b'`)

	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		t.Fatalf("sync must not abort on the plugin's own label swap, got: %v", err)
	}
	// Every startup after this must ALSO succeed (this is what "aborts
	// forever" means — a one-shot pass isn't proof it's fixed).
	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		t.Fatalf("second sync after the swap must also not abort, got: %v", err)
	}
}

// Found by the independent review of ut-docs#16: the sync's name-collision
// guards ("ON CONFLICT(name) DO NOTHING" and the DO UPDATE's CASE) prevent
// a crash but are otherwise silent — a plugin entry that can't materialize
// or rename because another row already holds its label just vanishes with
// no error and no log line. FindSuppressedPaymentNameEntries lets the
// caller (plugins.Init) surface that as a startup warning instead.
func TestFindSuppressedPaymentNameEntries(t *testing.T) {
	d := pgOpenDB(t, "suppressed.db")
	ctx := context.Background()
	repo := NewPluginRepo(d.DB)

	// A brand-new entry whose label is already taken by the built-in cash
	// tender — can never materialize.
	pgPlugin(t, d, "com.blocked.pay", 1)
	pgEntry(t, d, "pe-blocked", "com.blocked.pay", "blockedkey", "Cash")
	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// A healthy plugin entry that DID materialize must not be reported.
	pgPlugin(t, d, "com.healthy.pay", 1)
	pgEntry(t, d, "pe-healthy", "com.healthy.pay", "healthykey", "Healthy Pay")
	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}

	suppressed, err := repo.FindSuppressedPaymentNameEntries(ctx)
	if err != nil {
		t.Fatalf("FindSuppressedPaymentNameEntries: %v", err)
	}
	if len(suppressed) != 1 {
		t.Fatalf("expected exactly 1 suppressed entry, got %d: %+v", len(suppressed), suppressed)
	}
	got := suppressed[0]
	if got.PluginID != "com.blocked.pay" || got.Key != "blockedkey" || got.Label != "Cash" || got.BlockingID != "cash" {
		t.Fatalf("unexpected suppressed entry: %+v", got)
	}
}

// The label-swap crash-fix's own suppression (finding #2 of the ut-docs#16
// review) must ALSO surface here: a rename that the DO UPDATE guard
// deliberately skipped is exactly the same "can't take this label" case.
func TestFindSuppressedPaymentNameEntries_CoversLabelSwap(t *testing.T) {
	d := pgOpenDB(t, "suppressed-swap.db")
	ctx := context.Background()
	repo := NewPluginRepo(d.DB)

	pgPlugin(t, d, "com.swap.pay", 1)
	pgEntry(t, d, "pe-a", "com.swap.pay", "swapa", "Alpha")
	pgEntry(t, d, "pe-b", "com.swap.pay", "swapb", "Beta")
	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	mustExec(t, d, `UPDATE plugin_entries SET label = 'Beta' WHERE id = 'pe-a'`)
	mustExec(t, d, `UPDATE plugin_entries SET label = 'Alpha' WHERE id = 'pe-b'`)
	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		t.Fatalf("sync after swap: %v", err)
	}

	suppressed, err := repo.FindSuppressedPaymentNameEntries(ctx)
	if err != nil {
		t.Fatalf("FindSuppressedPaymentNameEntries: %v", err)
	}
	if len(suppressed) != 2 {
		t.Fatalf("expected both swapped entries reported as suppressed, got %d: %+v", len(suppressed), suppressed)
	}
}

package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
)

// The five settings keys migration 009 renames (ADR-0081 Decision 1/2):
// old name → the fiscal package's constant for the new name. The old names
// are spelled out as literals on purpose — nothing in the codebase names
// them anymore after the rename, and this table is the test's own record of
// what an upgraded till's settings table may still contain.
var fiscalSigningKeyRenames = map[string]string{
	"fiscal.tse_configured":      fiscal.KeySigningDeviceConfigured,
	"fiscal.tse_failing_since":   fiscal.KeySigningDeviceFailingSince,
	"fiscal.tse_override_until":  fiscal.KeyOverrideUntil,
	"fiscal.tse_override_reason": fiscal.KeyOverrideReason,
	"fiscal.tse_override_actor":  fiscal.KeyOverrideActor,
}

// fiscalSigningRenameMigrationVersion is the ledger version 009 lands under.
const fiscalSigningRenameMigrationVersion = 9

// openAtPreRenameSchema opens a fresh DB (every migration applied), then
// rewinds the ledger to just before migration 009 so the next Open on the
// same file runs 009 through the REAL migration runner — the same path an
// upgraded till takes — rather than a hand-executed copy of its SQL. The
// schema itself is unchanged by 009 (it only rewrites settings rows), so
// dropping the ledger row is an exact stand-in for a pre-009 database.
//
// It rewinds EVERY version at or above 009, not just 009 itself. Deleting
// only 009's row leaves any later migration recorded, so the runner sees a
// missing 009 sitting below a higher applied watermark and correctly refuses
// to boot ("a migration file was renumbered under an already-applied
// version") — which made this test fail the moment a 010 existed, for a
// reason that had nothing to do with the rename it covers. Re-running the
// later migrations is safe: this helper's contract is that they are
// re-appliable, which is why they use IF NOT EXISTS.
func openAtPreRenameSchema(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fiscal-signing-rename.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open (fresh): %v", err)
	}
	var applied int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, fiscalSigningRenameMigrationVersion).Scan(&applied); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if applied != 1 {
		t.Fatalf("migration %d not recorded as applied on a fresh DB — has it been renumbered?", fiscalSigningRenameMigrationVersion)
	}
	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version >= ?`, fiscalSigningRenameMigrationVersion); err != nil {
		t.Fatalf("rewind ledger: %v", err)
	}
	return d, path
}

func seedSettings(t *testing.T, d *DB, rows map[string]string) {
	t.Helper()
	for k, v := range rows {
		if _, err := d.DB.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)`, k, v); err != nil {
			t.Fatalf("seed setting %s: %v", k, err)
		}
	}
}

func settingValue(t *testing.T, d *DB, key string) (string, bool) {
	t.Helper()
	var v string
	err := d.DB.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	if err != nil {
		t.Fatalf("read setting %s: %v", key, err)
	}
	return v, true
}

// TestFiscalSigningKeysRename_PreservesValuesUnderNewNames (ADR-0081
// Decision 2): a till that stored fiscal state under ADR-0048's TSE-named
// keys reads the identical values under the signing-device names after the
// upgrade, and no row survives under an old name.
func TestFiscalSigningKeysRename_PreservesValuesUnderNewNames(t *testing.T) {
	d, path := openAtPreRenameSchema(t)
	seeded := map[string]string{
		"fiscal.tse_configured":      "true",
		"fiscal.tse_failing_since":   "2026-08-14T09:00:00Z",
		"fiscal.tse_override_until":  "2099-01-01T00:00:00Z",
		"fiscal.tse_override_reason": "provider outage, tickets queueing",
		"fiscal.tse_override_actor":  "admin1",
		// Out of scope for the rename (ADR-0081: never TSE-named) — must
		// pass through untouched.
		fiscal.KeySystemOfRecord: "true",
	}
	seedSettings(t, d, seeded)
	if err := d.Close(); err != nil {
		t.Fatalf("close pre-migration DB: %v", err)
	}

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open (upgrade, applies %03d): %v", fiscalSigningRenameMigrationVersion, err)
	}
	defer d.Close()

	for oldKey, newKey := range fiscalSigningKeyRenames {
		got, ok := settingValue(t, d, newKey)
		if !ok {
			t.Errorf("%s: no row under the new key after migration", newKey)
			continue
		}
		if want := seeded[oldKey]; got != want {
			t.Errorf("%s = %q, want the value seeded under %s (%q)", newKey, got, oldKey, want)
		}
		if v, still := settingValue(t, d, oldKey); still {
			t.Errorf("%s still present after migration (value %q) — must be renamed, not copied", oldKey, v)
		}
	}
	if v, ok := settingValue(t, d, fiscal.KeySystemOfRecord); !ok || v != "true" {
		t.Errorf("%s = (%q, %v) after migration, want (\"true\", true) untouched", fiscal.KeySystemOfRecord, v, ok)
	}
}

// TestFiscalSigningKeysRename_GateDecisionSurvivesUpgrade is the ADR-0081
// Decision 2 acceptance test in the gate's own terms: for each posture a
// shop can be in under the OLD key names, the gate — reading through the
// real SettingsRepo, exactly as production does — reaches the same Decision
// after the upgrade that ADR-0048 assigned that posture. The point is the
// gap this migration closes: without it the gate would read only the new
// names, find nothing, and re-block every configured shop as
// BlockedNeverConfigured (asserted first, on the pre-migration file, so the
// migration is proven load-bearing rather than merely harmless).
func TestFiscalSigningKeysRename_GateDecisionSurvivesUpgrade(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		oldRows   map[string]string
		want      fiscal.Decision
		wantAudit bool // AllowedWithOverride carries reason/actor
	}{
		{
			name:    "configured and healthy stays Allowed",
			oldRows: map[string]string{"fiscal.tse_configured": "true"},
			want:    fiscal.Allowed,
		},
		{
			name: "configured-but-failing without override stays blocked (override path intact)",
			oldRows: map[string]string{
				"fiscal.tse_configured":    "true",
				"fiscal.tse_failing_since": "2026-08-14T09:00:00Z",
			},
			want: fiscal.BlockedTSEFailing,
		},
		{
			name: "active owner override stays AllowedWithOverride and keeps its audit fields",
			oldRows: map[string]string{
				"fiscal.tse_configured":      "true",
				"fiscal.tse_failing_since":   "2026-08-14T09:00:00Z",
				"fiscal.tse_override_until":  now.Add(30 * time.Minute).Format(time.RFC3339),
				"fiscal.tse_override_reason": "provider outage, tickets queueing",
				"fiscal.tse_override_actor":  "admin1",
			},
			want:      fiscal.AllowedWithOverride,
			wantAudit: true,
		},
		{
			name:    "never configured stays BlockedNeverConfigured",
			oldRows: map[string]string{},
			want:    fiscal.BlockedNeverConfigured,
		},
	}
	for _, c := range cases {
		for _, country := range []string{"DE", "TR"} {
			t.Run(c.name+"/"+country, func(t *testing.T) {
				ctx := context.Background()
				d, path := openAtPreRenameSchema(t)
				rows := map[string]string{fiscal.KeySystemOfRecord: "true"}
				for k, v := range c.oldRows {
					rows[k] = v
				}
				seedSettings(t, d, rows)

				// Pre-migration, on a configured shop, the gate can only see
				// the new names and so must NOT already reach the wanted
				// verdict — otherwise this test would pass without 009.
				if _, configured := c.oldRows["fiscal.tse_configured"]; configured {
					g, err := fiscal.EvaluateGate(ctx, data.NewSettingsRepo(d.DB), country, now)
					if err != nil {
						t.Fatalf("EvaluateGate (pre-migration): %v", err)
					}
					if g.Decision != fiscal.BlockedNeverConfigured {
						t.Fatalf("pre-migration decision = %v, want BlockedNeverConfigured (old-named rows must be invisible to the renamed gate until 009 runs)", g.Decision)
					}
				}
				if err := d.Close(); err != nil {
					t.Fatalf("close pre-migration DB: %v", err)
				}

				d, err := Open(path)
				if err != nil {
					t.Fatalf("Open (upgrade): %v", err)
				}
				defer d.Close()

				g, err := fiscal.EvaluateGate(ctx, data.NewSettingsRepo(d.DB), country, now)
				if err != nil {
					t.Fatalf("EvaluateGate (post-migration): %v", err)
				}
				if g.Decision != c.want {
					t.Fatalf("post-migration decision = %v, want %v", g.Decision, c.want)
				}
				if c.wantAudit {
					if g.OverrideReason != "provider outage, tickets queueing" || g.OverrideActor != "admin1" {
						t.Fatalf("override audit fields not carried across the rename: %+v", g)
					}
					if !g.OverrideUntil.Equal(now.Add(30 * time.Minute)) {
						t.Fatalf("OverrideUntil = %v, want %v", g.OverrideUntil, now.Add(30*time.Minute))
					}
				}
			})
		}
	}
}

// TestFiscalSigningKeysRename_FreshInstallAndRerunAreNoOps: a fresh install
// has neither name (every UPDATE matches zero rows), and a till that already
// carries the new names must not be disturbed by the statements — 009 is
// plain idempotent row renames, safe under settings.key's PRIMARY KEY
// (at most one row can ever match each WHERE).
func TestFiscalSigningKeysRename_FreshInstallAndRerunAreNoOps(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM settings WHERE key LIKE 'fiscal.%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("fresh install has %d fiscal.* settings rows, want 0 (009 must not seed anything)", n)
	}

	// Re-running 009's statements against a DB already on the new names
	// (the idempotency the migration header promises) changes nothing.
	seedSettings(t, d, map[string]string{
		fiscal.KeySigningDeviceConfigured: "true",
		fiscal.KeyOverrideReason:          "already migrated",
	})
	migs, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var m *migration
	for i := range migs {
		if migs[i].Version == fiscalSigningRenameMigrationVersion {
			m = &migs[i]
		}
	}
	if m == nil {
		t.Fatalf("migration %d not found in the embedded set", fiscalSigningRenameMigrationVersion)
	}
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := execMigrationStatements(tx, *m); err != nil {
		t.Fatalf("re-running %s: %v", m.Name, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if v, ok := settingValue(t, d, fiscal.KeySigningDeviceConfigured); !ok || v != "true" {
		t.Fatalf("%s = (%q, %v) after re-run, want (\"true\", true)", fiscal.KeySigningDeviceConfigured, v, ok)
	}
	if v, ok := settingValue(t, d, fiscal.KeyOverrideReason); !ok || v != "already migrated" {
		t.Fatalf("%s = (%q, %v) after re-run, want (\"already migrated\", true)", fiscal.KeyOverrideReason, v, ok)
	}
}

// TestFiscalSigningKeysRename_MultiTillSkewDoesNotBlockBoot pins the
// multi-till upgrade path the bare in-place UPDATE could not survive
// (independent review, 2026-09-07). None of these keys is in
// data.PerTillSettingPrefixes, so they are shop-wide and replicate; and
// sync_admin_repo.go deliberately never prunes settings rows. A joined till
// that syncs from an already-upgraded primary therefore ends up holding BOTH
// the new-named row (upserted from the bundle) and its own old-named row —
// and then upgrades. A plain "UPDATE settings SET key = <new> WHERE key =
// <old>" hits "UNIQUE constraint failed: settings.key" there; because each
// migration runs inside a transaction, the ledger row is never written and
// every later boot fails identically, so the till cannot sell at all until
// someone edits the database by hand.
//
// Asserted here: the upgrade completes, the shop-wide value the primary
// synced is what survives, and no fiscal.tse_* row is left behind.
func TestFiscalSigningKeysRename_MultiTillSkewDoesNotBlockBoot(t *testing.T) {
	ctx := context.Background()
	d, path := openAtPreRenameSchema(t)
	rows := map[string]string{fiscal.KeySystemOfRecord: "true"}
	for oldKey, newKey := range fiscalSigningKeyRenames {
		// The till's own pre-upgrade row...
		rows[oldKey] = "this-till-stale"
		// ...alongside the row the upgraded primary already synced onto it.
		rows[newKey] = "from-primary"
	}
	// Values the gate can actually read back, so the post-upgrade decision
	// below is meaningful rather than a parse failure.
	rows[fiscal.KeySigningDeviceConfigured] = "true"
	rows["fiscal.tse_configured"] = "true"
	rows[fiscal.KeySigningDeviceFailingSince] = ""
	rows["fiscal.tse_failing_since"] = "2026-08-14T09:00:00Z"
	seedSettings(t, d, rows)
	if err := d.Close(); err != nil {
		t.Fatalf("close pre-migration DB: %v", err)
	}

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open (upgrade of a synced joined till) must not fail: %v", err)
	}
	defer d.Close()

	for oldKey, newKey := range fiscalSigningKeyRenames {
		if v, still := settingValue(t, d, oldKey); still {
			t.Errorf("%s still present after migration (value %q) — the stale old-named row must be dropped", oldKey, v)
		}
		v, ok := settingValue(t, d, newKey)
		if !ok {
			t.Errorf("%s: no row under the new key after migration", newKey)
			continue
		}
		if v == "this-till-stale" {
			t.Errorf("%s = %q — the value synced from the primary must win the tie, not this till's pre-upgrade copy", newKey, v)
		}
	}

	// The gate still reads a coherent posture afterwards: configured, and
	// healthy per the primary's synced (empty) failing-since.
	g, err := fiscal.EvaluateGate(ctx, data.NewSettingsRepo(d.DB), "DE", time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("EvaluateGate after skewed upgrade: %v", err)
	}
	if g.Decision != fiscal.Allowed {
		t.Fatalf("post-upgrade decision = %v, want Allowed (configured, primary reports healthy)", g.Decision)
	}
}

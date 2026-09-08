package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
)

// The two flat, country-agnostic posture keys migration 011 splits per
// country (ADR-0083, ut-docs#1767). They are 009's targets and 011's
// sources. Spelled out as literals on purpose, the way 009's test spells
// out the fiscal.tse_* names: nothing in the codebase names them anymore —
// every reader and writer goes through fiscal.SigningDeviceConfiguredKey /
// SigningDeviceFailingSinceKey — so this is the test's own record of what
// an upgraded till's settings table may still contain.
const (
	flatSigningDeviceConfiguredKey   = "fiscal.signing_device_configured"
	flatSigningDeviceFailingSinceKey = "fiscal.signing_device_failing_since"
)

// fiscalSigningSplitMigrationVersion is the ledger version 011 lands under.
const fiscalSigningSplitMigrationVersion = 11

// openAtPreSplitSchema is openAtPreMigrationSchema at 011.
func openAtPreSplitSchema(t *testing.T) (*DB, string) {
	t.Helper()
	return openAtPreMigrationSchema(t, fiscalSigningSplitMigrationVersion, "fiscal-signing-split.db")
}

// postSplitKey is where a row stored under flat lands after 011 on a till
// whose store.country is country: the per-country row for the two posture
// keys, the very same key for everything else (fiscal.system_of_record and
// the override keys stay global — ADR-0083 Decision 2).
func postSplitKey(flat, country string) string {
	switch flat {
	case flatSigningDeviceConfiguredKey:
		return fiscal.SigningDeviceConfiguredKey(country)
	case flatSigningDeviceFailingSinceKey:
		return fiscal.SigningDeviceFailingSinceKey(country)
	}
	return flat
}

// upsertSetting seeds one row whether or not the baseline already created
// it (store.country may be seeded by 001_init; the fiscal keys never are).
func upsertSetting(t *testing.T, d *DB, key, value string) {
	t.Helper()
	if _, err := d.DB.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
		t.Fatalf("seed setting %s: %v", key, err)
	}
}

// TestFiscalSigningKeysSplit_MovesFlatRowsOntoDeclaredCountry (ADR-0083
// Decision 6): a till that stored its posture under the flat names reads
// the identical values under its declared country's own rows after the
// upgrade, no flat row survives, the OTHER gated market gets no row at all,
// and the keys ADR-0083 keeps global pass through untouched.
func TestFiscalSigningKeysSplit_MovesFlatRowsOntoDeclaredCountry(t *testing.T) {
	for _, tc := range []struct{ country, other string }{
		{"TR", "DE"},
		{"DE", "TR"},
	} {
		t.Run(tc.country, func(t *testing.T) {
			d, path := openAtPreSplitSchema(t)
			seeded := map[string]string{
				flatSigningDeviceConfiguredKey:   "true",
				flatSigningDeviceFailingSinceKey: "2026-08-14T09:00:00Z",
				fiscal.KeySystemOfRecord:         "true",
				fiscal.KeyOverrideReason:         "stays global (ADR-0083 Decision 2)",
			}
			for k, v := range seeded {
				upsertSetting(t, d, k, v)
			}
			upsertSetting(t, d, "store.country", tc.country)
			if err := d.Close(); err != nil {
				t.Fatalf("close pre-migration DB: %v", err)
			}

			d, err := Open(path)
			if err != nil {
				t.Fatalf("Open (upgrade, applies %03d): %v", fiscalSigningSplitMigrationVersion, err)
			}
			defer d.Close()

			for flat, want := range seeded {
				target := postSplitKey(flat, tc.country)
				got, ok := settingValue(t, d, target)
				if !ok {
					t.Errorf("%s: no row after migration", target)
					continue
				}
				if got != want {
					t.Errorf("%s = %q, want the value seeded under %s (%q)", target, got, flat, want)
				}
				if target != flat {
					if v, still := settingValue(t, d, flat); still {
						t.Errorf("%s still present after migration (value %q) — must be renamed, not copied", flat, v)
					}
				}
			}
			for _, k := range []string{fiscal.SigningDeviceConfiguredKey(tc.other), fiscal.SigningDeviceFailingSinceKey(tc.other)} {
				if v, exists := settingValue(t, d, k); exists {
					t.Errorf("%s exists after migration (value %q) — the other market must get no row, the split is per declared country", k, v)
				}
			}
		})
	}
}

// TestFiscalSigningKeysSplit_GateDecisionSurvivesUpgrade is the ADR-0083
// acceptance test in the gate's own terms, mirroring 009's: for each posture
// a shop can be in under the FLAT names, the gate for its declared country
// — reading through the real SettingsRepo, exactly as production does —
// reaches the same Decision after the upgrade that ADR-0048 assigned it.
// Asserted first on the pre-migration file: a configured shop must read
// BlockedNeverConfigured there, because the per-country gate cannot see a
// flat row — so the migration is proven load-bearing, not merely harmless.
// And after the upgrade the OTHER gated market must still be
// BlockedNeverConfigured: the migration lands the posture on one row, and
// the independence ADR-0083 exists for holds from the first boot.
func TestFiscalSigningKeysSplit_GateDecisionSurvivesUpgrade(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		flatRows  map[string]string
		want      fiscal.Decision
		wantAudit bool
	}{
		{
			name:     "configured and healthy stays Allowed",
			flatRows: map[string]string{flatSigningDeviceConfiguredKey: "true"},
			want:     fiscal.Allowed,
		},
		{
			name: "configured-but-failing without override stays blocked (override path intact)",
			flatRows: map[string]string{
				flatSigningDeviceConfiguredKey:   "true",
				flatSigningDeviceFailingSinceKey: "2026-08-14T09:00:00Z",
			},
			want: fiscal.BlockedTSEFailing,
		},
		{
			name: "active owner override stays AllowedWithOverride and keeps its (global) audit fields",
			flatRows: map[string]string{
				flatSigningDeviceConfiguredKey:   "true",
				flatSigningDeviceFailingSinceKey: "2026-08-14T09:00:00Z",
				fiscal.KeyOverrideUntil:          now.Add(30 * time.Minute).Format(time.RFC3339),
				fiscal.KeyOverrideReason:         "provider outage, tickets queueing",
				fiscal.KeyOverrideActor:          "admin1",
			},
			want:      fiscal.AllowedWithOverride,
			wantAudit: true,
		},
		{
			name:     "never configured stays BlockedNeverConfigured",
			flatRows: map[string]string{},
			want:     fiscal.BlockedNeverConfigured,
		},
	}
	for _, c := range cases {
		for _, tc := range []struct{ country, other string }{{"DE", "TR"}, {"TR", "DE"}} {
			t.Run(c.name+"/"+tc.country, func(t *testing.T) {
				ctx := context.Background()
				d, path := openAtPreSplitSchema(t)
				upsertSetting(t, d, fiscal.KeySystemOfRecord, "true")
				upsertSetting(t, d, "store.country", tc.country)
				for k, v := range c.flatRows {
					upsertSetting(t, d, k, v)
				}

				if _, configured := c.flatRows[flatSigningDeviceConfiguredKey]; configured {
					g, err := fiscal.EvaluateGate(ctx, data.NewSettingsRepo(d.DB), tc.country, now)
					if err != nil {
						t.Fatalf("EvaluateGate (pre-migration): %v", err)
					}
					if g.Decision != fiscal.BlockedNeverConfigured {
						t.Fatalf("pre-migration decision = %v, want BlockedNeverConfigured (a flat row must be invisible to the per-country gate until 011 runs)", g.Decision)
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

				g, err := fiscal.EvaluateGate(ctx, data.NewSettingsRepo(d.DB), tc.country, now)
				if err != nil {
					t.Fatalf("EvaluateGate (post-migration): %v", err)
				}
				if g.Decision != c.want {
					t.Fatalf("post-migration decision for %s = %v, want %v", tc.country, g.Decision, c.want)
				}
				if c.wantAudit {
					if g.OverrideReason != "provider outage, tickets queueing" || g.OverrideActor != "admin1" {
						t.Fatalf("override audit fields not carried across the split: %+v", g)
					}
					if !g.OverrideUntil.Equal(now.Add(30 * time.Minute)) {
						t.Fatalf("OverrideUntil = %v, want %v", g.OverrideUntil, now.Add(30*time.Minute))
					}
				}
				other, err := fiscal.EvaluateGate(ctx, data.NewSettingsRepo(d.DB), tc.other, now)
				if err != nil {
					t.Fatalf("EvaluateGate (%s, post-migration): %v", tc.other, err)
				}
				if other.Decision != fiscal.BlockedNeverConfigured {
					t.Fatalf("post-migration decision for %s = %v, want BlockedNeverConfigured — the upgrade must not hand %s's posture to %s", tc.other, other.Decision, tc.country, tc.other)
				}
			})
		}
	}
}

// The target row is computed with the same normalisation the Go key
// functions apply (lower-case, trimmed): a shop carrying " tr " in
// store.country — /api/settings/upsert stores it as free text — lands on the
// row fiscal.SigningDeviceConfiguredKey(" tr ") names.
func TestFiscalSigningKeysSplit_NormalisesCountry(t *testing.T) {
	d, path := openAtPreSplitSchema(t)
	upsertSetting(t, d, flatSigningDeviceConfiguredKey, "true")
	upsertSetting(t, d, "store.country", " tr ")
	if err := d.Close(); err != nil {
		t.Fatalf("close pre-migration DB: %v", err)
	}
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open (upgrade): %v", err)
	}
	defer d.Close()
	want := fiscal.SigningDeviceConfiguredKey(" tr ")
	if want != "fiscal.signing_device_configured.tr" {
		t.Fatalf("test premise: key function normalises to .tr, got %q", want)
	}
	if v, ok := settingValue(t, d, want); !ok || v != "true" {
		t.Fatalf("%s = (%q, %v), want (\"true\", true)", want, v, ok)
	}
	if _, still := settingValue(t, d, flatSigningDeviceConfiguredKey); still {
		t.Fatal("flat row must be gone after the split")
	}
}

// A till holding a flat posture row but no usable store.country is not a
// reachable state (the gate needs a declared country before either key can
// be set true), so 011 deliberately leaves the flat row alone rather than
// guess — and, load-bearing for correctness, must not write a NULL key:
// settings.key is a plain TEXT PRIMARY KEY, which SQLite lets be NULL, and
// an unguarded `'prefix' || (SELECT ...)` against a missing row is NULL.
func TestFiscalSigningKeysSplit_NoDeclaredCountryLeavesFlatRowAlone(t *testing.T) {
	for _, tc := range []struct {
		name    string
		country *string
	}{
		{"no store.country row", nil},
		{"blank store.country", ptr("   ")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, path := openAtPreSplitSchema(t)
			upsertSetting(t, d, flatSigningDeviceConfiguredKey, "true")
			upsertSetting(t, d, flatSigningDeviceFailingSinceKey, "2026-08-14T09:00:00Z")
			if tc.country == nil {
				if _, err := d.DB.Exec(`DELETE FROM settings WHERE key = 'store.country'`); err != nil {
					t.Fatal(err)
				}
			} else {
				upsertSetting(t, d, "store.country", *tc.country)
			}
			if err := d.Close(); err != nil {
				t.Fatalf("close pre-migration DB: %v", err)
			}
			d, err := Open(path)
			if err != nil {
				t.Fatalf("Open (upgrade) must not fail: %v", err)
			}
			defer d.Close()

			if v, ok := settingValue(t, d, flatSigningDeviceConfiguredKey); !ok || v != "true" {
				t.Fatalf("%s = (%q, %v), want the flat row left untouched", flatSigningDeviceConfiguredKey, v, ok)
			}
			if v, ok := settingValue(t, d, flatSigningDeviceFailingSinceKey); !ok || v != "2026-08-14T09:00:00Z" {
				t.Fatalf("%s = (%q, %v), want the flat row left untouched", flatSigningDeviceFailingSinceKey, v, ok)
			}
			var n int
			if err := d.DB.QueryRow(`SELECT COUNT(*) FROM settings WHERE key IS NULL OR key LIKE 'fiscal.signing_device_%.'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatalf("found %d NULL-keyed or empty-suffix rows — the migration must never compute a target from a missing country", n)
			}
		})
	}
}

func ptr(s string) *string { return &s }

// TestFiscalSigningKeysSplit_MultiTillSkewDoesNotBlockBoot pins the same
// staggered-upgrade path 009's test pins (these are shop-wide settings, and
// a replica never prunes them): a joined till that synced from an
// already-upgraded primary holds BOTH the per-country row (upserted from
// the bundle) and its own flat row — and then upgrades. A bare UPDATE would
// hit "UNIQUE constraint failed: settings.key" inside the migration's
// transaction and the till could never boot again. Asserted: the upgrade
// completes, the primary's synced value wins, no flat row is left behind,
// and the gate reads a coherent posture afterwards.
func TestFiscalSigningKeysSplit_MultiTillSkewDoesNotBlockBoot(t *testing.T) {
	ctx := context.Background()
	d, path := openAtPreSplitSchema(t)
	upsertSetting(t, d, fiscal.KeySystemOfRecord, "true")
	upsertSetting(t, d, "store.country", "DE")
	// This till's own pre-upgrade rows: configured, but failing...
	upsertSetting(t, d, flatSigningDeviceConfiguredKey, "true")
	upsertSetting(t, d, flatSigningDeviceFailingSinceKey, "2026-08-14T09:00:00Z")
	// ...alongside what the upgraded primary synced: configured and healthy.
	upsertSetting(t, d, fiscal.SigningDeviceConfiguredKey("DE"), "true")
	upsertSetting(t, d, fiscal.SigningDeviceFailingSinceKey("DE"), "")
	if err := d.Close(); err != nil {
		t.Fatalf("close pre-migration DB: %v", err)
	}

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open (upgrade of a synced joined till) must not fail: %v", err)
	}
	defer d.Close()

	for _, flat := range []string{flatSigningDeviceConfiguredKey, flatSigningDeviceFailingSinceKey} {
		if v, still := settingValue(t, d, flat); still {
			t.Errorf("%s still present after migration (value %q) — the stale flat row must be dropped", flat, v)
		}
	}
	if v, ok := settingValue(t, d, fiscal.SigningDeviceFailingSinceKey("DE")); !ok || v != "" {
		t.Fatalf("%s = (%q, %v) — the primary's synced (healthy) value must win the tie, not this till's stale failing-since", fiscal.SigningDeviceFailingSinceKey("DE"), v, ok)
	}
	g, err := fiscal.EvaluateGate(ctx, data.NewSettingsRepo(d.DB), "DE", time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("EvaluateGate after skewed upgrade: %v", err)
	}
	if g.Decision != fiscal.Allowed {
		t.Fatalf("post-upgrade decision = %v, want Allowed (configured, primary reports healthy)", g.Decision)
	}
}

// TestFiscalSigningKeysSplit_FreshInstallAndRerunAreNoOps: a fresh install
// has no posture row of either shape (every statement matches nothing), and
// re-running 011's statements against a till already on per-country rows
// changes nothing and resurrects no flat row.
func TestFiscalSigningKeysSplit_FreshInstallAndRerunAreNoOps(t *testing.T) {
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
		t.Fatalf("fresh install has %d fiscal.* settings rows, want 0 (011 must not seed anything)", n)
	}

	upsertSetting(t, d, "store.country", "DE")
	upsertSetting(t, d, fiscal.SigningDeviceConfiguredKey("DE"), "true")
	upsertSetting(t, d, fiscal.SigningDeviceConfiguredKey("TR"), "false")
	migs, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var m *migration
	for i := range migs {
		if migs[i].Version == fiscalSigningSplitMigrationVersion {
			m = &migs[i]
		}
	}
	if m == nil {
		t.Fatalf("migration %d not found in the embedded set", fiscalSigningSplitMigrationVersion)
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
	if v, ok := settingValue(t, d, fiscal.SigningDeviceConfiguredKey("DE")); !ok || v != "true" {
		t.Fatalf("%s = (%q, %v) after re-run, want (\"true\", true)", fiscal.SigningDeviceConfiguredKey("DE"), v, ok)
	}
	if v, ok := settingValue(t, d, fiscal.SigningDeviceConfiguredKey("TR")); !ok || v != "false" {
		t.Fatalf("%s = (%q, %v) after re-run, want (\"false\", true) — another market's row is never touched", fiscal.SigningDeviceConfiguredKey("TR"), v, ok)
	}
	if _, flat := settingValue(t, d, flatSigningDeviceConfiguredKey); flat {
		t.Fatal("a re-run must not resurrect a flat row")
	}
}

package data

import (
	"context"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/entitlement"
)

// ut-docs#2821, ADR-0117 §2: cloud.link_tier/cloud.link_mode are written by
// the same cacheEntitlement call as entitlement.* (same transaction, same
// cadence — every valid sync-response block, ut-docs#2792's reasoning
// applies verbatim). Admin-syncing them shop-wide would move the main
// till's admin fingerprint on every cloud tick even when the tier never
// changes, so they must be excluded from admin sync exactly like
// entitlement.* is.
func TestCloudLinkSettingsArePerTill(t *testing.T) {
	for _, k := range []string{entitlement.KeyCloudLinkTier, entitlement.KeyCloudLinkMode} {
		if !perTillSetting(k) {
			t.Errorf("%s is admin-synced: every cloud_link refresh on the main till would move the admin cursor", k)
		}
	}
}

// The card's guard, mirroring TestEntitlementRefreshLeavesAdminCursorUnchanged
// (entitlement_per_till_2792_test.go): repeating the same block leaves the
// admin cursor unchanged even though cloud.link_tier/mode are rewritten.
func TestCloudLinkRefreshLeavesAdminCursorUnchanged(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "cloud_link_admin_cursor.db")
	repo := NewSyncAdminRepo(d.DB)
	settings := NewSettingsRepo(d.DB)
	block := entitlement.Block{Plan: "pro", SubscriptionStatus: "active", CloudLink: "realtime", CloudLinkMode: "always"}

	refresh := func(now time.Time) {
		t.Helper()
		kv, err := block.Values(now)
		if err != nil {
			t.Fatal(err)
		}
		if err := settings.SetMany(ctx, kv); err != nil {
			t.Fatal(err)
		}
	}
	refresh(time.Date(2026, 9, 26, 14, 0, 0, 0, time.UTC))
	before, err := repo.AdminFingerprint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	refresh(time.Date(2026, 9, 26, 14, 2, 0, 0, time.UTC))
	after, err := repo.AdminFingerprint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("an unchanged cloud_link refresh moved the admin cursor: %s -> %s", before, after)
	}
}

// Both ends enforce it, mirroring TestEntitlementNeverAppliedFromAdminBundle:
// a pre-fix main till still sending these rows must never overwrite a
// replica's own cloud_link cache.
func TestCloudLinkNeverAppliedFromAdminBundle(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "cloud_link_primary.db")
	replica := openMigratedDB(t, "cloud_link_replica.db")
	mustExec(t, primary, `INSERT OR REPLACE INTO settings (key, value) VALUES ('cloud.link_tier', 'realtime'), ('cloud.link_mode', 'always'), ('store.currency', 'EUR')`)
	mustExec(t, replica, `INSERT INTO settings (key, value) VALUES ('cloud.link_tier', 'periodic')`)

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	for _, rec := range bundle.Tables["settings"] {
		if rec["key"] == entitlement.KeyCloudLinkTier || rec["key"] == entitlement.KeyCloudLinkMode {
			t.Fatal("the main till's cloud_link cache leaked into the admin dump")
		}
	}
	legacy := wireTrip(t, bundle)
	legacy.Tables["settings"] = append(legacy.Tables["settings"], map[string]any{"key": entitlement.KeyCloudLinkTier, "value": "realtime", "updated_at": "2026-09-26T14:00:00Z"})
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, legacy); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var v string
	if err := replica.QueryRow(`SELECT value FROM settings WHERE key = 'cloud.link_tier'`).Scan(&v); err != nil || v != "periodic" {
		t.Fatalf("replica's own cloud_link cache overwritten by an admin pull: got %q, want periodic (err=%v)", v, err)
	}
	if err := replica.QueryRow(`SELECT value FROM settings WHERE key = 'store.currency'`).Scan(&v); err != nil || v != "EUR" {
		t.Fatalf("shop-wide settings must still sync: store.currency = %q err=%v", v, err)
	}
}

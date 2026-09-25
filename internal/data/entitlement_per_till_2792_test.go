package data

import (
	"context"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/entitlement"
)

// ut-docs#2792: entitlement.* is this till's own cache of its cloud
// relationship (ADR-0060 §4), refreshed on every cloud sync tick with a new
// last_confirmed_at. Synced shop-wide, that one timestamp moved the main
// till's admin fingerprint every tick, so every replica did a full admin
// re-pull + plugin check every 1.5–2 min. Each till syncs with the cloud
// itself and caches its own copy.
func TestEntitlementSettingsArePerTill(t *testing.T) {
	for _, k := range []string{
		entitlement.KeyPlan,
		entitlement.KeySubscriptionStatus,
		entitlement.KeyExpiresAt,
		entitlement.KeyLastConfirmedAt,
	} {
		if !perTillSetting(k) {
			t.Errorf("%s is admin-synced: every entitlement refresh on the main till would move the admin cursor", k)
		}
	}
}

// The card's guard: the entitlement check running again with an unchanged
// result (only last_confirmed_at moves) leaves the admin cursor — the
// fingerprint a replica's ?have= and the link hello carry — unchanged.
func TestEntitlementRefreshLeavesAdminCursorUnchanged(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "entitlement.db")
	repo := NewSyncAdminRepo(d.DB)
	settings := NewSettingsRepo(d.DB)
	block := entitlement.Block{Plan: "shop", SubscriptionStatus: "active"}

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
	refresh(time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC))
	before, err := repo.AdminFingerprint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	refresh(time.Date(2026, 9, 25, 14, 2, 0, 0, time.UTC))
	after, err := repo.AdminFingerprint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("an unchanged entitlement refresh moved the admin cursor: %s -> %s", before, after)
	}
}

// Both ends enforce it: a pre-fix main till still sends its entitlement
// rows, and the replica must keep its own cache.
func TestEntitlementNeverAppliedFromAdminBundle(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")
	mustExec(t, primary, `INSERT OR REPLACE INTO settings (key, value) VALUES ('entitlement.plan', 'chain'), ('store.currency', 'EUR')`)
	mustExec(t, replica, `INSERT INTO settings (key, value) VALUES ('entitlement.plan', 'shop')`)

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	for _, rec := range bundle.Tables["settings"] {
		if rec["key"] == entitlement.KeyPlan {
			t.Fatal("the main till's entitlement leaked into the admin dump")
		}
	}
	legacy := wireTrip(t, bundle)
	legacy.Tables["settings"] = append(legacy.Tables["settings"], map[string]any{"key": entitlement.KeyPlan, "value": "chain", "updated_at": "2026-09-25T14:00:00Z"})
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, legacy); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var v string
	if err := replica.QueryRow(`SELECT value FROM settings WHERE key = 'entitlement.plan'`).Scan(&v); err != nil || v != "shop" {
		t.Fatalf("replica's own entitlement overwritten by an admin pull: got %q, want shop (err=%v)", v, err)
	}
	if err := replica.QueryRow(`SELECT value FROM settings WHERE key = 'store.currency'`).Scan(&v); err != nil || v != "EUR" {
		t.Fatalf("shop-wide settings must still sync: store.currency = %q err=%v", v, err)
	}
}

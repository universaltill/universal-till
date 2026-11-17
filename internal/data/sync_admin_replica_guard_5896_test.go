package data

import (
	"context"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// ut-docs#5896: an admin pull that was in flight when the till was promoted
// must not write the old main's shop-wide lan_discovery.till_id / till.name
// back. ApplyAdminWithResult refuses (ErrNotReplica) when the till has no
// sync.primary_url, checked inside its own transaction.

// guardBundle returns a wire-tripped bundle from an "old main" carrying an
// item and the shop-wide identity settings, plus a target DB holding its own
// values (not yet marked as a replica).
func guardBundle(t *testing.T) (AdminBundle, func() *syncGuardTarget) {
	t.Helper()
	ctx := context.Background()
	primary := openMigratedDB(t, "guard-primary.db")
	mustExec(t, primary, `INSERT INTO items (id, sku, name, base_price) VALUES ('itm-old', 'OLD', 'Old Item', 100)`)
	mustExec(t, primary, `INSERT OR REPLACE INTO settings (key, value) VALUES (?, 'OLD-ID')`, LANDiscoveryTillIDSettingsKey)
	mustExec(t, primary, `INSERT OR REPLACE INTO settings (key, value) VALUES ('till.name', 'Old Main')`)
	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	bundle = wireTrip(t, bundle)
	var haveID, haveName bool
	for _, rec := range bundle.Tables["settings"] {
		switch rec["key"] {
		case LANDiscoveryTillIDSettingsKey:
			haveID = true
		case "till.name":
			haveName = true
		}
	}
	if !haveID || !haveName {
		t.Fatalf("bundle must carry both shop-wide keys (id=%v name=%v)", haveID, haveName)
	}
	return bundle, func() *syncGuardTarget {
		d := openMigratedDB(t, "guard-target.db")
		mustExec(t, d, `INSERT OR REPLACE INTO settings (key, value) VALUES (?, 'NEW-ID')`, LANDiscoveryTillIDSettingsKey)
		mustExec(t, d, `INSERT OR REPLACE INTO settings (key, value) VALUES ('till.name', 'Promoted')`)
		return &syncGuardTarget{d: d}
	}
}

type syncGuardTarget struct{ d *db.DB }

func (g *syncGuardTarget) setting(t *testing.T, key string) string {
	t.Helper()
	var v string
	if err := g.d.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	return v
}

func (g *syncGuardTarget) itemCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := g.d.QueryRow(`SELECT COUNT(*) FROM items WHERE id = 'itm-old'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func assertGuardRefused(t *testing.T, g *syncGuardTarget, err error) {
	t.Helper()
	if !errors.Is(err, ErrNotReplica) {
		t.Fatalf("apply on a till without sync.primary_url: err = %v, want ErrNotReplica", err)
	}
	if v := g.setting(t, LANDiscoveryTillIDSettingsKey); v != "NEW-ID" {
		t.Fatalf("lan_discovery.till_id = %q, want NEW-ID (old main's id written back)", v)
	}
	if v := g.setting(t, "till.name"); v != "Promoted" {
		t.Fatalf("till.name = %q, want Promoted (old main's name written back)", v)
	}
	if n := g.itemCount(t); n != 0 {
		t.Fatalf("refused apply wrote %d item rows", n)
	}
}

func TestApplyAdmin_RefusesWithoutPrimaryURL_5896(t *testing.T) {
	bundle, newTarget := guardBundle(t)
	target := newTarget()
	_, err := NewSyncAdminRepo(target.d.DB).ApplyAdminWithResult(context.Background(), bundle)
	assertGuardRefused(t, target, err)
}

func TestApplyAdmin_RefusesWithBlankPrimaryURL_5896(t *testing.T) {
	bundle, newTarget := guardBundle(t)
	target := newTarget()
	mustExec(t, target.d, `INSERT INTO settings (key, value) VALUES ('sync.primary_url', '   ')`)
	_, err := NewSyncAdminRepo(target.d.DB).ApplyAdminWithResult(context.Background(), bundle)
	assertGuardRefused(t, target, err)
}

func TestApplyAdmin_AppliesWithPrimaryURL_5896(t *testing.T) {
	bundle, newTarget := guardBundle(t)
	target := newTarget()
	markReplica(t, target.d)
	if _, err := NewSyncAdminRepo(target.d.DB).ApplyAdminWithResult(context.Background(), bundle); err != nil {
		t.Fatalf("apply on a replica: %v", err)
	}
	if v := target.setting(t, LANDiscoveryTillIDSettingsKey); v != "OLD-ID" {
		t.Fatalf("lan_discovery.till_id = %q, want OLD-ID", v)
	}
	if v := target.setting(t, "till.name"); v != "Old Main" {
		t.Fatalf("till.name = %q, want Old Main", v)
	}
	if n := target.itemCount(t); n != 1 {
		t.Fatalf("item rows = %d, want 1", n)
	}
}

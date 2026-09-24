package cloudsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/entitlement"
)

// ADR-0060 §3/§4 (ut-docs#2547): the sync response's optional
// data.entitlement block is cached into the four entitlement.* settings.

var entitlementKeys = []string{
	entitlement.KeyPlan,
	entitlement.KeySubscriptionStatus,
	entitlement.KeyExpiresAt,
	entitlement.KeyLastConfirmedAt,
}

func readEntitlementCache(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	repo := data.NewSettingsRepo(db)
	out := map[string]string{}
	for _, k := range entitlementKeys {
		v, ok, err := repo.Get(context.Background(), k)
		if err != nil {
			t.Fatalf("get %s: %v", k, err)
		}
		if ok {
			out[k] = v
		}
	}
	return out
}

// seededCache is a previously-confirmed cache a bad or absent block must
// leave untouched.
var seededCache = map[string]string{
	entitlement.KeyPlan:               "pro",
	entitlement.KeySubscriptionStatus: "active",
	entitlement.KeyExpiresAt:          "2026-12-01T00:00:00Z",
	entitlement.KeyLastConfirmedAt:    "2026-09-20T08:00:00Z",
}

func seedEntitlementCache(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := data.NewSettingsRepo(db).SetMany(context.Background(), seededCache); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
}

// runTickWith drives one real Tick against a fake cloud serving the given
// entitlement block (nil = omitted) plus one directive, and returns the
// directive results the cloud received — so every case also proves the
// entitlement handling never costs the tick its directive handling.
func runTickWith(t *testing.T, db *sql.DB, block json.RawMessage) []map[string]string {
	t.Helper()
	cloud := &fakeCloud{
		entitlement: block,
		directives: []map[string]any{
			{"id": "d-ent", "type": "set_setting", "payload": map[string]any{"key": "display.osk", "value": "on"}},
		},
	}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	hooks := Hooks{SetSetting: func(ctx context.Context, key, value string) (string, error) {
		return "ok", nil
	}}
	if err := Tick(context.Background(), testCfg(srv.URL), db, hooks); err != nil {
		t.Fatalf("Tick must not fail over the entitlement block: %v", err)
	}
	cloud.mu.Lock()
	defer cloud.mu.Unlock()
	if len(cloud.results) != 1 || cloud.results[0]["directive_id"] != "d-ent" || cloud.results[0]["status"] != "applied" {
		t.Fatalf("directive handling disturbed: results = %+v", cloud.results)
	}
	return cloud.results
}

func TestSyncEntitlementPresentIsCached(t *testing.T) {
	d := openMigratedDB(t, "ent_present.db")
	before := time.Now().UTC().Add(-time.Second)
	runTickWith(t, d.DB, json.RawMessage(`{"plan":"shop","subscription_status":"active","expires_at":"2026-10-24T00:00:00Z","refreshed_at":"2020-01-01T00:00:00Z"}`))
	after := time.Now().UTC().Add(time.Second)

	got := readEntitlementCache(t, d.DB)
	if got[entitlement.KeyPlan] != "shop" || got[entitlement.KeySubscriptionStatus] != "active" || got[entitlement.KeyExpiresAt] != "2026-10-24T00:00:00Z" {
		t.Fatalf("cache = %+v", got)
	}
	confirmed, err := time.Parse(time.RFC3339, got[entitlement.KeyLastConfirmedAt])
	if err != nil {
		t.Fatalf("last_confirmed_at %q not RFC3339: %v", got[entitlement.KeyLastConfirmedAt], err)
	}
	// The till's own clock, not the cloud's refreshed_at (2020 above).
	if confirmed.Before(before.Truncate(time.Second)) || confirmed.After(after) {
		t.Fatalf("last_confirmed_at = %v, want the till's now (%v..%v)", confirmed, before, after)
	}
	if confirmed.Location() != time.UTC {
		t.Fatalf("last_confirmed_at not UTC: %q", got[entitlement.KeyLastConfirmedAt])
	}
	if p := entitlement.EffectivePlan(context.Background(), data.NewSettingsRepo(d.DB), time.Now()); p != entitlement.PlanShop {
		t.Fatalf("EffectivePlan after a fresh read = %q, want shop", p)
	}
}

func TestSyncEntitlementNullExpiry(t *testing.T) {
	d := openMigratedDB(t, "ent_null_expiry.db")
	seedEntitlementCache(t, d.DB) // a previous non-empty expires_at must be cleared
	runTickWith(t, d.DB, json.RawMessage(`{"plan":"local","subscription_status":"none","expires_at":null,"refreshed_at":"2026-09-24T10:00:00Z"}`))
	got := readEntitlementCache(t, d.DB)
	if v, ok := got[entitlement.KeyExpiresAt]; !ok || v != "" {
		t.Fatalf("expires_at = %q (present=%v), want empty string for null", v, ok)
	}
	if got[entitlement.KeyPlan] != "local" || got[entitlement.KeySubscriptionStatus] != "none" {
		t.Fatalf("cache = %+v", got)
	}
	if got[entitlement.KeyLastConfirmedAt] == seededCache[entitlement.KeyLastConfirmedAt] {
		t.Fatal("last_confirmed_at not refreshed")
	}
}

func TestSyncEntitlementConfirmedLapseOverwritesCache(t *testing.T) {
	d := openMigratedDB(t, "ent_lapse.db")
	seedEntitlementCache(t, d.DB)
	runTickWith(t, d.DB, json.RawMessage(`{"plan":"pro","subscription_status":"lapsed","expires_at":null,"refreshed_at":"2026-09-24T10:00:00Z"}`))
	if got := readEntitlementCache(t, d.DB); got[entitlement.KeySubscriptionStatus] != "lapsed" {
		t.Fatalf("cache = %+v, want lapsed", got)
	}
	if p := entitlement.EffectivePlan(context.Background(), data.NewSettingsRepo(d.DB), time.Now()); p != entitlement.PlanLocal {
		t.Fatalf("EffectivePlan after confirmed lapse = %q, want local", p)
	}
}

// An old cloud omits the block: nothing is touched, not even
// last_confirmed_at (no fresh read happened).
func TestSyncEntitlementAbsentTouchesNothing(t *testing.T) {
	for name, block := range map[string]json.RawMessage{
		"omitted":       nil,
		"explicit null": json.RawMessage(`null`),
	} {
		t.Run(name, func(t *testing.T) {
			d := openMigratedDB(t, "ent_absent.db")
			seedEntitlementCache(t, d.DB)
			runTickWith(t, d.DB, block)
			assertCacheUnchanged(t, d.DB)
		})
	}
}

func TestSyncEntitlementAbsentOnFreshTillWritesNothing(t *testing.T) {
	d := openMigratedDB(t, "ent_absent_fresh.db")
	runTickWith(t, d.DB, nil)
	if got := readEntitlementCache(t, d.DB); len(got) != 0 {
		t.Fatalf("absent block wrote keys: %+v", got)
	}
}

// Malformed blocks are ignored whole: the cache stays as it was and the
// sync (including its directives) completes normally.
func TestSyncEntitlementMalformedKeepsCache(t *testing.T) {
	for name, block := range map[string]string{
		"unknown plan":        `{"plan":"platinum","subscription_status":"active","expires_at":null,"refreshed_at":"2026-09-24T10:00:00Z"}`,
		"unknown status":      `{"plan":"shop","subscription_status":"trialing","expires_at":null,"refreshed_at":"2026-09-24T10:00:00Z"}`,
		"missing plan":        `{"subscription_status":"active"}`,
		"wrong JSON type":     `{"plan":5,"subscription_status":"active"}`,
		"not an object":       `"shop"`,
		"array instead":       `[1,2,3]`,
		"status wrong type":   `{"plan":"shop","subscription_status":true}`,
		"expires wrong type":  `{"plan":"shop","subscription_status":"active","expires_at":12}`,
		"empty object":        `{}`,
		"plan with only case": `{"plan":"SHOP","subscription_status":"active"}`,
	} {
		t.Run(name, func(t *testing.T) {
			d := openMigratedDB(t, "ent_malformed.db")
			seedEntitlementCache(t, d.DB)
			runTickWith(t, d.DB, json.RawMessage(block))
			assertCacheUnchanged(t, d.DB)
		})
	}
}

// A settings write failure must never fail the tick or the directives.
func TestSyncEntitlementPersistFailureDoesNotFailTick(t *testing.T) {
	d := openMigratedDB(t, "ent_persist_fail.db")
	seedEntitlementCache(t, d.DB)
	if _, err := d.DB.Exec(`CREATE TRIGGER block_entitlement BEFORE UPDATE ON settings
		WHEN NEW.key LIKE 'entitlement.%' BEGIN SELECT RAISE(ABORT, 'disk says no'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	runTickWith(t, d.DB, json.RawMessage(`{"plan":"chain","subscription_status":"active","expires_at":null,"refreshed_at":"2026-09-24T10:00:00Z"}`))
	// SetMany is one transaction: nothing half-written.
	assertCacheUnchanged(t, d.DB)
}

func assertCacheUnchanged(t *testing.T, db *sql.DB) {
	t.Helper()
	got := readEntitlementCache(t, db)
	for k, want := range seededCache {
		if got[k] != want {
			t.Errorf("%s = %q, want unchanged %q", k, got[k], want)
		}
	}
}

// An unparsable (display-only) expires_at must not stop the refresh: plan,
// status and last_confirmed_at are written, expires_at is stored empty.
func TestSyncEntitlementBadExpiresStillRefreshes(t *testing.T) {
	d := openMigratedDB(t, "ent_bad_expires.db")
	seedEntitlementCache(t, d.DB)
	runTickWith(t, d.DB, json.RawMessage(`{"plan":"pro","subscription_status":"active","expires_at":"next tuesday","refreshed_at":"2026-09-24T10:00:00Z"}`))
	got := readEntitlementCache(t, d.DB)
	if got[entitlement.KeyPlan] != "pro" || got[entitlement.KeyExpiresAt] != "" {
		t.Fatalf("cache = %v, want plan pro with empty expires_at", got)
	}
}

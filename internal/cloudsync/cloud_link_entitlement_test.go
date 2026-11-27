package cloudsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/entitlement"
)

// ADR-0117 §2 (ut-docs#2821): cacheEntitlement writes cloud.link_tier /
// cloud.link_mode alongside entitlement.* whenever the block is valid.

var cloudLinkKeys = []string{entitlement.KeyCloudLinkTier, entitlement.KeyCloudLinkMode}

func readCloudLinkCache(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	repo := data.NewSettingsRepo(db)
	out := map[string]string{}
	for _, k := range cloudLinkKeys {
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

func TestSyncCloudLinkRealtimePresentIsCached(t *testing.T) {
	d := openMigratedDB(t, "cloud_link_realtime.db")
	runTickWith(t, d.DB, json.RawMessage(`{"plan":"pro","subscription_status":"active","expires_at":null,"refreshed_at":"2026-09-26T10:00:00Z","cloud_link":"realtime","cloud_link_mode":"always"}`))

	got := readCloudLinkCache(t, d.DB)
	if got[entitlement.KeyCloudLinkTier] != "realtime" || got[entitlement.KeyCloudLinkMode] != "always" {
		t.Fatalf("cloud_link cache = %+v, want realtime/always", got)
	}
	tier, mode := entitlement.CloudLink(context.Background(), data.NewSettingsRepo(d.DB), time.Now())
	if tier != "realtime" || mode != "always" {
		t.Fatalf("entitlement.CloudLink after a fresh read = (%q, %q), want (realtime, always)", tier, mode)
	}
}

// A block that carries plan/status but no cloud_link (an older cloud, or a
// plan the product owner hasn't put on the realtime tier) caches "periodic"
// — missing = periodic, never left unset and never an error.
func TestSyncCloudLinkAbsentFieldCachesPeriodic(t *testing.T) {
	d := openMigratedDB(t, "cloud_link_absent_field.db")
	runTickWith(t, d.DB, json.RawMessage(`{"plan":"local","subscription_status":"none","expires_at":null,"refreshed_at":"2026-09-26T10:00:00Z"}`))

	got := readCloudLinkCache(t, d.DB)
	if got[entitlement.KeyCloudLinkTier] != "periodic" {
		t.Fatalf("cloud_link tier = %q, want periodic", got[entitlement.KeyCloudLinkTier])
	}
	if v, present := got[entitlement.KeyCloudLinkMode]; !present || v != "" {
		t.Fatalf("cloud_link mode = %q (present=%v), want empty", v, present)
	}
}

// An absent block entirely (older cloud omits "entitlement" itself) touches
// nothing — cloud_link included, same as the four entitlement.* keys.
func TestSyncCloudLinkAbsentBlockTouchesNothing(t *testing.T) {
	d := openMigratedDB(t, "cloud_link_absent_block.db")
	seedEntitlementCache(t, d.DB)
	seedCloudLinkCache(t, d.DB)
	runTickWith(t, d.DB, nil)

	got := readCloudLinkCache(t, d.DB)
	if got[entitlement.KeyCloudLinkTier] != "realtime" || got[entitlement.KeyCloudLinkMode] != "always" {
		t.Fatalf("absent block touched cloud_link cache: %+v", got)
	}
}

// A malformed block (unknown plan) is ignored whole, cloud_link included —
// even though cloud_link itself was valid.
func TestSyncCloudLinkMalformedBlockKeepsCache(t *testing.T) {
	d := openMigratedDB(t, "cloud_link_malformed.db")
	seedCloudLinkCache(t, d.DB)
	runTickWith(t, d.DB, json.RawMessage(`{"plan":"platinum","subscription_status":"active","cloud_link":"realtime","cloud_link_mode":"always"}`))

	got := readCloudLinkCache(t, d.DB)
	if got[entitlement.KeyCloudLinkTier] != "realtime" || got[entitlement.KeyCloudLinkMode] != "always" {
		t.Fatalf("malformed block changed cloud_link cache: %+v, want the seeded values kept", got)
	}
}

var seededCloudLinkCache = map[string]string{
	entitlement.KeyCloudLinkTier: "realtime",
	entitlement.KeyCloudLinkMode: "always",
}

func seedCloudLinkCache(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := data.NewSettingsRepo(db).SetMany(context.Background(), seededCloudLinkCache); err != nil {
		t.Fatalf("seed cloud_link cache: %v", err)
	}
}

// ut-docs#7101 (ADR-0219): the on_demand wake-up rides the same block; the cache
// turns link_wanted_for_s into a deadline on the till's own clock.
func TestSyncCloudLinkOnDemandWantedIsCached(t *testing.T) {
	d := openMigratedDB(t, "cloud_link_on_demand.db")
	runTickWith(t, d.DB, json.RawMessage(`{"plan":"shop","subscription_status":"active","expires_at":null,"refreshed_at":"2026-09-26T10:00:00Z","cloud_link":"realtime","cloud_link_mode":"on_demand","link_wanted_for_s":300}`))
	repo := data.NewSettingsRepo(d.DB)
	tier, mode := entitlement.CloudLink(context.Background(), repo, time.Now())
	if tier != "realtime" || mode != "on_demand" {
		t.Fatalf("tier/mode = %q/%q", tier, mode)
	}
	if !entitlement.LinkWanted(context.Background(), repo, time.Now()) {
		t.Fatal("wanted for 300 s must read as wanted")
	}
	if entitlement.LinkWanted(context.Background(), repo, time.Now().Add(6*time.Minute)) {
		t.Fatal("wanted must lapse after its seconds")
	}
	runTickWith(t, d.DB, json.RawMessage(`{"plan":"shop","subscription_status":"active","expires_at":null,"refreshed_at":"2026-09-26T10:00:00Z","cloud_link":"realtime","cloud_link_mode":"on_demand","link_wanted_for_s":0}`))
	if entitlement.LinkWanted(context.Background(), repo, time.Now()) {
		t.Fatal("0 means not wanted")
	}
}

package cloudsync

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/entitlement"
)

// ADR-0060 amendment (ut-docs#3532): `claimed` is cached next to the block,
// written only when it is a JSON boolean; a block without it keeps the last
// value, and a malformed one never costs the rest of the block.

func readClaimed(t *testing.T, d *db.DB) (string, bool) {
	t.Helper()
	v, ok, err := data.NewSettingsRepo(d.DB).Get(context.Background(), entitlement.KeyStoreClaimed)
	if err != nil {
		t.Fatalf("get claimed: %v", err)
	}
	return v, ok
}

func TestCacheEntitlementKeepsClaimedWhenOmitted(t *testing.T) {
	d := openMigratedDB(t, "claimed_keep.db")
	runTickWith(t, d.DB, json.RawMessage(`{"plan":"local","subscription_status":"none","claimed":true}`))
	if v, ok := readClaimed(t, d); !ok || v != "true" {
		t.Fatalf("claimed after a block with it = %q (present=%v), want true", v, ok)
	}
	runTickWith(t, d.DB, json.RawMessage(`{"plan":"local","subscription_status":"none"}`))
	if v, ok := readClaimed(t, d); !ok || v != "true" {
		t.Fatalf("claimed after a block without it = %q (present=%v), want the last value true", v, ok)
	}
	runTickWith(t, d.DB, json.RawMessage(`{"plan":"local","subscription_status":"none","claimed":false}`))
	if v, _ := readClaimed(t, d); v != "false" {
		t.Fatalf("claimed after an explicit false = %q, want false", v)
	}
}

func TestCacheEntitlementMalformedClaimedStillCachesPlan(t *testing.T) {
	d := openMigratedDB(t, "claimed_malformed.db")
	for _, c := range []string{`"yes"`, `1`, `null`, `{}`} {
		runTickWith(t, d.DB, json.RawMessage(`{"plan":"shop","subscription_status":"active","claimed":`+c+`}`))
		if got := readEntitlementCache(t, d.DB); got[entitlement.KeyPlan] != "shop" {
			t.Fatalf("claimed=%s: plan not cached: %+v", c, got)
		}
		if v, ok := readClaimed(t, d); ok {
			t.Fatalf("claimed=%s: wrote %q, want no row", c, v)
		}
	}
}

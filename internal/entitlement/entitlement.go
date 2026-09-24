// Package entitlement is the till-side cache and evaluation of the ADR-0060
// subscription entitlement model (ut-docs#2547): the shop's plan tier as
// last confirmed by the cloud's POST /v1/stores/sync response, cached in the
// settings KV, and EffectivePlan — the one function the till consults to
// decide whether a store-wide paid capability is on.
//
// NEVER A SALE GATE (ADR-0060 §5 and §7, ADR-0027 §1). Nothing on the sale
// path — basket, tender, fiscal signing, receipt, EOD — may read this
// package. salepath_imports_test.go enforces that internal/pos,
// internal/fiscal and internal/print do not import it, directly or
// transitively. A lapsed or stale entitlement degrades paid cloud surfaces to
// the free Local tier; it never stops a sale and never soft-disables a
// mandated fiscal plugin.
//
// The vocabulary (plans, capabilities, Allows) mirrors ut-cloud's
// internal/subscription package; both fail closed on unknown values.
package entitlement

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Settings keys of the cached entitlement (ADR-0060 §4). They live here, not
// in internal/pages/common, for the same reason internal/fiscal's Key*
// constants do: the writer (internal/cloudsync) is not a page package.
const (
	// KeyPlan: local | shop | pro | chain.
	KeyPlan = "entitlement.plan"
	// KeySubscriptionStatus: active | lapsed | none.
	KeySubscriptionStatus = "entitlement.subscription_status"
	// KeyExpiresAt: UTC RFC3339, or empty. DISPLAY ONLY — never evaluated
	// (ADR-0060 §5).
	KeyExpiresAt = "entitlement.expires_at"
	// KeyLastConfirmedAt: UTC RFC3339 of the till's own clock at the last
	// successful read of a valid entitlement block.
	KeyLastConfirmedAt = "entitlement.last_confirmed_at"
)

// Grace is how long a cached plan is honoured without a fresh confirmation
// from the cloud before EffectivePlan degrades to Local (ADR-0060 §5: one
// number, not per-capability, not configurable per shop).
const Grace = 7 * 24 * time.Hour

// Plan is a subscription tier (ADR-0060 §1).
type Plan string

const (
	PlanLocal Plan = "local" // the free tier; also the effective plan of an unenrolled till
	PlanShop  Plan = "shop"
	PlanPro   Plan = "pro"
	PlanChain Plan = "chain"
)

var rank = map[Plan]int{PlanLocal: 0, PlanShop: 1, PlanPro: 2, PlanChain: 3}

// Valid reports whether p is one of the four known tiers.
func (p Plan) Valid() bool {
	_, ok := rank[p]
	return ok
}

// Subscription status values as delivered by the cloud (StoreAccount).
const (
	StatusActive = "active"
	StatusLapsed = "lapsed"
	StatusNone   = "none"
)

func validStatus(s string) bool {
	return s == StatusActive || s == StatusLapsed || s == StatusNone
}

// Capability is a store-wide paid capability gated by tier (ADR-0060 §2).
type Capability string

const (
	CapCloudBackup           Capability = "cloud_backup"
	CapCloudSync             Capability = "cloud_sync"
	CapBrowserCatalog        Capability = "browser_catalog"
	CapManagedTSE            Capability = "managed_tse"
	CapCentralCatalog        Capability = "central_catalog"
	CapConsolidatedReporting Capability = "consolidated_reporting"
)

var minTier = map[Capability]Plan{
	CapCloudBackup:           PlanShop,
	CapCloudSync:             PlanShop,
	CapBrowserCatalog:        PlanShop,
	CapManagedTSE:            PlanShop,
	CapCentralCatalog:        PlanChain,
	CapConsolidatedReporting: PlanChain,
}

// Allows reports whether plan includes capability c. Unknown plan or
// capability → false (fail closed, same as ut-cloud's subscription.Allows).
func Allows(plan Plan, c Capability) bool {
	floor, ok := minTier[c]
	if !ok {
		return false
	}
	pr, ok := rank[plan]
	if !ok {
		return false
	}
	return pr >= rank[floor]
}

// Reader is the slice of the settings store EffectivePlan needs;
// *data.SettingsRepo satisfies it.
type Reader interface {
	Get(ctx context.Context, key string) (string, bool, error)
}

// EffectivePlan is ADR-0060 §5's effectivePlan(): the cached plan when the
// cache says "active" and was confirmed no more than Grace before now;
// PlanLocal otherwise (no cache, unreadable cache, confirmed lapse/none,
// stale past Grace, or an unknown cached plan). A last_confirmed_at up to
// Grace in the future (clock skew) counts as fresh; further ahead is local. KeyExpiresAt is deliberately never
// read here — it is display-only.
func EffectivePlan(ctx context.Context, r Reader, now time.Time) Plan {
	get := func(k string) (string, bool) {
		v, ok, err := r.Get(ctx, k)
		if err != nil || !ok {
			return "", false
		}
		return strings.TrimSpace(v), true
	}
	confirmedRaw, ok := get(KeyLastConfirmedAt)
	if !ok {
		return PlanLocal
	}
	confirmed, err := time.Parse(time.RFC3339, confirmedRaw)
	if err != nil {
		return PlanLocal
	}
	if status, _ := get(KeySubscriptionStatus); status != StatusActive {
		return PlanLocal
	}
	if now.Sub(confirmed) > Grace {
		return PlanLocal
	}
	// Symmetric bound: a confirmation more than Grace in the future was
	// cached while the till's clock was wrong; without this it would stay
	// "fresh" for as long as the skew lasts, with no sync needed.
	if confirmed.Sub(now) > Grace {
		return PlanLocal
	}
	planRaw, _ := get(KeyPlan)
	if p := Plan(planRaw); p.Valid() {
		return p
	}
	return PlanLocal
}

// Block is the optional "entitlement" object of the POST /v1/stores/sync
// response data (ADR-0060 §3).
type Block struct {
	Plan               string  `json:"plan"`
	SubscriptionStatus string  `json:"subscription_status"`
	ExpiresAt          *string `json:"expires_at"`
	// RefreshedAt is the cloud's serve time. Informational only: the cache
	// records the till's own clock as last_confirmed_at, so a skewed cloud
	// clock can never stretch or shrink the grace window.
	RefreshedAt string `json:"refreshed_at"`
}

// Values validates b and returns the four settings rows to write, with
// last_confirmed_at = now (UTC). An unknown plan or status is an error — the
// caller keeps its cache. A non-RFC3339 expires_at is stored empty.
func (b Block) Values(now time.Time) (map[string]string, error) {
	plan := Plan(strings.TrimSpace(b.Plan))
	if !plan.Valid() {
		return nil, fmt.Errorf("entitlement: unknown plan %q", b.Plan)
	}
	status := strings.TrimSpace(b.SubscriptionStatus)
	if !validStatus(status) {
		return nil, fmt.Errorf("entitlement: unknown subscription_status %q", b.SubscriptionStatus)
	}
	expires := ""
	if b.ExpiresAt != nil {
		if s := strings.TrimSpace(*b.ExpiresAt); s != "" {
			// Display-only (ADR-0060 §5): an unparsable value is dropped,
			// never allowed to reject the block — that would stop
			// last_confirmed_at moving and degrade a paid till after Grace
			// over a field nothing evaluates.
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				expires = t.UTC().Format(time.RFC3339)
			}
		}
	}
	return map[string]string{
		KeyPlan:               string(plan),
		KeySubscriptionStatus: status,
		KeyExpiresAt:          expires,
		KeyLastConfirmedAt:    now.UTC().Format(time.RFC3339),
	}, nil
}

// Package entitlement is the till-side cache and evaluation of the ADR-0060
// subscription entitlement model (ut-docs#2547): the shop's plan tier as
// last confirmed by the cloud's POST /v1/stores/sync response, cached in the
// settings KV, and EffectivePlan — the one function the till consults to
// decide whether a store-wide paid capability is on. The same sync-response
// block also carries ADR-0117 §2's cloud_link tier (ut-docs#2821) — a
// separate setting family (Key*, LinkTier/LinkMode, CloudLink) cached and
// staled the same way, since it rides the same block and the same
// last_confirmed_at.
//
// NEVER A SALE GATE (ADR-0060 §5 and §7, ADR-0027 §1). Nothing on the sale
// path — basket, tender, fiscal signing, receipt, EOD — may read this
// package. salepath_imports_test.go enforces that internal/pos,
// internal/fiscal and internal/print do not import it, directly or
// transitively. A lapsed or stale entitlement degrades paid cloud surfaces to
// the free Local tier; it never stops a sale and never soft-disables a
// mandated fiscal plugin. The same applies to cloud_link: a stale or
// periodic tier only means the till stays on the 2-minute check-in
// (ADR-0117 §8) — never a sale-path concern.
//
// The vocabulary (plans, capabilities, Allows) mirrors ut-cloud's
// internal/subscription package; both fail closed on unknown values.
package entitlement

import (
	"context"
	"fmt"
	"sort"
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
	// KeyCloudLinkTier: realtime | periodic (ADR-0117 §2). Deliberately not
	// under the "entitlement." prefix (a different setting family), but
	// rewritten on the same cadence as the block above — PerTillSettingPrefixes
	// excludes "cloud.link_" from admin sync for exactly the same reason
	// (ut-docs#2792): every valid block rewrites it, so syncing it shop-wide
	// would move the main till's admin fingerprint every cloud tick.
	KeyCloudLinkTier = "cloud.link_tier"
	// KeyCloudLinkMode: always | on_demand, only meaningful when
	// KeyCloudLinkTier is "realtime" — empty otherwise (ADR-0117 §2;
	// on_demand is a recorded option, not built).
	KeyCloudLinkMode = "cloud.link_mode"
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

// Capabilities returns every known capability, sorted, so a caller that
// reports the full capability set (GET /api/entitlement) cannot drift from
// minTier when a capability is added.
func Capabilities() []Capability {
	out := make([]Capability, 0, len(minTier))
	for c := range minTier {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
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

// CloudLink is ADR-0117 §2's till-side read of the cached cloud_link tier
// and mode, mirroring EffectivePlan's staleness rule exactly: the cached
// value is honoured only while last_confirmed_at (the same timestamp
// EffectivePlan reads — both are written by the same cacheEntitlement call)
// is no more than Grace old, in either direction (clock-skew symmetric,
// same reasoning as EffectivePlan). No cache, an unparsable confirmation, a
// stale one, or an unrecognised cached tier all degrade to ("periodic",
// "") — fail closed, same as an unenrolled or lapsed till's EffectivePlan.
// NEVER a sale gate (package doc): this is for the cloud-link client
// (ADR-0117 task 4, not built here) to decide whether to dial, nothing on
// the sale path.
func CloudLink(ctx context.Context, r Reader, now time.Time) (tier, mode string) {
	get := func(k string) (string, bool) {
		v, ok, err := r.Get(ctx, k)
		if err != nil || !ok {
			return "", false
		}
		return strings.TrimSpace(v), true
	}
	confirmedRaw, ok := get(KeyLastConfirmedAt)
	if !ok {
		return "periodic", ""
	}
	confirmed, err := time.Parse(time.RFC3339, confirmedRaw)
	if err != nil {
		return "periodic", ""
	}
	if now.Sub(confirmed) > Grace || confirmed.Sub(now) > Grace {
		return "periodic", ""
	}
	tierRaw, _ := get(KeyCloudLinkTier)
	tier = LinkTier(tierRaw)
	if tier != "realtime" {
		return "periodic", ""
	}
	modeRaw, _ := get(KeyCloudLinkMode)
	return "realtime", LinkMode("realtime", modeRaw)
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
	// CloudLink and CloudLinkMode are ADR-0117 §2's realtime/periodic tier
	// fields, present on the same block. Raw and unvalidated: LinkTier/
	// LinkMode normalise them in Values (an older cloud or a block that
	// never sets them defaults to "periodic", never an error — missing =
	// periodic, ADR-0117 §2).
	CloudLink     string `json:"cloud_link"`
	CloudLinkMode string `json:"cloud_link_mode"`
}

// Values validates b and returns the six settings rows to write (the four
// ADR-0060 §4 rows plus ADR-0117 §2's cloud_link tier/mode), with
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
	tier := LinkTier(b.CloudLink)
	return map[string]string{
		KeyPlan:               string(plan),
		KeySubscriptionStatus: status,
		KeyExpiresAt:          expires,
		KeyLastConfirmedAt:    now.UTC().Format(time.RFC3339),
		KeyCloudLinkTier:      tier,
		KeyCloudLinkMode:      LinkMode(tier, b.CloudLinkMode),
	}, nil
}

// LinkTier normalises a raw ADR-0117 §2 cloud_link wire value. Only the
// exact string "realtime" is realtime; anything else — missing, an
// unrecognised value, a future value this build doesn't understand — fails
// closed to "periodic" (same fail-closed convention as Plan/subscription
// status).
func LinkTier(raw string) string {
	if raw == "realtime" {
		return "realtime"
	}
	return "periodic"
}

// LinkMode normalises a raw ADR-0117 §2 cloud_link_mode wire value. Only
// meaningful for a realtime tier: on a periodic tier it is always "". For a
// realtime tier, only the exact string "on_demand" selects it (the recorded
// option ADR-0117 §2 keeps but does not build); anything else — missing,
// "always", an unrecognised value — is "always".
func LinkMode(tier, raw string) string {
	if tier != "realtime" {
		return ""
	}
	if raw == "on_demand" {
		return "on_demand"
	}
	return "always"
}

// Cached is this till's entitlement cache as the main till relays it to a
// replica over the authenticated LAN sync channel (ut-docs#2792): the vouch
// answer of POST /api/sync/cloud-device. entitlement.* is per-till in the
// admin sync (it is rewritten on every cloud tick and would move the admin
// cursor each time), and a replica without its own store token (ut-docs#2730)
// cannot ask the cloud itself — so the main till passes its cache on.
// Relayed verbatim, LastConfirmedAt included: the replica's grace window
// stays anchored to the main till's last real confirmation, so a replica can
// never keep a plan alive that its main till has stopped confirming.
type Cached struct {
	Plan               string `json:"plan"`
	SubscriptionStatus string `json:"subscription_status"`
	ExpiresAt          string `json:"expires_at"`
	LastConfirmedAt    string `json:"last_confirmed_at"`
}

// ReadCached reads the four settings rows. ok is false when the cache was
// never confirmed by the cloud (no last_confirmed_at) or is unreadable —
// nothing worth relaying.
func ReadCached(ctx context.Context, r Reader) (c Cached, ok bool) {
	get := func(k string) (string, error) {
		v, _, err := r.Get(ctx, k)
		return strings.TrimSpace(v), err
	}
	var err error
	if c.LastConfirmedAt, err = get(KeyLastConfirmedAt); err != nil || c.LastConfirmedAt == "" {
		return Cached{}, false
	}
	for _, f := range []struct {
		key string
		dst *string
	}{{KeyPlan, &c.Plan}, {KeySubscriptionStatus, &c.SubscriptionStatus}, {KeyExpiresAt, &c.ExpiresAt}} {
		if *f.dst, err = get(f.key); err != nil {
			return Cached{}, false
		}
	}
	return c, true
}

// RelayValues validates a relayed cache and returns the four settings rows
// to write, verbatim (normalised to UTC RFC3339). Same fail-closed rules as
// Block.Values: an unknown plan or status, or a missing/unparsable
// confirmation time, is an error and the caller keeps its own cache; an
// unparsable expires_at is display-only and stored empty.
func (c Cached) RelayValues() (map[string]string, error) {
	plan := Plan(strings.TrimSpace(c.Plan))
	if !plan.Valid() {
		return nil, fmt.Errorf("entitlement: unknown relayed plan %q", c.Plan)
	}
	status := strings.TrimSpace(c.SubscriptionStatus)
	if !validStatus(status) {
		return nil, fmt.Errorf("entitlement: unknown relayed subscription_status %q", c.SubscriptionStatus)
	}
	confirmed, err := time.Parse(time.RFC3339, strings.TrimSpace(c.LastConfirmedAt))
	if err != nil {
		return nil, fmt.Errorf("entitlement: relayed last_confirmed_at %q: %w", c.LastConfirmedAt, err)
	}
	expires := ""
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(c.ExpiresAt)); err == nil {
		expires = t.UTC().Format(time.RFC3339)
	}
	return map[string]string{
		KeyPlan:               string(plan),
		KeySubscriptionStatus: status,
		KeyExpiresAt:          expires,
		KeyLastConfirmedAt:    confirmed.UTC().Format(time.RFC3339),
	}, nil
}

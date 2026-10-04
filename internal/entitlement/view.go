package entitlement

import (
	"context"
	"strings"
	"time"
)

// State is the merchant-facing reading of the cached entitlement
// (ut-docs#2569, ADR-0060 §6): what the status-bar chip, the Settings →
// Subscription card and the paused-feature banners say.
type State string

const (
	// StateFree: the cached plan is local, empty or unknown, or the till
	// has never had a confirmation — nothing paid to pause.
	StateFree State = "free"
	// StateActive: EffectivePlan honours the cached paid plan.
	StateActive State = "active"
	// StateStale: a paid plan the till couldn't confirm within Grace
	// (past Grace, a clock skew beyond Grace, or an unknown/empty status).
	StateStale State = "stale"
	// StateLapsed: a fresh read said the subscription is lapsed or none.
	StateLapsed State = "lapsed"
)

// View is Describe's result. Display only: nothing here is a gate, and the
// sale path never reads it (package doc, salepath_imports_test.go).
type View struct {
	State State
	// Plan is the cached plan (PlanLocal when empty or unknown).
	Plan Plan
	// Effective is EffectivePlan(now).
	Effective Plan
	// ExpiresAt is zero when unset or unparsable. DISPLAY ONLY — never
	// compared to now (ADR-0060 §5).
	ExpiresAt time.Time
	// LastConfirmedAt is zero when unset or unparsable.
	LastConfirmedAt time.Time
	// Paused lists the capabilities the cached plan includes but the
	// effective plan does not, sorted.
	Paused []Capability
}

// Describe derives the View from the cached settings and EffectivePlan —
// it reuses EffectivePlan's Grace rule rather than restating it.
func Describe(ctx context.Context, r Reader, now time.Time) View {
	get := func(k string) (string, bool) {
		v, ok, err := r.Get(ctx, k)
		if err != nil || !ok {
			return "", false
		}
		return strings.TrimSpace(v), true
	}
	v := View{Plan: PlanLocal, Effective: EffectivePlan(ctx, r, now)}
	confirmedRaw, confirmedSet := get(KeyLastConfirmedAt)
	// An empty value (a replica copies every entitlement key, set or not —
	// internal/enroll/replica.go) means never confirmed, same as an absent key.
	confirmedSet = confirmedSet && confirmedRaw != ""
	if t, err := time.Parse(time.RFC3339, confirmedRaw); err == nil {
		v.LastConfirmedAt = t
	}
	if raw, _ := get(KeyExpiresAt); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			v.ExpiresAt = t
		}
	}
	planRaw, _ := get(KeyPlan)
	cached := Plan(planRaw)
	// Unknown/empty/local plan, or a till the cloud never confirmed: free.
	// The read error case lands here too (every get fails → no plan).
	if !cached.Valid() || cached == PlanLocal || !confirmedSet {
		v.State = StateFree
		v.Effective = PlanLocal
		return v
	}
	v.Plan = cached
	if v.Effective == cached {
		v.State = StateActive
		return v
	}
	if status, _ := get(KeySubscriptionStatus); status == StatusLapsed || status == StatusNone {
		v.State = StateLapsed
	} else {
		v.State = StateStale
	}
	for _, c := range Capabilities() { // already sorted
		if Allows(cached, c) && !Allows(v.Effective, c) {
			v.Paused = append(v.Paused, c)
		}
	}
	return v
}

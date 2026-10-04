package pages

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#2569 (ADR-0060 §6): the merchant-facing side of the cached
// subscription entitlement — the status-bar chip, Settings → Subscription
// and the paused-feature banners on the surfaces a paid capability feeds.
// Every one renders from entitlement.Describe, read from the settings KV
// only: no network call, never a modal, and never a sale gate (nothing on
// the sale path reads any of this — the chip lives in the shared status
// bar and decides nothing). It explains; it does not switch anything off.

// subscriptionView is what the templates read (settings.html's
// .subscription, subscription_block.html, subscription_paused_banner.html).
type subscriptionView struct {
	State string // free | active | stale | lapsed
	Plan  string // the cached plan (local when empty/unknown)
	// Status is the cached subscription_status for a lapsed view (lapsed
	// or none), so the card names it; empty otherwise.
	Status string
	// Renews is the formatted expires_at — only for an active plan, and
	// only when set. Display only (ADR-0060 §5).
	Renews string
	// LastConfirmed is the formatted last_confirmed_at, empty if never.
	LastConfirmed string
	// Paused lists the capabilities the cached plan includes but the
	// effective plan does not (sorted).
	Paused []string
}

// PausedOnly is Paused filtered to the space-separated capability names in
// only (all of Paused when only is empty) — so a surface shows the banner
// only when one of its own capabilities is paused.
func (v subscriptionView) PausedOnly(only string) []string {
	want := strings.Fields(only)
	if len(want) == 0 {
		return v.Paused
	}
	var out []string
	for _, c := range v.Paused {
		for _, w := range want {
			if c == w {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

func subscriptionViewFor(ctx context.Context, r entitlement.Reader, now time.Time, locale string) subscriptionView {
	ev := entitlement.Describe(ctx, r, now)
	v := subscriptionView{State: string(ev.State), Plan: string(ev.Plan)}
	if ev.State == entitlement.StateLapsed {
		v.Status = entitlement.StatusLapsed
		if s, _, err := r.Get(ctx, entitlement.KeySubscriptionStatus); err == nil && strings.TrimSpace(s) == entitlement.StatusNone {
			v.Status = entitlement.StatusNone
		}
	}
	if ev.State == entitlement.StateActive && !ev.ExpiresAt.IsZero() {
		v.Renews = httpx.FormatDate(ev.ExpiresAt, locale)
	}
	if !ev.LastConfirmedAt.IsZero() {
		v.LastConfirmed = httpx.FormatDate(ev.LastConfirmedAt, locale)
	}
	for _, c := range ev.Paused {
		v.Paused = append(v.Paused, string(c))
	}
	return v
}

// registerSubscriptionUI: GET /ui/subscription-chip, polled from the status
// bar (base.html's #sb-subscription-mount, every 60 s — the cache only
// changes on a sync tick). An empty 200 unless the viewer can open Settings
// (a cashier can't act on it — same rule as the sb-enrol chip, ut-docs#3079)
// and the subscription is stale or lapsed.
func registerSubscriptionUI(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /ui/subscription-chip", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "settings") {
			w.WriteHeader(http.StatusOK)
			return
		}
		ev := entitlement.Describe(r.Context(), d.Settings, time.Now())
		if ev.State != entitlement.StateStale && ev.State != entitlement.StateLapsed {
			w.WriteHeader(http.StatusOK)
			return
		}
		httpx.RenderPartial("ui/partials/subscription_chip.html", map[string]any{
			"state": string(ev.State),
		})(w, r)
	})
}

package pages

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
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
	// CanCheck: this viewer may run "Check now" (the settings permission,
	// and not a replica registered through its main till — that one gets
	// the renewal from the main till). Hides the button; the endpoint
	// refuses on its own (ut-docs#4285).
	CanCheck bool
	// CanRenew: CanCheck on a registered till — it gets the "Renew or
	// change plan" panel opener (ut-docs#4286). Hides the button; the
	// panel's endpoints refuse on their own.
	CanRenew bool
	// CheckResult is "Check now"'s one-line result, translated; empty on a
	// plain render.
	CheckResult string
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

// subscriptionViewWithCheck is the Settings page's view: the cached
// entitlement plus whether this viewer gets "Check now".
func subscriptionViewWithCheck(r *http.Request, d *common.Deps, locale string) subscriptionView {
	v := subscriptionViewFor(r.Context(), d.Settings, time.Now(), locale)
	v.CanCheck = canCheckSubscription(d, r)
	v.CanRenew = v.CanCheck && enroll.HasCredentials(d.Cfg)
	return v
}

// canCheckSubscription is subscriptionView.CanCheck for this request.
func canCheckSubscription(d *common.Deps, r *http.Request) bool {
	return canPerform(d, r, "settings") && !enroll.CurrentStatus().ViaMainTill
}

// refreshOutcome is what a forced entitlement refresh learned.
type refreshOutcome int

const (
	// refreshTimedOut: no check-in answered within the wait (offline, or
	// the cloud is slow). Nothing changed.
	refreshTimedOut refreshOutcome = iota
	// refreshActive: the cloud confirmed a plan that allows cloud sync.
	refreshActive
	// refreshNotActive: the cloud answered; still lapsed / no paid plan.
	refreshNotActive
)

// The refresh's wait for the check-in to land, and its poll step; vars so
// tests run in milliseconds.
var (
	refreshEntitlementWait = 8 * time.Second
	refreshEntitlementPoll = 250 * time.Millisecond
)

// The server-side floor on operator check-ins (review of ut-docs#4286):
// every panel open, panel poll and Check now refreshes, so several tabs, a
// double click or a forged poll loop could otherwise kick the cloud as
// fast as requests arrive. A refresh within refreshKickFloor of the last
// one that kicked reads the cache instead. refreshClock is the test seam.
var (
	refreshKickFloor = 5 * time.Second
	refreshClock     = time.Now
	refreshKickMu    sync.Mutex
	refreshLastKick  time.Time
)

// refreshEntitlementNow asks the cloudsync loop for an operator check-in
// now (ADR-0148 §2) and waits up to refreshEntitlementWait for it to land,
// read as a change of entitlement.last_confirmed_at (every answered
// check-in, 200 or 402, rewrites it). Shared by "Check now" and the
// Renew / Choose-a-plan panel (ut-docs#4286), which refreshes before it
// offers a checkout. Never a sale gate: it reads settings only. Returns
// ctx's error if the request goes away first. Within refreshKickFloor of
// the last kick it does not kick again and answers from the cache.
func refreshEntitlementNow(ctx context.Context, d *common.Deps) (refreshOutcome, error) {
	before, _, err := d.Settings.Get(ctx, entitlement.KeyLastConfirmedAt)
	if err != nil {
		return refreshTimedOut, err
	}
	if !claimRefreshKick() {
		// Under the floor: answer from the cache the last kick refreshed.
		if entitlement.SyncAllowed(ctx, d.Settings) {
			return refreshActive, nil
		}
		return refreshNotActive, nil
	}
	// last_confirmed_at has one-second resolution: a check-in our kick
	// starts in the same wall second as before would write the same string
	// and read as no answer. Kick from the next second instead (at most a
	// second's wait; tester-found, ut-docs#4285).
	if prev, perr := time.Parse(time.RFC3339, strings.TrimSpace(before)); perr == nil {
		if wait := time.Until(prev.Truncate(time.Second).Add(time.Second)); wait > 0 && wait <= time.Second {
			select {
			case <-ctx.Done():
				return refreshTimedOut, ctx.Err()
			case <-time.After(wait):
			}
		}
	}
	requestOperatorCheckin(d, operatorCheckinWindow)
	deadline := time.NewTimer(refreshEntitlementWait)
	defer deadline.Stop()
	poll := time.NewTicker(refreshEntitlementPoll)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return refreshTimedOut, ctx.Err()
		case <-deadline.C:
			return refreshTimedOut, nil
		case <-poll.C:
			now, _, err := d.Settings.Get(ctx, entitlement.KeyLastConfirmedAt)
			if err != nil || now == before {
				continue
			}
			if entitlement.SyncAllowed(ctx, d.Settings) {
				return refreshActive, nil
			}
			return refreshNotActive, nil
		}
	}
}

// claimRefreshKick reports whether this refresh may kick a check-in (none
// kicked within refreshKickFloor) and, if so, records it as the last kick.
func claimRefreshKick() bool {
	refreshKickMu.Lock()
	defer refreshKickMu.Unlock()
	now := refreshClock()
	if !refreshLastKick.IsZero() && now.Sub(refreshLastKick) < refreshKickFloor {
		return false
	}
	refreshLastKick = now
	return true
}

// checkNowScopes maps "Check now"'s scope to the element its result
// replaces (innerHTML): the Settings → Subscription card's body, or the
// paused banner on the registration card.
var checkNowScopes = map[string]string{
	"card":   "#subscription-check",
	"banner": "#subscription-check-banner",
}

// checkNowOnly keeps the known capability names in raw (the banner's
// "only" filter, echoed back from the page), dropping anything else.
func checkNowOnly(raw string) string {
	known := map[string]bool{}
	for _, c := range entitlement.Capabilities() {
		known[string(c)] = true
	}
	var out []string
	for _, f := range strings.Fields(raw) {
		if known[f] {
			out = append(out, f)
		}
	}
	return strings.Join(out, " ")
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
		// ut-docs#4286: a viewer who may check, on a registered till, opens
		// the Renew / Choose-a-plan panel from the chip; otherwise (a
		// replica registered via its main till, an unregistered till) it
		// keeps the link to Settings.
		httpx.RenderPartial("ui/partials/subscription_chip.html", map[string]any{
			"state":     string(ev.State),
			"openPanel": canCheckSubscription(d, r) && enroll.HasCredentials(d.Cfg),
		})(w, r)
	})

	// "Check now" (ADR-0148 amendment 2026-10-14, ut-docs#4285): a lapsed
	// till checks with the cloud now and shows the answer in place. Same
	// elevation gate and not-registered messages as POST
	// /api/enrol/check-plan; always 200 (HTMX swaps the body).
	posRepo := data.NewPOSRepo(d.Db)
	registerSubscriptionPanel(mux, d, posRepo)
	mux.HandleFunc("POST /api/subscription/check-now", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = r.ParseForm()
		scope := r.Form.Get("scope")
		target, ok := checkNowScopes[scope]
		if !ok {
			scope, target = "card", checkNowScopes["card"]
		}
		only := checkNowOnly(r.Form.Get("only"))
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/subscription/check-now", target,
				httpx.T(locale, "elevation.summary.subscription_check_now"),
				[]elevationHiddenField{{Name: "scope", Value: scope}, {Name: "only", Value: only}}, elev)
			return
		}
		if !enroll.HasCredentials(d.Cfg) {
			key := "settings.enrol.not_registered"
			if enroll.CurrentStatus().ViaMainTill {
				key = "settings.enrol.registered_via_main"
			}
			fmt.Fprintf(w, `<p class="error" role="status">❌ %s</p>`, html.EscapeString(httpx.T(locale, key)))
			return
		}
		outcome, err := refreshEntitlementNow(r.Context(), d)
		if err != nil {
			logging.L().Warnf("subscription check-now: %v", err)
		}
		view := subscriptionViewFor(r.Context(), d.Settings, time.Now(), locale)
		view.CanCheck = canCheckSubscription(d, r)
		view.CanRenew = view.CanCheck
		result := "timed_out"
		switch outcome {
		case refreshActive:
			result = "active"
			// The status-bar chip and the registration card listen for this
			// and re-read the fresh cache (ut-docs#4285).
			w.Header().Set("HX-Trigger", "ut-subscription-changed")
			view.CheckResult = httpx.T(locale, "subscription.check_result.renewed_no_date")
			if view.Renews != "" {
				view.CheckResult = fmt.Sprintf(httpx.T(locale, "subscription.check_result.renewed"), view.Renews)
			}
		case refreshNotActive:
			result = "not_active"
			view.CheckResult = httpx.T(locale, "subscription.check_result.still_lapsed")
		default:
			view.CheckResult = httpx.T(locale, "subscription.check_result.offline")
		}
		settingsAudit(r, posRepo, elev, "enrollment", "-", "subscription_check_now", map[string]any{"result": result})
		funcs := httpx.FuncsFor(locale)
		if scope == "banner" {
			httpx.RenderWith([]string{
				filepath.Join("web", "ui", "partials", "subscription_check_banner.html"),
				filepath.Join("web", "ui", "partials", "subscription_paused_banner.html"),
				// subscription_renew_open (ut-docs#4286) lives in the block.
				filepath.Join("web", "ui", "partials", "subscription_block.html"),
			}, funcs)("subscription_check_banner.html", map[string]any{"sub": view, "only": only})(w, r)
			return
		}
		httpx.RenderWith([]string{
			filepath.Join("web", "ui", "partials", "subscription_block.html"),
			filepath.Join("web", "ui", "partials", "subscription_paused_banner.html"),
		}, funcs)("subscription_block.html", map[string]any{"subscription": view})(w, r)
	})
}

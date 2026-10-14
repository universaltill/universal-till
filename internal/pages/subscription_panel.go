package pages

import (
	"context"
	"encoding/base64"
	"html/template"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#4286: the Renew / Choose-a-plan panel. A non-modal <dialog>
// (base.html's #subscription-panel, opened with .show() over #ut-scrim —
// the status bar stays reachable) whose body these three endpoints fill:
//
//	POST /api/subscription/panel          step 1 → 2: refresh from the
//	     cloud first (owner decision 2026-10-14); active → "Paid until"
//	     with no QR; otherwise the two choices. back=1 skips the refresh
//	     (Back from step 3).
//	POST /api/subscription/panel/handoff  step 3: a QR (and the URL as
//	     text) of the cloud's public /plans redirect, intent renew|change,
//	     paid on the owner's phone — the till never navigates away.
//	POST /api/subscription/panel/poll     step 3's re-check, every 20 s
//	     for at most 10 minutes; each one is an operator check-in
//	     (ADR-0148 §2). Flips the panel to the active result once the plan
//	     changed.
//
// All three are manager/admin (the "settings" permission, #3079); panel
// and handoff take the elevation prompt like Check now, the poll simply
// stops for anyone else. Never a sale gate: nothing here is on the sale
// path, and the panel only explains and hands off.

const (
	// subscriptionPanelPollMax × subscriptionPanelPollEvery = 10 minutes.
	subscriptionPanelPollMax   = 30
	subscriptionPanelPollEvery = "20s"
	subscriptionPanelTarget    = "#subscription-panel-body"
)

// subscriptionPanelFiles is the panel's template set.
var subscriptionPanelFiles = []string{filepath.Join("web", "ui", "partials", "subscription_panel.html")}

// subscriptionSignature is what a poll compares against the moment the QR
// was shown: plan, status and paid-until. A renewal or a plan change
// alters it; an unchanged active plan (an upgrade QR still open) does not.
func subscriptionSignature(ctx context.Context, r entitlement.Reader) string {
	var parts []string
	for _, k := range []string{entitlement.KeyPlan, entitlement.KeySubscriptionStatus, entitlement.KeyExpiresAt} {
		v, _, _ := r.Get(ctx, k)
		parts = append(parts, strings.TrimSpace(v))
	}
	return strings.Join(parts, "|")
}

// hasPaidPlan: the cached plan is a paid one, so "Renew current plan"
// means something.
func (v subscriptionView) hasPaidPlan() bool {
	return v.Plan != "" && v.Plan != string(entitlement.PlanLocal)
}

func renderSubscriptionPanel(w http.ResponseWriter, r *http.Request, locale, name string, data map[string]any) {
	httpx.RenderWith(subscriptionPanelFiles, httpx.FuncsFor(locale))(name, data)(w, r)
}

// subscriptionPanelNotReady renders the "not registered / registered via
// the main till" message (as Check now does) and reports true when the
// till cannot hand off.
func subscriptionPanelNotReady(w http.ResponseWriter, r *http.Request, d *common.Deps, locale string) bool {
	if enroll.HasCredentials(d.Cfg) {
		return false
	}
	key := "settings.enrol.not_registered"
	if enroll.CurrentStatus().ViaMainTill {
		key = "settings.enrol.registered_via_main"
	}
	renderSubscriptionPanel(w, r, locale, "subscription_panel.html", map[string]any{
		"step": "message", "message": httpx.T(locale, key),
	})
	return true
}

func registerSubscriptionPanel(mux *http.ServeMux, d *common.Deps, posRepo *data.POSRepo) {
	mux.HandleFunc("POST /api/subscription/panel", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = r.ParseForm()
		back := r.Form.Get("back") == "1"
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			hidden := []elevationHiddenField{}
			if back {
				hidden = append(hidden, elevationHiddenField{Name: "back", Value: "1"})
			}
			renderElevationPrompt(w, r, "/api/subscription/panel", subscriptionPanelTarget,
				httpx.T(locale, "elevation.summary.subscription_panel"), hidden, elev)
			return
		}
		if subscriptionPanelNotReady(w, r, d, locale) {
			return
		}
		ctx := r.Context()
		wasActive := entitlement.Describe(ctx, d.Settings, time.Now()).State == entitlement.StateActive
		outcome := refreshNotActive
		if !back {
			var err error
			outcome, err = refreshEntitlementNow(ctx, d)
			if err != nil {
				logging.L().Warnf("subscription panel: %v", err)
			}
			result := map[refreshOutcome]string{refreshActive: "active", refreshNotActive: "not_active", refreshTimedOut: "timed_out"}[outcome]
			settingsAudit(r, posRepo, elev, "enrollment", "-", "subscription_panel", map[string]any{"result": result})
		}
		view := subscriptionViewFor(ctx, d.Settings, time.Now(), locale)
		if (outcome == refreshActive || back) && view.State == string(entitlement.StateActive) {
			if !wasActive {
				w.Header().Set("HX-Trigger", "ut-subscription-changed")
			}
			renderSubscriptionPanel(w, r, locale, "subscription_panel.html", map[string]any{"step": "active", "sub": view})
			return
		}
		renderSubscriptionPanel(w, r, locale, "subscription_panel.html", map[string]any{
			"step":     "choices",
			"sub":      view,
			"offline":  outcome == refreshTimedOut,
			"canRenew": view.hasPaidPlan(),
		})
	})

	mux.HandleFunc("POST /api/subscription/panel/handoff", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = r.ParseForm()
		intent := r.Form.Get("intent")
		if intent != "renew" {
			intent = "change"
		}
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/subscription/panel/handoff", subscriptionPanelTarget,
				httpx.T(locale, "elevation.summary.subscription_panel"),
				[]elevationHiddenField{{Name: "intent", Value: intent}}, elev)
			return
		}
		if subscriptionPanelNotReady(w, r, d, locale) {
			return
		}
		m := enroll.Effective(d.Cfg).Marketplace
		target := enroll.PlansURL(m.EndpointURL, m.StoreID, intent, locale)
		var qr template.URL
		if png, err := qrcode.Encode(target, qrcode.Medium, 220); err == nil {
			// A fixed data:image/png prefix over our own encoder's bytes.
			qr = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png))
		}
		settingsAudit(r, posRepo, elev, "enrollment", "-", "subscription_handoff", map[string]any{"intent": intent})
		// The poll takes no PIN (it runs on a timer), so a session that got
		// here only through a manager's PIN (a cashier elevated) would stop
		// 20 s later. Show the stopped guidance from the start instead of
		// promising re-checks it cannot make (review of ut-docs#4286).
		poll := map[string]any{"pollStopped": true}
		if canCheckSubscription(d, r) {
			poll = subscriptionPanelPoll(0, subscriptionSignature(r.Context(), d.Settings))
		}
		renderSubscriptionPanel(w, r, locale, "subscription_panel.html", map[string]any{
			"step":   "handoff",
			"intent": intent,
			"url":    target,
			"qr":     qr,
			"poll":   poll,
		})
	})

	mux.HandleFunc("POST /api/subscription/panel/poll", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = r.ParseForm()
		n, err := strconv.Atoi(r.Form.Get("n"))
		if err != nil || n < 0 {
			n = subscriptionPanelPollMax
		}
		n++
		was := r.Form.Get("was")
		stopped := map[string]any{"pollStopped": true}
		// No PIN prompt on a timer: anyone but a manager/admin on a till
		// that can check (or an unregistered one) just stops.
		if !canCheckSubscription(d, r) || !enroll.HasCredentials(d.Cfg) {
			renderSubscriptionPanel(w, r, locale, "subscription_panel_poll", stopped)
			return
		}
		if n > subscriptionPanelPollMax {
			// A spent or forged count: stop without asking the cloud.
			renderSubscriptionPanel(w, r, locale, "subscription_panel_poll", stopped)
			return
		}
		ctx := r.Context()
		// ADR-0148 §2: each re-check is an operator check-in. Wait for its
		// answer (as step 1 does) so a renewal shows on THIS poll, not the
		// next one 20 s later (tester-found, ut-docs#4286).
		if _, err := refreshEntitlementNow(ctx, d); err != nil {
			logging.L().Warnf("subscription panel poll: %v", err)
		}
		if entitlement.SyncAllowed(ctx, d.Settings) && subscriptionSignature(ctx, d.Settings) != was {
			w.Header().Set("HX-Retarget", subscriptionPanelTarget)
			w.Header().Set("HX-Reswap", "innerHTML")
			w.Header().Set("HX-Trigger", "ut-subscription-changed")
			view := subscriptionViewFor(ctx, d.Settings, time.Now(), locale)
			renderSubscriptionPanel(w, r, locale, "subscription_panel.html", map[string]any{"step": "active", "sub": view})
			return
		}
		if n >= subscriptionPanelPollMax {
			renderSubscriptionPanel(w, r, locale, "subscription_panel_poll", stopped)
			return
		}
		renderSubscriptionPanel(w, r, locale, "subscription_panel_poll", subscriptionPanelPoll(n, was))
	})
}

// subscriptionPanelPoll is the step-3 poller's data: the count so far and
// the signature the QR was shown against.
func subscriptionPanelPoll(n int, was string) map[string]any {
	return map[string]any{"pollN": strconv.Itoa(n), "was": was, "pollEvery": subscriptionPanelPollEvery}
}

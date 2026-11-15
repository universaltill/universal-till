package pages

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Cloud-terms acceptance on operator-initiated registration
// (ut-docs#4717, ut-docs#4720). Every UI path that registers this till with
// the cloud (Settings -> Register now, the auto-register opt-in toggle, the
// setup wizard's opt-in) needs the operator to tick "accept_terms" once per
// terms version; enroll.RecordTermsAcceptance then stores it per till and
// enroll's register() sends it. The plugin store's first Download and the
// install-from-marketplace API also ask (ut-docs#4791, termsGateLazyRegistration);
// other lazy-registration triggers stay ungated (see that helper).

// termsTicked reports whether the form carries the accept_terms checkbox.
func termsTicked(r *http.Request) bool {
	v := r.Form.Get("accept_terms")
	return v == "on" || v == "1"
}

// termsAlreadyAccepted reports whether the current terms version is already
// accepted on this till.
func termsAlreadyAccepted(ctx context.Context, d *common.Deps) bool {
	_, _, _, ok := enroll.AcceptedTerms(ctx, d.Settings)
	return ok
}

// termsView is the template data of the accept step: the checkbox label, the
// link to the terms, or - when already accepted - the muted "Terms accepted"
// line.
func termsView(ctx context.Context, d *common.Deps, locale string) map[string]any {
	v := map[string]any{
		"url":     enroll.TermsURL(locale),
		"label":   fmt.Sprintf(httpx.T(locale, "settings.enrol.terms.label"), enroll.TermsVersion),
		"version": enroll.TermsVersion,
		// ut-docs#4792: false when the acceptance is stored on this till but
		// the cloud has not confirmed it yet (registered before the terms
		// step, or the till was offline).
		"sent": enroll.TermsSent(ctx, d.Settings),
	}
	if version, _, at, ok := enroll.AcceptedTerms(ctx, d.Settings); ok {
		date := at
		if t, err := time.Parse(time.RFC3339, at); err == nil {
			date = t.Format("2006-01-02")
		}
		v["accepted"] = true
		v["line"] = fmt.Sprintf(httpx.T(locale, "settings.enrol.terms.accepted"), version, date)
	}
	return v
}

// termsGateLazyRegistration is the accept step for the plugin store's lazy
// registration (ut-docs#4791). A till that is not yet registered and has not
// accepted the current terms may only register (enroll.EnsureRegistered) once
// the operator ticked the box: ticked records the acceptance (register()
// then sends it) and returns refuse=false; not ticked returns refuse=true and
// the caller answers 428 settings.enrol.terms_required without registering.
// Already registered or already accepted: refuse=false, nothing recorded.
//
// Deliberately NOT gated, so a till can still register without the step:
// the setup wizard's base-plugin install (offline-first, must not block the
// wizard; its step 7 shows the accept step), cloud-directed installs, plugin
// update and TSE kickoff. Such a till is recorded not_accepted and Settings
// (ut-docs#4792) offers the box afterwards.
func termsGateLazyRegistration(ctx context.Context, d *common.Deps, ticked bool, locale string) (refuse bool, err error) {
	if enroll.CurrentStatus().Registered || termsAlreadyAccepted(ctx, d) {
		return false, nil
	}
	if !ticked {
		return true, nil
	}
	if err := enroll.RecordTermsAcceptance(ctx, d.Settings, locale); err != nil {
		return false, err
	}
	return false, nil
}

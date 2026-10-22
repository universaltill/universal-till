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
// enroll's register() sends it. Non-UI triggers (EnsureRegistered from a
// plugin install) never ask and stay as they were.

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

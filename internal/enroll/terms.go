package enroll

import (
	"context"
	"strings"
	"time"
)

// TermsVersion is the cloud-service terms version this till build asks the
// operator to accept. It must be a version ut-cloud's internal/terms
// publishes, else the cloud answers 400 terms_version_unknown to
// POST /v1/stores/register (ut-docs#4717, ut-docs#4720).
const TermsVersion = "v0.1-draft"

// Per-till settings holding the operator's acceptance. They belong to the
// till that registers, so they are per-till (data.PerTillSettingPrefixes,
// db.TillCloudIdentityPrefixes) and never sync or join-copy to another till.
const (
	keyTermsVersion    = "marketplace.terms_version"
	keyTermsLocale     = "marketplace.terms_locale"
	keyTermsAcceptedAt = "marketplace.terms_accepted_at" // RFC3339 UTC
)

// TermsURL is where the operator reads the cloud terms in their language.
// Paths are region-tagged (ADR-0038); the interim page is published by
// ut-docs#4670. English is the operative text, so every locale other than
// German and Turkish links the English page.
func TermsURL(locale string) string {
	site := "en-gb"
	switch termsLang(locale) {
	case "de":
		site = "de-de"
	case "tr":
		site = "tr-tr"
	}
	return "https://universaltill.com/" + site + "/legal/cloud-terms/"
}

// termsLang is the lowercased primary language subtag ("de-AT" -> "de").
func termsLang(locale string) string {
	l := strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(l, "-_"); i >= 0 {
		l = l[:i]
	}
	return l
}

// RecordTermsAcceptance stores that the operator accepted the current
// TermsVersion now, in the given UI locale (kept as its language subtag).
// Callers invoke it only after an explicit tick on an operator-initiated
// registration; nothing here ever records an acceptance on its own.
func RecordTermsAcceptance(ctx context.Context, kv Settings, locale string) error {
	for _, e := range [][2]string{
		{keyTermsVersion, TermsVersion},
		{keyTermsLocale, termsLang(locale)},
		{keyTermsAcceptedAt, time.Now().UTC().Format(time.RFC3339)},
	} {
		if err := kv.Set(ctx, e[0], e[1]); err != nil {
			return err
		}
	}
	return nil
}

// AcceptedTerms returns the stored acceptance; ok is true only when it is of
// the current TermsVersion (an older acceptance does not cover a new text).
// register() sends the acceptance only when ok. Non-UI triggers
// (EnsureRegistered from a plugin install) never ask: they send an
// acceptance only if one is already recorded.
func AcceptedTerms(ctx context.Context, kv Settings) (version, locale, acceptedAt string, ok bool) {
	version, _, _ = kv.Get(ctx, keyTermsVersion)
	if version != TermsVersion {
		return "", "", "", false
	}
	locale, _, _ = kv.Get(ctx, keyTermsLocale)
	acceptedAt, _, _ = kv.Get(ctx, keyTermsAcceptedAt)
	return version, locale, acceptedAt, true
}

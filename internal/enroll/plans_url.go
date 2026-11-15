package enroll

import (
	"net/url"
	"strings"
)

// WebBase is the cloud's public web base for an API endpoint URL: the
// endpoint minus a trailing "/" and "/api". The claim URL and the plans
// handoff (ut-docs#4286) are both built on it, so the till never
// hard-codes a cloud or my. host (multi-cloud / on-prem).
func WebBase(endpointURL string) string {
	return strings.TrimSuffix(strings.TrimRight(endpointURL, "/"), "/api")
}

// cloudDefaultRegion is the locale form the cloud's URLs use for a bare
// till language (the cloud's own locales are language-region, lowercase in
// URLs — /en-gb/, /tr-tr/). A display-language default, not a market rule:
// it only picks which translation of my.'s plans page opens.
var cloudDefaultRegion = map[string]string{
	"en": "en-gb",
	"de": "de-de",
	"tr": "tr-tr",
	"ar": "ar-sa",
	"fa": "fa-ir",
	"pt": "pt-pt",
	"fr": "fr-fr",
	"es": "es-es",
	"it": "it-it",
}

// cloudFallbackLocale is the cloud's own default locale.
const cloudFallbackLocale = "en-gb"

// CloudLocale maps a till UI locale ("fa", "en-US", "de_DE") to the
// lowercase language-region form the cloud's ?lang= takes. A region tag the
// cloud ships is kept; any other tag falls back to its language's default
// region, then to en-gb. Anything that is not a plain language tag is
// en-gb, so nothing a caller passes can reach the URL unchecked.
func CloudLocale(tillLocale string) string {
	tag := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(tillLocale), "_", "-"))
	base, region, hasRegion := strings.Cut(tag, "-")
	if !isLetters(base, 2, 3) || (hasRegion && !isLetters(region, 2, 2)) {
		return cloudFallbackLocale
	}
	if hasRegion && (tag == "en-us" || cloudDefaultRegion[base] == tag) {
		return tag
	}
	if l, ok := cloudDefaultRegion[base]; ok {
		return l
	}
	return cloudFallbackLocale
}

func isLetters(s string, minLen, maxLen int) bool {
	if len(s) < minLen || len(s) > maxLen {
		return false
	}
	for _, c := range s {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}

// PlansURL is the cloud's public plans redirect for this store
// (ut-cloud's GET /plans, ut-docs#4286): <web base>/plans?store=…&intent=…
// &lang=…, which 302s to my.'s plans page for that store. intent is
// "renew" or "change" (anything else is "change"). It carries no secret —
// my. still asks the owner to sign in and checks ownership.
func PlansURL(endpointURL, storeID, intent, tillLocale string) string {
	if intent != "renew" {
		intent = "change"
	}
	q := url.Values{}
	q.Set("store", storeID)
	q.Set("intent", intent)
	q.Set("lang", CloudLocale(tillLocale))
	return WebBase(endpointURL) + "/plans?" + q.Encode()
}

// PartnerURL is the cloud's public Support-partner redirect for this store
// (ut-cloud's GET /partner, ut-docs#5108): <web base>/partner?store=…&lang=…,
// which 302s to my.'s Support partner screen for that store, where the
// shop's owner connects or disconnects a partner. It carries no secret —
// my. still asks the owner to sign in and checks ownership.
func PartnerURL(endpointURL, storeID, tillLocale string) string {
	q := url.Values{}
	q.Set("store", storeID)
	q.Set("lang", CloudLocale(tillLocale))
	return WebBase(endpointURL) + "/partner?" + q.Encode()
}

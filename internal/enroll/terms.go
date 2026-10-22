package enroll

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/config"
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
	// keyTermsSentVersion is the terms version the cloud has confirmed for
	// this till (register() sent it, or POST /v1/stores/terms answered 200).
	// Same terms_ prefix, so it is per-till and never joins/syncs.
	keyTermsSentVersion = "marketplace.terms_sent_version"
)

// ErrNoTermsAcceptance: SendTermsAcceptance was called with no acceptance of
// the current TermsVersion recorded on this till.
var ErrNoTermsAcceptance = errors.New("no cloud-terms acceptance recorded on this till")

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

// TermsSent reports whether the cloud has been told about this till's
// acceptance of the current TermsVersion.
func TermsSent(ctx context.Context, kv Settings) bool {
	v, _, _ := kv.Get(ctx, keyTermsSentVersion)
	return v == TermsVersion
}

// markTermsSent stores that the cloud has the acceptance of version v.
func markTermsSent(ctx context.Context, kv Settings, v string) error {
	return kv.Set(ctx, keyTermsSentVersion, v)
}

// SendTermsAcceptance sends the stored acceptance of an already registered
// till to the cloud (POST /v1/stores/terms, store-token Bearer;
// ut-docs#4792): a till registered before the terms step, or through a
// plugin install, never sent one. On 200 it records the sent marker. An
// error leaves the local acceptance intact so the operator can retry; the
// caller logs the raw error and shows a translated message.
func SendTermsAcceptance(ctx context.Context, cfg *config.Config, kv Settings) error {
	m := Effective(cfg).Marketplace
	storeID, token := currentStoreAuth(m)
	if m.EndpointURL == "" || storeID == "" || token == "" {
		return ErrTillNotRegistered
	}
	version, locale, _, ok := AcceptedTerms(ctx, kv)
	if !ok {
		return ErrNoTermsAcceptance
	}
	fields := map[string]string{"store_id": storeID, "terms_version": version}
	if locale != "" {
		fields["terms_locale"] = locale
	}
	payload, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	url := strings.TrimRight(m.EndpointURL, "/") + "/v1/stores/terms"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return newCloudHTTPError("terms", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return markTermsSent(ctx, kv, version)
}

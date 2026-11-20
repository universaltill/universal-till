# Review: accept merchant-terms-v1.0 at enrolment (ut-docs#6449, legal B3/C8)

Scope: `internal/enroll/terms.go`, `terms_test.go`, `web/locales/en.json`,
`web/i18n-meta/en.meta.json`, `web/help/en/claim.md`.

## What changed
- `TermsVersion` is `merchant-terms-v1.0` (was `v0.1-draft`); `TermsURL` and
  `TermsURLChromeless` link `/<site>/legal/merchant-terms/`.
- Re-ask needed no new code: `AcceptedTerms` and `TermsSent` compare the stored
  value to `TermsVersion`, so a till holding `v0.1-draft` shows the accept step
  again and is never reported as sent.
- English wording "cloud service terms" -> "Merchant Terms" (en only, ADR-0160).

## Tests
URL for every locale, version pin, chromeless URL, and a till with a stored
`v0.1-draft` acceptance (and sent marker) is asked again. No network.

## Findings
- Deploy order: release with or after the ut-cloud PR for #6448 and after the
  pages are live; the cloud rejects unknown versions (`terms_version_unknown`).
- Weblate re-flags the four changed en strings; other locales keep the old
  "cloud service terms" wording until translated (consent text, legal review).

# Code review — a registered till accepts the cloud terms later (ut-docs#4792)

Date: 2026-10-22 · Branch: `feat/4792-accept-terms-registered` · Follow-up
to ut-docs#4720 (part of #4671). The cloud side is ut-cloud#576 (merged:
`POST /api/v1/stores/terms`). Built by Sonnet, reviewed by an independent
Opus pass (lane:cloud-41).

## What shipped

- `internal/enroll/terms.go`:
  - `SendTermsAcceptance` posts the stored acceptance to
    `/v1/stores/terms` with the till's store-token Bearer. It works like
    `ClaimCode`: `ErrTillNotRegistered` when the till isn't registered, and a
    `RegisterHTTPError` on a non-200.
  - `ErrNoTermsAcceptance` when nothing is recorded.
  - A per-till marker `marketplace.terms_sent_version`. The existing
    `marketplace.terms_` prefix keeps it per-till, and a scope test row pins
    that. It is set on a 200, and by `register()` when it sent the acceptance.
  - `TermsSent`.
- `POST /api/enrol/terms`:
  - Always 200, same HTMX shape as Register now.
  - Order: a till that isn't registered (including a replica registered via
    its main till) is refused; the tick is validated before `checkOrElevate`
    so no approver PIN is burnt; elevation carries the tick; the acceptance
    is recorded locally; then it is sent.
  - The raw error is only logged. The operator sees a translated message.
  - A success writes an audit row and an out-of-band swap removes the accept
    block.
  - The demo build denies the route.
- Settings → Till registration (registered till) has three states:
  - accepted and sent: the muted line, unchanged;
  - not accepted: the accept box and **Accept terms**;
  - accepted but not yet sent (offline): the muted line, a note and **Send
    to the cloud**.
- Six new keys in en/ar/fa/tr with `en.meta.json` metadata (the terms
  wording is `review: legal`). Help `claim.md` in all 5 languages: one
  paragraph and two troubleshooting entries.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | A till that registered with terms through #4720 before this change has no sent marker, so it shows "not reached the cloud yet" and a Send button once | **Accepted**: pressing it re-records the same acceptance with a fresh audit row, which does no harm. Those builds shipped days ago and there are no real shops yet |
| 2 | minor | A `400 terms_version_unknown` (cloud older than the till's version) shows the generic "Couldn't reach the cloud" message | **Accepted**: the version constant ships with the matching cloud. The raw code is logged |
| 3 | nit | The local acceptance gets no till audit row when the send then fails; the audit row is written on success only | **Accepted**: the same as Register now. The acceptance timestamp is stored, and the cloud audits the delivered one |

## Verified

- TDD re-run by the reviewer. With the handler's send call stubbed out and
  the marker write removed, these failed:
  `TestEnrolTerms_TickRecordsSendsMarksAndAudits`, `…CloudDownKeepsAcceptanceLocally`,
  `…ServiceRefusedShowsTheCloudUnavailableMessage`,
  `…AcceptedButUnsentResendsWithoutTick` and
  `TestSendTermsAcceptanceSendsStoredAcceptanceAndSetsMarker`. All passed
  again once the code was restored.
- `go test ./internal/pages/... ./internal/enroll/... ./internal/data/...`
  is green. `gofmt` is clean, `go build ./...` passes and `golangci-lint`
  reports 0 issues.
- These guards pass: i18n, i18n-meta, help-topics, help-drift,
  rtl-logical-css, no-showmodal, compliance-claims, competitor-naming,
  mail-addresses, data-access, demo-env, core-neutral, kiosk-engine.
- `go test ./...`: `internal/plugins` timed out locally after 600s. That
  package is untouched and imports nothing changed here; CI decides.
- No driven browser run this cycle. The three settings states are rendered
  and asserted in `enrol_terms_registered_test.go`. There are no new
  screenshots because the help text has no image for this card.
- Language packs: the six new keys go to de/es/pt in follow-up PRs in the
  same cycle (`check-lang-pack-drift.sh`).

## Verdict

Safe to merge once CI is green.

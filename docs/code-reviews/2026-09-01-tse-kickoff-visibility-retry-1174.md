# 2026-09-01 — TSE kickoff failure visibility, manual retry, audit trail (ut-docs#1174)

## Summary

A German (`DE`) shop's setup wizard kicks off TSE (fiscal signing device)
provisioning with Universal Till Cloud. If the cloud definitively rejects the
kickoff (`not_configured`, `subscription_inactive`, …), nothing previously
told the operator — the wizard finished silently and Settings' existing
`tseProvisioning` banner could be dismissed away permanently with no way
back. This adds a manual retry, makes the notice non-dismissable while the
shop genuinely has no working TSE, surfaces a fast rejection inline right
after the wizard, and audits every provisioning state transition.

**No fiscal-signing logic changed.** `internal/fiscal/fiscal.go`'s
`EvaluateGate`/`RequiresHardGate` already hard-blocks every DE/TR sale
unconditionally whenever `fiscal.tse_configured` is false, independently of
anything in this change — verified untouched. This is scoped entirely to
operator-visibility, retry, and audit logging.

**Out of scope, tracked separately:** ut-docs#1265 (the cloud/fiskaly test
credentials aren't wired up in the homelab deployment yet — infra/secrets
access this pipeline doesn't have) and ut-docs#1174's own acceptance
criterion 2 ("on the test Pi, retry against the sandbox-configured cloud
reaches a provisioned test TSE") — no physical hardware access from a cold
cloud session. The code is ready for someone with hardware + #1265 to
exercise the real E2E path.

## Change

- `internal/pages/setup_tse.go`:
  - `POST /api/settings/retry-tse-provisioning`'s logic (`retryTSEProvisioning`):
    requeues a `kickoff_rejected`/`credential_failed` state to
    `pending_kickoff` (keeping the identity) and makes ONE synchronous,
    time-boxed kickoff attempt, same budget as the wizard's own attempt.
    Requires a **complete** stored identity (mirrors `requeueTSEKickoff`'s
    existing guard) — refuses otherwise rather than POSTing an empty
    `business_identity` to the cloud.
  - `tseProvisioningDismissBlocked`: a hard-gated country's (DE/TR, via
    `fiscal.RequiresHardGate` — never a second hardcoded `"DE"`) provisioning
    record is **not dismissible at all** while it exists, regardless of
    status. Every status this key is ever set to means "not configured yet"
    (`finishTSEProvisioning` clears the whole record to `nil` on success,
    the only way `fiscal.tse_configured` flips true), so this is
    structurally correct rather than an enumerated list of "failure"
    statuses — see "Independent review" below for why that distinction
    mattered.
  - `auditTSEProvisioning`: logs `tse_provisioning_state_changed`
    (`entityType="fiscal"`, `entityID="tse"`, payload `{status,
    error_code}`) on every real transition (accepted, rejected, credential
    failed, configured, manual requeue) — actor `"system"` for
    wizard-hook/background-ticker-driven transitions, the real session user
    for the manual retry/dismiss. Only fires after the underlying
    `saveTSEProvisioningState`/`Settings.Set` succeeds — never audits a
    transition that didn't actually persist.
- `internal/pages/settings_page.go`:
  - `POST /api/settings/dismiss-tse-provisioning` now answers **409**
    (localized body) instead of clearing state when
    `tseProvisioningDismissBlocked`; audits `tse_provisioning_dismissed` on
    a successful dismiss.
  - New `POST /api/settings/retry-tse-provisioning`, same manager gate as
    dismiss; re-renders the block for its htmx swap.
- `web/ui/partials/tse_provisioning_block.html` (new, extracted from
  `settings.html` so the retry endpoint can re-render identical markup):
  Retry button shown only when retryable; Dismiss button replaced by a
  "why" hint when blocked.
- `internal/pages/setup_page.go`: after the wizard's own synchronous kickoff
  attempt, a definitive rejection is carried to the wizard's landing page as
  `?tse_setup=rejected` — **only** when that page is `/` (the only page that
  reads it; appending it to `/import`/`/catalog` would be a dead marker).
- `internal/pages/index_page.go` / `web/ui/pages/index.html`: `/` re-verifies
  the REAL stored state before rendering a one-shot dismissible banner — the
  query param alone can never conjure a warning that isn't true.
- `web/locales/{en,fa,ar,tr}.json`: 4 new keys (`settings.tse.retry_btn`,
  `.dismiss_blocked`, `.nothing_to_retry`, `.see_settings`).
- `web/help/{en,fa,ar,tr}/display.md`: manual item 17 describing the notice,
  Retry, and the non-dismissable failed state.
- `web/help/img/manifest.json`: regenerated via a real `make docs-shots` run
  (92 screenshots, all pass); 3 unrelated `sell.png` encoder-noise byte
  diffs (20–29 bytes, no visible content change, confirmed by re-running the
  guard after reverting them) reverted per the 2026-08-27 #1112 precedent.
- `internal/pages/setup_tse_test.go`: extended/added tests — see
  Verification.

## Verification

| Check | Result |
|---|---|
| `gofmt -l .` / `go build ./...` / `go vet ./...` | clean / clean / clean |
| `go test ./...` (full) | green |
| `guard-i18n.sh` | ✓ 1322 keys, all locales match |
| `guard-data-access.sh` | ✓ |
| `guard-compliance-claims.sh` | ✓ |
| `guard-help-topics.sh` | ✓ |
| `guard-docs-shots.sh` | ✓ (after real `make docs-shots`) |
| Real driven run | app + fake cloud on localhost; DE wizard kickoff accepted live; seeded `kickoff_rejected`/`subscription_inactive`; `/settings` showed Retry present/Dismiss absent + blocked hint; dismiss → 409, state preserved; retry → 200, re-rendered `awaiting_ready`, fake cloud hit once, audit rows recorded under `system` (wizard) then the real actor (manual retry); `/?tse_setup=rejected` banner rendered with the real message; plain `/` did not |

**TDD**: every new/changed behavior had a failing test first, confirmed
red for the right reason, then green after the fix — the dismiss-block
change, the retry endpoint (3 variants), the audit entries (3 variants),
and the wizard/index inline surfacing (2 variants + a guard test). Full
list of new/changed tests: `TestSettingsShowsTSEProvisioningChipAndDismiss`
(updated), `TestSettingsTSEBlock_HardGatedUnresolvedShowsRetryHidesDismiss`,
`TestDismissTSEProvisioning_AllowedWhenPendingAndAudited` (moved to a
non-hard-gated country, see below), `TestDismissTSEProvisioning_NonHardGatedCountryStillDismissible`,
`TestRetryTSEProvisioning_RetriesRejectedKickoff`,
`TestRetryTSEProvisioning_NoRetryableStateIsCleanError`,
`TestRetryTSEProvisioning_RetriesCredentialFailed`,
`TestDismissTSEProvisioning_BlockedRegardlessOfStatusAfterRetry`,
`TestRetryTSEProvisioning_IncompleteIdentityIsCleanError`,
`TestTSEAudit_WizardKickoffRejected`, `TestTSEAudit_KickoffAccepted`,
`TestTSEAudit_CredentialOutcomes`, `TestSetupWizardDE_RejectedKickoffSurfacesInline`,
`TestSetupWizardDE_PendingKickoffDoesNotSurfaceInline`,
`TestIndexTSEBanner_RequiresRealRejectedState`.

## Independent review (two rounds, different-model, Opus)

Build: Fable subagent. Review: Opus subagent, fresh context, no prior
involvement.

**Round 1 found 2 blockers**, both fixed and independently re-verified:

- **B1 — a 2-click retry-then-dismiss bypass.** The first cut of
  `tseProvisioningDismissBlocked` enumerated specific "failure" statuses
  (`kickoff_rejected`/`credential_failed`). Reviewer reproduced, against the
  real handlers: retry a `kickoff_rejected` state while the cloud is only
  *transiently* unreachable → `tseKickoffAttempt`'s transient branch leaves
  the state at `pending_kickoff`, a status the block-list didn't cover → the
  record became freely dismissible again, silencing the operator's only
  explanation while the hard gate still refuses every sale. **Fix**: the
  block is no longer status-enumerated — ANY stored record for a hard-gated
  country blocks dismiss, since every status this key holds means
  "not configured yet." Regression test:
  `TestDismissTSEProvisioning_BlockedRegardlessOfStatusAfterRetry` (mutation-tested
  by the reviewer against the old logic — fails as expected, passes after the
  fix).
- **B2 — lang-pack drift would block `main`.** The 4 new locale keys were
  missing from `ut-plugin-language-de` and `ut-plugin-language-es`;
  `check-lang-pack-drift.sh` is advisory on a PR but a hard failure on push
  to `main`. **Fix**: real German/Spanish translations added to both packs
  (PRs universaltill/ut-plugin-language-de#130,
  universaltill/ut-plugin-language-es#129) — merged before this PR per the
  cross-repo lane-ownership rule ("the lane that merges a core change owns
  its implied pack follow-up, in the same cycle").

Also fixed in the same pass (non-blocking notes from round 1, cheap and
in-scope): **N1** `retryTSEProvisioning` now requires a complete identity
(a `credential_failed` state can carry a zero identity if it was reached
after a dismiss of an in-progress state before this fix; retrying it would
have POSTed an empty `business_identity`); **N2** the wizard's
`?tse_setup=rejected` marker is now only appended when the redirect target
is `/` (it was previously a dead marker on `/import`/`/catalog`); **N3**
`auditTSEProvisioning` now only fires after its preceding state save
actually succeeds, on all four background call sites plus dismiss.

**Round 2** (scoped strictly to the B1/B2/N1-N3 fixes, not a full re-review
— earned by round 1 finding blocker-class issues, per this pipeline's
process-depth rule): confirmed B1 closed structurally (traced every writer
of the provisioning-state key; a cleared record for a hard-gated country
always implies `tse_configured == true`), re-mutation-tested both B1 and N1
fixes independently, confirmed N3's audit-after-save ordering on all 5 call
sites, confirmed N2 doesn't suppress the plain (non-csv_excel) wizard path,
and confirmed both pack PRs' content and their own `check-key-drift.sh`
pass against core's updated `en.json`. Found one more non-blocking nit
(**N7** — `finishTSEProvisioning`'s "configured" audit was conditioned on
the state-*clear* succeeding rather than the load-bearing
`fiscal.tse_configured` write) — fixed by hoisting the audit call above the
clear, so a failed clear can no longer suppress the record that TSE really
did become configured. Two cosmetic dead-code nits in the redirect-marker
logic were also cleaned up (a `sep`/`strings.Contains` computation that was
unreachable by construction once the marker is gated to `redirectTo ==
"/"`, and an avoidable settings read on the `csv_excel` branch).

**Verdict: safe to merge.**

## Outcome

Merge order: universaltill/ut-plugin-language-de#130 and
universaltill/ut-plugin-language-es#129 merge first (so
`check-lang-pack-drift.sh` stays green on this repo's own push to `main`),
then this PR.

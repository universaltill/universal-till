# Review: registration and claim-code failures show translated messages, never the cloud's body (ut-docs#3990)

- **Date:** 2026-10-10
- **Lane:** lane:cloud-41 (author: Opus 5.5)
- **Reviewer:** Sonnet 5.5, in a separate worktree. The routing table asks for Fable, but the Fable request was refused (out of usage credits), so Sonnet reviewed per `MODEL-ROUTING.md`.
- **Card:** universaltill/ut-docs#3990 (a follow-up to #3861)

## What shipped

1. **Claim this store** (`POST /api/enrol/claim-code`):
   - `enroll.ClaimCode` now returns a typed `*RegisterHTTPError` (op `claim-code`) and the sentinel `ErrTillNotRegistered`.
   - The handler logs the raw error and renders only translated text:
     - a refused shop gets `settings.enrol.service_unavailable`;
     - an unregistered till gets `settings.enrol.not_registered`;
     - anything else gets the new key `settings.enrol.claim_failed`.
   - Before this change, the handler printed the cloud's escaped JSON, including the reason the shop was refused.
2. **Replica refusal:**
   - `registerDeviceID` returns `*RegisterHTTPError` (op `device register`).
   - The main till's `POST /api/sync/cloud-device` now answers 403 `{"error":"service_unavailable"}` when the cloud refuses its shop. It used to answer 502 `cloud_unavailable`. Only the code crosses the LAN; the cloud's message stays in the main till's log.
   - `primarySource.RequestVouch` turns that answer into a typed refusal, so `IsServiceRefused` holds on the replica and its card shows the service-unavailable text.
   - An older main till still answers 502, and the replica shows the generic message, as before.
3. **Misconfiguration and busy:**
   - `RegisterNow` returns `ErrNotConfigured`, and `ErrAttemptBusy` (which still wraps `ctx.Err()`).
   - The `enrolFailureKey` helper maps them to the new keys `settings.enrol.not_configured` and `settings.enrol.busy`.
4. **Locales:** 3 new keys in en, ar, fa and tr. The de, es and pt pack PRs follow once core is merged.
5. **Manual:** a new section in `web/help/{en,de,ar,fa,tr}/claim.md`, "When Register now or Claim this store fails". The German text quotes the de pack's real labels.
6. **Screenshots:** the docs-shots surface hash was refreshed (`Docs-Shots-Unchanged`). No manual screenshot shows these error-only branches.

## Tests (TDD: each test failed against `main` for the stated reason before the fix)

- **pages:**
  - `TestClaimCode_ServiceRefusedShowsTranslatedMessageOnly`
  - `TestClaimCode_OtherFailureShowsGenericMessageNotRawBody`
  - `TestClaimCode_UnregisteredTillSaysNotRegistered`
  - `TestEnrolNow_NotConfiguredHasOwnMessage`
  - `TestEnrolNow_BusySlotHasOwnMessage`
  - `TestSyncCloudDevice_RelaysServiceRefusal`
  - All drive the real mux.
- **enroll:**
  - `TestRegisterNowOnReplicaCarriesMainTillsServiceRefusal`
  - `TestRegisterNowOnReplicaOtherMainTillFailureIsNotRefusal`
  - `TestVouchForReplicaReportsServiceRefusal`
  - `TestRegisterNowNotConfiguredIsSentinel`
  - `TestRegisterNowBusySlotIsSentinel`
- **Reviewer re-verification:** the reviewer independently reverted the production files for the claim-code, relay and replica tests and saw each one fail for the right reason:
  - the raw-body leak;
  - `status 502, want 403`;
  - `main till answered 403 … want IsServiceRefused`.
  - All three passed again after restore. The `-race` runs were clean.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `POST /api/enrol/pair` still renders `err.Error()`, the same leak class, on the Pair path. | Out of this card's scope and not a regression. Filed as a follow-up Backlog card. |
| 2 | minor | `enroll.Pair` keeps its own free-text "not configured" and "slot held" errors. | Same follow-up card as finding 1. |
| 3 | nit | The busy test leaves a background `RegisterNow` running until cleanup. | Accepted. It is not racy: `<-entered` orders it, and cleanup closes the channel and waits. |
| 4 | nit | The German claim help still mixes "beanspruchen" and "übernehmen" in older lines. | Accepted, out of scope. The new section uses the pack's term. |

The reviewer confirmed these checks:

- Nothing branches on the old error strings.
- The relay leaks nothing beyond the code.
- A 403 from a newer main till does not trigger the replica's self-registration fallback, which handles only 404, 405 and 401.
- The ar, fa and tr translations match the neighbouring `settings.enrol.*` terms.

## Verified beyond automated tests

The real binary was driven against a fake cloud that answers 403 `service_unavailable` with a secret reason in the body:

- **Claim code on a registered till:** shows the translated text only. The log carries the body.
- **Register now on an unregistered till:** shows the translated text only.
- **Claim on an unregistered till:** shows "This till is not registered yet."

Screenshots of the Settings → Till registration card were taken at 1024×600 in en and fa (RTL) and looked at. The message wraps inside the card.

Not looked at:

- the phone width, 360 px. The span is the same pre-existing element as #3861's;
- the dark theme;
- the claim-code card's own screenshot. That card renders only once enrolled; its branch was checked over HTTP.

## Gate

- `gofmt` clean, `go build`, `golangci-lint` 0 issues.
- All `build`-job guards pass, except these environment-only ones:
  - `shellcheck` is not installed in this container, and no shell script changed;
  - the two docs-shots self-tests plant files and need a clean tree. They passed after commit.
- `go test ./...` is green.

## Verdict

Safe to merge.

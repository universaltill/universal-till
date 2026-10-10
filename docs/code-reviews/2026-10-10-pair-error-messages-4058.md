# Review — "Pair with a shop" no longer renders the raw error (ut-docs#4058)

**Date:** 2026-10-10 · **Lane:** cloud-24 · **Author model:** Sonnet 5.5 · **Reviewer model:** Opus 5.5 (independent, fresh context, separate worktree)

## What shipped

- `POST /api/enrol/pair` rendered `html.EscapeString(err.Error())` after
  "Pairing failed". Operators saw untranslated English: the cloud's error
  code (`cloud answered 403 service_unavailable`), or a dial error naming
  the endpoint. This is the same leak #3861 and #3990 fixed for Register
  now and Claim this store.
- `enroll.Pair` now returns typed errors:
  - `ErrNotConfigured` when there is no endpoint.
  - `ErrAttemptBusy` (wrapping `ctx.Err()`) when the attempt slot is held.
  - `*RegisterHTTPError{Op: "pair"}` for a non-200 answer. It replaces the
    package-private `redeemError`, so `IsServiceRefused` recognises a 403
    `service_unavailable`.
- Only the sanitised error code (`redeemErrCodeRe`) goes into the error.
  The raw body never does, which keeps `Pair`'s invariant that errors
  never carry the pairing code or the token.
- The handler keeps its two typed refusals (store mismatch, replica without
  a store). Any other error is logged and mapped through `enrolFailureKey`.
  An unmapped error shows `settings.enrol.pair_failed` alone. No new
  locale keys.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | The environment-pinned identity refusal (`UT_MARKETPLACE_*` set) is still an untyped error. It now shows the generic "check the code and the internet connection" text instead of the English reason. | **Accepted, follow-up card.** This only happens on installer/developer setups, the code is not spent, and the reason is still logged. A proper message needs a new key plus language-pack PRs; filed as ut-docs#4062. |
| 1b | nit | An empty or over-long code also gets the generic text. | Accepted: the input is `required`, and the generic text already says "check the code". |
| 2 | nit | Network/HTTP failures are logged twice (in `Pair` and the handler). | Accepted: the handler line also covers the early returns `Pair` doesn't log. |
| 3 | nit | The shared `not_configured` / `busy` texts say "register". | Accepted: understandable in the pair dialog. |
| 4 | nit | The `!status.Registered` branch joins two sentences with ": ". | Pre-existing and practically unreachable. Left as is. |
| 5 | nit | `TestEnrolFailureKey_CoversPairErrors` passes on `main`. | Accepted: it guards the mapping. The handler tests and `TestPairBusySlotIsSentinel` guard the fix. |
| 6 | nit | `web/help/en/claim.md` lists the failure messages only under Register now. | Not stale (it never quoted the raw reason). Left as is. |

## Verification

- **TDD re-verified by the reviewer.** With only `pair.go` and
  `settings_page.go` reverted to `main`, every new or changed test failed
  for the right reason except #5. Examples: the rendered body leaked
  `dial tcp … connection refused` and `cloud answered 403 …`, and
  `postPair` returned `redeemError`. All tests pass on the restored tree.
  The enroll Pair tests also pass with `-race -count=3`.
- **Gate:**
  - `gofmt`, `go build ./...`, `go vet ./...` and `golangci-lint run ./...` (0 issues) are clean.
  - `go test ./...` passes in every package except `internal/plugins`. That package (WASM compile tests, not touched by this diff) hit the 10-minute default timeout on this 4-core container while the reviewer's test run was loading it too; CI's run is the check for it.
  - Guards pass: data-access, i18n, core-neutral, page-http-error, netaccess, kiosk-engine, card-data-schema, compliance-claims, competitor-naming, deadcode-baseline-growth, help-topics, help-drift, no-showmodal and no-inline-handlers.
  - `guard-deadcode-baseline` could not run locally: the deadcode tool was built with go1.26 and the repo needs go1.27. It runs in CI's desktop job. `shellcheck` is not installed here; no shell script changed.
- **Visual:** the change only affects the text inside the existing
  `#pair-msg` error span (same markup and class). Handler tests assert the
  rendered HTML. I did not take a screenshot or drive the dialog in a
  browser.

## Verdict

Safe to merge.

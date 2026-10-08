# Review — keep D4's rotated credential from a sync 402 (ut-docs#3651)

**Date:** 2026-10-08 · **Card:** universaltill/ut-docs#3651 · **Pair:** ut-cloud `fix/3651-rotate-on-402`
**Author:** Opus 5.5 (dev subagent) · **Reviewer:** Fable (independent, different model)

## What shipped
- `internal/cloudsync`: `post` now wraps a new `postBody`. `postBody` also hands a non-200 body to its one caller, `pushSync`; every other endpoint still gets `nil`.
- On a sync `402 plan_required`, `pushSync` calls `applyPlanRequiredCredential`, which decodes `data.device_id`/`data.device_token` (bounded by `cloudErrorMaxBytes`) into locals. It then applies them through `enroll.ApplyRotatedCredential`: own device id only, never over an env-pinned token. After that it caches the entitlement as before.
- The tick still fails with the same 402, so Retry-After and the ADR-0148 gating are unchanged. The token never reaches `statusError`, its `Error()` text or any log.

Why: ut-cloud now answers an unpaid store's legacy till with its ADR-0116 D4 credential inside the 402, for tills at v0.30.23 or later. Without this half the till would discard the credential and later be stranded on `401 token_retired`.

## Findings (shared review with the ut-cloud half)
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major (process) | The cloud floor is 0.30.23, so this change must ship in v0.30.23. | Merge this PR first, before any release. The cloud PR checks the tag before merging. |
| 3 | minor | `applyPlanRequiredCredential` comment claimed exact parity with the 200 path, but it skips `ApplyRotatedCredential`'s unsaved-credential retry when the fields are absent. | Comment fixed. Harmless, because `planUnpaidCheckin` runs `RetryUnsavedCredential` before every unpaid POST. |
| 6 | nit | `postBody` returns the body for every non-200. | Accepted: only the 402 branch reads it, and parsing is bounded. |

## Verified
- TDD re-verified by the reviewer in a separate worktree. With `cloudsync.go` reverted, `TestPushSync402_RotatedCredentialIsKept` fails (`rotate_credential_test.go:246: marketplace.token = "legacy-shared-token-0001"…`). Restored, it passes. The other three new tests (foreign id, no fields, other endpoint) guard against regressions and pass against both versions by design.
- The tests also assert the token is absent from `err.Error()`, from `%v/%+v/%#v` of the error, and from captured logs.
- `gofmt`, `go build ./...`, `go vet`, `golangci-lint` (v2.14, 0 issues), full `go test ./...`. Guards: data-access, core-neutral, netaccess, i18n and pipefail are OK. Deadcode-baseline reports only `internal/logging` functions that the desktop root reaches; that root is skipped locally without GTK headers, and CI analyses it. They are unrelated to this diff.
- No UI or user-facing strings, so no help topic or screenshots apply.

## Verdict
Safe to merge.

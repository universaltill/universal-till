# Review: TestFlight upload fails when no internal tester can see the build (ut-docs#3217)

**Date:** 2026-10-02 · **Lane:** `lane:cloud-24` · **Branch:** `fix/3217-testflight-visibility-guard`
**Author model:** Opus 5.5 · **Reviewer:** Fable (independent subagent), complexity medium

## What shipped

- `scripts/asc-testflight-check/` — stdlib-only Go command. After the upload it
  signs an ES256 App Store Connect token from the `.p8` the job already wrote,
  polls `/v1/builds` (with `include=buildBetaDetail`) until the build is
  `VALID` and its internal beta state is `READY_FOR_BETA_TESTING` /
  `IN_BETA_TESTING`, then requires one **internal** beta group that can see the
  build (`hasAccessToAllBuilds` or the build attached) with ≥1 tester. Fails
  with a message naming the missing piece (no internal group / group without
  testers / no group sees the build / `FAILED`, `INVALID`,
  `MISSING_EXPORT_COMPLIANCE`, `PROCESSING_EXCEPTION` / timeout).
- `ios-testflight.yml`: new step after the export/upload, before the
  `always()` key cleanup; secrets via `env:` only. `timeout-minutes` 60 → 120.
- `scripts/ci/ios-testflight-workflow_test.sh` pins the step (exists, runs the
  checker for `com.universaltill.pos`, sits between the upload and the key
  cleanup) with a self-test fixture.
- `ios/README.md` "Signing and TestFlight" documents the new failure modes.

## Findings (Fable review) and outcome

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | Tester count relied only on `meta.paging.total` (optional in Apple's schema) → possible false "no testers" on every run | **Fixed**: fall back to `len(data)` (limit=1); test with `meta` omitted |
| 2 | major (mitigated) | `VALID` ≠ installable: `MISSING_EXPORT_COMPLIANCE`/`PROCESSING_EXCEPTION` would pass | **Fixed**: `buildBetaDetail.internalBuildState` polled and checked; tests |
| 3 | minor | FAILED/INVALID tests passed even if the short-circuit was removed (timeout message contained the state) | **Fixed**: assert "processing ended …", no "within", exact poll count |
| 4 | minor | Same-host token guard tested only directly | **Fixed**: end-to-end test with an off-host `links.next`; other host gets 0 requests |
| 5 | minor | Retry path untested | **Fixed**: 503, 503, 200 test |
| 6 | minor | One transient blip aborted the 40-min wait | **Fixed**: `transientError` polled through until the deadline; test |
| 7 | minor | Timeout re-run uploads a new build; thin job timeout slack | **Fixed**: README sentence; job timeout 120 |
| 8 | nit | Raw error body in `::error::`; random missing-flag order | **Fixed**: body flattened + capped at 1 KB; missing flags sorted. Not done: guard pin of `-timeout` ≤ job timeout (accepted; both live in one file, comment explains the budget) |

## Verified beyond the unit tests

- TDD: tests written first and failed (no implementation). Each fix
  re-verified by mutation: removing the paging fallback, transient tolerance,
  same-host check, retries, export-compliance check, or FAILED short-circuit
  each turns a named test red; restored, all pass.
- The workflow guard fails when the checker call is removed (mutated the real
  workflow) and its self-test fixture trips the new message.
- `go build ./...`, `go test ./...` (full suite), `go test -race` on the
  package, `golangci-lint` (0 issues), every guard in `ci.yml`'s build job
  (only environmental failures: local shellcheck 0.11 vs pinned 0.9, and
  `retry-with-backoff.sh` which is a helper needing arguments).
- Not runnable here: a real App Store Connect call (needs the signing secrets
  on a macOS runner). The next `ios-testflight` dispatch is the live check.

## Security

The key is only read from the existing `$RUNNER_TEMP` file; tokens are minted
per request (15-min life); pagination links to any other host are refused
before the bearer token is attached; errors log the URL path only.

## Verdict

Safe to merge.

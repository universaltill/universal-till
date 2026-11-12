# Review: re-run a release macOS job GitHub never gave a runner (ut-docs#5175)

Date: 2026-10-30 · Branch: `fix/5175-release-macos-runner-retry` · Built by
Sonnet 5.5, reviewed by Opus 5.5 (fresh context, read-only, mutation-tested).

## What happened

Release run 40889814547 published v0.35.3 with 15 assets and no macOS `.dmg`.
`macos-app` (`macos-14`) was cancelled after ~15 min with no steps run. Its
check-run annotation reads "The job was not acquired by Runner of type hosted
even after multiple attempts", alongside "Due to capacity constraints, jobs
targeting macOS arm64 runners may experience longer queue times". The cause
was GitHub's hosted macOS arm64 capacity, not our config. The earlier run
40882053145 lost `macos-app` and `verify-versions` the same way. In that run
`publish-release` was skipped, and `cleanup-orphaned-tag` removed the tag.

## Recovery (done this cycle)

On run 40889814547 we used "Re-run failed jobs" (attempt 2). `macos-app` got a
runner and succeeded, and `macos-dmg-attach` attached the `.dmg`.
`cleanup-orphaned-tag` stayed skipped. v0.35.3 now has 16 assets, and
`checksums.txt` lists `unitill-pos-0.35.3-macOS-arm64.dmg`.

## What shipped

- `scripts/ci/release-runner-retry.sh` calls `rerun-failed-jobs` only when all
  three of these hold:
  - `publish-release` succeeded.
  - Every failed, cancelled or timed-out job is `cancelled` and carries the
    "was not acquired by Runner" annotation.
  - `run_attempt < MAX_ATTEMPTS` (3).

  A failing `gh` call fails the script.
- `.github/workflows/release-runner-retry.yml` runs on `workflow_run` of
  `Release` when the run's conclusion is not `success`.
  - Top-level permissions are `{}`. The job has `contents: read`,
    `actions: write` and `checks: read`.
  - Event values reach the script only through `env:`.
- `scripts/ci/release-runner-retry_test.sh` has 12 cases and is wired into
  ci.yml's `build` job. Comments in `release.yml` above `macos-app` and in
  `docs/arch/desktop-app.md` were updated.

## Review findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | The test missed four mutations: removing the publish gate, `\|\| true` on the annotations call, `\|\| true` on the rerun POST, and removing the `main` call | Fixed. Cases (d2) starved `verify-versions` with `publish-release` skipped, (h), (i), and (j), which executes the script directly with a stub `gh` on PATH. The orchestrator re-ran all four mutations plus "script missing" in a temp copy, and each fails the test. |
| 2 | Low | Docs did not say a starved `verify-versions` is not retried | Fixed in desktop-app.md |
| 3 | Low | The attempt limit was worded three different ways | Fixed. All three now say "at most once in practice (bounded by MAX_ATTEMPTS=3)". |
| 4 | Low | `actions/checkout` relied on the repo being public | Fixed: `contents: read` |

The reviewer also checked these and found no problem:
- A re-run touches only `macos-dmg-attach`, which uses `--clobber` and is
  designed to run again. `cleanup-orphaned-tag` stays skipped on a re-run.
- `checksums`, `publish-release` and `verify-versions` do not depend on
  `macos-app`.
- `filter=latest` behaves correctly on attempt 2 and later, and a job id is
  the same as its check-run id.
- No event value is interpolated into a script, and no PR code is checked out.

## Verified beyond the automated tests

- The live recovery above, checked against the release's asset list and
  `checksums.txt`.
- `shellcheck` reports 0 issues on the new scripts, and `actionlint` is clean
  on the new workflow.
- `guard-ci-tests-sandboxed.sh` passes.
- Every `bash scripts/ci/…` step in ci.yml's `build` job passes, apart from
  `guard-shellcheck-version`. That guard failed only because this sandbox has
  shellcheck 0.11 and the guard pins 0.9, which CI's runner provides.
- No Go code changed.

## Not verified

The workflow itself has not run yet. It will first run on the next Release
that does not succeed. A re-run started with `GITHUB_TOKEN` may not fire a
further `workflow_run`, so expect one automatic retry.

Verdict: safe to merge.

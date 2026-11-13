# Review: a starved arm runner no longer stops a release (ut-docs#5205)

Date: 2026-10-30 · Branch: `ci/5205-release-arm-starvation` · Built by
Sonnet 5.5, reviewed by Opus 5.5 (fresh context, own worktree,
mutation-tested, checked against real release runs via the API).

## What shipped

- `.github/workflows/release.yml`
  - `windows-installer`, `macos-app`, `android-app` and `verify-versions`
    also need `needs.goreleaser.result != 'skipped'`. Before, a skipped
    goreleaser (for example the hosted `ubuntu-22.04-arm` leg of
    `linux-shells` never getting a runner) let all four run and fail while
    waiting for a release that never existed. One starved job then looked
    like several real failures. They still run after a goreleaser
    *failure*, as before.
  - `cleanup-orphaned-tag` first asks
    `release-runner-retry.sh keep-tag`. On `keep` it warns and leaves the
    tag and draft in place; otherwise it deletes them, as before. The job
    gained `actions: read` and `checks: read`.
  - The dead `grep -qx "dev"` branch is gone from verify-versions' `check()`
    and from the macos-app `.dmg` loop. `strings` drops runs shorter than 4
    characters, so it never fired. An unstamped build still fails on the
    version check, and that error message now says so.
- `scripts/ci/release-runner-retry.sh`
  - New verdict `PRERUN`: publish-release did not succeed, and every failed
    job was runner-starved. It re-runs only on attempt 1
    (`PREPUBLISH_MAX_ATTEMPTS=2`), because a re-run started with
    GITHUB_TOKEN may not fire another `workflow_run`.
  - New `keep-tag` mode uses the same rule, so cleanup keeps the tag
    exactly while an automatic retry is still coming. An API error means
    `delete`, the old behaviour.
- Tests: `release-runner-retry_test.sh` gained pre-publish cases, keep-tag
  cases (including direct execution through a stub `gh`) and a wiring
  section for release.yml. `release-marketplace-key_test.sh` gained an
  unstamped-build fixture and a "no 3-char dev grep" check.
- Docs: `docs/arch/desktop-app.md` and the script and workflow headers.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | If keep-tag says keep but no retry happens (workflow_run not delivered, retry starved, rerun POST fails), the tag burns a version number. | **Partly fixed.** A failing rerun now prints an `::error::` saying the tag must be deleted by hand. The cleanup warning already says so. A self-deleting retry is **deferred** to ut-docs#5209. |
| 2 | minor | cleanup runs about 3 s after the starved job's cancel. If the annotation is not readable yet, it deletes the tag, and the later retry re-runs into a missing tag. The result is a wasted run; nothing is published. | **Deferred** to ut-docs#5209: check the tag exists before a PRERUN re-run. |
| 3 | minor | A *cancelled* (starved) goreleaser or android-app still cascades into real-looking failures. This is less likely than arm starvation and is not a regression. | **Deferred** to ut-docs#5209. |
| 4 | minor | The wiring test did not pin `= keep` (a fail-open `!= delete` mutant survived). | **Fixed**, and mutation-checked. |
| 5 | nit | Two mutants survived: a RERUN verdict giving keep, and an unknown CLI argument falling through to `main`. | **Fixed** with new cases. |
| 6 | nit | The unstamped-fixture assertions pass on the old code too. | **Fixed** with a comment that says so; the literal-grep assertions are the regression catchers. |
| 7 | nit | A docs line was too long, and a paragraph's wording was stale. | **Fixed.** |

## Verified beyond the unit tests

- The reviewer read real runs through the API:
  - Run 40882053145: one job was starved and the run concluded `failure`,
    not `cancelled`, so the `!cancelled()` gates hold.
  - The annotation text matches exactly.
  - Matrix legs are listed as `linux-shells (ubuntu-22.04-arm, arm64)`.
  - Run 40889814547 attempt 2: "Re-run failed jobs" re-runs skipped
    dependents and `cleanup-orphaned-tag`. `event_name` stays
    `workflow_dispatch`, so cleanup's guard still holds.
- `if [ "$(… keep-tag)" = keep ]` under `set -euo pipefail` was checked in
  bash. A crash or empty output means delete, never keep.
- TDD was re-verified in a separate worktree. With the old script and
  workflow restored, `release-runner-retry_test.sh` fails 24 checks and
  `release-marketplace-key_test.sh` fails 2. With the change, both pass.
- Gates: `release-runner-retry_test.sh`, `release-marketplace-key_test.sh`,
  `release-macos-nonblocking_test.sh` and `release-job-timeouts_test.sh`
  pass. shellcheck 0.11 on `scripts/ci/*.sh` reports 0 issues. Both
  workflows parse as YAML.

## Verdict

Safe to merge. The next arm-starved release run is the live check: cleanup
should keep the tag, and release-runner-retry should re-run the arm leg.

# Code review — every release job is time-bounded (ut-docs#4059)

## What shipped

- **`.github/workflows/release.yml`**: a job-level `timeout-minutes` on
  the eight jobs that had none. Bounds are several times the slowest of
  the last 7 releases: `prepare` 10 (≤0.4 min seen), `linux-shells` 20
  (≤1.9), `windows-installer` 30 (≤1.9, plus a ≤10 min "wait for release"
  loop), `android-app` 30 (≤6), `verify-versions` 20 (≤1.2), `checksums`
  15 (≤0.3), `publish-release` 10 (≤0.3), `cleanup-orphaned-tag` 10. The
  four jobs that already had one (`release-tests`, `goreleaser`,
  `macos-app`, `macos-dmg-attach`) are unchanged.
- Both **"Build osslsigncode (pinned commit)"** steps (`goreleaser`,
  `windows-installer`): step-level `timeout-minutes: 10`, and `apt-get
  update` runs with `-o Acquire::Retries=3 -o Acquire::http::Timeout=30`
  through `scripts/ci/retry-with-backoff.sh 3 5 --clear-apt-lists`, the
  same helper ci.yml uses for this step (ut-docs#1933).
- **`packaging/windows/install-osslsigncode.sh`**: `apt-get install` gets
  the same Acquire options; the clone runs under `timeout 300` and prints
  an `::error::` naming the card when it fails or times out.
- **`scripts/ci/release-job-timeouts_test.sh`** (new), wired into
  ci.yml's `build` job next to the macOS non-blocking test: every job
  under `jobs:` needs a job-level timeout; every osslsigncode step needs a
  step timeout and the bounded apt options; the install script needs the
  clone `timeout` and the apt options. A self-test on a synthetic
  workflow (an unbounded job, a job key with a trailing comment) keeps
  the parser honest.

Trigger: release run 38031306576 (v0.31.6), whose `windows-installer`
sat in the osslsigncode step for 30+ min while the release stayed a draft.

## TDD

The test was written first. On the unchanged tree it failed with 17
checks (8 jobs without a timeout, 3 per osslsigncode step, 3 in the
install script); re-verified by the orchestrator with the change stashed.
Green after the change. Mutation: removing `checksums`' timeout fails the
test naming that job.

## Review (Opus 5.5, fresh context; Sonnet built it)

Verdict: safe to merge, no blockers.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `jobs_of` skipped a job key with a trailing comment (`  job: # …`) → that job false-passes | Fixed: shared `JOB_KEY` regex; self-test covers it |
| 2 | minor | `job_block` ran past a following commented job key and borrowed its timeout | Fixed: same regex ends the block; self-test covers it |
| 3 | nit | A clone timeout exited 124 with no message | Fixed: `::error::` line |
| 4 | nit | Acquire retries don't cure a "Hash Sum mismatch" mirror | Fixed: `retry-with-backoff.sh --clear-apt-lists`, as in ci.yml |
| 5 | nit | Self-test covers only the job parser, not the step parser | Accepted: the step parser reports "no step found" rather than false-passing when its shape changes |

Reviewer also checked: bounds against wait loops (the release-wait
loops exit on the first try once goreleaser has made the draft), `timeout`
under `set -euo pipefail` (124 aborts), ci.yml's own call of the install
script (unaffected), SIGPIPE safety (here-strings only).

## Verified

- `release-job-timeouts_test.sh` and `release-macos-nonblocking_test.sh` pass.
- `shellcheck` on the new test and the install script: 0 issues.
- `actionlint` on release.yml/ci.yml: same findings before and after
  (pre-existing SC2046/SC2034, none in changed lines).
- No UI, locale or help change; no secrets touched.
- Not verified here: a real release run. The next release exercises it;
  a hung step now fails within 10 min and is recovered with "Re-run
  failed jobs".

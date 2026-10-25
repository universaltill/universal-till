# Review: guard-test sandbox turns off auto-gc / auto-maintenance (ut-docs#4941)

Opus 5.5 wrote it; Sonnet 5.5 reviewed it independently.

## What shipped

- `enter_guard_test_sandbox` sets `gc.auto 0` and `maintenance.auto false` in the
  sandbox repo right after `git init`.
- Why: `main` build 92cd6c8e failed after `docs-shots-locale-drift_test.sh`
  passed every check, with `rm: cannot remove '/tmp/…/.git/objects': Directory
  not empty`. The test's own `git commit` can start git's auto gc/maintenance,
  which detaches by default and keeps writing objects while the EXIT trap's
  `rm -rf` runs.
- New case in `guard-test-sandbox_test.sh`: the sandbox repo has both settings,
  read with `--local` so a global CI config cannot satisfy it. Without the fix
  the case fails ("FAIL: the sandbox repo has auto-gc and auto-maintenance off",
  checked by hand). With the fix it passes.

## Findings

- Root cause is plausible: `git commit` runs `gc --auto`/`maintenance run --auto`,
  detached by default. Both switches cover the legacy and the new path. Accepted.
- Optional `core.fsmonitor false` / `core.untrackedCache false`: not added. Both
  are off by default and are not writers during cleanup.
- Read the test's config with `--local`: done.

## Verified

`guard-test-sandbox_test.sh`, `docs-shots-locale-drift_test.sh` and
`guard-ci-tests-sandboxed.sh` pass. `shellcheck` reports 0 issues on both
changed files.

The race itself cannot be reproduced on demand. The evidence is the CI log
and the reasoning above. A recurrence after this change would point elsewhere.

Verdict: safe to merge.

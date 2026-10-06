# Code Review — docs-shots guard: non-blocking on pull_request (ut-docs#2794)

Date: 2026-10-06
Branch: `fix/2794-docs-shots-guard-pr-mode`
Reviewer: independent Fable subagent (different model from the Opus author).
Scope: `scripts/ci/run-docs-shots-guard.sh` (new), `scripts/ci/run-docs-shots-guard_test.sh` (new), `.github/workflows/ci.yml`.

## Problem

`web/help/img/manifest.json`'s `surface_sha256` covers nearly the whole app
UI surface (`web/ui/**`, `web/public/**`, most of `internal/pages/**`), and
`scripts/ci/guard-docs-shots.sh` hard-fails the `build` job on both
`pull_request` and `push` to `main` whenever it's stale. Because the hash is
effectively global, any two concurrent UI PRs conflict with each other on
this generated file — whichever merges second has to merge main, run
`make docs-shots` (~3 min), push, and sit through the full CI run again
(~25 min), sometimes for several rounds (documented: 5 rounds in 2.5h on
2026-09-25). Hit again this same cycle landing universal-till#1709.

## What it delivers

- `run-docs-shots-guard.sh`: runs the real guard unchanged; if it fails on a
  `pull_request` event, prints a `::warning::` (including the guard's own
  exit code) and exits 0 instead of failing the job. On `push` (main) or any
  other/unset event it propagates the real failure untouched — so real
  drift still turns main red immediately after merge, handled like any
  other red-main CI.
- `guard-docs-shots.sh` itself, its three sibling test scripts, and
  `e2e/tests-docs/lib.js` are byte-for-byte unchanged — confirmed via
  `git diff --exit-code` on all five. No new ADR: this is a CI-enforcement-
  timing change, not a product/architecture decision (checked `adr/` for
  `docs-shots`/`surface_sha256` — no existing ADR governs it).
- `ci.yml`: the "Manual screenshot freshness guard" step now calls the
  wrapper instead of the guard directly; one new step added right after it
  running the wrapper's own regression test, matching the file's existing
  per-guard pattern. Diff is otherwise +3/-1.

A fuller fix (auto-regenerate and bot-commit to main after merge, so
screenshots literally never lag even transiently) was considered and
deliberately deferred — it needs commit-attribution allowlist changes and a
branch-protection review across repos, out of scope for this card. Filed as
a Backlog follow-up (ut-docs#TBD, filed by the orchestrator after this
review).

## TDD verification

Author's claim (red → green → mutation) independently re-verified by the
reviewer, not taken on faith:

- Wrapper moved aside → `run-docs-shots-guard_test.sh` fails for the right
  reason (`No such file or directory`, 6 assertions), not a path/env bug.
  Restored → green (11/11, then 12/12 after the two post-review tweaks
  below), byte-identical via `cmp` after restore.
- Reviewer's own mutations, each caught by the test: `exit "${status}"` →
  `exit 0` (3 fail-closed assertions fail); `=` → `!=` on the event check (6
  fail); redirecting the guard's own output to `/dev/null` (both
  "shows the real guard's own output" assertions fail).
- Fail-closed probed beyond the committed test cases: `pull_request_target`,
  `PULL_REQUEST`, padded/typo'd strings, `workflow_dispatch`, `merge_group`,
  unset, empty — all correctly hard-fail on a stale guard. `ci.yml`'s own
  triggers (`push: [main]`, `pull_request`) mean no other event ever reaches
  this job regardless.
- Exit-code plumbing (`cmd || status=$?` under `set -euo pipefail`) is the
  canonical errexit-safe idiom; `${GITHUB_EVENT_NAME:-}` is `set -u`-safe.
  Verified a stub exiting 2 propagates as 2 on push; a nonexistent
  `GUARD_DOCS_SHOTS_BIN` fails with bash's own clear "No such file" message
  rather than a confusing one.

## Review findings

No must-fix findings — safe to merge as submitted. Two nice-to-haves, both
applied before commit:

1. The `::warning::` text said "stale" for any non-zero guard exit
   (including a crash). Appended `(guard exit ${status})` so a crash isn't
   misread as ordinary staleness — `run-docs-shots-guard.sh` line ~33.
2. The test's local tempdir variable was named `TMPDIR`, shadowing the
   conventional `mktemp` env var. Renamed to `WORK_DIR` throughout
   `run-docs-shots-guard_test.sh`.

Both changes re-verified green: `run-docs-shots-guard_test.sh` (12/12),
`guard-docs-shots_test.sh`, `guard-docs-shots-cross-check_test.sh`,
`update-docs-shots-surface-hash_test.sh` all still pass unchanged, and
`python3 -c "import yaml; yaml.safe_load(...)"` confirms `ci.yml` still
parses.

## Disposition

Safe to merge.

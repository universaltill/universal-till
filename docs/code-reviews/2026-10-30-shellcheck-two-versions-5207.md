# 2026-10-30 — shellcheck version guard accepts 0.9.0 and 0.11.0 during the runner migration (ut-docs#5207)

## What shipped
- `scripts/ci/guard-shellcheck-version.sh`: the single `BASELINE_VERSION`
  becomes `ACCEPTED_VERSIONS=("0.9.0" "0.11.0")`, compared exactly. ubuntu-latest
  is migrating Ubuntu 24 (shellcheck 0.9.0) → Ubuntu 26 (0.11.0)
  (actions/runner-images#14748), and runners stay mixed while it rolls out.
  ut-docs#5206 (#2233) moved the pin to 0.11.0 only, so a job that lands on a
  not-yet-migrated runner fails "installed 0.9.0, expected 0.11.0". Messages
  now print the accepted list.
- `guard-shellcheck-version_test.sh`: passes for both accepted versions;
  refuses 0.10.0 (between them), 0.9.0-rc1 and 10.9.0 (near-misses), and the
  existing 0.11.3, 0.8.0, no-binary and unparseable cases.

The rest of ut-docs#5207 (the SC2329 directives and making main green on 0.11.0)
had already landed via ut-docs#5206 / universal-till#2233. This branch is the
remaining acceptance item: the regression test covers the two-version window.

## Review
Built with Sonnet; independently reviewed by Opus 5.5 in a fresh context.
- Exact-match logic, `set -u` with the array, ci.yml (no hard-coded version):
  no findings.
- Low: no near-miss test cases → **fixed** (0.9.0-rc1, 10.9.0 added).
- Low: header said "known-good baseline" (singular), and the rejection message
  cited only ut-docs#1955 → **fixed**.
- Low: during the window, 0.9.0 doesn't report SC2329, so a new trap-only
  function with only `disable=SC2317` can pass on a 24 runner and fail on a 26
  one. This comes with the mixed window. Accepted; the follow-up card that
  drops 0.9.0 ends it.

## Verified beyond the automated tests
- TDD: with the guard reverted, the new 0.9.0 case fails
  ("expected guard to accept … 0.9.0, but it rejected it"); restored, all
  cases pass.
- `shellcheck scripts/ci/*.sh` is clean on **both** real binaries
  (shellcheck-py 0.9.0.6 and 0.11.0.1). 0.9.0 ignores the unknown SC2329 code
  in the existing directives. The guard itself accepts the real 0.9.0 binary.
- Every `bash scripts/ci/*.sh` step of ci.yml's `build` job (120) passes
  locally. No Go code changed.

## Verdict
Safe to merge.

## Deferred
- Drop 0.9.0 from `ACCEPTED_VERSIONS` (and the test) once the migration is
  complete — Backlog card filed from ut-docs#5207.

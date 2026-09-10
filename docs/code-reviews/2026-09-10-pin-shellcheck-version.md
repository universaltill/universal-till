# 2026-09-10 — Pin/guard the shellcheck version used in CI

**Issue:** universaltill/ut-docs#1955
**Branch:** `fix/1955-pin-shellcheck-version`

## What shipped

The `Shellcheck (scripts/ci/*.sh)` step in `.github/workflows/ci.yml` relied
on whatever `shellcheck` binary `ubuntu-latest` happens to ship preinstalled,
with no version pin at all — unlike the adjacent `golangci-lint-action` step,
which pins an exact version (`v2.5.0`) for exactly this reason. A future
runner-image bump could silently change which findings shellcheck reports
and turn `main` red with no code change in this repo.

Chose the card's second suggested option (fail loudly on drift) over pinning
an exact binary, to avoid adding a new supply-chain dependency (a downloaded
release binary or a third-party GitHub Action) for a problem a pure shell
comparison already solves:

- New `scripts/ci/guard-shellcheck-version.sh`: runs `shellcheck --version`,
  parses the `version:` line, compares it against a hardcoded
  `BASELINE_VERSION="0.10.0"` (what `ubuntu-latest` shipped at the time this
  card was filed), and fails loudly with a clear message on any mismatch or
  if `shellcheck` isn't found on `PATH` at all.
- New `scripts/ci/guard-shellcheck-version_test.sh`: regression test using a
  disposable fake `shellcheck` binary placed earlier on `PATH` (so it's
  independent of whatever shellcheck the test runner actually has) —
  baseline accepted, newer/older version rejected, missing binary rejected,
  and unparseable `--version` output rejected *with* the intended
  diagnostic (see finding below).
- `.github/workflows/ci.yml`: two new steps ("Shellcheck version guard",
  "Shellcheck version guard regression test") added immediately before the
  existing "Shellcheck (scripts/ci/*.sh)" step, and that step's own comment
  updated to point at the new guard.
- `CLAUDE.md`: `guard-shellcheck-version.sh` added to the CI-blocking guard
  list, plus a short paragraph explaining the approach (mirrors the existing
  golangci-lint-version note).

## Independent review (fresh-context Sonnet subagent, per complexity:easy routing)

Reviewed cold, diff-only, with instructions to actually run things rather
than just read. Verified: `shellcheck` 0 issues on both new scripts; the
regression test's PATH-based mocking genuinely exercises the guard's real
`command -v shellcheck` resolution rather than bypassing it; the new
workflow steps parse as valid YAML with correct indentation, in the right
place; `CLAUDE.md`'s new prose accurately describes the guard; no secrets,
no real client/shop names; confirmed the diff touches no UI surface and no
shop-owner-visible behavior, so the UX checklist and the "manual ships with
the feature" rule don't apply here.

**Finding (real, fixed before merge):** the version-parsing line

```bash
actual_version="$(grep '^version:' <<<"${version_output}" | awk '{print $2}')"
```

under this script's own `set -euo pipefail`, silently killed the whole
script the moment `grep` found no match — e.g. an unexpected
`shellcheck --version` output format, or a broken binary printing nothing —
because `pipefail` propagates `grep`'s exit 1 through the pipe, and that
propagates through `set -e` on the bare assignment statement. This happened
*before* the very next block's own "could not parse a version" diagnostic
ever got to run, i.e. exactly the case that block was written to handle
never actually produced its intended message — the guard just died with no
output at all.

**Fix:** appended `|| true` to that pipeline (mirroring the identical
pattern already used one line above, `shellcheck --version 2>/dev/null ||
true`), and added a fifth regression-test case (a fake `shellcheck` that
prints output with no `version:` line at all) that checks not just for a
nonzero exit but for the specific "could not parse a version" diagnostic in
the output — a bare exit-code check would not have caught a regression back
to this bug, since the pre-fix code also exited nonzero, just silently.

**TDD-verified personally, not taken on the reviewer's word:** reverted the
`|| true` fix locally, re-ran `guard-shellcheck-version_test.sh`, confirmed
the new 5th case fails with exactly "guard rejected unparseable --version
output but without its intended diagnostic (silent failure regression?)" —
i.e. it reproduces the bug — then restored the fix and confirmed all 5
cases pass again.

## Verified beyond automated tests

- `gofmt -l .`, `go build ./...`, `go test ./...`, `golangci-lint run ./...`
  (0 issues) — this change touches no Go code, but the full gate was run
  anyway per the pipeline's "one full gate before commit" rule.
- `shellcheck scripts/ci/*.sh` (0 issues, including the two new files).
- Every other CI-blocking guard script in `scripts/ci/` run locally — all
  pass (the only pre-existing non-clean output, `guard-help-drift.sh`'s
  already-tracked/baselined drift entries per ut-docs#1962/#1973, is
  unrelated to this change).
- `guard-shellcheck-version.sh` run against this sandbox's actual installed
  shellcheck (`0.9.0`, via `apt-get install shellcheck` — this sandbox is
  not the real `ubuntu-latest` GitHub Actions runner) correctly reports the
  expected mismatch against the `0.10.0` baseline. That is the guard working
  as designed on a machine with a different shellcheck version than CI's,
  not a defect — CI's actual `ubuntu-latest` runner ships `0.10.0` per the
  original card, so the guard is expected to pass there.

## Safe-to-merge verdict

Safe to merge. No blocking issues remain; the one real finding was fixed
and independently re-verified.

## Explicitly deferred

Nothing deferred — this card's acceptance criteria are fully met by the
drift-detection approach (the alternative, pinning an exact downloaded
binary, was considered and explicitly not chosen — see "What shipped").

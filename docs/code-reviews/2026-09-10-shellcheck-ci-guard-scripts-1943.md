# Code review: shellcheck in CI for scripts/ci/*.sh (ut-docs#1943)

**Date:** 2026-09-10
**Card:** universaltill/ut-docs#1943
**PR:** fix/1943-shellcheck-ci-guard-scripts
**Complexity:** medium (Dev: Sonnet inline, Review: Opus subagent, fresh
context, different model from the implementation — per `MODEL-ROUTING.md`)

## What shipped

- A `shellcheck scripts/ci/*.sh` step added to `.github/workflows/ci.yml`'s
  `build` job, right after `golangci-lint`, before the 45+ Go-specific
  guard steps (fast failure first). `ubuntu-latest` ships shellcheck
  preinstalled, so no separate install step.
- Every pre-existing shellcheck finding across the 55+ scripts under
  `scripts/ci/` fixed or individually suppressed with a reason (69
  findings → 0), so the new gate starts green:
  - **Real bug fixed (SC2251):** `check-brand-assets.sh` had three checks
    written as a bare `! grep -Fq PATTERN FILE`. Under `set -euo
    pipefail`, bash explicitly does not trigger `errexit` on a command
    whose exit status is inverted with `!` — so these three checks
    silently did nothing when the forbidden marker *was* present, the
    exact violation they exist to catch. Confirmed empirically (a planted
    violation passed silently) before fixing. Replaced with an explicit
    `must_not_contain()` helper. This guard had no regression test at
    all before this change; added `check-brand-assets_test.sh`
    (backup/restore-via-`trap` convention, matching
    `guard-webkit-version_test.sh`).
  - `check-lang-pack-drift.test.sh` (SC2155 ×2): split
    declare-and-assign so a command-substitution failure in `tr` can't
    be masked.
  - `guard-android-manifest-features_test.sh` (SC2015): `A && B || C`
    rewritten as explicit `if`/`then`/`else`.
  - `guard-plugin-settings-bump_test.sh` (SC2016 ×4): single-quoted
    fixture bodies contain literal Go backticks (raw-string test data),
    not unexpanded shell variables — double-quoting instead, as
    shellcheck suggests, would make bash try to command-substitute them.
    Suppressed with a reason on the preceding comment line.
  - 11 files' `cleanup()` functions (SC2317, ×56 total including the new
    test file): false positive — each is invoked only via `trap cleanup
    EXIT`, which shellcheck cannot trace. Suppressed with a reason.
- `CLAUDE.md`'s "Before committing" guard list now documents
  `shellcheck scripts/ci/*.sh`.

## Independent review (Opus, fresh context, isolated from the Sonnet
implementation's reasoning)

**Verdict: SAFE TO MERGE after two working-tree fixes, both applied,
both re-verified.**

### Findings

1. **HIGH — the fix reintroduced the same fail-open class it was written
   to remove** (`check-brand-assets.sh`). The two recursive
   `must_not_contain` calls passed `-R` *after* the pattern
   (`grep -Fq 'PATTERN' -R "$dir"`). Only GNU grep permutes options that
   follow an operand — on BSD/macOS grep (which this script already
   supports; it carries a `shasum` fallback for exactly that reason) and
   on GNU grep under `POSIXLY_CORRECT`, `-R` is read as a filename, grep
   exits 2, and the naive `if grep ...; then fail; fi` treated that
   error identically to a clean "not found." Demonstrated live: a
   planted violation under `POSIXLY_CORRECT=1` waved through with an
   unrelated "No such file or directory" on stderr and guard exit 0.
2. **MEDIUM — the helper conflated grep's error exit (2) with "clean"
   (1).** Root cause of finding 1 surviving silently.
3. **MEDIUM — test coverage gap.** The original `check-brand-assets_test.sh`
   only exercised the single-file call shape; the recursive shape (the
   one that broke) had zero coverage.
4. **LOW — shellcheck's version is unpinned in CI**, unlike the adjacent
   golangci-lint step (`v2.5.0`). A future `ubuntu-latest` image bump
   could turn `main` red with no code change. Not blocking; filed as a
   follow-up (universaltill/ut-docs#1955).

### Fix applied (same PR, before merge)

`must_not_contain` now takes plain paths only, always runs
`grep -RFq -e "$pattern" "$@"` (`-R` before the pattern — `-R` on a plain
file just reads that file, so one call shape covers both single-file and
recursive callers), captures the real exit status, and treats anything
other than 0/1 as a hard failure with its own message. Two new test
cases added: the recursive-wordmark violation, and the same violation
under `POSIXLY_CORRECT=1` (pins the portability fix on Linux CI without
needing a Mac in the loop). Also corrected `CLAUDE.md`'s "same-line
disable=SCxxxx" wording — a shellcheck directive comment parses only
`key=value` pairs; trailing prose on that same line fails to parse
(SC1072/SC1073), which is why every suppression in this rollout actually
carries its reason on the *preceding* comment line.

### What the reviewer personally re-ran (not taken on trust)

- **TDD claim independently re-verified**: restored `check-brand-assets.sh`
  to its pre-fix (`HEAD`) content, ran the new test against it — it
  failed exactly as expected (exit 1, naming the planted violation as
  incorrectly accepted). Restored the fixed version — all cases green.
  Confirmed no stray fixtures/backups left in the working tree.
- `shellcheck scripts/ci/*.sh` → 0 findings, before and after the HIGH
  fix.
- All 15 `# shellcheck disable=` suppressions audited individually: all
  11 SC2317 files confirmed to genuinely use `trap cleanup EXIT`; all 4
  SC2016 sites confirmed to contain literal Go backticks where
  double-quoting would trigger real command substitution. No blanket
  suppression, no malformed directive (no trailing prose on a directive
  line) anywhere in the diff.
- Go toolchain: `gofmt -l .` (clean), `go build ./...`, `go vet ./...`,
  `golangci-lint run ./...` (0 issues), full `go test ./...` (green).
- All 12 touched `_test.sh` regression suites run individually: 11
  green; `guard-deadcode-baseline_test.sh` fails on missing cgo/GTK
  headers in this sandbox — confirmed identical failure against `HEAD`,
  i.e. pre-existing/environmental (that guard's real gate is the
  separate `desktop-shell` CI job), not a regression from this diff.
- CI YAML parses cleanly; step ordering confirmed sensible (shellcheck
  right after golangci-lint, before the Go-specific guards; the new
  brand-asset regression-test step directly follows its guard step,
  matching every other guard/test pairing in the file).

### Scope / AC

No scope creep — diff touches only the CI workflow, `CLAUDE.md`, and
`scripts/ci/*.sh`. All three of the card's acceptance criteria are met:
shellcheck runs as a blocking CI step; it passes clean (every finding
fixed or individually disabled with a stated reason, no blanket
suppression); `CLAUDE.md`'s guard list documents it.

## Verified beyond automated tests

- Ran the fixed `check-brand-assets.sh` guard directly under both plain
  and `POSIXLY_CORRECT=1` execution, on the real repo tree.
- Ran every one of the 23 `scripts/ci/guard-*.sh`/`check-*.sh` guard
  scripts individually against the real repo tree — all pass.
- Full `go test ./...` (not just the packages touched by this diff, of
  which there are none — this change touches no `.go` files).

## Follow-up filed

- universaltill/ut-docs#1955 — pin the shellcheck version used in CI
  (LOW, non-blocking).

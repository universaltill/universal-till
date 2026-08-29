# Code review — guard-webkit-version.sh doesn't scan ci.yml (ut-docs#1286)

- **Date:** 2026-08-29
- **Branch:** `fix/1286-guard-webkit-version-ci-yml`
- **Reviewer:** independent reviewer (fresh-context Sonnet, this pipeline's
  `complexity:easy` review tier — "different model" relaxes to "different
  instance" per the `reviewer` skill).
- **Verdict: SAFE TO MERGE.** One should-fix and one moderate finding, both
  fixed before this record was written; two accepted nits.

## What shipped

Found by independent review of `universal-till#624` (ut-docs#1071's desktop-
shell CI job): `scripts/ci/guard-webkit-version.sh` (ADR-0028: the Linux
desktop shell must target webkit2gtk-4.1, never the abandoned webkit2gtk-4.0)
grepped for a stray `webkit2gtk-4.0` reference across
`internal/thirdparty/webview_go`, `.github/workflows/release.yml` and
`.goreleaser.yaml` — but not `.github/workflows/ci.yml`, even though `#624`
had just added its own `libwebkit2gtk-4.1-dev` apt line to that exact file.
The guard's own stated threat model ("copy-pasting an old apt/goreleaser
snippet") is precisely the class of mistake this blind spot would miss.

The fix:

- Added `.github/workflows/ci.yml` to the guard's grep path list (one line).
- New `scripts/ci/guard-webkit-version_test.sh`, following the existing
  `guard-*_test.sh` pattern in this directory (`guard-data-access_test.sh`
  etc.): baseline pass on the real codebase, plant a violation, assert the
  guard rejects it, restore, assert the guard passes again.
- Wired the new test into `.github/workflows/ci.yml` as its own step, right
  after the existing "Linux desktop webkit2gtk version guard" step — same
  guard→regression-test pairing every sibling guard uses.

## Independent review — what was checked, and what it found

Dispatched to a fresh-context Sonnet subagent with no prior context on the
change, per the `complexity:easy` review tier. It flagged:

1. **[Should-fix, fixed] Real-file mutation was pattern-delete, not
   backup/restore.** The first draft of the test planted its fixture line
   directly in the real, tracked `.github/workflows/ci.yml` and removed it
   with `sed -i '/.../d'`. Every sibling guard test plants a *disposable*
   fixture file instead. If the process were killed between plant and
   cleanup (SIGKILL, OOM, a killed CI runner) the fixture comment line could
   survive uncommitted in a dev's working tree with nothing visibly broken
   (it's a YAML comment) and get silently committed later. **Fix:** the test
   now `cp`s `ci.yml` to a backup file before mutating, and both the
   `trap ... EXIT` and the normal-path cleanup restore from that backup via
   `cp` rather than a pattern match — a partial/interrupted run at worst
   leaves a `.guard_webkit_test_backup` file, never a corrupted `ci.yml`,
   and cleanup is idempotent (safe to run again against an already-restored
   file).
2. **[Moderate, fixed] No post-cleanup re-verification.** The first draft
   removed the fixture and reported success without re-running the guard to
   confirm removal actually worked — a drifted pattern could in principle
   leave the violation in place while the test still reported "all cases
   passed." **Fix:** added a final `expect_pass "ci.yml after the fixture is
   removed"` call after the restore, mirroring `guard-data-access_test.sh`'s
   own "guard still passes on the clean codebase" closing check.
3. **[Nit, accepted as-is] `sed -i` was GNU-only** (moot after fix #1 — the
   rewritten script no longer uses `sed -i` at all).
4. **[Nit, accepted as-is] Fixed file allowlist vs. a `.github/workflows/*`
   glob.** A future new workflow file with its own webkit2gtk line would
   reproduce this exact class of gap unless someone remembers to add it to
   the guard's explicit list again. Not fixed here: the card's own suggested
   fix was the one-line explicit addition, matching the existing
   `release.yml`/`.goreleaser.yaml` convention already in the guard: widening
   to a glob is a real improvement but a separate, deliberate scope decision
   (would also need checking no legitimate `.github/workflows/*.yml` webkit
   mention exists today) rather than folded into this minimal fix.

## Gates run, real output

- `bash scripts/ci/guard-webkit-version.sh` — passes on the real,
  unmodified codebase (before and after this change).
- `bash scripts/ci/guard-webkit-version_test.sh` — all three cases pass:
  clean-codebase baseline, fixture correctly rejected, and (post-fix) the
  restore is re-verified rather than assumed.
- `gofmt -l .` — empty (no `.go` files touched by this change).
- `git status`/`git diff --stat` after the test run — confirms
  `.github/workflows/ci.yml` is byte-identical to its pre-test state and no
  backup file (`ci.yml.guard_webkit_test_backup`) is left behind.

No Go build/test/vet run — this change touches only shell scripts and a
YAML workflow file, no `.go` source.

## Scope notes

- Deliberately did not widen the guard to a `.github/workflows/*.yml` glob
  (see finding 4) — kept to the card's own scoped, minimal fix.
- No i18n, money, data-access, or offline-first surface touched — this is a
  CI-tooling-only change with no product-behavior impact, so none of this
  product's other standing guards apply.

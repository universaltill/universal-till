# 2026-08-14 — Compliance-claims wording CI guard (ut-docs#681)

## What shipped

The product owner approved a fiscal-compliance wording denylist for the
German pilot on `ut-docs#667` (2026-08-13): certain phrases assert a legal
outcome ("GoBD-compliant", "revisionssicher", "you are compliant", claiming
we file the merchant's §146a notification) the product isn't entitled to
claim; ADR-0040 already binds this in principle, but nothing enforced it.

- `scripts/ci/guard-compliance-claims.sh`: scans `web/locales/*.json`,
  `web/help/**/*.md`, `web/ui/**/*.html` for the 20 approved forbidden terms
  (English + German outcome claims, plus concrete §146a-filing phrasings),
  case- and language-insensitive. Single `grep -n -i -F -f <patternfile>`
  pass per file — a naive bash while-read-per-line first draft took minutes
  on the real tree; this is ~0.5s. A `compliance-claim:allow` same-line
  marker (mirroring `guard-i18n.sh`'s `i18n:ignore`) suppresses a reviewed
  exception in help/UI files; locale JSON has no comment syntax, so that
  surface has no escape hatch (documented, accepted gap).
- `scripts/ci/guard-compliance-claims_test.sh`: same `expect_pass`/
  `expect_fail` fixture-dir convention as `guard-autofill-suppression_test.sh`
  — every one of the 20 forbidden terms individually, the help/UI surfaces
  independently, the allow-marker, per-surface fail-closed, and the real
  repo tree.
- `.github/workflows/ci.yml`: wired in after the i18n guard pair, matching
  the existing step style exactly.
- `CLAUDE.md`: new "Compliance wording (Germany pilot)" section, so a
  contributor writing German copy hits the rule before CI does, matching how
  every other mechanically-enforced guard is documented there.

## Independent review

Fresh-context Opus subagent, worktree-isolated (`complexity:medium` →
Sonnet built, Opus reviewed). Ran the guard and its test against the real
tree (both green, test genuinely fast — timed), parsed the CI YAML, and did
its own adversarial probing beyond the bundled test (line-wrap/HTML-tag/
whitespace/NBSP/Unicode-dash splits — all confirmed-and-accepted misses of
a line-based literal-substring check; JSON key vs. value — correctly
caught either way). Findings, triaged:

1. **MEDIUM — fixed.** This repo's own CI runner has no `LANG` set (the C
   locale, `ut-docs#662`'s own precedent), and `grep -i` only case-folds
   non-ASCII letters (Ü→ü) in a UTF-8 locale — so the guard would have
   **failed open in CI specifically** for capitalized forms of the one
   German term containing an umlaut
   (`wir übernehmen ihre §146a-anmeldung`), on the exact runner this guard
   exists to protect, for exactly the class of term ("the German forms are
   where the actual pilot risk sits") its own header comment calls out.
   Fixed: `export LC_ALL=C.UTF-8` at the top of the script. Verified with
   the reviewer's own repro (`LC_ALL=C` misses `Ü`→`ü`; `LC_ALL=C.UTF-8`
   catches it) and a new all-caps test case.
2. **MEDIUM — fixed.** The original fail-closed check used one combined
   counter across all three surfaces, so it would only fire if
   *everything* went empty at once — the realistic drift (a renamed
   extension, a moved tree, one surface's `find` glob going stale) is one
   surface silently going empty while the guard stays green. Fixed: three
   independent counters, each failing closed on its own. Re-verified the
   existing "forbidden term in a help topic" / "in a UI template" /
   "allow-marker" tests still exercise *real* detection rather than
   passing vacuously from an empty sibling dir under the new stricter
   check — they did not, at first (caught this myself while fixing #2):
   every planted-term fixture only populated the ONE surface under test,
   leaving the other two surfaces empty, so the new per-surface check
   would have failed them for the wrong reason. Fixed by giving every
   fixture a real baseline file in all three dirs.
3. **LOW-MEDIUM — fixed.** 7 of 20 terms (the whole §146a/German-filing
   block, including the one with the locale bug above) had no test
   coverage. Added all 7, plus the umlaut all-caps case.
4. **LOW — fixed.** Documented the line-based check's real detection gaps
   (line wraps, HTML-tag splits, whitespace/NBSP, Unicode dash look-alikes)
   in the script's own header, so the next reader doesn't over-trust it as
   exhaustive.
5. **LOW/informational — accepted, not changed.** The allow-marker
   suppresses every match on its line, not just the one it's meant to
   excuse. Matches `guard-i18n.sh`'s `i18n:ignore` convention exactly
   (same line-scoped behavior) — consistent with the rest of this repo's
   guards, not a new gap.
6. **LOW — deferred, not fixed here.** The approved denylist's only durable
   copy is this script; suggest recording it in `ut-docs` (a `reference/`
   doc, or an ADR-0040 amendment) so the guard isn't the source of truth
   for a product-owner decision. Cross-repo, out of scope for this PR —
   worth a follow-up card if nobody gets to it first.
7. **LOW — fixed.** Added the guard to `CLAUDE.md`'s standing rules,
   matching every other mechanically-enforced guard already documented
   there.
8. **Informational — accepted, not changed.** `web/public/**` JS isn't
   scanned (same known, documented gap `guard-i18n.sh` already has); the
   locales `find` is `-maxdepth 1` (flat) while help/UI are recursive — an
   intentional asymmetry (locale files are flat per-language, help/UI are
   nested trees) rather than a bug, but worth knowing about.

Also verified: `ADR-0040`'s citation is accurate and verbatim; the diff
touches only `scripts/ci/*`, `.github/workflows/*`, `CLAUDE.md` — nothing
under `internal/pages`, `web/ui/**`, or `web/public/**`, so
`guard-docs-shots.sh`'s screenshot-freshness hash is untouched (confirmed
green); convention conformance (header style, `set -euo pipefail`,
`${BASH_SOURCE[0]}` `ROOT_DIR` resolution, explicit-args-for-fixtures,
✓/❌ output) matches `guard-autofill-suppression.sh` throughout; no client/
shop names, no secret-shaped literals.

## Verified beyond automated tests

- `bash scripts/ci/guard-compliance-claims.sh` and `_test.sh` against the
  real tree, both green, both fast (~0.5–1.7s, confirmed with `time`).
- `go build ./...` clean (this diff touches no Go code).
- `guard-i18n.sh` and `guard-docs-shots.sh` still green — confirms this
  diff has zero effect on either surface.

## Safe-to-merge verdict

**Yes**, after fixing findings #1–#4 and #7 in this commit. #6 is a genuine
but cross-repo, non-blocking follow-up; #5 and #8 are accepted,
convention-consistent, documented gaps.

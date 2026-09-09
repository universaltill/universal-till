# Review: guard-i18n %% literal-count parity check (ut-docs#1873)

**Date**: 2026-09-09
**Card**: universaltill/ut-docs#1873 — "i18n verb-parity guard doesn't catch
an unbalanced %% (real false negative, found in #1865 review)"
**Complexity**: easy
**Reviewer model**: fresh-context Sonnet subagent (per this card's
`complexity:easy` tier — see `scrum-master` skill's model routing)

## Problem

`guard-i18n.sh`'s check 8 (ut-docs#1865) compares the ordered list of
printf/template verbs (`%d`, `%s`, `{{name}}`, `{0}`, ...) extracted from
each locale value against `en.json`'s for the same key, catching a
dropped/invented/reordered verb. `%%` (a literal percent sign) is
correctly discarded from that list — it's never a verb, never consumes a
format argument. But that meant a translator dropping one `%` from a `%%`
pair (`"50%% off, %d left"` -> `"50% off, %d left"`) extracted the
*identical* verb-token list on both sides (`['%d']`), so the check passed
vacuously even though the locale value is now corrupted for Go's `fmt`
parser: `"50% off, %d left"` parses `% o` as a broken verb attempt,
shifting what `%d` even means and consuming the argument in the wrong
place — the exact `%!d(MISSING)`-class symptom check 8 exists to prevent.
Found during the independent review of ut-docs#1865 itself; not a
regression, a pre-existing gap the new guard didn't close.

## What shipped

Applied identically (by hand — ut-docs#312: no shared implementation
between core and the language packs) in three independent copies:

- `universal-till/scripts/ci/guard-i18n.sh`
- `ut-plugin-language-de/scripts/check-key-drift.sh`
- `ut-plugin-language-es/scripts/check-key-drift.sh`

Each gets a `percent_literal_count(s)` helper that reuses the same
tokenizer regex already used by `verb_tokens()`/`verb_re`/`TOKEN_RE`
(rather than a naive `s.count("%%")`, which could double-count or
disagree with how the regex actually tokenizes overlapping/adjacent `%`
runs), and `verbs_match()` now compares that count between the base value
and the locale value as its first check, before the existing
template/printf-token comparisons. A count mismatch is reported with a
dedicated note ("differing count of literal %% occurrences: N vs M")
instead of the generic verb-mismatch message, since the verb-token lists
themselves are identical in this exact failure mode and would otherwise
print a diff with nothing visibly different.

New regression tests reproduce the card's exact example
(`"50%% off, %d left"` vs `"50% off, %d left"`) in all three repos:

- `universal-till/scripts/ci/guard-i18n_verbcheck_test.sh` — new
  `plant`/`expect_fail` case.
- `ut-plugin-language-de/scripts/check-key-drift.test.sh` — new case 24.
- `ut-plugin-language-es/scripts/check-key-drift.test.sh` — new case 24.

## What was verified beyond the new tests

- Each repo's full existing verb-parity test suite still passes (no
  regression in any of the 20+ pre-existing cases per file — positional
  reordering, mixed positional/implicit rejection, template tokens, the
  two "%" -adjacent-to-prose false-positive regression cases, etc.).
- Ran each guard/check against the REAL, unmodified locale files (not just
  test fixtures): `universal-till`'s `guard-i18n.sh` passes clean against
  all of `web/locales/*.json`. Both language packs' `check-key-drift.sh`
  still fail against real data, but only on pre-existing, unrelated drift
  (untranslated voucher keys from ut-docs#1832's just-merged UI, and two
  orphan `elevation.summary.*` keys) — confirmed by grep that no
  `"placeholder token"` mismatch or `"%%"` note appears anywhere in that
  output. Not this card's scope: both cards belong to a different,
  concurrently in-flight lane's work (ut-docs#1842/#1860), per this
  session's lane-collision check.
- `gofmt -l .` clean in `universal-till` (no `.go` files touched by this
  change).

## Independent review

A fresh-context Sonnet subagent, given no prior context beyond this card's
description, independently reviewed the diff in all three repos and
verified:

1. All three `percent_literal_count` implementations use the same
   tokenizer/regex as the rest of the file's verb extraction, confirmed
   directly against edge cases (`"%%a%%b"` -> 2, `"%%%d"` -> 1, no
   double-counting, no interference with real verb tokens).
2. The count check is the first statement in every `verbs_match()`, an
   unconditional early return, never skipped on any path.
3. The reporting code in each file correctly keys the printed note off the
   matching pair's own counts (no stale/wrong-variable use).
4. Reverted just the fix in each file (via `git show HEAD:<path>`),
   reran the new test case in isolation, and confirmed it fails without
   the fix and passes with it, in all three repos — then restored the
   working tree and confirmed it matched the intended diff exactly.
5. All three repos' full regression suites pass, and the real-data runs
   show no new false positive.

One cosmetic finding (fixed before commit): the new test case's header
comment in both language-pack `check-key-drift.test.sh` files was labelled
"case 21", colliding with an existing case 21 earlier in the same file —
purely a comment/label issue (the test harness takes a free-text case
name, nothing functional depended on the number), renumbered to "case 24"
to match where it's actually inserted. No other issues found.

**Verdict: PASS.**

## Branches / PRs

- `universal-till` — `fix/1873-i18n-percent-literal-count-guard`
- `ut-plugin-language-de` — `fix/1873-i18n-percent-literal-count-guard`
- `ut-plugin-language-es` — `fix/1873-i18n-percent-literal-count-guard`

(PR links added once opened — see the closing issue comment on
ut-docs#1873.)

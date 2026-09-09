# i18n locale format/template verb-parity guard (ut-docs#1865)

**PRs:** universaltill/universal-till#954, universaltill/ut-plugin-language-de#208, universaltill/ut-plugin-language-es#209
**Card:** universaltill/ut-docs#1865
**Complexity:** medium — Dev at Sonnet, Review at Opus (fresh context)

## What shipped

`guard-i18n.sh` (core) and `check-key-drift.sh` (both language packs) compared
locale KEY SETS against `en.json` but never the printf/template verbs (`%d`,
`%s`, `{{name}}`, `{0}`, ...) inside matching values. A translation could
drop, invent, or reorder a verb and every gate stayed green — found during
the ut-docs#1841 review, where a manager-PIN destructive-action dialog's
entire "how many will be removed" sentence depends on one `%d`.

- **Core** (`universal-till`): new check 8 in `guard-i18n.sh`, built from
  scratch — no prior verb comparison existed there.
- **Both packs** (`ut-plugin-language-{de,es}`): `check-key-drift.sh` already
  had a verb-parity check (ut-docs#297), but its `TOKEN_RE` (`%[a-zA-Z]`) was
  too narrow — no flag/width/precision support, no Go positional-verb
  support — and produced a real false positive on this repo's own data
  (`"a 10%-off code"` parsed as flag `-` + verb `o`). Hardened, not rebuilt.

Design, shared across all three (kept as independent hand-ported copies per
ut-docs#312 — no shared implementation between core and the packs):
- Printf flag characters (`-`/`+`/space/`#`) excluded from the verb grammar
  entirely; every real verb in this codebase is a width/precision digit run
  plus a verb letter, never a flag.
- Order matters for plain (non-positional) verbs — writing order is
  argument order for `fmt.Sprintf`. The one exception is Go's explicit
  positional verbs (`%[1]s`), which may legitimately reorder since the
  index, not the writing position, says which argument goes where; a
  same-index shape change still fails.
- Template tokens (`{{name}}`/`{0}`) are a separate dialect, always
  compared as their own ordered list, independent of the printf side.

## Independent review (Opus, fresh context, worktree-isolated)

TDD revert-verify: reverted the hardened verb grammar back to the original
permissive regex, confirmed `guard-i18n_verbcheck_test.sh` genuinely failed
(4 cases) reproducing the exact `10%-off` false positive, then restored and
confirmed green again — the regression test is real, not a tautology.

**Findings, all fixed before merge:**

1. **Blocker-class correctness bug**: the positional/implicit-verb
   partition didn't distinguish printf verbs from template tokens, so a
   value containing both a positional verb (`%[1]d`) and a template token
   (`{{name}}`) was unsatisfiable by *any* translation, including a
   byte-for-byte copy of `en.json` — `fully_positional()` saw the
   template token's `idx=None` and rejected it as "mixed". Fixed by
   partitioning tokens into printf vs. template dialects before applying
   the positional-comparison rule; template tokens are now always
   compared as their own ordered list.
2. **Non-discriminating test**: the original "positional reorder allowed"
   test case would pass vacuously even against a build with *no*
   positional-verb support at all (zero tokens extracted on both sides).
   Added a case where the verb *shape* changes at the same explicit index
   (`%[1]d` → `%[1]s`), which only passes with real positional comparison.
3. **Misleading failure output**: a mixed-dialect rejection printed two
   byte-identical token lists with no indication *why* they mismatched.
   Added an explicit "(mixes positional and implicit verbs...)" note.
4. **Residual false positive**: the hardened grammar still matched a verb
   immediately followed by more letters with no boundary (`"10%off"` with
   no hyphen, or `"%20den"`-shaped Turkish). Closed with a trailing
   `(?![A-Za-z])` negative lookahead on both verb alternatives — checked
   against all 2102 keys × 6 real locale files (universal-till's own
   ar/en/fa/tr plus both packs' de/es): zero extractions changed.

**Noted, not fixed (real but out of scope for this card, or low severity):**

- An *unbalanced* `%%` (en `"50%% off, %d left"` vs. a locale typo'd to
  `"50% off, %d left"`) is not caught — both extract `['%d']` and the guard
  passes, while the actual rendered string is broken exactly the way this
  card exists to prevent (`go vet` confirms the real Go-level bug). Not a
  regression — nothing caught this before either. Filed as
  universaltill/ut-docs#1873 for follow-up rather than silently left
  untested.
- Duplicate positional indices in one string (`"%[1]s and %[1]s"`) collapse
  in the index→shape map, so a locale dropping the second `%[1]s` reference
  passes. Low severity (Go still formats correctly from a single indexed
  arg; only the string's own duplication is lost) and no real string in
  this codebase does this today.
- Doc-comment header drift (check-list index, the packs' own top-of-file
  prose) fixed as part of the same PRs, not filed separately.

## Verified beyond automated tests

- `bash scripts/ci/guard-i18n.sh` (core) and both packs'
  `UT_CORE_EN_JSON=... bash scripts/check-key-drift.sh`: all pass clean
  against the real, current locale data (1509 core keys; 2102/2102 pack
  keys each; 0 verb mismatches).
- All pre-existing `guard-i18n*_test.sh` (core) and `check-key-drift.test.sh`
  (both packs) suites re-run and pass unchanged — no regression in the
  key-parity, baseline-ratchet, empty-value, or allowlist logic this check
  sits alongside.
- `git status --porcelain` confirmed clean in all three repos after every
  test run, including the new `guard-i18n_verbcheck_test.sh`, which
  mutates `web/locales/*.json` in place and must restore them byte-for-byte
  (including trailing newline — an earlier draft of the test's own
  restore logic lost the newline via a `$(cat ...)` capture; fixed to use
  file-copy backup/restore instead).
- No UI, money, offline-first, or plugin-signing surface touched — pure CI
  tooling; confirmed explicitly rather than silently skipped.

## Safe-to-merge verdict

**Safe to merge**, all three PRs, in any order (no cross-repo drift risk —
core adds no new locale keys, so `lang-pack-drift` is unaffected by landing
order here).

## Explicitly deferred

- universaltill/ut-docs#1873 — unbalanced `%%` false negative (noted above).
- No dedup of the two packs' `check-key-drift.sh` copies (ut-docs#312,
  standing, out of scope for this card).

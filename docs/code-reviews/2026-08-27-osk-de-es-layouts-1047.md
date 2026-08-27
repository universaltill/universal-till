# Code review: OSK de/es keyboard layouts (ut-docs#1047)

**Date:** 2026-08-27
**Card:** universaltill/ut-docs#1047 — "OSK has no de/es keyboard layout —
can't type accented characters after ut-docs#1022"
**Complexity:** medium — dev at Sonnet (inline), review at Opus (independent
subagent, worktree-isolated)

## What shipped

`web/public/osk.js`'s `LAYOUTS` table covered `en`/`tr`/`fa`/`ar` only; `de`
and `es` — both shipped as language plugins, and Germany is this product's
active pilot market — silently fell back to the `en` layout via
`baseLayout()`, and `localeSupported()` (added in ut-docs#1022) correctly
left the native keyboard un-suppressed for them, meaning an operator on a
`de`/`es` till had no way to type ä/ö/ü/ß/ñ/á/é/í/ó/ú through the OSK at
all.

Added real `de` (QWERTZ: z/y swapped vs QWERTY, plus ü/ö/ä/ß) and `es`
(QWERTY base plus á/é/í/ó/ú/ñ appended to the existing rows, the same shape
`tr`'s entry already uses) entries to `LAYOUTS`. No other code changed —
`baseLayout()`/`localeSupported()` key off `LAYOUTS`' own keys, so both
functions handle the new locales automatically. Shift/uppercase: verified
directly in Node that `'ß'.toLocaleUpperCase('de')` → `'SS'` (Unicode's
default case mapping for ß, since it has no traditional single-character
uppercase) and that Spanish accented vowels/ñ uppercase to themselves with
the accent preserved (á → Á) — both work with the existing
`k.toLocaleUpperCase(baseLayout())` call already in `press()`/`render()`,
no special-casing needed.

`e2e/tests/osk-central-guard.spec.ts`: the pre-existing "locale osk.js has
no layout for" test used `?lang=de`, whose premise this change makes
false — switched to `?lang=zz` (a locale that will never gain a layout) so
it keeps testing the fallback path itself. Added two new tests that drive
real key taps (not just attribute checks) for `de` and `es`: typing a real
word, the shift+ß→"SS" and shift+á→"Á" cases, and — the load-bearing part —
asserting real DOM row *order*, not just key presence, since `data-k`
click lookups are positionless and would pass against a wrong-position
layout just as easily as a correct one.

## Independent review — findings

Spawned as a worktree-isolated Opus subagent (different model from the
Sonnet session that wrote the code), briefed to actually run the gate and
re-verify the TDD claim itself, not read the diff and trust it.

- **[major, fixed] `es` home row had `ñ` one position out of place.**
  Shipped as `l, í, ñ`; both the physical Spanish ISO keyboard and the
  ES mobile keyboards put ñ directly after l (`…J K L Ñ`). The acceptance
  criteria explicitly called for matching the real physical layout, not
  "the missing letters somewhere." Fixed to `l, ñ, í`, and the matching
  e2e assertion (which had hardcoded the wrong order) corrected in the
  same edit — a test asserting the deviation would have cemented it.
- **[minor, accepted as follow-up] `es` cannot type `ü`.** Needed for real
  Spanish words (*pingüino, vergüenza, bilingüe*) and present in `de`/`tr`
  but not `es`; no dead-key mechanism exists to reach it another way.
  Filed as universaltill/ut-docs#1147.
- **[minor, accepted as follow-up] `¿`/`¡` unreachable in `es`** (absent
  from both the `es` layout and the shared `sym` layer). Cheapest fix
  benefits every locale via `sym`. Filed as universaltill/ut-docs#1148.
- **[nit, addressed] `de`'s comment overclaimed physical ß placement** —
  softened to note the OSK's placement (row-end, following the `tr`
  precedent) is a deliberate choice, not a literal physical-keyboard
  position (ß is on the German number row on a real keyboard).
- **[nit, no action] `ẞ` (U+1E9E) exists as a valid capital eszett** under
  the 2017 orthography reform — noted only so a future reader doesn't
  "fix" the `SS` mapping; `SS` is the correct Unicode default and what
  this change documents and uses.
- **[nit, no action] The `zz` test relies on `?lang=` accepting an
  unvalidated arbitrary value** (`internal/httpx`) — a pre-existing,
  unrelated characteristic of the handler, not something this change
  introduces; if locale validation is ever added, the test fails loudly
  rather than false-passing.

The two recurring bug classes this pipeline watches for (a file-write
handler missing `os.MkdirAll`; a cwd-relative path where `paths.Data(...)`
belongs) are structurally inapplicable — the diff touches zero Go files
and no file-write/path-resolution code at all (`web/public/osk.js` +
`e2e/tests/osk-central-guard.spec.ts` only).

## Verified beyond the automated suite

- **TDD claim independently re-verified**, not taken on faith: the
  reviewer reverted the `de` z/y swap in an isolated worktree, rebuilt the
  server fresh (this repo `go:embed`s `web/public/`, so a stale
  long-running server would silently mask a source edit — confirmed ports
  free before each rebuild), and got a real, on-topic failure
  (`top letter row must be real QWERTZ order`, diff showing `z`→`y`).
  Restored, fresh server again, confirmed green. The dev session had
  already done this same revert/rebuild/restore cycle once during its own
  Tester step, independently, before review.
- Case-mapping claims (`'ß'.toLocaleUpperCase('de')` → `'SS'`,
  `'á'.toLocaleUpperCase('es')` → `'Á'`, `'ñ'` → `'Ñ'`) checked directly in
  Node by both the dev session and the reviewer.
- Driven in a real browser (not just the automated suite): screenshots of
  both the `de` and `es` on-screen keyboards taken and read — clean
  touch-sized keys, no overlap/clipping/wrap, correct glyphs in correct
  positions. Not checked: dark theme, RTL — both locales here are LTR and
  no new CSS was introduced, only new `LAYOUTS` data consumed by
  pre-existing, unchanged rendering code.
- `guard-i18n.sh`'s scope confirmed directly (globs
  `web/ui/**/*.html`/`web/locales/*.json`/`internal/**/*.go`, and its own
  script comments document `web/public/` as a known-uncovered gap) —
  substantively also correct: these are literal keyboard glyphs, the same
  category as the pre-existing un-keyed `en`/`tr`/`fa`/`ar` entries, not
  new prose.
- `guard-help-topics.sh` passes; no `internal/pages` route changed, so no
  manual topic update is warranted — this is a background OSK-locale-
  coverage fix, not a new shop-owner-visible screen.

## Gate

`gofmt -l .` clean · `go build ./...` clean · `go vet ./...` clean ·
`guard-i18n.sh` ✓ (1292 keys resolve, all locales match) ·
`guard-help-topics.sh` ✓ · e2e `osk-central-guard.spec.ts` (12/12) +
`settings-osk.spec.ts` (5/5) + `kiosk-cursor.spec.ts` (2/2) = 17/17 passed,
re-run clean after the ñ-position fix.

## Verdict

**Safe to merge.** Tightly scoped (2 files, no Go/data-layer/money/plugin
code touched), reuses the existing `LAYOUTS` shape and the `tr` entry's
own append-to-row-end pattern rather than inventing a new mechanism, and
the new tests are genuinely regression-detecting — independently confirmed
by deliberately breaking the code twice (both the QWERTZ swap and, via the
review's own `ñ` finding, an actual real placement bug it caught before
merge) and watching the right assertion fail both times.

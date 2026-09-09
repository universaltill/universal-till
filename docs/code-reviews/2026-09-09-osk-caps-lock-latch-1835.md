# Code review — on-screen keyboard: double-tap Shift latches caps lock (ut-docs#1835)

- **Date:** 2026-09-09
- **Branch:** `fix/1835-osk-caps-lock-latch`
- **Reviewer:** independent reviewer (fresh-context subagent, Sonnet —
  this pipeline's `complexity:easy` review tier, "different model"
  relaxes to "different instance" per `scrum-master`'s Model routing
  rules), isolated worktree
- **Verdict: PASS — safe to merge.** No blocking findings; one
  non-blocking coverage gap, fixed in this branch after review.

## What shipped

`web/public/osk.js`'s Shift key was a plain one-shot toggle with no
caps-lock — every capital letter cost a separate Shift tap. German
capitalises every noun, so a merchant typing catalog item names (the
most text-entry-heavy task in the product) paid this per word; the
report also noted the same cost on Settings → Data's typed-word
destructive-action confirmations (`CLEANUP`/`RESET`/`PURGE`/`RESTORE`,
all-caps).

**Fix:**
- Two new module-level state vars: `shiftLatched` (the latch itself) and
  `lastShiftTap` (timestamp of the previous Shift tap), plus a
  `SHIFT_LATCH_MS = 400` constant.
- `press('⇧')` is now a small state machine: a tap while latched clears
  back to no-shift; a second tap within `SHIFT_LATCH_MS` of a still-armed
  one-shot (`shift === true`, i.e. no character was typed in between)
  latches; anything else falls through to the original plain toggle
  (`shift = !shift`) — so a single tap, or a slow second tap, behaves
  exactly as before.
- The default (character-insert) case only clears `shift` when NOT
  latched — a latch persists across every subsequent character until
  Shift is tapped again.
- `render()` marks the Shift key `osk-caps` when latched (new class,
  distinct from the existing one-shot `osk-on`).
- `show()` (already resets `shift = false` on every keyboard open) and
  `hide()` (previously had no shift reset at all) both now also clear
  `shiftLatched`, so closing the keyboard — with or without reopening —
  never leaves a latch live for the next field.
- `app.css`: new `.osk-caps` rule, `background: var(--warning); color:
  var(--accent-contrast)` plus an inset white ring
  (`box-shadow: inset 0 0 0 2px var(--accent-contrast)`) — the ring is
  theme-invariant (drawn in `--accent-contrast`, `#ffffff` on every
  shipped theme) so the latched state stays tellable apart by shape, not
  just hue, on the one shipped theme (`amber`) whose own `--accent` is
  close in hue to `--warning`. Existing design tokens only, no new
  hardcoded colors.
- New spec `e2e/tests/osk-caps-lock-latch-1835.spec.ts`: double-tap
  latches and persists across multiple characters; a single tap keeps
  the exact pre-existing one-shot behavior (no regression); a slow
  (>400ms) second tap just toggles off, same as a plain tap always did;
  closing and reopening the keyboard clears the latch; and (added after
  independent review, see Findings below) the latch survives a round
  trip through the `?123`/`ABC` symbol layer and survives backspace.

No i18n impact — pure interaction/CSS change, no new user-facing
strings. No ADR — contained, non-architectural client-side fix. No help
topic added: no topic describing the on-screen keyboard as a concept
exists today (checked all 26 routed topics; `web/help/en/display.md` has
one generic sentence with no per-key detail, and doesn't document the
pre-existing single-tap-Shift behavior at that granularity either), and
this is an interaction nuance with no new page/route — both Dev and the
independent reviewer reached this same conclusion separately, matching
precedent (`2026-08-27-osk-de-es-layouts-1047.md`,
`2026-08-29-osk-numeric-minus-key-1276.md`).

`web/help/img/manifest.json` + two screenshots (`en/sell.png`,
`ar/sell.png`) regenerated via `make docs-shots`, required whenever
`web/public/**` changes (`guard-docs-shots.sh`) — visually inspected,
both look normal (no OSK visible in either, nothing broken); the specific
files that differ changed between consecutive regen runs with no source
change in between, consistent with encoder/rendering noise rather than
real content drift (same conclusion as the 2026-08-29 precedent review).

## Verification performed

| Check | Result |
|---|---|
| `gofmt -l .` / `go build ./...` / `go vet ./...` | empty / pass / pass (no Go files touched) |
| `go test ./...` (full suite) | pass |
| `golangci-lint run ./...` | 0 issues |
| Every CI-blocking guard in `ci.yml`'s `build` job | all pass, except `guard-deadcode-baseline.sh` — reproduced identically on unmodified `main` (verified via `git stash`): pre-existing sandbox gap (no GTK/WebKit `pkg-config` headers), matches the documented `cmd/unitill-desktop` gap in this repo's own `CLAUDE.md` (ut-docs#1581), unrelated to this change |
| New spec alone | 5/5 pass (4 written by Dev, 1 added after independent review) |
| Full OSK e2e surface (`-g "osk\|OSK"`) | 83/83 pass, no regressions |

### TDD claim, verified twice (Dev, then independently by Reviewer)

Both verified the same way: revert `web/public/osk.js` + `web/public/app.css`
to the parent commit, re-run the new spec, confirm the latch-specific
tests fail for the diagnosed reason, restore, confirm green again.

- **Dev:** the 2 latch-specific tests failed (`osk-caps` class never
  appears — `expect(locator).toHaveClass` timeout), while the 2
  regression-guard tests (single tap, slow second tap) still passed
  unchanged — proving those two test pre-existing behavior, not the new
  feature. Restored → 4/4 green.
- **Reviewer (independent, isolated worktree):** same revert
  (`git checkout HEAD~1 -- web/public/osk.js web/public/app.css`),
  reran the spec — the two latch-specific tests failed with genuine
  assertion errors (`expected class /osk-caps/, got "osk-key"`), the
  single-tap and slow-second-tap tests still passed. Restored → 4/4
  green again.

## Findings and disposition

1. **Non-blocking, fixed — missing coverage for the latch surviving a
   layer switch and backspace.** The reviewer traced the state machine
   by hand (not just read it) and confirmed by code-reading that a latch
   correctly survives a `?123`→`ABC` round trip (the `sym` layer has no
   Shift key at all, so nothing there can touch `shiftLatched`) and
   survives backspace (which never touches `shift`/`shiftLatched`), but
   the spec never asserted either. **Fixed:** added a 5th test exercising
   both paths for real — types a capital, backspaces it, types another
   capital, confirms the latch (and `osk-caps` class) survive throughout;
   also confirms the Shift key genuinely has no rendered instance on the
   `sym` layer (`toHaveCount(0)`), not just that the click didn't error.

## Checked and found clean

- **State machine, traced by hand against every scenario asked for:**
  2 fast taps → latch; a 3rd tap clears the latch to no-shift (not back
  to a fresh one-shot — deliberate); 4th+ fast taps behave as a fresh
  single tap (no runaway re-latching, since the 3rd tap already zeroed
  `shift`); typing a character between two Shift taps correctly voids
  the "second tap" reading (falls through to a fresh single-tap toggle,
  never a latch) since a char always clears the one-shot `shift` first;
  the `SHIFT_LATCH_MS` boundary is inclusive (`<=`) — harmless either way.
- **Pre-existing quirk, confirmed out of scope:** `case 'SPACE'` returns
  early and never reaches the shift-clearing line, so Space doesn't clear
  an armed one-shot either before or after this diff — unchanged
  behavior, not introduced here, not part of #1835's acceptance criteria.
- **CSS tokens:** `.osk-caps` uses only existing custom properties
  (`--warning`, `--accent-contrast`); verified directly against all four
  shipped theme files (`amber`/`fresh`/`monarch`/`slate`) that
  `--accent-contrast` is `#ffffff` everywhere it's defined and correctly
  inherits the base value via normal CSS cascade where a theme doesn't
  redefine it (`monarch.css`) — the inset ring stays white on every
  theme, not just the ones that happened to be screenshotted.
- **No disk I/O at all** — pure client-side JS/CSS diff; the two
  recurring bug classes this pipeline watches for (missing
  `os.MkdirAll`, a cwd-relative path where `paths.Data(...)` belongs)
  don't apply (confirmed by grep, not just assumed from the diff shape).
- No real client/shop names or secret-shaped literals anywhere in the
  diff.
- `guard-osk-loaded.sh`, `guard-i18n.sh`, `guard-compliance-claims.sh`,
  `guard-help-topics.sh` all green.

## Follow-up cards filed

None — the one gap the independent review found was cheap enough to
close in this same branch rather than deferring it.

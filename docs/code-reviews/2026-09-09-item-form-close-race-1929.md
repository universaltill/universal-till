# Code review: race-tolerant close helper for the catalog item-form dialog (ut-docs#1929)

**Date:** 2026-09-09
**Card:** ut-docs#1929 — "e2e: race-tolerant close helper for the catalog
item-form dialog (flake risk from ut-docs#1901)"
**Branch:** `fix/1929-item-form-close-race`
**Design:** none needed — mechanical test-infrastructure fix, no product
code, no ADR implications.
**Build model:** Sonnet (card is `complexity:easy`)
**Reviewer:** independent (fresh-context Sonnet subagent, isolated
worktree, did not see the implementation's own reasoning)

## What shipped

Since ut-docs#1901, the catalog add/edit-item `<dialog>` (`#item-form-modal`)
auto-closes itself ~1.5s after a save-success notice appears. Eight e2e call
sites raced that timer by waiting for the same success notice and then
clicking `#item-form-close-btn` explicitly — if the timer won first, the
button was already `display:none` (`.item-form-modal:not([open])`) and the
click would time out. Not yet observed flaking, but a plausible risk under
real CI load.

Added `closeItemForm(page)` to `e2e/tests/helpers.ts`: calls `.close()` on
the dialog directly via `page.evaluate` (idempotent regardless of which side
of the race won — native `<dialog>.close()` on an already-closed dialog is a
documented no-op) and asserts `toBeHidden()` instead of racing a specific
path to the closed state.

## Scope correction found during BA verification

The card's own text said "eight call sites" across six files, counting
`catalog-reactivate-double-submit-1365.spec.ts` twice. Re-grepping the actual
current code before touching anything (`grep -rn "item-form-close-btn"
e2e/tests/`) found the real shape was **9 call sites across 7 files**:
`catalog-reactivate-double-submit-1365.spec.ts` has only 1 site, not 2, and
`osk-decimal-sale-catalog-fields-1284.spec.ts` has 1 site the card never
mentioned at all. Fixed all 9 real sites rather than the 8 the card
enumerated: converted 8 to the new helper (`catalog-barcode-backfill-1356`,
`catalog-category-brand-select-1430`, `catalog-reactivate-double-submit-1365`,
`catalog-row-oob-1363` ×3, `catalog-save-notice-917`,
`osk-decimal-sale-catalog-fields-1284`), and — per the card's own intent —
left exactly one site (`catalog-active-checkbox-1367.spec.ts`) on the literal
button click, so `#item-form-close-btn` itself still gets real coverage.

## Independent review: no findings, PASS

The reviewer independently re-grepped both the pre-fix and post-fix commits
to verify the recount above itself rather than trust either the card or the
implementation's own claim, and confirmed it exactly: 9 sites pre-fix, 8
converted + 1 deliberately-untouched post-fix, none missed, none wrongly
converted. It also traced the helper's mechanism against the real markup —
`web/ui/pages/catalog.html`'s close button (`onclick="...close()"`) and its
own `setTimeout(..., 1500)` auto-close call both resolve to the same native
`.close()`, and `web/public/app.css`'s `.item-form-modal:not([open]) {
display: none; }` is exactly what `toBeHidden()` checks — confirming the
helper's assertion is the correct one, not just a plausible one.

## Verified beyond automated trust

- **Real driven run, twice**: the affected 7 spec files (24 tests) run
  against a real Go server + real Chromium (`--project=default`), both by
  the implementer and independently by the reviewer in its own isolated
  worktree — 24/24 passed both times.
- **Full e2e suite**: 381/381 passed (6.7m), satisfying the card's own
  acceptance criterion ("Full e2e suite still green").
- `gofmt -l .` empty (no Go files touched — confirmed by diff stat).
- `scripts/ci/guard-e2e-fixtures-import.sh` passes (94 specs checked).

## Explicitly out of scope / not applicable

Test-only diff (`e2e/tests/**`) — no product code, no user-facing strings,
no money/data-access/plugin-signing/i18n/UX-guideline/help-manual surface
touched. Confirmed, not assumed, by both the implementer and the reviewer
independently reading the diff stat.

## Verdict

Safe to merge.

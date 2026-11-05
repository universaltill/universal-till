# Review: setup-wizard pickers as a WAI-ARIA radiogroup (ut-docs#1103)

Date: 2026-11-05 · Branch: `fix/1103-setup-picker-radiogroup` · Lane: cloud-24
Built by a Sonnet subagent. Reviewed independently by an Opus subagent. Tester was a separate agent.

## What shipped

- `web/ui/pages/setup.html`: the country and shop-type pickers change from
  `role="group"` with `aria-pressed` toggles to `role="radiogroup"` with
  `role="radio"` and `aria-checked`. They use a roving `tabindex`: the
  checked tile is the one tab stop, or the first tile when nothing is
  checked. "Show all countries" now focuses `[aria-checked=true]`.
- `web/public/radiogroup.js` (new): a delegated `keydown` listener for groups
  marked `data-roving-radiogroup`. It loads only on the setup page.
  - Arrow keys, Home and End move through the visible tiles in DOM order and
    wrap at the ends.
  - Left/Right swap under RTL.
  - Selection goes through `next.click()`, so the tile's own `@click` runs
    exactly as it does for a tap (country prefill, `currencyTouched`).
  - When only one tile is visible (the collapsed country grid), the keys do
    nothing.
  - The script contains no strings.
- `web/public/app.css`:
  - `[aria-checked="true"]` is styled the same as `[aria-pressed="true"]`,
    including the `fx-light` rule.
  - The `.picker-tile:focus-visible` ring now uses
    `2px solid var(--focus-border)` with a 2px offset. The old ring was
    35%-alpha blue, about 1.6:1, which fails WCAG 1.4.11.
  - `.builtin-icon-grid` padding goes from .15rem to .3rem so its scroll box
    doesn't clip that ring.
- `e2e/tests/login.spec.ts`: the `aria-pressed` assertions now check
  `aria-checked`. New tests cover the country radiogroup (one tab stop,
  arrows, wrap, Home/End, currency prefill), RTL mirroring, and the
  shop-type group (first tile is the tab stop, focusing it doesn't select
  it, Space and arrows select). The collapsed-grid no-op and the
  expand-focus check are added to the detected-country test.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| R1 | Should-fix | The new ring reaches 4px outside the tile, and `.builtin-icon-grid` (a scroll box with .15rem padding) clips about 1.6px of it on edge tiles in the catalog/category icon picker. | Fixed: padding is now .3rem. `icon-library-2506.spec.ts` passes 11/11. |
| R2 | Nit | The collapsed-grid "arrows do nothing" check also passes without `radiogroup.js`, because arrow keys on a plain button already do nothing. | Accepted. It still guards against an auto-expand regression. The handler's real behaviour is pinned by the arrow and wrap tests, which fail without the script (verified twice). |
| R3 | Nit | The RTL test assumed GB is not the last visible country. | Fixed: an explicit `expect(idx).toBeLessThan(values.length - 1)`. |
| R4 | Nit | Shift+Arrow also moves the selection. | Accepted: harmless, and native radios do the same in Chromium. |
| UX1 | Required (UX) | The focus ring was below 3:1. | Fixed as above. Screenshots confirm there is no ring after a tap. |

## Verification beyond unit tests

- **TDD and false-pass checks:**
  - Tester stashed `setup.html` and `radiogroup.js`: every new or changed
    test failed. With `radiogroup.js` removed alone, the three keyboard
    tests failed.
  - Reviewer emptied `radiogroup.js` in a throwaway worktree: the arrow test
    failed at `toBeFocused`.
- **e2e:** `login.spec.ts` on the auth project passes 20/20 after the review
  fixes. `icon-library-2506.spec.ts` passes 11/11.
- **Driven run (Tester):** a fresh till, driven with Playwright in en and
  fa (RTL), at 1024×600 and 360×740.
  - Keyboard-only flow works.
  - The accessibility snapshot shows a radiogroup of 16 radios with exactly
    one checked.
  - Tapping a tile shows no focus ring.
  - The category colour picker looks unchanged.
- **Go:** `go build ./...` and `go vet ./internal/pages/` pass.
  `go test ./internal/pages/ -timeout 1500s` passes in 693s. The package
  takes longer than the 600s default on its own; this change doesn't affect
  that.
- **Guards:** i18n, rtl-logical-css, no-inline-handlers, no-showmodal,
  fixed-dates and help-topics all pass.

Pre-flight: Till user-visible behaviour: `web/help/` n/a (quickstart doesn't describe the pickers; there is no pixel change except the keyboard-only focus ring, so docs-shots don't need regenerating); `reference/` n/a; README n/a. en.json keys: n/a (none added). ADR: n/a (existing patterns, ADR-0008 HTMX/Alpine unchanged). Review record: this file. Follow-ups: deferred → Backlog card "picker-tile a11y follow-ups" (filed with this change).

## Deferred

- Other `.picker-tile` users (catalog colour and icon grids, category editor,
  photo-cutout background tiles) still use `role=group` with `aria-pressed`.
  The basket order-type radiogroup has no roving tabindex.
- In forced-colors (Windows High Contrast) mode, the selected state of every
  picker tile disappears.
- Other weak focus outlines remain at `app.css` ~L2164 and ~L6650.
- An older issue, not caused by this change: at 1024×600, when the "PINs do
  not match" error shows, Back/Next sit about 20px below the fold (the page
  still scrolls).

## Verdict

Approve. The Should-fix and one nit are fixed. The two remaining nits are
accepted with reasons.

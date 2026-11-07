# 2026-11-02 — e2e: phone popup-fit with server-filled content (ut-docs#3411)

## What shipped
- One new test in `e2e/tests/phone-popup-fit-3351.spec.ts`, per phone width
  (390 / 440 px), reusing the spec's `measure()` / `problems()` and its
  Full-motion `beforeEach`:
  - seeds an item with one modifier group + a long-ish option label as a
    sell-screen tile (same path as `seedModifierItem` in
    `popup-zoom-2944.spec.ts`), taps the tile and fit-checks the **filled**
    `#modifier-modal` (asserts the option text is inside first);
  - seeds one table (`createTable`), puts a dine-in line in the basket, opens
    the phone basket sheet → table picker and fit-checks the filled
    `#table-modal` (asserts the seeded option is visible first);
  - cleans up the tile, the item and the tables in `finally`.
- **Extra check found while writing it:** a CSS mutation forcing the
  option grid to 700 px first *passed*. `measure()`'s `reachable()` waves
  through anything inside a box that scrolls or clips sideways, and the
  popup itself scrolls, so a too-wide grid just opens cut off at the popup
  edge. The new test therefore also fails when anything in either picker
  (popup included) overflows sideways (`scrollWidth > clientWidth + 1` on a
  non-`visible` `overflow-x` box). The sweep's own rule is unchanged; the
  same blind spot for every other popup is filed as a follow-up card.
- Test-only. No product code, no locale keys, no help change.

## Review
Written by Opus; independent review by a Sonnet subagent in a detached
worktree (ran the targeted test, the full file and a mutation).

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | low | Table-picker coverage not mutation-verified. | **Already verified** by the author: forcing `.table-picker-option` to 700 px fails at both widths (`#table-modal … scrollWidth=721`). |
| 2 | low | `closeAll(page)` first in `finally` throws on a dead page and skips the data cleanup. | **Fixed**: `closeAll(...).catch(() => {})`. |
| 3 | info | Empty "Pf3411 Cat" category left behind (same as the 2944 precedent). | Accepted: per-worker throwaway till; nothing reads it. |
| 4 | info | `seedModifierItem` duplicates the 2944 helper. | Accepted for now; hoist into `helpers.ts` when a third spec needs it. |
| 5 | info | A future deliberate ellipsis label would trip the sideways check. | Accepted: whitelist that selector then; comment states the intent. |

## Verified
- `npx playwright test tests/phone-popup-fit-3351.spec.ts --project=default`:
  4 passed (sweep still finds 31 distinct popups at both widths); the new
  test re-run after fix 2: 2 passed.
- Mutations (author and reviewer), each failing at 390 and 440 px, then restored:
  `#modifier-modal label, button { min-width:700px; nowrap }` →
  `dialog#modifier-modal scrollWidth=751, clientWidth=349/394`;
  `.table-picker-option { min-width:700px }` → `dialog#table-modal scrollWidth=721`.
- CI `build`-job guards run locally: all pass except `guard-deadcode-baseline`
  (local deadcode binary built with Go 1.26) and `guard-shellcheck-version`
  (no shellcheck installed) — environment only; no Go or shell file changed.
- No screenshots looked at: the test is geometric; no surface changed.

## Verdict
Safe to merge.

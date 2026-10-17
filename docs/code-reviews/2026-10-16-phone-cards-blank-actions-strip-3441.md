# Review: phone cards — hide whitespace-only header-less cells (ut-docs#3441)

**Date:** 2026-10-16 · **Branch:** `fix/3441-blank-card-actions-strip` · **Lane:** `lane:cloud-41`
**Author model:** Sonnet (easy card) · **Reviewer:** Opus 5.5, fresh context, separate worktree

## What shipped
- `web/public/table-cards.js`: a header-less (`.t-cards-actions`) cell with no
  element children and whitespace-only text also gets `.t-cards-blank`; the
  class is reset on every re-annotation, so a cell that gains an element
  loses it.
- `web/public/app.css` (inside `@media (max-width: 480px)`):
  `.t-cards-actions:is(:empty, .t-cards-blank) { display: none; }`. `:empty`
  alone never matched a cell whose template left whitespace (e.g.
  `orders_list.html`'s print-warning cell with no failure), so every phone
  order card carried a blank ~9px flex strip.
- Generic fix in the shared place — covers every list page with an empty
  `<th>`, not only `/orders` (the card's "audit other pages" ask).
- `e2e/tests/phone-cards-blank-actions-3441.spec.ts`: synthetic table
  (whitespace / button / text cells + blank→content re-annotation) and a real
  cash sale on `/orders` at 360px.
- `web/help/img/manifest.json`: surface hash only (`Docs-Shots-Unchanged`):
  every docs shot is 1024×600, and the new CSS applies only ≤480px.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Blank is decided at annotation time; a later text-only client fill of such a cell (no new element → no `htmx:load`) would stay hidden. Reviewer audited every empty-`<th>` table in `web/ui` — no cell is filled that way today; the pre-existing `:empty` rule had the same property. | Accepted; documented the contract in table-cards.js's header comment. |
| 2 | nit | A cell holding only non-rendering elements (`<input type=hidden>`) still shows the strip. No template does this. | Accepted, out of scope. |
| 3 | nit | Real-order test assumes the worker till has no failing printer. True for the default e2e seed. | Accepted. |

## Verified beyond automated tests
- TDD re-verified by the reviewer: with only the two `web/public` files reverted
  both new tests fail (`Expected "none", Received "flex"`); restored, 3441 +
  3357 + 3359 specs: 9 passed. Promotions 3653 phone spec: 4 passed (Dev).
- Screenshots looked at: `/orders` with two real orders at 360×800 (no strip
  between Status and Update; cards otherwise unchanged) and 1024×600 (table
  layout unchanged). Not checked: RTL locale and dark theme — the change only
  adds `display:none` to an empty cell, no direction- or colour-dependent rule.
- Guards: rtl-logical-css, i18n, docs-shots — clean.

## Verdict
Safe to merge.

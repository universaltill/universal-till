# 2026-10-02 — Phone: /orders renders a real order as a card (ut-docs#3357)

**Lane:** lane:cloud-41b · **Built by:** Sonnet 5 (orchestrator, card sized `complexity:medium`) · **Reviewed by:** Fable (independent subagent, separate worktree)

## Background

The product owner reported `/orders` as "a grid view and not good for a mobile
phone". Investigation found ut-docs#3359's generic table→card mechanism
(`universal-till#1607`, merged this cycle) already covers `/orders` — #3359's
own ask explicitly lists "order status" among the pages it must cover — so no
change to `internal/pages/order_status.go` or `orders_list.html` was expected.
The real gap: neither #3359's own e2e suite nor the general
`phone-layout-sweep-3297` sweep ever drives a real order through `/orders` —
the sweep visits it with an empty board, so the table never renders
(`order_status.go`'s `{{ if not .Orders }}` branch).

## What shipped

- `e2e/tests/phone-orders-cards-3357.spec.ts` (new): rings up a real sale
  (same flow as `orders-terminal-row-removal-1389.spec.ts`), switches to a
  360px viewport, and drives `/orders` for real: the row renders as a
  `.t-cards-row` card within the viewport width; the title cell renders with
  no `::before` label; the Status cell's header renders as a real `::before`
  label (not just a present `data-label` attribute); every action button's
  bounding box sits inside the card's own box; a one-tap status change
  (Preparing) updates the chip in place; advancing to the terminal status
  (Collected) removes the card via the existing OOB-delete mechanism
  (ut-docs#1389), proving it still fires once the row is styled as a card.
- `web/public/app.css`: `table.t-cards .btn-actions { flex-wrap: wrap;
  justify-content: flex-start; }` (≤480px block, next to the existing
  `.t-cards-actions` wrap rule).

## Review findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | Draft spec asserted the Status cell was *visible* but never that its column header actually rendered as a `::before` label — a mutation test (`content: none` injected on the label rule) still passed every assertion in the draft | Fixed: assert `getComputedStyle(statusCell, '::before').content === '"Status"'` |
| 2 | major | Real production bug the draft spec didn't catch: the Actions column has a real header ("Update"), so its buttons render through the page's own `.btn-actions` div, not `.t-cards-actions` (table-cards.js only applies that class to a header-less cell) — `.btn-actions`'s base rule has no `flex-wrap`, so at 360px four one-tap buttons summed past the card's content width and spilled ~21px outside the card border on both sides | Fixed: `table.t-cards .btn-actions { flex-wrap: wrap }` in `app.css`; spec now asserts every action button's bounding box is inside the card's own bounding box |
| 3 | major (process) | No code-review record for this card | This file |
| 4 | minor | `orders_list.html`'s empty print-warning header cell becomes `.t-cards-actions`, but whitespace text nodes stop `app.css`'s `:empty` selector from firing, so every card carries a blank ~9px flex strip | Accepted, filed as a follow-up (cosmetic, outside this card's scope — see ut-docs#3357's close-out comment) |
| 5 | nit | `toHaveCount(0)` on the terminal-status card can be satisfied by the SSE `orders-push` re-fetch even if the OOB delete itself broke (same limitation `orders-terminal-row-removal-1389.spec.ts` already has) | Accepted, pre-existing class of gap, not introduced here |
| 6 | nit | `isMobile: true`/`hasTouch: true` during the desktop-viewport ring-up is a hybrid no real device has | Accepted — matches `phone-table-cards-3359.spec.ts`'s own `/reports` case |

## Verified beyond automated tests

- TDD, both major findings: reverted `app.css`'s new `.btn-actions` rule → the strengthened spec fails with the exact measured overflow (`x=17.3` vs required `>= 36.5`, i.e. ~19px outside the card's left edge); restored → passes. Separately set the Status label's `content: none` → the strengthened label assertion fails; reverted → passes.
- Full related-suite run after the fix: `phone-orders-cards-3357`, `orders-terminal-row-removal-1389`, `phone-table-cards-3359`, `phone-layout-sweep-3297` (all widths/routes) — 92 passed, 0 failed.
- `go build ./...` clean.

## Not verified

A real iPhone/Safari (Chromium mobile emulation only), matching #3359's own review.

## Verdict

Safe to merge.

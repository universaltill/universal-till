# Review: parked-orders popup second tap can resume the wrong order (ut-docs#3622)

Date: 2026-10-03 · Lane: `lane:cloud-41` · Built by Sonnet 5, reviewed by Opus 5.5 (fresh context, isolated worktree)

## What shipped
- `web/public/app.css`: while a parked order's own `.parked-move[open]`
  (Move-table panel) is open, that row's resume button
  (`.parked-order`/`.parked-order-form`) gets `pointer-events: none`, so a
  tap landing anywhere it may have shifted to can never resume the order.
- `e2e/tests/parked-move-tap-shift-3622.spec.ts`: opens a parked order's
  Move-table toggle at the kiosk floor (1024x600), records its screen
  position, taps that exact position again, and asserts (a) whatever
  `document.elementFromPoint` finds there is not the order's own resume
  target, (b) that target's computed `pointer-events` is `none`, and (c)
  the order never lands in `#basket`.

## Root cause (confirmed empirically, not just reasoned)
`.parked-orders-list > li` is `display: grid` with
`grid-template-columns: minmax(0, 1fr) auto 46px` — **per `<li>`**, not
shared across the list. ut-docs#3582 (merged minutes before this card was
filed) reworked this row from the old flex-wrap layout to this grid, but it
turns out to have the **same failure mode via a different mechanism**:
opening `.parked-move[open]` relocates it to `grid-column: 1 / -1; grid-row:
2`, vacating column 2's row-1 track. With nothing left to size it, that
`auto` column can collapse toward 0, and the order's own `minmax(0, 1fr)`
column (column 1) grows to absorb the freed space — widening into the old
toggle position. The independent reviewer's own probe measured this
directly: closed tracks `477/129/46px` → open tracks `606.5/0/46px` at
1024x600.

## TDD
- Reproduced the bug first: the original test (pre-fix CSS) failed with
  the modal closing and Coca-Cola back in `#basket` — confirmed independently
  by the reviewer in an isolated clone (reverted the CSS, ran the spec, saw
  the same failure, restored, saw it pass).
- After the reviewer's finding #2 (see below), re-verified the strengthened
  test myself: reverted the CSS again, confirmed the new deterministic
  assertion (`elementFromPoint`/`pointer-events`) fails at the same point,
  restored, confirmed green.

## Findings
| Sev | Finding | Outcome |
|---|---|---|
| should-fix | The toggle (`<summary>`) itself also relocates when opened, so a second tap at the old position no longer closes the panel (lands on inert/empty space instead) — not unsafe, but the "tap again to close" intent isn't met. | Deferred: ut-docs#3630 (follow-up card). |
| should-fix | The original end-state-only assertions (`modal` visible, `#basket` lacking the item) could pass on a race if the resume request were slow enough to not have landed by the first poll. | Fixed: added a deterministic `elementFromPoint`/computed-`pointer-events` check before the second tap, independent of any request timing. |
| nit | Inert order button gives no visual cue while its panel is open. | Accepted, no change — covered by the same follow-up card's UX pass. |
| nit | Possible CSS specificity conflict. | Checked: none — the new rule only sets `pointer-events`, no neighbouring rule touches it. |

Checked with no issue: selector scope (only `.parked-order`/
`.parked-order-form`, not Cancel or the toggle itself — verified by the
reviewer hit-testing all three at their own centres), the sub-640px stacked
layout (order button is already full-width there, so the rule is a no-op,
causes no harm), keyboard (pointer-events doesn't affect focus/Enter
activation — the order button stays Tab-reachable and still resumes on
Enter; not a keyboard-equivalent bug, since focus doesn't follow layout
shifts), no Go/SQL/locale changes (money, offline-first, data-access, i18n
not applicable), no real shop/client name in test data
("E2E Tap Shift A/B 3622").

## Verified beyond unit tests
Ran together with the two related existing specs
(`held-table-move-2702.spec.ts`, `parked-orders-popup-2137.spec.ts`) — 8/8
green, no regressions. Reviewer independently re-ran the same three specs
plus the full gate in a separate clone.

Gate: `go build ./...`, `go vet ./...`, `go test ./internal/pages/...`,
`golangci-lint run ./...` (0 issues), `bash scripts/ci/guard-data-access.sh`,
`bash scripts/ci/guard-i18n.sh` — all green, both in the author's checkout
and independently in the reviewer's clone.

## Manual / help topic
Not needed: this is a bug fix to existing interaction behaviour (a
mis-tap no longer corrupts the sale), not a new control or flow. No
`web/help/` topic describes this popup's Move-table toggle at this level
of detail, and nothing discoverable changed.

## Verdict
Safe to merge.

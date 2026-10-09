# Review: parked-orders Move table toggle stays put when opened (ut-docs#3630)

- Date: 2026-10-09
- Lane: lane:cloud-24
- Branch: `fix/3630-parked-move-toggle-stable`
- Author: Sonnet dev subagent (complexity:easy); independent reviewer: Opus 5.5 (fresh context, own worktree)

## What shipped

On the parked-orders popup, opening a row's **Move table** `<details>` used to
relocate the whole element from row 1 / column 2 to the start of row 2, so the
cashier's second tap at the same spot (to close it) landed on inert space.

- `web/ui/partials/parked_orders.html`: a row WITH move targets now also
  renders the invisible `.parked-move-placeholder` right before its
  `<details>`, so column 2 keeps the toggle's width when the panel opens.
- `web/public/app.css`: the open `<details>` spans its `<li>` from row 1
  (`grid-column: 1 / -1; grid-row: 1 / span 2`; narrow layout
  `grid-row: 2 / span 2`) and its `<summary>` is pinned to the inline end,
  exactly over column 2 (`margin-inline-end` = Cancel column + gap, shared via
  `--parked-cancel-col` / `--parked-row-gap` on the li rule). `white-space:
  nowrap` on placeholder and summary keeps both boxes identical. The #3622
  `pointer-events: none` rule stays as belt and braces.
- Native `<details>/<summary>` kept (keyboard/AT, ut-docs#2702); no new strings.

## Tests

- New e2e `e2e/tests/parked-move-toggle-stable-3630.spec.ts` (LTR + RTL at
  1024×600): toggle box unchanged within 1px after opening; summary is the hit
  target at the original centre; Cancel stays the hit target at its centre; the
  order button does not reflow; a second tap at the same point closes the panel
  and does not resume the order.
- New Go test `TestParkedOrders_MovableRowRendersPlaceholderBeforeDetails`.
- TDD verified twice (dev, then reviewer independently): with the parent CSS +
  template the spec fails `toggle x moved … Received: 483.23`; the Go test
  fails `movable row h1 should carry exactly one placeholder, got 0`. The
  "order button reflowed" assertion was re-verified by the orchestrator with
  the old template: fails `Received: 131.77` (475 → 607px), passes with it.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | The e2e spec passed even without the placeholder (CSS alone pins the summary); only the Go markup test proved the placeholder, and without it the order button grows under the summary, hiding its meta text. | Fixed: order-button geometry assertion added; verified failing without the placeholder. |
| 2 | nit | `46px + .5rem` coupling repeated in three places. | Fixed: custom properties on the li rule. |
| 3 | nit | A very long label at 360px wrapped the closed placeholder but not the open fit-content summary (slight wobble; tap still closed it). Shipped locales (ar/en/fa/tr) are short. | Fixed: `white-space: nowrap` on both. |

Checked fine by the reviewer: Cancel and every option button stay topmost at
their centres while open; row 1 stays 46px; narrow layout at 600/360 in LTR and
RTL keeps the summary in place and closes on second tap, no horizontal scroll;
placeholder is `aria-hidden`, `visibility: hidden`, not in tab order; Enter /
Space toggle and focus ring unchanged; all-rows-movable and mixed rows; the
`.parked-move` selector is used only by this partial.

## Verified beyond automated tests

Screenshots at 1024×600 (LTR closed/open, RTL open) looked at: the toggle sits
in the same top-right spot when open, options wrap on the line below, Cancel
visible, no overlap or clipping. Not verified: WebKit engines (Chromium only),
real touch hardware (mouse-driven), the 360px layout through the sell flow (not
reachable there yet, ut-docs#3060; the reviewer exercised it by resizing after
opening).

## Gates

`gofmt -l .` clean; `go build ./...`; `go vet ./internal/pages/...`;
`go test ./internal/pages/... ./web/...`; guards: i18n, no-showmodal,
data-access, compliance-claims, competitor-naming, help-topics, help-drift,
pipefail-grep-q, e2e-no-browser, docs-shots (surface hash refreshed: no manual
screenshot shows this popup). e2e: 33 passed across the #3630, #3622, 2702,
2137, 3582, 2147 and 2873 specs.

## Verdict

Safe to merge.

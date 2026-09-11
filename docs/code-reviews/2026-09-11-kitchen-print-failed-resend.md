# Kitchen print failed — resend button (ut-docs#2063)

## What shipped

The **⚠ Kitchen print failed** warning on the Orders board (`/orders`,
also shared verbatim with the per-station `/kitchen-display/{station}`
board) had no clearing path anywhere in the till — unlike the matching
**⚠ Receipt print failed** warning, which clears by reprinting from the
Journal. The clearing code path, `POST /api/print/kitchen`
(`internal/pages/kitchen_print.go`), already existed, was already
tested, and already cleared the failure flag (`SetKitchenPrintFailed`)
on a fully successful send — but nothing under `web/ui` ever called it.

Per the card's own acceptance criteria ("a decision is made: wire a real
resend action, or document the warning as non-clearing by design"), this
wires a real **Resend kitchen ticket** button directly into the warning
cell of `web/ui/partials/orders_list.html`, posting to the existing
endpoint via htmx (`hx-post="/api/print/kitchen"`, `hx-vals` carrying
`receipt_no`), mirroring the existing status-change buttons on the same
row. No new Go logic was needed — the endpoint, its tests, and its
flag-clearing behavior were already correct; only the missing UI trigger
was added.

- `web/ui/partials/orders_list.html`: new button + message span inside
  the `.KitchenPrintFailed` block; `$receiptNo` hoisted to be declared
  once per row (was previously declared later, inside the actions `<td>`
  only) so both blocks can use it.
- `internal/pages/order_status_test.go`: two new tests —
  `TestOrdersListFragment_KitchenPrintFailed_HasResendButton` (button
  present, correct `receipt_no`, correct label) and
  `TestOrdersListFragment_NoKitchenFailure_NoResendButton` (button
  absent on a healthy row).
- `web/locales/{en,ar,fa,tr}.json`: new key `orders.print.kitchen_resend`.
- `web/help/en/printing.md` (Kitchen tickets) and
  `web/help/en/order-status.md` (Notes) updated to describe the new
  button instead of the old "nothing clears this" text. Edited as prose
  only (no new headings/bullets) so the change doesn't newly desync the
  ar/fa/tr/de translations' *structure* — `guard-help-drift.sh` only
  checks structural counts, not content accuracy, and those topics were
  already flagged as pre-existing, English-only content
  (ut-docs#332/#1962/#1973); this card doesn't widen that gap.
- `web/help/img/manifest.json` + regenerated screenshots via
  `make docs-shots` (120 screenshots, 30 topics × 4 locales).

## Independent review

Fresh-context Sonnet subagent (complexity: easy, per Model routing by
complexity), isolated worktree. Found **no blocking issues**. Two
non-blocking findings, both addressed:

1. **Cross-station over-send.** `POST /api/print/kitchen` resends every
   station's ticket for a sale, not just the one that failed — pre-existing
   endpoint behavior, but this card newly puts a resend trigger in front
   of station-scoped kitchen staff on `/kitchen-display/{station}` (the
   partial is shared). Filed as a follow-up: ut-docs#2098. Not fixed here —
   out of this card's scope (it's the existing endpoint's contract, not a
   regression this diff introduces), and the reviewer's own verdict was
   "not severe enough to block this easy card."
2. **Doc timing.** The printing.md text originally said a resend clears
   the warning "in a few seconds" — the endpoint fires no SSE/`orders-push`
   nudge on success, so clearing only happens via the fragment's plain 15s
   poll. Fixed: reworded to "within 15 seconds."

TDD independently re-verified by the reviewer: reverted only the template
change (kept the new tests), confirmed
`TestOrdersListFragment_KitchenPrintFailed_HasResendButton` fails with a
real assertion error (not a compile error) against the un-fixed template;
restored, confirmed both new tests pass again.

## Verified beyond automated tests

- `gofmt -l .`, `go vet ./...`, `go build ./...` clean.
- `go test ./...` — full suite green (all packages).
- `golangci-lint run ./...` — 0 issues.
- `guard-i18n.sh`, `guard-help-topics.sh`, `guard-help-drift.sh`,
  `guard-docs-shots.sh`, `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-compliance-claims.sh` — all green.
- `shellcheck` unavailable in this cloud sandbox (no binary on `PATH`) —
  no shell scripts were touched by this diff, so this is a pre-existing
  environment gap, not a regression.
- **Real driven verification against the actual running binary**: built
  the server, ran with `UT_AUTH=off` against a throwaway sqlite DB, seeded
  a completed sale with `SetKitchenPrintFailed` set directly via the
  repository, confirmed the button renders in the `/ui/orders` fragment
  with the correct `receipt_no`, then POSTed through it for real — got
  back the existing endpoint's real `✗ No kitchen printer configured`
  response (no printer configured in this sandbox) and confirmed the
  warning correctly stayed set (endpoint only clears on full success,
  unchanged existing behavior).
- **Visual check, all three renders read cleanly, no overlap/clipping/
  misalignment**: took real Playwright/Chromium screenshots of `/orders`
  at 1024×600 with two seeded rows (one kitchen-only failure, one
  kitchen+receipt) in `en` (LTR), `ar` (RTL) and `fa` (RTL, longest
  string — wraps to two lines inside the button, same as the existing
  Cancel-order button already does, no overflow). RTL mirrors the whole
  row correctly (rail moves to the right, column order flips) with no
  literal left/right CSS in the new markup — only existing `.btn`/`.muted`
  classes, no new styles. Not verified: dark theme, or the 1280×800
  pilot-tablet viewport specifically — no device available in this cloud
  session.

## Safe-to-merge verdict

**Safe to merge.** No blocking findings from the independent review;
both non-blocking findings addressed (one fixed inline, one filed as a
scoped follow-up, ut-docs#2098).

## Explicitly deferred

- ut-docs#2098 — scope kitchen resend to the requesting station (or hide
  the button on the per-station board) so a station worker's resend
  doesn't duplicate-print to every other station.
- ar/fa/tr/de translations of the prose changes in `printing.md`/
  `order-status.md` stay behind English (structure unchanged, so
  `guard-help-drift.sh` doesn't flag it) — same accepted, pre-existing gap
  as ut-docs#332/#1962/#1973, not widened by this card.

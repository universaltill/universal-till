# Review — compact tender panel: thin scan row, one bottom row (Pay + Hold / New sale / Open orders) (ut-docs#2702)

- **Author:** Opus 5.5 (Dev subagents). **Reviewer:** Fable 5.1, fresh context. Complexity:medium: one round, plus a scoped re-check of the restored Move table (new code added after round 1).
- **Scope:** scan row as compact icon buttons (44px targets); one action row `Pay {total}` (opens the payment overlay) + icons Hold / New sale / Open orders with a count badge (`GET /ui/open-orders-badge`, localized digits, 99+). The held-sales strip and the standalone Quick-pay button are gone. The preferred method (`payments.default_method`) leads the payment panel at full width. Freed height goes to the product grid (+~78px at 1280×800).

## Round 1
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | CI | docs-shots manifest stale | Regenerated on the merged tree before merge |
| 2 | High (feature loss) | "Move table" only existed on the removed strip | **Restored** in the Open orders popup: per dine-in row `<details>` list of free tables → same `POST /api/pos/held/table` (`view=parked-orders`), same free-table validation, cross-till claim hand-over and occupied toast. Go + e2e tests |
| 3 | Medium | Help still described the strip (open-orders.md, tables.md ×5 languages) | **Fixed** |
| 4 | — | Branch behind main (#2704 guard) | Rebased; guard passes |
| 5 | Low | Badge refreshes only on page load / this till's `held-changed`; each sale-screen load now reconciles with the primary | Documented in `open_orders_badge.go` |
| 6 | Nit | stale comment; unused `hold.strip.title` | Comment fixed; dead key → **#2710** |

## Re-check of the Move table fix
| # | Sev | Finding | Outcome |
|---|---|---|---|
| F1 | Medium | The popup's refusal notice used `id="toast-message"` and survived closing the popup. The sale screen's `#toast-message` checks then found it: after a cash sale the payment overlay stayed open, and split tender reported "table occupied" | **Fixed**: own id `parked-orders-toast` + popup body cleared on close. Regression e2e (refuse → close → cash sale → overlay closes) proven red without the fix |
| F2 | Low | Keyboard focus dropped to `<body>` after a move | **Fixed**: focus returns to that order's Move table control (asserted in the same e2e) |
| F3 | Low | The popup sheet covers the nav rail below ~34rem | Accepted: same frame as `#pfand-modal`/`#elevation-modal` |
| F4 | Nit | Help implies the badge is live across tills | Accepted, documented in code |

## Checked, no issue (reviewer)
Order-type rule preserved (the at-pay prompt was UI-only and still gates `posOpenPayment()`, the only opener of the overlay); the overlay keeps New Sale / Hold / New Customer copies; layouts 1024×600, 1280×800, 850×700 stacked, 360px, fa RTL; badge `hx-target="this"` (inheritance trap tested); deleted specs only covered strip geometry, and the reachability spec hit-tests every control at 5 sizes with 0–3 held sales; `view` param strictly allowlisted; move route gate unchanged; CSS brace-balance test proven red on a stray `}`.

## Verification
`go test -race` all packages (Makefile timeouts for pages/plugins); pages targeted tests; e2e compact-tender-panel-2702, tender-panel-reachable, parked-orders-popup-2137, open-orders-row-geometry-2147, hold-named-tab, held-table-move-2702 (3), table-picker-error-gate-1638; guards i18n, help-drift, help-topics, e2e-no-browser, data-access; gofmt, golangci-lint. Real touch hardware not tested (headless Chromium).

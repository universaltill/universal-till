# Review — pay-at-counter orders become held sales, paid as normal signed sales (ut-docs#2703)

- **Author:** Opus 5.5 (Dev subagents). **Reviewer:** Fable 5.1, fresh context, two rounds (round 1 found a blocker-class kitchen/money issue, so round 2 was scoped to the fix). Card complexity:hard.
- **Design (product owner):** an unpaid pay-at-the-counter order is exactly a held order. Kiosk checkout parks the kiosk basket as a held sale ("C-12 · Takeaway", table, order-time prices/modifiers) through the till's own `heldSaleWriteThrough`. It is recalled with the existing resume path and paid through the normal tender, so it becomes a fiscally signed sale in day-close. A walk-up order prints to the kitchen at payment. A table-QR order prints at checkout and is not reprinted.

## Round 1
| # | Sev | Finding | Outcome |
|---|---|---|---|
| F1 | High | Basket-level `KitchenSent` flag: items added at the till to a recalled table order never printed, and a printer outage at checkout lost the ticket | **Fixed**: per-line `KitchenSentQty` (snapshot/restore), synchronous checkout print (5 s timeout) that marks lines only on success, tender prints only the per-line delta |
| F2 | Medium | C-numbers not unique across tills | **Fixed**: `C-<sync.receipt_prefix><n>`, read in the same tx as `NextDisplayNo` does |
| F3 | Medium | Replica push reached the main but the reply was lost → the order could be tendered twice (pre-existing, ADR-0093) | Follow-up **#2712** |
| F4/F5 | Low | Park failure burns a C-number; voiding every line loses the C-number | Accepted; help updated |
| F6 | Process | de/es packs | Pack PRs in the same cycle |

## Round 2 (scoped to the fix)
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | Two engine reads at table-QR checkout: an add between them could mark a line sent that was never printed | **Fixed**: one `Snapshot()`; ticket lines and marks both come from it (`counterOrderTicketFromSnapshot`) |
| 2 | Low | `completeTender` built the kitchen filter from a second engine read | **Fixed**: the handler passes `kitchenDeltaFilter(lines)` built from the same slice as the sale lines |
| 3,4,6 | Low | Checkout spam surface / orphaned `held` C-rows / blank prefix still collides | Follow-up **#2714** |
| 5 | Low | Removing a sent item doesn't notify the kitchen; re-adding reprints | Documented in help (5 languages) |

## Checked, no issue (reviewer)
Fiscal: `display_no` is display-only (receipt_no, `fiscal.sign.ask`, DSFinV-K key off the receipt/sale id; no UNIQUE on display_no; `NextDisplayNo` ignores `C-…`). Exactly one signed transaction per paid order, none at park/resume. ADR-0048 hard gate unchanged. ADR-0086 respected. Kiosk security: the snapshot is built server-side from the kiosk engine; no client price/label input. Normal sales' kitchen printing is unchanged (nil filter). Positional sale-line mapping is sound (`line_no = i+1`, no skip/reorder; vouchers/discounts/deposits aren't sale_lines). TDD re-verified in scratch worktrees (reverting each fix fails its tests).

## Verification
`go test -race ./...` (Makefile timeouts for pages/plugins); pos/data race; pages Counter|Kitchen|Tender|Print|Held|SelfOrder|Kiosk (+ -race); e2e `kiosk-counter-order-held-2703` (kiosk → Open orders → recall → Cash → receipt → gone) 3/3 repeat; gofmt/vet; guards i18n, help-drift, help-topics, kiosk-engine, data-access.

# Code review — ut-docs#820: assign/move table on an order + table visibility

Date: 2026-08-20
Reviewer: Opus (independent subagent, worktree-isolated) — a different model from
the implementer (Sonnet)
Branch: `feat/820-table-assignment` — PR universaltill/universal-till#406
Commits: `713de50e` (feature), `b24bdb13` (move-then-resume fix + screenshots),
`152394da` (review B1 fix: Move-table UI + soft-gate + dead-key removal)

## What shipped

- **Migration 056** — adds `sales.table_id` (+ index) and `sales_archive.table_id`,
  both nullable/additive, mirroring how `order_type` was added in migration 026;
  the reset-archive column list is updated for `sales`. `held_sales.table_id`
  already existed from #814's migration 054 — this wires it up in Go.
- **`internal/pos`** — `Service.SetTable`/`ClearTable`/`TableID()`/`TableLabel()`;
  `Basket.TableID/TableLabel`; carried through hold/resume via `BasketSnapshot` +
  `Snapshot`/`Restore`; cleared on Tender/reset. `SaleInput.TableID` threaded into
  `InsertSale` (persisted on the completed sale).
- **`internal/data`** — `HeldSale.TableID` + `HeldSalesRepo.SetTable`; `POSRepo.GetTable`,
  `POSRepo.IsTableFree(id, excludeHeldSaleID)`; `SaleDetail.TableID/TableLabel`
  resolved via LEFT JOIN on `tables` in `GetSaleDetail` (feeds both receipt and
  kitchen ticket). `nullIfEmpty` used for "no table" → NULL everywhere.
- **`internal/pages`** — `POST /api/pos/table` (assign/clear on the live basket,
  label resolved server-side); `GET /ui/pos/table-picker` (a cashier-reachable
  fragment showing the current table + free tables, distinct from the
  manager-gated floor-plan editor); `POST /api/pos/held/table` (move a parked
  order, `IsTableFree`-validated) **plus its held-strip UI control**; receipt
  label captured at tender; `KitchenTicket.Table` (previously an unused stub)
  populated from `detail.TableLabel`.
- **UI** — table chrome (current table, clear, picker, and the per-held-order
  "Move table" control) lives entirely in the `table-picker` / held-strip
  fragments, which render nothing when no tables are configured (ADR-0054
  soft-gate). `receipt.html` table line. i18n `basket.table.*` in en/ar/fa/tr.
  Manual (`sell.md`, `tables.md`, all 4 locales) + regenerated `sell` screenshots.

## What the independent review found

### Blocker (fixed before merge)

- **B1 — the "move table" half was backend-only.** `POST /api/pos/held/table`
  (correctly `IsTableFree`-validated and unit-tested) had **no UI** reaching it,
  yet the manual in all four locales instructed the cashier to "tap **Move
  table** on the strip", and three i18n keys (`basket.table.move`,
  `.move_success`, `.assigned`) were dead — confirming dropped frontend wiring.
  Since the issue is titled "assign/**move** table…", the move half is a named
  deliverable, and shipping a manual that describes a non-existent control
  violates the standing "manual must never be ahead of the product" rule.

  **Resolution (completing the deliverable, not trimming the manual):** the
  held strip now renders a `<details>` "Move table" control per parked order,
  listing only currently-free tables (via `ListTablesWithState`) and excluding
  the order's own table, POSTing to the existing validated endpoint. The two
  genuinely-unused keys (`basket.table.assigned`, `.move_success`) were removed;
  `basket.table.move` is now live. Covered by new tests
  `TestHeldStrip_RendersMoveControlToFreeTable` /
  `TestHeldStrip_NoMoveControlWhenNoTables`.

  **A layout regression the review's e2e run surfaced was fixed in the same
  commit:** the basket previously rendered an always-on "Table: none" row plus a
  "no free tables" row even for shops with zero tables, pushing basket lines
  down and covering the totals (broke `sale-screen-213` and `phone-width-413`).
  All table chrome now lives in the fragment, which renders nothing when no
  tables are configured — the ADR-0054 soft-gate. New tests
  `TestTablePicker_NoTablesConfiguredRendersNothing` /
  `_ConfiguredButAllOccupiedShowsEmptyState` pin the distinction (a no-tables
  shop shows nothing; a tables-but-all-busy shop shows the "no free tables"
  message).

### Accepted / not blocking

- **N1** — `POST /api/pos/table` (assign to the live basket) trusts the picker's
  occupied-filtering rather than re-checking `IsTableFree` server-side. Low risk
  on single-till/single-process, and occupancy is driven only by `held_sales`, so
  a completed sale sharing a `table_id` with a parked order isn't corrupting.
  Optional defense-in-depth for a future card.
- **N2** — the new semantic CSS classes (`held-move`, `table-picker-*`,
  `held-chip-table`, `receipt-table`) are unstyled; interactive elements reuse
  the existing `.btn`/`.secondary`/`.muted` tokens, so it's functional. Acceptable
  for v1 (the regenerated screenshots reflect the plain rendering); a small
  stylesheet pass is worth a follow-up.
- **N3** — the receipt uses the label captured live at tender while the kitchen
  ticket re-resolves via the join; a sub-second rename between could differ.
  Negligible.

## What was verified beyond the automated tests

- Full `go test ./...` green; `go build`, `go vet`, `gofmt` clean; guards green
  (data-access, i18n, kiosk-engine, help-topics, plugin-menu-read, docs-shots,
  compliance-claims).
- **TDD re-verification #1 (move-then-resume fix):** reverting the resume overlay
  makes `TestHoldMoveThenResume_ReflectsMovedTable` FAIL with `TableID="tbl-1"`
  (want `tbl-2`); restoring makes it PASS. Real, non-tautological guard —
  confirmed independently by the reviewer in an isolated worktree.
- **TDD re-verification #2 (receipt label):** deleting the `{{ if .TableLabel }}`
  block makes `TestRenderReceipt_TableLabelShownWhenAssigned` FAIL; restoring
  makes it PASS. Asserts both presence and absence — not tautological.
- **Archive parity:** `TestResetThenRestoreRoundTrip_SaleTableID` confirms
  `table_id` survives reset → archive → restore.
- **Real driven runs:** the two originally-failing e2e geometry specs
  (`sale-screen-213`, `phone-width-413`, 18 assertions) re-run green after the
  soft-gate fix; a freshly-booted till with two seeded tables + a parked order
  confirms `/ui/held` renders the Move control, offers the free table, excludes
  the order's own table, and shows the table chip. Screenshots regenerated and
  visually checked (light/LTR + fa/RTL by the Tester earlier; RTL mirrors
  correctly via logical properties).
- Repository pattern, offline-first (no network in the assign/move/tender path),
  money (untouched), kiosk isolation, RTL — all confirmed by read.

## Deferred / disclosed

- LAN journal replay (`sync_sales.go`) deliberately does not thread `TableID`:
  table ids are local per-till UUIDs with no cross-till table sync (none exists
  per ADR-0054/#814), so forwarding a replica's `table_id` would risk an FK
  violation against the primary's own `tables`. The column is nullable and left
  unset on replay. A genuine future question, not silently handled — worth a
  Backlog card if cross-till table service is ever wanted.
- Bill merging/splitting (#817), guest table ordering (#815), reservations
  (#103) — separate cards.

## Verdict

**SAFE TO MERGE.** The data model, repositories, service layer, tender
persistence, receipt/kitchen-ticket visibility, the move-then-resume fix, and
(after B1) the move-from-strip UI are all correct, tested, and standards-compliant.
N1–N3 are non-blocking.

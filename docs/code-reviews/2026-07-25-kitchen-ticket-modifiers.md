# 2026-07-25 — Kitchen ticket carries item modifiers (ADR-0020 Phase 5)

## Context
While scoping Phase 5 ("order handoff / kitchen ticket"), found that the
kitchen-ticket mechanism (`internal/pages/kitchen_print.go`, fired async
after every completed sale — cashier or kiosk, via `printKitchenAsync`
inside `completeTender`) built its ticket from `POSRepo.GetSaleDetail`,
which loaded each line's name/qty/price but never re-read the line's
chosen modifiers (`sale_line_modifiers`, added in Phase 1) after the sale
completed. Meanwhile `internal/print/kitchen.go`'s `KitchenItem` struct
already had a `Modifiers []string` field with full ESC/POS rendering
support (indented `- modifier` lines under each item, already tested) —
that plumbing existed and was simply never fed real data. A customer's
"extra shot" or "no onions" was silently dropped before it ever reached
the kitchen printer, for both cashier and kiosk orders — directly
undercutting the reason item modifiers exist at all (a real ask: "customer
should be able to add extra shot to the coffee or customize the
sandwich").

This turned out to be nearly all of Phase 5's substance: the shared
kitchen-ticket mechanism ADR-0020 called for already exists and already
fires for kiosk orders (since kiosk checkout goes through the same
`completeTender` pipeline as the cashier), so Phase 5 didn't need new
plumbing — just this one real gap closed.

## Design
`SaleDetailLine` gained a `Modifiers []string` field. `GetSaleDetail`'s
line query now also selects `sl.id` (previously unselected — nothing
needed it), and a second, single query joins `sale_line_modifiers` to
`sale_lines` on `sale_id = ?` (one query for the whole sale, not one per
line) and groups `option_name_snapshot` by `sale_line_id` into a map;
each line's modifiers are attached by looking up its own primary-key ID
in that map — not by positional/index matching between two independently-
ordered result sets, so there's no ordering assumption to get wrong.
`buildKitchenTicket` (`kitchen_print.go`) now passes `l.Modifiers`
through to `print.KitchenItem.Modifiers`, which the print layer already
knew how to render.

## Independent review
Opus-model review, proportionate to a read-then-print change (no
payment/inventory/tax logic touched) but with the join/index-matching
and "no regression in refund/journal/invoice/sync" claims verified for
real rather than assumed.

**Confirmed correct:**
- The modifier attachment is genuinely map-keyed by each line's own
  `sale_lines.id` (a `TEXT PRIMARY KEY`, so no collision risk possible),
  not an index pairing between two separately-ordered queries — reviewer
  traced this precisely and confirmed the two queries' `ORDER BY` clauses
  are irrelevant to correctness, only to display order within a line.
- A line with zero modifiers gets a `nil` `Modifiers` (missing map key),
  never an error or a misattributed neighbor.
- The `defer`→explicit-`.Close()` refactor of `lineRows`/`modRows` has no
  leak and no double-close on any exit path (traced every early return).
- Every other `GetSaleDetail`/`GetSaleDetailByID` caller
  (`journal_page.go`, `invoice_page.go`, `refund_page.go`, `pos_api.go`,
  `sync_sales.go`) reads `SaleDetailLine` by named field only, none
  references `.Modifiers`, and no template renders it — confirmed zero
  behavior change for any of them, not just "should be fine."
- The modifier fetch is one query for the whole sale (not N+1).
- `RenderKitchenTicket`/`RenderKitchenTicketText` already correctly
  skip empty/whitespace-only modifier strings and produce zero output
  for a `nil` or empty `Modifiers` slice — no stray blank line risk.
- **Reviewer ran a mutation test**: broke the map-grouping key, both new
  tests failed with the expected wrong-output shape; reverted, both
  passed — direct evidence the tests bite a real regression, not just
  exercise the happy path.

**No findings.** One minor, no-action observation: modifier display
order relies on SQLite `rowid` (insertion order), since
`sale_line_modifiers` has no snapshotted `sort_order` column — fine for
a kitchen ticket's purposes, noted for awareness only.

## Verification
`go build ./...`, `go vet ./...`, `go test ./...` (full suite, zero
regressions), `bash scripts/ci/guard-data-access.sh`,
`bash scripts/ci/guard-i18n.sh` — all green, both by me and independently
by the reviewer.

Live-verified against a real built binary: seeded a real modifier
group/option on a real catalog item via direct SQL, added it to a kiosk
cart through the real HTTP modifier-picker flow, pointed
`printer.kitchen_addr` at a local file (device-transport mode), completed
the kiosk checkout for real, and inspected the raw ESC/POS bytes written
— confirmed `Instant Coffee 200g` followed by `  - Extra shot` on the
next line, exactly matching the expected ticket format.

New tests: `TestPOSRepo_GetSaleDetail_IncludesLineModifiers`,
`TestBuildKitchenTicket_IncludesLineModifiers`.

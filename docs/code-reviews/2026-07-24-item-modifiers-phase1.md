# 2026-07-24 — Item modifiers, Phase 1: schema + repo + basket-engine plumbing (ADR-0020)

## Context
Farshid (2026-07-24): a self-order kiosk is top priority, and it needs item
customization (his examples: extra shot in a coffee, build-your-own
sandwich) — without which a cafe kiosk is close to useless. He also
confirmed the cashier till needs this too, not just the kiosk, so Phase 1
ships it on the existing cashier POS first. Full design:
`ut-docs/adr/0020-self-order-kiosk-and-item-modifiers.md`,
`specs/011-self-order-kiosk/spec.md`.

This is Phase 1 only: catalog data model + repo + basket-engine plumbing.
Deliberately headless — no cashier-facing UI yet (that's the next task);
this slice is fully covered by unit/integration tests instead of manual
click-through, since nothing user-facing exists to click through.

## Design
**The core decision**: rather than adding a parallel "modifiers total"
that every money computation would need to know about, a chosen
modifier's price delta is folded directly into the basket line's existing
`PriceCents` at add-time (`Service.AddLineWithModifiers`,
`internal/pos/service.go`). `Modifiers []data.SelectedModifier` rides
alongside purely as a display/persistence snapshot. This means the entire
downstream money pipeline — `recomputeTotals`, `computeSaleTotals`,
`CompleteSale`'s persistence loop, the receipt-fallback duplicate calc in
`pos_api.go` — needed **zero changes**, because none of them care where a
line's unit price came from.

Other pieces:
- Migration 017: `item_modifier_groups`/`item_modifier_options` (the
  catalog-declared customization menu, additive-only price deltas in v1)
  and `sale_line_modifiers` (immutable snapshot of what was actually
  chosen — never a live FK to the option, so a later catalog edit can't
  rewrite a past receipt).
- `internal/data/modifier_repo.go`: full CRUD for groups/options (ready for
  the admin catalog UI in the next task) + `ListGroupsForItem` +
  `InsertSaleLineModifiers`.
- Merge behavior extended (`BasketLine.ModifierSignature()`) so two
  differently-customized instances of the same item stay distinct lines,
  while identical customizations still merge quantity like today.
- Hold/resume (`hold.go`) carries `Modifiers` through so a held sale
  doesn't silently drop a customer's customization on recall.

**A real gap this creates, documented and deliberately deferred, not
fixed here**: `Service.Remove`/`UpdateLine` are SKU-keyed and match
every/first line sharing a SKU — safe before modifiers existed (at most
one line per SKU, guaranteed by the old merge logic), unsafe once two
modifier-distinct lines can share a SKU. Nothing calls
`AddLineWithModifiers` outside tests yet, so this has no live exposure.
Documented via doc comments on both methods plus a regression test
(`TestRemove_KnownGap_DeletesAllLinesSharingSKU`) that forces the next
task (cashier UI) to deal with it explicitly — wiring "remove this line"
to the existing SKU-keyed method for a modifier-bearing item would be a
real, live bug.

## Independent review
Opus-model review, adversarial brief, explicitly weighted toward the
"zero downstream changes" claim since that's the single load-bearing
design decision of the whole change, plus the documented gap's "no live
exposure" claim.

**Confirmed correct (reviewer verified independently, traced the code
directly rather than trusting the claim):**
- Every money-path consumer reads a line's own stored price
  (`PriceCents`/`UnitPrice`), never re-derives it from `ItemID` via a
  fresh catalog lookup — checked `recomputeTotals`, `computeSaleTotals`,
  the `CompleteSale` insert loop, the `pos_api.go` duplicate calc, **and
  proactively checked the refund and sale-sync paths too**
  (`refund_page.go`, `sync_sales.go`) since those were the real risk not
  explicitly named in the brief — both read the stored line price, no
  catalog re-lookup anywhere. The folded delta cannot be silently dropped.
- Merge/identity logic is sound: identical selections merge (any order,
  via sorted signature), different ones stay distinct; option-ID comma
  collision is inert since option IDs are always UUIDs.
- The documented `Remove`/`UpdateLine` gap is genuinely inert — grepped
  every caller of `AddLineWithModifiers` repo-wide, zero non-test callers.
- Hold/resume JSON round-trip is lossless (`SelectedModifier` fully
  exported).
- `sale_line_modifiers` persists correctly inside the same transaction,
  same `lineID`, for every line; zero modifiers → zero rows, not a
  garbage row.
- Migration constraints and cascade behavior are exactly as intended:
  option/group deletion never cascades into historical
  `sale_line_modifiers` rows (only `sale_lines` deletion does) — the
  snapshot really is immutable and independent of the source rows'
  lifecycle.
- `TestCompleteSale_PersistsModifiers`'s assertions are meaningful, not
  vacuous — would fail on either a dropped or double-counted delta.

**No findings requiring a fix.** Two non-bug notes from the reviewer:
`SelectedModifier` uses PascalCase (not snake_case) JSON tags — fine,
it's an internal hold-blob field, not a wire API, so the repo's
snake_case JSON rule doesn't apply; and refund/sync don't forward the
modifier snapshot rows themselves (only the folded price), acceptable for
a headless v1 since money correctness doesn't depend on it.

## Verification
`go build ./...`, `go vet ./...`, `go test ./...` (full suite, 28
packages, zero regressions), `bash scripts/ci/guard-data-access.sh` — all
green, both by me and independently by the reviewer. New tests:
`TestAddLineWithModifiers_FoldsDeltaIntoPrice`,
`TestAddLineWithModifiers_IdenticalSelectionsMerge`,
`TestAddLineWithModifiers_DifferentSelectionsDoNotMerge`,
`TestModifierSignature_OrderIndependent`,
`TestModifierSignature_EmptyForNoModifiers`,
`TestRemove_KnownGap_DeletesAllLinesSharingSKU`,
`TestSnapshotRestoreRoundTrip_PreservesModifiers`,
`TestCompleteSale_PersistsModifiers`,
`TestModifierRepo_CreateAndListGroups`,
`TestModifierRepo_ListGroupsForItem_SkipsInactive`,
`TestModifierRepo_DeleteGroupCascadesOptions`,
`TestModifierRepo_CreateOption_RejectsNegativeDelta`.

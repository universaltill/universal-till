# Dead-code burn-down: `internal/pos` slice (ut-docs#1566, slice 9)

**Card:** universaltill/ut-docs#1566 — "Burn down 97 unreachable functions in
universal-till" (`complexity:hard`, multi-slice, ongoing since 2026-09-04).
**This slice:** the final package on the board, `internal/pos`'s 19 baseline
entries — 67 → 52 lines remaining in `scripts/ci/deadcode-baseline.txt`.
**Dev:** Fable subagent, isolated worktree (per `complexity:hard` routing).
**Review:** Opus, cold — no dev reasoning shared, diff + real source only.

## Scope

19 functions in `internal/pos` flagged unreachable by `deadcode` against the
two shipped binaries: `tax.go` (2), `pricing.go` (6), `catalog_search.go`
(3), `catalog_ops.go` (1), `inventory.go` (2), `order_status.go` (2),
`service.go` (3). Tax code and pricing code — triaged carefully per the
parent card's own explicit caution, not deleted on the static tool's say-so
alone.

## Disposition

**Deleted — 15 entries**, each confirmed zero non-test callers by
repo-wide search; every test that used one retargeted to the live
`internal/data` repo method it was always exercising through a redundant
`internal/pos`-package delegation layer — same assertions, same underlying
code, just called directly:

- `tax.go` (whole file): `TaxEngine` interface, `PercentTaxEngine.Compute`,
  `BasisPointsTaxEngine.Compute` — zero callers anywhere, tests included.
  The live tax path, `ComputeTaxBasisPoints` (`internal/pos/money.go:40`),
  is untouched.
- `pricing.go` (whole file): `PricingRepo` interface,
  `ResolveCurrentPrice`/`AppendPriceHistoryItem`/`AppendPriceHistoryVariant`
  and their `*SQL`-suffixed implementations — each a one-line delegation to
  `data.NewPOSRepo(db)`, no SQL text held directly (repository-pattern
  clean). Tests retargeted from a test-only `testPricingRepo` shim straight
  to `data.NewPOSRepo(db)`.
- `catalog_search.go` (whole file): `CatalogRepo` interface,
  `NewCatalogSearcher`, `CatalogSearcher.SearchActiveItems`,
  `CatalogSearcher.LookupActiveVariant`. Tests retargeted to
  `data.NewPOSRepo(db)` directly; the ut-docs#1176 NULL-SKU regression this
  file's tests guard stays covered, now against the repo query itself.
- `inventory.go`: `AggregateInventory`, `CheckNegativeInventory` — the
  wrapper's `tx == nil` guard is duplicated in
  `POSRepo.CheckNegativeInventory` (`internal/data/pos_repo.go:490-492`),
  so nothing is lost.
- `catalog_ops.go`: `UpdateItem` — superseded by
  `UpdateItemReturningWasActive` (ut-docs#1399), the catalog form's only
  item-update path (`internal/pages/catalog/handlers.go:1155`).
- `order_status.go`: `OrderStatusRank` — the retargeted test now pins
  `orderStatusRank` directly, same values.

**Kept with a doc comment — 4 entries** (test-only-reachable by design, stay
in the baseline): `Service.ScanQty`, `Service.Tender`,
`Service.SetLineOrderType` (comment already accurate, untouched),
`OrderStatusBroadcaster.SubscriberCount`.

**Flagged as a real gap: none.** Every deletion was a superseded delegation
layer, not an unwired feature.

## Independent review

Opus, reviewing cold against the real source (not the dev's prose), covering
9 explicit checks: the `tax.go`/`pricing.go`/`catalog_search.go` deletions
against a repo-wide caller search, the `inventory.go`/`catalog_ops.go`/
`order_status.go` deletions against their claimed superseding paths, all 4
kept-with-comment claims, a from-scratch gate run, and the baseline diff
verified against a live `deadcode` run on both trees.

**Verdict: APPROVE WITH FIXES.**

**One inaccurate claim found and fixed** (comment-only, no behaviour
change): `internal/pos/catalog_search_test.go`'s header comment claimed
both `SearchActiveItems` and `LookupActiveVariant` have live repo-direct
production callers. Only `SearchActiveItems` does
(`internal/pages/kitchen_stations_page.go`, `ai_api.go`).
`POSRepo.LookupActiveVariant` has **no** production caller today — a
pre-existing state this repo already recorded in
`docs/code-reviews/2026-08-28-no-sku-uuid-leak.md`, not something this
burn-down introduced. Rewritten to state the two methods' different status
explicitly, and to record that the wrapper's own empty-ID guard survives
unchanged in `POSRepo.LookupActiveVariant` (asserted by
`internal/data/pos_repo_search_test.go`).

**Verified independently, not just re-read:**
- Repo-wide grep for `TaxEngine`/`PercentTaxEngine`/`BasisPointsTaxEngine`:
  zero hits post-deletion.
- `ComputeTaxBasisPoints` traced to 11 real call sites across
  `internal/pos` and `internal/pages` — the actual live tax path, untouched.
- `POSRepo.ResolveCurrentPrice` traced to its real production caller
  (`internal/pages/ai_api.go:152`).
- The real production tender path (`completeTender`,
  `internal/pages/pos_api.go:194`) traced end to end: calls
  `pos.CompleteSale` then `engine.Reset()`, confirming `Service.Tender`'s
  comment is accurate and it has zero production callers (3 test call
  sites only).
- `POSRepo.CheckNegativeInventory`'s `tx == nil` guard confirmed present at
  `internal/data/pos_repo.go:490-492`; the *actual* live negative-inventory
  rule confirmed to run through a separate path entirely
  (`internal/pos/sales.go:827-844`, gated on
  `SaleInput.AllowNegativeInventory`) — the deleted wrapper and
  `POSRepo.CheckNegativeInventory` were both unwired duplicates of that
  rule, not the rule itself.
- Full gate re-run independently in the worktree: `gofmt -l .` empty,
  `go build ./...` clean, `go vet ./...` clean,
  `go test -count=1 ./internal/pos/... ./internal/data/... ./internal/pages/...`
  all green.
- `scripts/ci/deadcode-baseline.txt`'s diff confirmed to remove exactly the
  15 burned-down lines and touch nothing else, by running the real
  `deadcode` tool (pinned version, `-test=false`) against both the pre- and
  post-diff tree: **zero new unreachable entries**, exactly 15 fewer.

**`scripts/ci/guard-deadcode-baseline.sh` itself fails in this sandbox** on
a pre-existing, already-documented limitation (no GTK/WebKit dev headers to
type-check `cmd/unitill-desktop` under `-tags=desktop`) — every prior slice
on this card has hit this. Substitute: the real `deadcode` tool run directly
without the desktop tag, independently by both dev and reviewer, confirming
zero new unreachable functions.

## Verified beyond automated tests

- Read the real production wiring for every "no caller" claim rather than
  trusting comment prose: `internal/pages/pos_api.go`'s tender/scan
  handlers, `internal/pages/kitchen_stations_page.go`/`ai_api.go`'s catalog
  search calls, `internal/pos/sales.go`'s negative-inventory enforcement.
- Cross-checked one claim against this repo's own prior review record
  (`docs/code-reviews/2026-08-28-no-sku-uuid-leak.md`) rather than trusting
  a fresh repo-wide grep alone.
- Ran the full gate twice independently (dev, then reviewer, then a third
  time by the orchestrator after merging `main` back in) rather than trusting
  a single earlier green result.

## Follow-up (not blocking, filed as observations only)

Removing these wrappers leaves several `internal/data` methods with no
production caller of their own: `POSRepo.AggregateInventory`,
`POSRepo.CheckNegativeInventory`, `POSRepo.LookupActiveVariant`,
`POSRepo.AppendPriceHistoryItem`/`Variant`, `CatalogRepo.UpdateItem`.
`deadcode` does not flag any of them (confirmed empirically), so no CI
impact — but they're real candidates for a future `internal/data` slice.
`POSRepo.CheckNegativeInventory` is the most interesting: it duplicates a
rule genuinely enforced elsewhere (`internal/pos/sales.go:827-844`), down to
an identical error string — a latent divergence trap worth a dedicated look,
not raised as a new card here since it's outside this slice's package scope
and this card's own acceptance criteria forbid behaviour changes.

## Baseline

`scripts/ci/deadcode-baseline.txt`: 67 → 52 lines. `internal/pos` fully
cleared — this was the last package still outstanding on ut-docs#1566 per
the prior slice's own comment.

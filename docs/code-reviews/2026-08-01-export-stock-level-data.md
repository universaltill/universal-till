# Code review: export/report dispatcher payload gains stock level data

**Date:** 2026-08-01
**Branch:** `feat/export-stock-level-data`
**Closes:** universaltill/ut-docs#59 ("speedy" POS parity: stock level export)
**Reviewer:** independent subagent, model override `opus` (different from the implementing session's model)

## What shipped

Mirrors the ut-docs#221/PR#144 precedent that added `Sales` to the generic `export.requested.ask` plugin-dispatch payload:

- **`internal/data/export_repo.go`**: new `ExportStockRow` type (`item_id`, `name`, `sku`, `location_id`, `location_name`, `current_qty`, `reorder_level`) and `POSRepo.StockForExport(ctx) ([]ExportStockRow, error)` — reshapes the existing `ListStockLevels` rows (already serving the `/inventory` page) with JSON tags for the wire. No new SQL: it calls `ListStockLevels` directly rather than duplicating its query.
- **`internal/pages/data_api.go`**: `exportRequestPayload` gains `Stock []data.ExportStockRow`; the manager-gated `POST /api/data/export` handler now also calls `StockForExport` and populates it before `AskPlugin`.
- Tests: `TestStockForExport` / `TestStockForExport_Empty` (repo layer, including the inactive-item filter), `TestExportDispatch_PayloadIncludesStockData` (full HTTP → event-bus payload capture, handler layer).

## Independent review

Spawned a `general-purpose` subagent with `model: opus` — a different model from the one running this pipeline session — briefed with the exact diff scope, the relevant `CLAUDE.md` rules (repository pattern, money, i18n, offline-first, plugin signing), the `ut-docs#221`/PR#144 precedent as the format/depth bar, and told explicitly to run the full gate and mutation-test the new tests itself rather than trust the Dev/Tester steps' word.

**Real finding, fixed before merge (should-fix, binding rule):** `ut-docs/reference/plugin-manifest.md`'s documented `export.requested.ask` payload/response contract (added by ut-docs#227 specifically to keep this contract current) covered `sales` only — this diff added `stock` to the same wire contract without updating it. Both this repo's and `ut-docs`' `CLAUDE.md` make same-session doc updates for behaviour changes binding, so this was fixed in-session: the contract doc now documents `stock`'s shape, that it's a live snapshot ignoring `from`/`to` (no stock-movement history exists to reconstruct a past-dated level), and its two filters a plugin author needs to know (active items only, item-level rows only — variant stock is tracked at the parent item per ADR-0011, not surfaced per-variant).

**Real finding, fixed before merge (nit):** `data_api.go` constructed `data.NewPOSRepo(d.Db)` twice in the same handler (once per export call) — collapsed to one local, no behaviour change.

**Real finding, deferred as new Backlog cards (genuinely separable, not this task's scope):**
- **Variant-scoped stock is silently absent from the export** (new card, see below) — `ListStockLevels`'s `INNER JOIN items` (deliberately, per `TestListStockLevels_Batch8`'s own doc comment) only returns item-level inventory rows; a shop using item variants gets a stock export that cannot reconcile against a physical count, with nothing in the payload signalling the gap. The underlying item-level-stock product model is an accepted, intentional position (`internal/cloudsync/cloudsync.go`'s own comment cites ADR-0011), which is why this is a backlog note rather than a blocker — but a real export-completeness gap worth a card of its own.
- **ut-docs#228** (open: gate `export.requested.ask` on a `sales:read` permission, since today `AskPlugin` only checks the coarser `events:receive`) is now also under-scoped: this diff adds a second full ledger (item-level stock, reorder thresholds) to the same coarsely-gated payload. Commented on #228 to recommend it gate `sales` and `stock` on separate permissions rather than one flat check, rather than silently leaving stock ungated when #228 eventually lands.

**Nits noted, not acted on:** `sales` marshals `null` when empty while `stock` (via `make([]T, len(...))`) marshals `[]` — `stock`'s behaviour is arguably the better of the two; harmless either way, no decoder in this ecosystem's plugin guests breaks on either. `Stock` is unbounded and ignores `from`/`to` (documented, and deliberate — no stock-movement history to scope it by), same "no request-size cap in `internal/plugins`" situation the prior review already noted for `Sales`, not new to this diff.

**Everything else checked out clean**, independently re-verified via mutation testing (not taken on the Dev/Tester steps' word): removing the `ReorderLevel` field mapping in `ExportStockRow`'s construction broke `TestStockForExport` with a genuine assertion failure (`ReorderLevel:0` vs. expected `3`); reassigning `Stock: stock[:0]` in the handler broke `TestExportDispatch_PayloadIncludesStockData` with `expected itm-stk in stock payload, got []`. Both reverted, both pass again. Also confirmed: zero SQL in `StockForExport` (calls `ListStockLevels`, doesn't duplicate it); `CurrentQty`/`ReorderLevel` correctly stay plain `float64`/`int` (no money-typing hazard — stock quantities are physical units, not currency); zero i18n/template changes (backend payload only); zero impact on the checkout/offline path (confined to the existing manager-gated `POST /api/data/export`, one read-only local query); no new plugin-execution path (still routed through the existing `AskPlugin`/`manifest_verifier.go`); no file writes anywhere in the diff (so neither the missing-`os.MkdirAll` nor the cwd-relative-path bug class applies); no real client/shop name in any fixture.

**Full gate, before and after the doc/nit fixes:** `go build ./...`, `go vet ./...`, `gofmt -l` — clean. `go test ./...` — one failure, `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`, independently confirmed via `git stash` to fail identically against the pre-existing base commit (container runs as uid 0, so the test's read-only-directory assumption doesn't hold) — pre-existing and unrelated, not a regression, same finding as the prior #144 review. `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh` — pass.

## Verified beyond automated tests

- Read `internal/data/pos_repo.go`'s `ListStockLevels`/`LowStockItem` (source of the reshape) and `internal/db/migrations/001_init.sql`'s `inventory`/`items` schema directly to confirm every field mapped in `ExportStockRow` is real and correctly typed, not hallucinated.
- Ran the full Playwright e2e suite (28/29 passed; the one failure, the same pre-existing image-loading flake in `catalog-image-to-till.spec.ts` documented in the #144 review, reproduces identically) to confirm no UI regression, even though this change touches no UI/i18n files.
- Confirmed the ADR-0011 citation added to the contract doc is accurate (`internal/cloudsync/cloudsync.go`'s own "stock is tracked at item level (ADR-0011)" comment, cross-checked against the ADR's text) before writing it into `ut-docs/reference/plugin-manifest.md`.

## Deferred (real, out of scope for this card — tracked as new Backlog cards, not silently dropped)

- New card: variant-scoped stock silently absent from the export payload (item-level-only, per ADR-0011 — a real completeness gap for shops using variants, worth its own scoping pass).
- ut-docs#228: rescope to gate `sales` and `stock` on separate permissions, not one flat `sales:read` check, now that the payload carries both ledgers.

## Verdict

**Safe to merge.** No blocking findings; the one binding-rule doc gap was fixed and independently re-verified in-session; the nit was fixed; both genuinely separable gaps are now tracked on the board.

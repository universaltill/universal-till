# Code review — variant reachable by SKU-exact and name search (ut-docs#751)

**Date:** 2026-08-15
**Branch:** `fix/variant-sku-name-search-price-history` → `main`
**Repo:** universaltill/universal-till
**Author (Dev):** Claude (Sonnet), autonomous SDLC pipeline
**Reviewer:** Claude (Opus), independent subagent, isolated worktree
**Complexity:** medium
**Commits reviewed:** `2cf1eb78` (Dev), `538e60f5`/`a7343870` (Reviewer — test fixes, cherry-picked onto this branch)

## What shipped

`internal/data/pos_repo.go` only. Two new private queries, `resolveVariantSKU` and
`resolveVariantNameLike`, mirroring the existing `resolveVariant` (barcode) shape.
`resolveSKU`/`resolveNameLike` now fall through to them when no *item* matches.
`ResolveShortcutLine`'s SKU-exact and name-like branches price the result through a
new `resolveRowPrice` helper (VariantID alone when set, dropping ItemID) and compose
the same `"Item - Variant"` display name the barcode branch already composes. Plus
tests in `internal/data/pos_repo_resolve_test.go`.

## Independent verification of the root cause

The reviewer did not take the commit message's word for it — checked out
`origin/main`'s `internal/data/pos_repo.go` and ran independent probes against it.

**The originally filed mechanism (ut-docs#751 as written) is refuted.** On `main`,
`resolveSKU` and `resolveNameLike` select from `items` only — `res.VariantID` is
never assigned, so `resolvePrice(ctx, row.ItemID, row.VariantID, …)` at the two call
sites was *always* invoked with exactly one non-empty ID. `ResolveCurrentPrice`'s
`(itemID != "" && variantID != "")` guard could not fire, so nothing was ever
silently swallowed and no `price_history` override was ever ignored on those paths.

**The real defect, confirmed empirically (by both Dev and Reviewer, independently).**
Probe on `main`: seed item `i1`/SKU `S1` with variant `v1`/SKU `S1-L`, then
`ResolveShortcutLine(ctx, "S1-L")` → `ok=false`, zero-value line. Same for name search
on the variant's own name. A variant was **completely unreachable** by exact-SKU or
name search — findable only by variant barcode or via an item-level match.

The shipped tests were confirmed genuinely RED on `main` by the reviewer:
`TestResolveShortcutLine_VariantSKUExact` and `_VariantNameLike` both fail there.
`TestResolveShortcutLine_ItemSKUStillWinsOverVariantSKU` (Dev's original version)
**passed on `main` unchanged** — see the test-quality finding below.

## Correctness findings on the new code

**SQL — no defects found.**
- Both new queries bind via `?` placeholders. No string interpolation anywhere in the
  diff.
- `WHERE v.is_active = 1 AND i.is_active = 1` on both — matches `resolveVariant`'s
  barcode-path predicates exactly. Verified against the schema: `items` and
  `item_variants` (`001_init.sql` + every later `ALTER TABLE`) have no
  `deleted_at`/`is_deleted`/`archived_at` column — `is_active` is the only liveness
  flag, so there is no soft-delete dimension being missed.
- `JOIN items i ON i.id = v.item_id` is an inner join, so an orphaned variant cannot
  leak. `LEFT JOIN tax_codes` + `COALESCE(t.rate_basis_points, 0)` matches the item
  paths.
- Case sensitivity is consistent with what it replaces: `v.sku = ?` is case-sensitive
  like `i.sku = ?`; `v.name LIKE ?` is ASCII-case-insensitive like `i.name LIKE ?`.

**`resolveRowPrice` and its call sites — correct.** Prices by `VariantID` alone when
set, `ItemID` alone otherwise; never both.

**The untouched `resolveShortcut` call site is correctly left alone — verified
against the schema, not the claim.** `shortcut_buttons` (`001_init.sql`) is
`(barcode, item_id, label, image_path, sort_order)` — no `variant_id` column, and
`resolveShortcut` scans into `ItemID`/`Label` only, so its call always passes
`variantID == ""`. Leaving it is correct.

**Priority order — no regression.** Reviewer probe-verified the one shape that could
go wrong: item A's SKU equal to item B's *variant*'s SKU (possible in real data,
since `items.sku` and `item_variants.sku` are separately-`UNIQUE`). Item A correctly
wins, at item A's price and name. `TestResolveShortcutLine_PriorityOrder` still
passes unchanged.

**One deliberate behaviour change, correctly directed.** When a search term exactly
matches a variant's SKU *and* fuzzily matches some unrelated item's name, `main`
returned the item (name-LIKE was the only path that could hit); the branch now
returns the variant (SKU-exact fires first) — exact-beats-fuzzy is the precedence
`ResolveShortcutLine` already implements between the item SKU and item name paths,
so this is the consistent outcome, not a regression.

**Downstream — nothing else needed changing.** A SKU/name-resolved variant line is
byte-identical in shape to a barcode-resolved one (`ItemID` *and* `VariantID` both
set on `ShortcutLine`/`pos.BasketLine`) — exactly the shape universal-till#357 made
tenderable, already covered end to end by `TestTenderHandler_VariantBarcodeScanIsTenderable`.
`internal/ui/buttons.go`'s `PriceResolverAdapter` is a 1:1 field copy over the same
`ResolveShortcutLine` call and needs no change. No other caller of
`resolveSKU`/`resolveNameLike` exists besides `ResolveShortcutLine`.
`SearchItemsForShortcuts` (the admin shortcut-button designer search) binds
`shortcut_buttons.item_id`, which has no variant dimension, so it is correctly
item-only and out of scope here.

## Test quality — the substantive review finding

The reviewer mutation-tested the shipped suite rather than accepting "tests exist."
Six mutations of `pos_repo.go`; two were caught by Dev's original tests, four passed
`go test ./...` unnoticed:

1. **`TestResolveShortcutLine_ItemSKUStillWinsOverVariantSKU` was not actually a
   regression guard.** Dev's original version seeded one item and no variant — a
   near-duplicate of the pre-existing `TestResolveShortcutLine_ExactSKUFallback`. It
   passed on `main`, passed with the fallback deleted entirely, and passed with
   `resolveSKU`'s priority inverted. **Fixed**: rewritten to build the real
   collision (item `i1`/SKU `MUG-001` vs. a *different* item's variant with the same
   SKU), asserting the item wins.
2. **Nothing covered the `is_active` predicates on either new query.** Dropping
   `v.is_active = 1` still passed everything — a discontinued variant would resolve
   and still price (falling back to the row's own price when `ResolveCurrentPrice`
   errors on the inactive row), quietly going back on sale at the till. **Fixed**:
   added `TestResolveShortcutLine_InactiveVariantNotResolvable`, covering an
   inactive variant under an active item and an active variant under an inactive
   parent, across both SKU and name lookup.
3. The name-LIKE test asserted no display name, so disabling the `"Item - Variant"`
   composition on that path went undetected. **Fixed**: added the `line.Name`
   assertion to `_VariantNameLike`.
4. Nothing asserted `line.SKU`, so dropping `resolveVariantSKU`'s `res.SKU = sku`
   would ship an empty SKU to the basket unnoticed. **Fixed**: added the `line.SKU`
   assertion to `_VariantSKUExact`.
5. Added `TestResolveShortcutLine_ItemNameStillWinsOverVariantName`, the name-path
   twin of the SKU priority guard (previously no coverage at all).

All escaping mutations now fail the suite; the reviewer re-ran each to confirm.

## Verified beyond automated tests

- Both Dev and Reviewer independently read `resolveSKU`/`resolveNameLike`/
  `resolveVariant`/`resolvePrice`/`ResolveCurrentPrice`/`lookupPriceHistory` as they
  exist on `origin/main`, and ran probes against that exact file, to establish the
  pre-fix behaviour first-hand rather than trust the originally filed issue.
- Reviewer read `001_init.sql` for `items`, `item_variants`, `variant_barcodes`,
  `shortcut_buttons`, `price_history`, and every subsequent `ALTER TABLE` on those
  tables, to check the active/soft-delete and `variant_id` claims against the schema.
- Traced the resolved line through `internal/ui/buttons.go` → `pos.BasketLine` → the
  `/api/pos/tender` handler test added by #357.
- Confirmed a future-dated variant `price_history` row is correctly ignored on the
  new paths, and that ambiguous variant-name searches are deterministic
  (`ORDER BY v.name`).

## Gates run

`go build ./...` clean · `go vet ./...` clean · `go test ./...` full module: 100%
pass (both before and after the reviewer's test additions) ·
`go test ./internal/data/... -run TestResolveShortcutLine -v`: 12/12 pass ·
`bash scripts/ci/guard-data-access.sh` ✓ · `bash scripts/ci/guard-kiosk-engine.sh` ✓ ·
`bash scripts/ci/guard-plugin-menu-read.sh` ✓.

**TDD re-verified independently, twice**: Dev reverted the fix and confirmed both new
tests failed with `ok=false` before restoring it. The Reviewer independently
re-derived the same pre-fix behaviour from `origin/main` directly (not from the
revert), then additionally mutation-tested the fix itself (6 mutations) to probe test
quality — 4 of which exposed real gaps, since fixed (see above).

CLAUDE.md compliance: `internal/data` only, so repository-pattern compliant. Price
stays `int64` minor units at the DB boundary, consistent with the surrounding code —
no `money.Money` obligation crossed. No user-facing strings, no locale keys, no
routes or UI, so no i18n, help-topic, README or ADR obligation. Migrations untouched.

## Non-blocking notes (no action taken)

- The miss path now issues 7 queries per `ResolveShortcutLine` instead of 5, and
  `PriceResolverAdapter.Resolve` calls it up to 4× for one scan (a pre-existing
  inefficiency this change amplifies to ~28 local SQLite queries on an unresolvable
  code). Not a correctness or offline-first concern.
- `resolveSKU`/`resolveNameLike` now fall through on any `Scan` error, not just
  `sql.ErrNoRows`. Net behaviour is unchanged (a genuinely broken DB fails the second
  query too and still returns `false`) — style, not a bug.
- `internal/data/install_status_repo.go` fails `gofmt -l`. Pre-existing on `main`,
  untouched by this branch, no CI gate runs gofmt. Out of scope here.

## Safe-to-merge verdict

**Yes.** The fix is correct, minimal, correctly scoped to the data layer, and mirrors
the established barcode-path pattern rather than inventing a new one. The root-cause
correction versus the originally filed issue is honest and independently reproduced
by the reviewer from a cold read of `main`. The only real defect found was in the
tests, not the production code — a regression guard that could not detect the
regression it was named for, and no coverage of the `is_active` predicates that stop
a withdrawn product going back on sale. Both are now fixed and mutation-verified.

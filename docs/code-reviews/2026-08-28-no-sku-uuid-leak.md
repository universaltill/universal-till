# 2026-08-28 — Item creation no longer copies its UUID into `sku` (ut-docs#1176)

## Summary

Independent review of `fix/1176-no-sku-uuid-leak` (WIP commit `00b843d`,
parent `e400422` = current `main`). An item created or imported without a
source SKU used to have its own internal UUID copied into the `sku` column
(`CreateItem`/`CreateItemTx`'s `if in.SKU == "" { in.SKU = in.ID }`), which
then leaked verbatim into every staff-facing surface showing SKU (inventory
grid, item search, receipts) and polluted SKU-based search. `items.sku` is a
nullable `UNIQUE` column (`001_init.sql`); the fix stores SQL `NULL` instead
of a UUID.

## Change (as committed)

- `internal/data/catalog_repo.go`: `CreateItem`/`CreateItemTx` no longer set
  `in.SKU = in.ID` on an empty SKU; the INSERT passes `nullableString(in.SKU)`
  instead of the raw `in.SKU`, so a missing SKU stores `NULL`.
- `internal/data/catalog_repo.go`: `ListItems`'s query changed from bare
  `sku` to `COALESCE(sku, '')` — it scans directly into `itm.SKU`, a plain
  (non-nullable) Go `string`, which would otherwise error on every row with
  no real SKU.
- `internal/data/pos_repo.go`: `SearchActiveItems`'s query changed from bare
  `i.sku` to `COALESCE(i.sku, '')` for the same reason (this is the
  Designer/Catalog search-box backing query).
- New `internal/data/catalog_repo_no_sku_test.go` (4 tests) and one new test
  in `internal/pos/catalog_search_test.go`.

## Verification performed independently

- `go build ./...`, `go vet ./...`, `gofmt -l .` — all clean, no output.
- `go test ./...` — full repo suite, every package, green (`internal/data`
  73.6s, `internal/pos` 4.0s, `internal/pages` 85.3s, `internal/plugins`
  86.0s, everything else in between — no failures, no skips).
- `bash scripts/ci/guard-data-access.sh` — pass (no inline SQL introduced
  outside `internal/data`/`internal/db`).
- `bash scripts/ci/guard-i18n.sh` — pass (diff touches no template/JS
  strings, as expected for a pure DB-layer change).

### TDD re-verification (not taken on trust)

Since the fix branch's tip was already checked out in the primary worktree,
this review worked from a separate isolated worktree: the commit's exact
parent matched this worktree's own `HEAD`, so the fix commit was applied
with `git cherry-pick --no-commit` (clean, no conflicts) to reproduce the
diff without disturbing either worktree's branch checkout.

With the new tests in place, the pre-fix `catalog_repo.go`/`pos_repo.go`
were restored (`git show <parent>:<path> > <path>`) and the four targeted
tests re-run:

```
--- FAIL: TestCreateItem_NoSKUStoresNullNotUUID
    catalog_repo_no_sku_test.go:37: expected sku to be NULL for an item with
    no source SKU, got "05b1f528-b6d0-47ff-bf66-076e5e17b6b2"
    (id="05b1f528-b6d0-47ff-bf66-076e5e17b6b2") — the UUID must not land in sku
--- FAIL: TestCreateItemTx_NoSKUStoresNullNotUUID
    catalog_repo_no_sku_test.go:68: expected sku to be NULL for an item with
    no source SKU, got "fafc5c8e-cdeb-44e4-86ed-cf9ec3161da1" ...
--- PASS: TestCreateItem_TwoItemsWithNoSKUDoNotCollide   (correctly still
    passes pre-fix — it isn't testing the leak, it's testing the UNIQUE
    constraint doesn't collide, which the UUID-fallback also satisfied)
--- FAIL: TestListItems_NoSKUReturnsEmptyStringNotUUID
    catalog_repo_no_sku_test.go:120: expected empty SKU for an item with no
    real SKU, got "e3d5c912-c89a-4ed2-a3e1-49cc0b5edfd1"
```

```
--- FAIL: TestSearchActiveItems_NullSKUDoesNotError
    catalog_search_test.go:70: SearchActiveItems must tolerate a NULL sku
    column, got: pos.search_active_items: sql: Scan error on column index 1,
    name "sku": converting NULL to string is unsupported
```

All four failures are real assertion/runtime errors against genuine old
behaviour — not compile errors masking the check. The fix was then restored
(`git checkout <fix-commit> -- ...`), confirmed the working tree matched the
fix commit exactly (empty `git diff`), and all five tests (4 + 1) pass.

## Independent findings beyond the automated tests

### Landmine sweep (`\bsku\b` across `internal/data`, the only place raw SQL
may live, plus everywhere `.SKU` is read elsewhere in `internal/`)

The two fixed call sites were not the only bare-`sku` scans in the codebase,
but they were the only two that were actually broken:

- `internal/data/related_items_repo.go` (`SuggestForBasket`) selects bare
  `i.sku` but filters `i.sku IS NOT NULL AND i.sku <> ''` in the `WHERE`
  clause first — never scans a NULL. Fine as-is.
- `internal/data/kitchen_stations_repo.go` (`ListItemStationOverrides`),
  `internal/data/pos_repo.go` (`ListStockLevels`, `SuggestionsForBasket`-
  adjacent queries, `resolveSKU`), `internal/data/export_repo.go`
  (`variantStockForExport`) — all already either `COALESCE(...,'')` or scan
  into `sql.NullString` first. These predate this branch, so `items.sku`
  and `item_variants.sku` were evidently already nullable in practice
  before this fix (e.g. via `UpdateItem`/`UpdateVariant`'s existing
  `COALESCE(NULLIF(?, ''), sku)` pattern) — this PR closes the one
  *creation*-path gap (`CreateItem`/`CreateItemTx`) that still minted a
  UUID instead of NULL, plus the two read paths (`ListItems`,
  `SearchActiveItems`) that hadn't yet been hardened for it.
- `internal/data/pos_repo.go`'s `resolveSKU`/`resolveVariantSKU` scan bare
  `i.sku`/select alongside `WHERE i.sku = ?` — safe, because SQL `NULL = ?`
  is never true, so a NULL-sku row can never be the one that satisfies the
  `Scan`.
- `internal/pages/import_page.go`'s in-file duplicate-SKU veto builds a
  `map[string]bool` keyed by SKU but explicitly skips when
  `res.Items[i].SKU == ""` before ever using it as a key — two no-SKU rows
  don't collide in that map. Fine as-is.

**Finding — Medium, pre-existing on `main`, not introduced by this branch,
not fixed by it either:**
`internal/data/pos_repo.go`'s `POSRepo.LookupActiveVariant` (line ~293) is
the same landmine class the branch fixed elsewhere, still live:

```go
SELECT id, item_id, sku, name, price, cost_price, is_active FROM item_variants WHERE id = ?
...
Scan(&v.ID, &v.ItemID, &v.SKU, &v.Name, &v.Price, &cost, &v.IsActive)
```

`item_variants.sku` is nullable `UNIQUE` (`sync_admin_repo.go` already lists
it as such), and `CatalogRepo.CreateVariant` already stores `NULL` via
`nullableString(in.SKU)` for a variant created with no SKU — independent of
this branch, that's the existing, correct behaviour on `main`. But
`LookupActiveVariant` scans that column straight into `catalogtypes
.VariantInput.SKU`, a plain (non-nullable) `string`. Verified directly (test
written, run, and removed again — not part of this diff): inserting a
variant with `sku = NULL` and calling `LookupActiveVariant` on it fails with

```
data.pos.lookup_active_variant: sql: Scan error on column index 2, name
"sku": converting NULL to string is unsupported
```

i.e. the exact same failure mode `SearchActiveItems` had before this fix.
Currently `LookupActiveVariant` has no live caller under `internal/pages` or
elsewhere in the running app (only tests and the `CatalogSearcher` interface
reference it) — grepped the whole tree to confirm — so this is not reachable
through any UI path today and does not block this PR, which is scoped to
ut-docs#1176 (the *item*-level UUID leak). Recommend a fast-follow fix
(`COALESCE(sku, '')` or `sql.NullString` here, matching the pattern this PR
already uses everywhere else) before `LookupActiveVariant` grows a caller.

### Helper consistency

`nullable(*string)` is for pointer fields (`CategoryID`/`BrandID`/
`TaxCodeID`); `nullableString(string)` is for plain-string fields. The diff
uses `nullableString(in.SKU)` at both `CreateItem` and `CreateItemTx` — `in.
SKU` is `string`, not `*string` (`internal/catalogtypes/types.go`), so this
is the correct helper, consistent with `UpdateItem`/`UpdateVariant`'s
pre-existing `nullableString(in.SKU)` calls and `CreateVariant`'s (also
pre-existing, unrelated to this diff).

### Other checks

- No `os.MkdirAll`/cwd-relative-path concerns — the diff has zero file I/O,
  pure DB-layer.
- No real client/shop name or secret anywhere in the diff or the new tests
  (item/shop names used: "Mystery Item", "Imported No-SKU Item", "No SKU
  Item", "First/Second No-SKU" — all synthetic).
- Diff touches no `web/`/template/UI file — i18n and help-manual-topic
  requirements don't apply (`guard-i18n.sh` run anyway, confirmed passing
  as a sanity check, not because this diff could plausibly trip it).

## Verdict

**Safe to merge as-is.** The stated fix is correct, TDD-verified for real
(genuine pre-fix failures with the expected error text, genuine post-fix
passes), and doesn't regress any sibling package (full `go test ./...`
green). The one adjacent landmine found (`LookupActiveVariant`) is
pre-existing on `main`, out of this ticket's scope, and currently
unreachable — logged above as a deferred fast-follow rather than a blocker.

## Deferred / follow-up

- `internal/data/pos_repo.go`'s `LookupActiveVariant` should get the same
  `COALESCE(sku, '')` (or `sql.NullString`) treatment before it gains a real
  caller — track as a small fast-follow, not blocking this PR.

# 2026-09-02 — `POSRepo.LookupActiveVariant` NULL-SKU scan crash

Card: ut-docs#1205 (found during ut-docs#1176's independent review, 2026-08-28).

## What shipped

`internal/data/pos_repo.go`'s `POSRepo.LookupActiveVariant` scanned the
nullable, `UNIQUE` `item_variants.sku` column directly into a non-nullable
Go `string`. `CatalogRepo.CreateVariant` already stores `NULL` for a
variant created with no SKU (pre-existing behavior), so calling
`LookupActiveVariant` on such a variant crashed:

```
sql: Scan error on column index 2, name "sku": converting NULL to string is unsupported
```

Same landmine class as ut-docs#1176, which fixed the equivalent bug in
`CatalogRepo`'s item-lookup queries via `COALESCE(sku, '')` — that same,
already-established pattern (used at five other call sites in this file)
is applied here.

Fix: `SELECT id, item_id, sku, name, price, cost_price, is_active` →
`SELECT id, item_id, COALESCE(sku, ''), name, price, cost_price, is_active`.

New regression test `TestPOSRepo_LookupActiveVariant_TolerantOfNullSKU`
(`internal/data/pos_repo_search_test.go`) creates a real no-SKU variant via
`CatalogRepo.CreateVariant` (not a hand-crafted `INSERT`) — confirming the
row is genuinely `NULL` via a raw `sql.NullString` scan — then confirms
`LookupActiveVariant` returns it with `SKU == ""` instead of crashing.

Not previously reachable through any UI path (`LookupActiveVariant` has no
live caller under `internal/pages` today — only tests and the
`CatalogSearcher` interface reference it), filed so it wasn't forgotten
before it grows a real caller.

## Independent review

Fresh-context Sonnet subagent (`complexity:easy` routing). **Verdict: SAFE
TO MERGE, no blocking issues.**

- Confirmed `COALESCE(sku, '')` wraps only the `sku` column; the `SELECT`
  list's 7 columns still match the `Scan(...)` call's 7 targets 1:1, no
  off-by-one.
- Searched exhaustively for every other query touching `item_variants.sku`
  / `items.sku` under `internal/data/`: all already either wrap with
  `COALESCE` or are guarded by a `WHERE sku = ?` / `WHERE sku IS NOT NULL`
  clause that makes a `NULL` row unreachable. `LookupActiveVariant` was the
  only remaining unguarded direct scan of a nullable `sku` column.
- Test genuinely exercises the real write path (`CatalogRepo.CreateVariant`
  with a blank SKU) and the real read path — no false-pass risk.
- No regression risk to `TestPOSRepo_LookupActiveVariant_ValidatesInput` or
  `internal/pos/catalog_search.go`'s passthrough caller — both still pass
  untouched.
- Independently re-verified the TDD claim: reverting only `pos_repo.go`
  reproduces the exact reported scan error; restoring returns to green.

## Verification

| Check | Result |
|---|---|
| `gofmt -l internal/data/pos_repo.go internal/data/pos_repo_search_test.go` | empty |
| `go build ./...` / `go vet ./...` | clean / clean |
| `go test ./internal/data/... ./internal/pos/...` | pass |
| `go test -race ./internal/data/... -run TestPOSRepo_LookupActiveVariant` | pass |
| `guard-data-access.sh` (raw SQL stays confined to `internal/data`) | pass |
| `guard-i18n.sh` / `guard-compliance-claims.sh` / `guard-help-topics.sh` / `guard-docs-shots.sh` / `guard-kiosk-engine.sh` / `guard-plugin-menu-read.sh` | all pass (no user-facing surface touched) |
| TDD red→green, independently re-verified by the reviewer | see above |

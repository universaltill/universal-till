# Review: reject "both ItemID and VariantID set" in CurrentQty/CurrentQtyBatch (ut-docs#1353)

## What shipped

`internal/data/pos_repo.go`'s `CurrentQty` and `CurrentQtyBatch` (the
batched stock-check lookup added by ut-docs#1318) did not validate that a
key carries exactly one of `ItemID`/`VariantID`, unlike their three
sibling functions — `AggregateInventory`, `RecordStockMovement`,
`RecordStockMovementsBatch` — which already reject "both set" with
`"cannot specify both itemID and variantID"`.

This mattered beyond consistency: `CurrentQtyBatch` re-keys its output
map by the DB row's own `item_id`/`variant_id` columns, not by the
caller's original `StockKey`. A caller passing an invalid dual-field key
would silently match an existing item-level row via
`stockKeyPredicate`'s OR-shaped WHERE clause, but the returned map entry
is keyed by the row's single-field identity — so a lookup by the
caller's own (dual-field) key finds nothing, reads as "no stock", and
previously surfaced downstream in `internal/pos/sales.go`'s stock-check
loop as a misleading `"insufficient stock for item ..."` error instead of
naming the actual problem.

Fix: both functions now reject "both set" up front, matching the
sibling functions' existing error message and (for the batch method) the
`%d:`-indexed style `RecordStockMovementsBatch` already uses.

Two new tests in `internal/data/pos_repo_batch_test.go`:
`TestPOSRepo_CurrentQtyBatch_RejectsBothItemAndVariantSet` and
`TestPOSRepo_CurrentQty_RejectsBothItemAndVariantSet`.

## Why this was safe (no live bug, no regression risk)

`SaleLineInput`/`StockKey` are legitimately allowed to carry both
`ItemID` and `VariantID` **upstream** of `CompleteSale` — a barcode-
scanned variant line arrives that way from `ui.PriceResolverAdapter`
(ut-docs#744). `CompleteSale` (`internal/pos/sales.go:683-687`)
normalizes this unconditionally, early in the function, clearing
`ItemID` whenever `VariantID` is set — strictly before the stock-check
loop (`internal/pos/sales.go:707-740`) that calls `CurrentQtyBatch`
(line 719). The only other call site, `internal/pages/sync_sales.go`'s
`warnIfStockNegative` (`CurrentQty` at line 373), runs strictly *after*
`pos.CompleteSale` has already returned, reading the same
already-mutated `in.Lines` slice (the in-place mutation is documented at
sales.go:673-682 as deliberately visible to exactly this caller).

So no live code path reaches either new guard with legitimate
dual-field data — confirmed by grepping every call site of
`CurrentQty`/`CurrentQtyBatch` in the repo (two non-test call sites
total, both traced above). The fix is defense-in-depth / error-message
clarity, not a behavior change for any real sale.

## Independent review

Fresh-context Sonnet subagent (easy-complexity routing), run in an
isolated worktree. It independently traced the full `CompleteSale`
normalization ordering and every call site rather than taking the above
claim on faith, ran the full build/vet/fmt/test gate, and did the
required TDD re-verification: reverted only the production-code change,
confirmed both new tests fail with the exact mis-keying symptom
described above (`CurrentQtyBatch` silently resolving `{itmA, varC}` to
`{itmA, ""}`'s row), then restored the fix and confirmed both pass.

**Verdict: SAFE TO MERGE. No findings** (no blockers, should-fix, or
nits).

## Verified

- `gofmt -l internal/data/pos_repo.go internal/data/pos_repo_batch_test.go` — clean.
- `go build ./...`, `go vet ./...` — clean.
- `go test ./internal/data/... ./internal/pos/... ./internal/pages/...` — all pass.
- `go test $(go list ./... | grep -v '/internal/plugins$')` — full suite green (run before review).
- `go test -timeout 20m ./internal/plugins` — green (run before review).
- `golangci-lint run ./...` — 0 issues.
- `scripts/ci/guard-data-access.sh` — passes (no SQL added outside `internal/data`).
- TDD re-verification (both by the implementer and independently by the reviewer): both new tests fail against the pre-fix code with the exact described symptom, and pass against the fix.
- No file I/O in this diff (the two recurring bug classes — missing `os.MkdirAll`, cwd-relative paths instead of `paths.Data(...)` — don't apply).
- No real client/shop name, no secret-shaped literal.
- No UI/help/i18n/locale surface touched — diff is confined to `internal/data/pos_repo.go` and its test file.

## Deferred / out of scope

None. This card was explicitly scoped by its own issue text as a
clear-error-message fix, not a behavior change — nothing found expands
that scope.

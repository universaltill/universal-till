# Code review: PriceResolverAdapter resolves the chain once, not per shape (ut-docs#1660)

**Date:** 2026-09-08
**Card:** ut-docs#1660 (`complexity:easy`)
**Diff:** `internal/ui/buttons.go`, `internal/ui/resolver_querycount_test.go`

## What shipped

`PriceResolverAdapter.Resolve` (`internal/ui/buttons.go`) used to probe its
underlying resolve chain (`a.resolve`, backed by
`POSRepo.ResolveShortcutLineDecoded`) once per candidate "shape" it was
checking for — variant, then item, then shortcut, then a SKU/name fallback
— discarding every result whose shape didn't match. `a.resolve` is a pure,
deterministic lookup: the same `code` always returns the same row, so this
cost up to 4 identical round trips per scan for a lookup that only ever
needed one. Found during independent review of ut-docs#1360
(`docs/code-reviews/2026-09-06-scan-line-batch-barcode-tiers-1360.md`),
which fixed the equivalent problem one layer down, in
`internal/data`.

The fix collapses `Resolve` to a single call to `a.resolve`, returning
immediately on a hit. The only remaining second call is a conditional
retry with `strings.TrimSpace`-trimmed input, kept because `Resolve` is a
public method on the `pos.PriceResolver` interface and could in principle
be called directly with untrimmed input — every current production caller
(`internal/pos.Service`) already trims before calling it, so this retry
never fires today, but dropping it outright would be a real (if currently
unreachable) behavior change for a hypothetical future caller.

The four old per-shape helper methods (`resolveVariant`, `resolveItem`,
`resolveShortcut`, `resolveTextSearch`) are removed — tracing
`ResolveShortcutLineDecoded` and its helpers shows every genuine hit sets
either `VariantID != ""` or `ItemID != "" && VariantID == ""`; there is no
reachable case with both empty, so `resolveShortcut`'s "has an ItemID"
check was dead on every real hit and `resolveTextSearch`'s "return true
unconditionally" was reached only via a raw-code miss.

## What was verified beyond automated tests

- **TDD, verified twice independently** — once by me (revert `buttons.go`
  to the pre-fix version with `git stash`, confirm the new test fails,
  restore, confirm it passes), and again by the independent reviewer in
  its own isolated worktree, checking out the fixed files via
  `git checkout <branch> -- <paths>`, reverting only `buttons.go` to the
  commit's own parent, and re-running. Both runs saw the same failure
  shape: every subtest but "variant barcode hit" (the first branch
  checked in the old code, so it already cost exactly 1 call) failed with
  a SELECT count 2x-4x the new code's count — e.g. `"12 SELECTs, want
  exactly 6"` for the SKU-fallback shape, `"28 SELECTs, want exactly 7"`
  for a miss (the old code's most expensive path: barcode tier fails,
  shortcut tier fails, then a SKU attempt and a name-LIKE attempt each
  fail their own multi-query chain).
- **Regression test** (`internal/ui/resolver_querycount_test.go`) opens a
  real on-disk SQLite file through a counting `driver.Connector` (same
  technique as `internal/data`'s `TestSalesForExport_ConstantQueryCount`/
  `TestResolveScanLine_QueryCount`, reimplemented locally since that
  helper is package-private to `internal/data`) and asserts, for every
  shape `Resolve` can return (variant hit, item hit, shortcut hit,
  SKU-exact hit, name-LIKE hit, miss), that one `Resolve` call costs
  exactly the same number of SELECTs as one direct
  `posRepo.ResolveShortcutLineDecoded` call — proving the multiplier is
  gone without pinning `ResolveShortcutLineDecoded`'s own per-shape cost
  (which varies for reasons outside this card's scope).
- **Full gate, run twice** (by me before commit, and independently by the
  reviewer in its own worktree): `gofmt -l`, `go vet`, `go build ./...`,
  `go test ./...` (full suite, all green), `go test ./internal/ui/...
  -race`, `golangci-lint run ./internal/ui/...` (0 issues),
  `scripts/ci/guard-data-access.sh` (passes — the new test's literal
  `CREATE TABLE`/`INSERT` SQL is in a `_test.go` file, which the guard
  excludes by design, same as the sibling fixtures in `buttons_test.go`/
  `buttons_store_test.go` it reuses `mustExec` from).
- **Independent review** (fresh-context Sonnet subagent, per this card's
  `complexity:easy` routing, isolated worktree): traced
  `ResolveShortcutLineDecoded` and every helper it calls independently
  (not trusting the fix's own doc comment) and confirmed the same
  shape-exhaustiveness argument; specifically checked the trimmed-retry
  fallback against a whitespace-padded-code edge case and found no gap.
  Verdict: **SAFE TO MERGE**, no blocking issues.

## Non-goals confirmed untouched

- `cacheScan`'s caching strategy — unchanged.
- `internal/data` — unchanged; this fix is entirely in `internal/ui`.
- Resolution priority/behavior — unchanged; call-count reduction only.

## Deferred / out of scope

Nothing deferred. The card's acceptance criteria (single call per
`Resolve`, regression test, no behavior change) are fully met by this
diff.

## Verdict

**Safe to merge.**

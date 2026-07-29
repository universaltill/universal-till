# Test coverage batch 3: checkout resolution chain — found and fixed 2 real bugs

2026-07-29

Third batch of the pos_repo.go backfill: `ResolveShortcutLine` and
`ResolveCurrentPrice` — the barcode-scan/search resolution and price-history
override logic every single till ring-up goes through. This is exactly the
kind of code where TDD (Farshid's standing instruction going forward) pays
for itself: writing real tests against real intended behavior — not just
"what does the code currently do" — surfaced two genuine production bugs
before they were ever reported live.

## Bug 1: `ends_at` compared as a raw string, not via `datetime()`

`internal/data/pos_repo.go`, `lookupPriceHistory` (the query behind
`ResolveCurrentPrice`'s price_history override):

```sql
AND datetime(starts_at) <= CURRENT_TIMESTAMP
AND (ends_at IS NULL OR ends_at > CURRENT_TIMESTAMP)   -- bug: raw string compare
```

`starts_at` was normalized with `datetime(...)`, but `ends_at` was compared
as a raw string against SQLite's `CURRENT_TIMESTAMP` (format
`YYYY-MM-DD HH:MM:SS`). Production writes `ends_at` as RFC3339
(`YYYY-MM-DDTHH:MM:SSZ`). On the **same calendar day**, `'T' > ' '`
lexically, so an already-expired promo/markdown that ended earlier the same
day kept string-comparing as "not yet ended" — **a price override that
should have stopped applying hours ago would keep silently applying at
checkout**, undercharging (or overcharging) every sale of that item until
midnight. A sibling query elsewhere in the same file (line ~2242) does this
correctly (`datetime(ends_at) >= CURRENT_TIMESTAMP`), so this was an
inconsistency, not a deliberate choice.

**Fix**: wrap `ends_at` in `datetime()`, matching `starts_at` and the
sibling query.

**Caught by**: a new same-day-expiry test case (`ends_at` 5 minutes in the
past, `starts_at` 2 hours in the past, both same calendar day) — confirmed
it fails against the pre-fix code (`price=77` instead of the expected
fallback `300`) and passes with the fix.

## Bug 2: variant display name silently dropped

`ResolveShortcutLine`'s variant-barcode branch computed a combined
`"Item - Variant"` display name into a local `name` variable, then never
used it — `toShortcutLine` reads `row.ItemName`, not the local `name`. Every
variant scanned at checkout (e.g. "Latte - Large") displayed as just the
plain item name ("Latte"), losing the variant distinction on the sale
screen.

**Fix**: assign the composed name back onto `row.ItemName` before calling
`toShortcutLine`, instead of discarding it in an unused local.

**Caught by**: the priority-order test now asserts
`line.Name == "Latte - Large"` for the variant-match case; fails against the
pre-fix code (`got "Latte"`).

## Also fixed: the priority-order test didn't actually prove ordering

The original draft of `TestResolveShortcutLine_VariantBarcodeTakesPriorityOverItemBarcode`
only exercised the variant-barcode path in isolation — no competing match,
so it proved the path *works*, not that it *wins* when several resolvers
could all match the same code. Independent review (opus) caught this.
Rewrote as `TestResolveShortcutLine_PriorityOrder`: deliberately creates a
single barcode string (`"COLLIDE"`) that simultaneously matches a variant
barcode, an item barcode, a shortcut button barcode, and an item's exact
SKU (bypassing `AddBarcode`'s `ensureBarcodeAvailable`, which would
normally prevent this exact state — inserted directly to construct the
test scenario), then removes each match in turn and asserts the next
resolver down the priority chain takes over. This is the test that actually
exercises `ResolveShortcutLine`'s real branching logic end to end.

## Also added

- `ResolveCurrentPrice` validation/not-found/inactive-item tests.
- `ResolveCurrentPrice` variant-price test.
- Individual-path tests for item barcode, shortcut barcode, exact SKU,
  name-like fallback, not-found, and inactive-item exclusion.
- `internal/testsupport/sqlite_catalog.go`: added `shortcut_buttons` to the
  shared minimal catalog schema (matches `001_init.sql` +
  `004_shortcut_order.sql` exactly), needed for the shortcut-barcode path.

## Independent review (opus)

Verified every assertion against the real production code, specifically
scrutinized the priority-order proof and the price_history date-window
logic including timezone handling. Found the two issues above (both fixed
before commit); confirmed the schema addition matches the real migrations
exactly; confirmed no timezone flakiness in the date-window tests
themselves (both sides are UTC).

## Verification

`go build ./...`, `go test ./...`, `go test ./internal/data/... -count=3
-shuffle=on`, `scripts/ci/guard-data-access.sh`,
`scripts/ci/guard-i18n.sh` — all pass. Both new regression tests confirmed
to fail against the pre-fix code and pass against the fix (`git stash` the
production fix, rerun, confirm failure; restore, rerun, confirm pass).

## Coverage delta

`ResolveShortcutLine`, `ResolveCurrentPrice`, `lookupPriceHistory`,
`resolveVariant`, `resolveItem`, `resolveShortcut`, `resolveSKU`,
`resolveNameLike`, `resolvePrice`, `toShortcutLine`: 0% → covered.

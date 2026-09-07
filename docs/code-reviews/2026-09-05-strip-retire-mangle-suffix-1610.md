# Code review — strip admin-sync's retire mangle before display (ut-docs#1610)

- **Date:** 2026-09-05
- **Branch:** `fix/1610-strip-retire-mangle-suffix`
- **Reviewer:** independent review (Opus), deliberately a different model from
  the one that wrote the implementation
- **Complexity:** hard
- **Verdict: SAFE TO MERGE** after the four additional read sites found in
  review were fixed on the branch (see Finding R1).

## What shipped

`internal/data/sync_admin_repo.go`'s `deleteMissing` (the admin-sync
`ApplyAdmin` pull) retires an FK-blocked row in place instead of hard-deleting
it: `is_active = 0` where the table has that column, plus a mangle of every
`unique` column to `"<value>~<id>"` so the primary's replacement row can still
upsert without tripping the UNIQUE constraint. Six admin tables use the SAME
column for identity-uniqueness and for display — `brands.name`,
`tax_codes.name`, `payment_methods.name`, `users.username`,
`stock_locations.name`, `registers.name` — so the mangle is a display-layer
data-corruption bug. Two sub-bugs:

1. `brands` was the only one of the six with no `is_active` column at all, so
   an FK-blocked brand prune ran *only* the mangle: the row was never flagged
   retired anywhere, and its name was permanently corrupted.
2. Even where `is_active` was set, nothing stripped the `~<id>` suffix before
   the value reached a display path.

The fix:

- **New migration `internal/db/migrations/004_brands_is_active.sql`** —
  `ALTER TABLE brands ADD COLUMN is_active INTEGER NOT NULL DEFAULT 1;`
  Additive, *not* an edit to `001_init.sql`.
- **`sync_admin_repo.go`** — `brands`' `adminTables` entry gains
  `hasIsActive: true`; new unexported `stripRetireMangle(id, name string)
  string`, an exact-suffix-only strip.
- **Read sites** — the strip applied in `catalog_repo.go` (`ReadLookup`,
  `GetLookup`, `GetTaxCode`, `ListAllTaxCodes`), `pos_repo.go`
  (`ListStockLocations`, `ListStockLocationsForAdmin`,
  `ListRegistersForAdmin`), and `auth_repo.go` (`scanUser`, the single scan
  choke point behind `ListUsers`/`GetUser`/`ListActiveUsersWithPIN`).
- **`catalog_repo.go`** — `lookupUnfilteredByActive = map[string]bool{"brands":
  true}`; `ReadLookup` skips its `is_active` filter for `brands` only.
- Tests: `strip_retire_mangle_test.go` (scoping table), a new
  `sync_admin_retire_display_test.go` (brand/tax-code/user, driven through the
  real `ApplyAdmin` retire path), and display assertions added to the existing
  register and stock-location retire tests.

## Findings

### R1 — Four display readers reach the mangled name through a JOIN and were missed (major; FIXED on this branch)

The implementation's own `stripRetireMangle` doc comment asserted that "every
repository reader that surfaces one of those to staff … runs its scanned value
through this". That was **not true as submitted**. A repo-wide sweep for every
read of the six tables' display columns — not just the eight sites listed in
the handoff — found four readers that reach the column through a `JOIN` rather
than a direct `SELECT ... FROM <table>`, none of them `is_active`-filtered:

| Reader | Surfaces as |
| --- | --- |
| `POSRepo.ListStockLevels` (`pos_repo.go`) | the `/inventory` page (`inventory_api.go` renders `LocationName` straight into HTML), and `StockForExport`'s item rows |
| `POSRepo.GetLowStockItems` (`pos_repo.go`) | the reorder / low-stock list |
| `POSRepo.variantStockForExport` (`export_repo.go`) | `StockForExport`'s variant rows → an export plugin's `location_name` |
| `POSRepo.ListRegisterLocations` (`fiscal_repo.go`) | `FiscalRegisterDEStore.List` → the §146a Abs. 4 AO fiscal-register page, for **both** `registers.name` and `stock_locations.name` |

Two things make this more than a completeness nit:

- For `stock_locations` these are the **most likely** places the corruption
  actually shows up. A location whose prune is FK-blocked is FK-blocked
  *precisely because* `inventory` rows reference it — which is exactly the row
  set these queries return. The fix patched the location pickers and the
  `/locations` admin page but missed the inventory surface that the blocking
  FK itself implies.
- `ListRegisterLocations` is the textbook case the fix's own rationale
  describes: its doc comment says it deliberately drops `ListRegisters`'
  `is_active = 1` filter so a decommissioned till still shows its register
  name. That is the "admin listing that deliberately shows retired rows"
  category the strip exists for — and it feeds a compliance-facing page.

**Fixed on this branch**: `stripRetireMangle` applied at all four (both the
register and the location name in `ListRegisterLocations`), with a new
regression test
`TestAdminApply_RetiredLocationAndRegisterNamesUnmangledInStockAndFiscalReaders`
that drives the real `ApplyAdmin` retire path and asserts the resolved value
from `ListStockLevels`, `GetLowStockItems`, `StockForExport` (item **and**
variant rows) and `ListRegisterLocations`. `stripRetireMangle`'s doc comment
now records the join-reader trap explicitly, so the next person adding a
reader asks "does this value reach a person?" rather than "does this query
name the table in its FROM clause?".

### R2 — The `ReadLookup` brands deviation is correct (dismissed — verified against precedent)

`ReadLookup("brands")` skipping the `is_active` filter looked like the highest-
risk judgement call in the diff, so it was checked against the codebase's own
precedent rather than the diff's claim:

- `internal/pages/catalog/handlers.go` `listLookups` feeds **one**
  `ReadLookup("brands")` result to *both* the item-edit brand `<select>` **and**
  `lookupNameFunc(brands)`, registered as the `brandName` template func used by
  the catalog list and the `catalog_row.html` OOB fragment.
- `taxCodeNameFunc`'s doc comment records ut-docs#1178 review finding F1: the
  tax-code resolver is built from the **full** set (active *and* inactive) so a
  retired code still resolves to its real name instead of rendering "—".
  `catalog.html`'s own comment records the matching `<select>` reason: a select
  that cannot offer the item's current value silently clears it on the next
  save.

Brands are structurally identical, so filtering here would have traded a
display-corruption bug for a data-loss bug. Keeping the row and stripping the
suffix is the right call, and it also removes a pre-existing test/production
divergence: `main`'s hand-written test schemas
(`internal/testsupport/sqlite_catalog.go`, `internal/pages/ui_smoke_test.go`)
*already* declared `brands.is_active`, while `001_init.sql` did not — so
`ReadLookup` took the filtered branch under test and the "no such column"
fallback in production. Both are now unfiltered for brands.

Checked and clear alongside it: `ValidateLookup` does **not** test `is_active`,
so a retired brand can still be re-submitted (adding the column introduces no
new save-path rejection); the only other `ReadLookup` callers are
`"categories"` (no `is_active` column) and `cloudsync_wire.go`'s
`"stock_locations"` (still filtered, unchanged); no test seeds an inactive
brand, so nothing encoded a "hide deactivated brands" expectation.

### R3 — "Nothing else writes `brands.is_active`" holds (dismissed — verified)

There is no brand-management UI and no `CreateLookup`/`UpdateLookup` repository
method. Outside test fixtures and the demo seed, the only writer of any
`brands` row in the repo is admin-sync's `upsertRow` / `deleteMissing`. The
justification for leaving `ReadLookup("brands")` unfiltered therefore stands.

### R4 — `payment_methods` deliberately left untouched is correct (dismissed — verified)

Every read that surfaces `payment_methods.name` to a user filters
`is_active = 1`: `ListActivePaymentMethods` (tender UI, settings) and
`ListActiveNonCashPaymentMethods` (kiosk). There is no single-row
`GetPaymentMethod` and no admin listing showing inactive tenders. Other reads
of the table either don't select `name` (`SumCashPaymentsForShift`'s join) or
compare it without displaying it (`FindPaymentKeyConflicts`,
`FindPaymentNameConflicts`, `FindSuppressedPaymentNameEntries` — and the value
those return is the plugin-supplied candidate label, not the DB value).

One residual, **accepted, not fixed**: `FindOrphanedPaymentMethods` selects
`pm.name` unfiltered, and it reaches a *startup log line*
(`orphanPaymentMethodWarning`) — not a UI surface. A mangled name could appear
there for a plugin-owned tender that was both admin-sync-retired and had its
plugin uninstalled. The line prints the row id alongside the name, so it stays
diagnosable. Not worth widening the change for.

### R5 — `stripRetireMangle`'s scoping (dismissed — no gap found)

`strings.TrimSuffix(name, "~"+id)` guarded by `id == ""` matches
`deleteMissing`'s mangle exactly (`c || '~' || pk`), and `t.pk[0]` is `id` for
all six tables. The existing table test covers the cases that matter: literal
`~` mid-name, trailing `~` with no id, another row's id, a longer id sharing a
prefix (`b10` vs `b1`), trailing whitespace, case sensitivity, empty id, the
degenerate `"~b1"`, and a doubled suffix stripping exactly once. Multi-byte
names need no special handling — `TrimSuffix` is a byte-suffix comparison and
both operands come from the same UTF-8 column, so there is no partial-rune
case. An id containing `~` is handled correctly (the whole `"~"+id` is the
suffix).

The accepted residual is inherent to the design, not a defect in this patch: a
row whose *real* name genuinely ends in `"~" + its own id` renders with that
suffix stripped. That requires an operator to name a brand after the row's own
primary key; the alternative (a separate `retired_name` column) is far more
invasive for a case that does not occur.

Noted in passing as **pre-existing and out of scope** — `deleteMissing`'s
idempotence check is `LIKE '%~' || pk`, and SQLite's `LIKE` is
ASCII-case-insensitive and treats `_`/`%` in the id as wildcards. Both are in
`main` today, both would need a name/id pathologically shaped to trigger, and
neither is made worse by this change.

### R6 — Migration correctness (dismissed — verified, including by experiment)

- **`DEFAULT 1` backfills existing rows.** Verified experimentally rather than
  from memory, using the repo's own `modernc.org/sqlite` driver: a table
  created without the column, populated, then altered with
  `ADD COLUMN is_active INTEGER NOT NULL DEFAULT 1`, returns
  `is_active = 1` (`typeof` = `integer`, non-NULL) for the **pre-existing**
  row. A brand predating the migration becomes active, not NULL. (Scratch test
  removed; it was a probe, not a deliverable.)
- **Additive rather than editing `001_init.sql` is the right call**, and the
  migration's stated reason checks out: `internal/db/db.go`'s
  `migrationChecksum` is computed over the comment-stripped statement text and
  `verifyAppliedMigrations` hard-fails boot on drift for any version at or
  below the ledger watermark, with `idempotentRerunVersions` empty (and
  documented as "should stay empty"). Editing `001_init.sql`'s
  `CREATE TABLE brands` is a statement change, so it would brick every
  already-migrated device. `002`/`003` set the same precedent.
- **No version collision**; `guard-migration-version-collision.sh` and its
  self-test both pass.

### R7 — `DumpAdmin`/`upsertRow` need no change for the new column (dismissed — traced)

Traced both directions of version skew rather than trusting the claim.
`DumpAdmin` is `SELECT *` + `scanGeneric` (column-name driven), so
`brands.is_active` travels automatically. `upsertRow` builds its column list
from `tableColumns` (the **live replica** schema) and skips any column absent
from the wire record (`if !ok { continue }` — "column the primary doesn't
know"). So: an older primary that doesn't send `is_active` leaves the
replica's value untouched; a newer primary sending it to an unmigrated replica
has the field ignored. Both safe, no code change needed. The new brand test
exercises the full dump → wire → apply round trip, applied twice, which also
confirms the retire is idempotent and doesn't double-mangle.

### R8 — Manual (`web/help/`) needs no update (dismissed — checked)

There is no brands topic (the only `brand` hits in `web/help/en/` are the
idiom "brand new"), and nothing in the manual documents what a name looks like
after an FK-blocked prune. `multitill.md`'s description of registers as
shop-wide and manager-deactivatable stays accurate. This change fixes silent
corruption on an invisible sync path; it adds, removes and alters nothing a
shop owner does or sees. No new page routes, so `guard-help-topics.sh`'s route
coverage is unaffected (it passes).

### R9 — Recurring bug classes and hygiene (checked, not applicable / clean)

- Missing `os.MkdirAll` before a file write: not applicable, no file writes.
- cwd-relative path where `paths.Data(...)` belongs: not applicable, no
  filesystem paths touched.
- Demo/test data: `Acme Foods`, `Alice`/`alice`, `Front Till`, `Back Room`,
  `Local Store`, `Widget`, `Reduced 7%`. No real client or shop name. No
  secret-shaped literal (`'hash'` in the users fixture is a placeholder string
  in a `pin_hash` column, not a credential).

## Verification performed

Everything below was run by the reviewer on the branch, after the R1 fixes.

- `gofmt -l .` — no output.
- `go build ./...` — clean.
- `go test ./...` — full suite green (this change is read by `internal/pages`
  and `internal/auth`, so the whole suite matters, not just `internal/data`).
- `golangci-lint run ./...` — **0 issues**.
- Every CI-blocking guard in `.github/workflows/ci.yml`'s `build` job — all
  pass, including `guard-data-access.sh` (+ its self-test),
  `guard-migration-version-collision.sh` (+ its self-test), `guard-i18n.sh`,
  `guard-compliance-claims.sh`, `guard-help-topics.sh`, `guard-docs-shots.sh`
  and `check-brand-assets.sh`.

### Independent TDD re-verification (not taken on trust)

The TDD claim was re-proved from scratch rather than accepted. Method: revert
`sync_admin_repo.go`, `catalog_repo.go`, `pos_repo.go` and `auth_repo.go` to
`main`, **keep** the new test files and the new migration, and observe.

**Stage 1 — the unit test genuinely drives the function into existence.** With
the four files reverted, the package does not build:

```
# github.com/universaltill/universal-till/internal/data [.../internal/data.test]
internal/data/strip_retire_mangle_test.go:31:13: undefined: stripRetireMangle
```

**Stage 2 — the five integration tests fail for the right reasons.** With
`strip_retire_mangle_test.go` moved aside so the rest can compile and run:

```
--- FAIL: TestAdminApply_RegisterRetiredInPlaceWhenFKBlockedBySatelliteShiftHistory
    ListRegistersForAdmin name for retired register = "Front Till~reg-1", want "Front Till"
--- FAIL: TestAdminApply_StockLocationRetiredInPlaceWhenFKBlockedBySatelliteInventoryHistory
    ListStockLocations name for retired location = "Back Room~loc-1", want "Back Room"
    ListStockLocationsForAdmin name for retired location = "Back Room~loc-1", want "Back Room"
--- FAIL: TestAdminApply_BrandRetiredInPlace_DisplayNameUnmangled
    an FK-blocked brand prune must retire in place: is_active=1, want 0
    ReadLookup(brands) name for retired-but-referenced brand = "Acme Foods~b1", want "Acme Foods"
    GetLookup(brands, b1).Name = "Acme Foods~b1", want "Acme Foods"
--- FAIL: TestAdminApply_TaxCodeRetiredInPlace_DisplayNameUnmangled
    GetTaxCode(tc-red).Name = "Reduced 7%~tc-red", want "Reduced 7%"
    ListAllTaxCodes name for retired tax code = "Reduced 7%~tc-red", want "Reduced 7%"
--- FAIL: TestAdminApply_UserRetiredInPlace_DisplayUsernameUnmangled
    ListUsers username for retired user = "alice~u-alice", want "alice"
    GetUser(u-alice).Username = "alice~u-alice", want "alice"
```

These are real red tests, not false-passes, and the brands test independently
proves **both** sub-bugs — `is_active=1, want 0` (no retire flag) and the
mangled name — in a single run.

**Stage 3 — restore, all green:**

```
--- PASS: TestStripRetireMangle_OnlyExactOwnIDSuffix
--- PASS: TestAdminApply_RegisterRetiredInPlaceWhenFKBlockedBySatelliteShiftHistory
--- PASS: TestAdminApply_StockLocationRetiredInPlaceWhenFKBlockedBySatelliteInventoryHistory
--- PASS: TestAdminApply_BrandRetiredInPlace_DisplayNameUnmangled
--- PASS: TestAdminApply_TaxCodeRetiredInPlace_DisplayNameUnmangled
--- PASS: TestAdminApply_UserRetiredInPlace_DisplayUsernameUnmangled
```

### The reviewer's own R1 fix was held to the same standard

The new regression test was proved red before it was accepted as green, by
stashing *only* the three fixed source files:

```
--- FAIL: TestAdminApply_RetiredLocationAndRegisterNamesUnmangledInStockAndFiscalReaders
    ListStockLevels (inventory page) LocationName = "Back Room~loc-1", want "Back Room"
    GetLowStockItems (reorder list) LocationName = "Back Room~loc-1", want "Back Room"
    StockForExport location_name = "Back Room~loc-1", want "Back Room" (variant_id="")
    StockForExport location_name = "Back Room~loc-1", want "Back Room" (variant_id="var-1")
    ListRegisterLocations (fiscal register page) RegisterName = "Front Till~reg-1", want "Front Till"
    ListRegisterLocations (fiscal register page) LocationName = "Back Room~loc-1", want "Back Room"
```

then passing with them restored. Note the fixture detail the test records in a
comment: the item and its variant must live on the **primary**, because
`items`/`item_variants` are themselves admin-synced — a satellite-local item
would be retired (`is_active = 0`) by the same apply and then filtered straight
out of the readers under test, producing a test that passes for the wrong
reason.

## Deferred / explicitly not done

- `FindOrphanedPaymentMethods`' unfiltered `pm.name` in a startup **log** line
  (R4). Accepted as-is: a log, not a display surface, and the id is logged
  beside it.
- `deleteMissing`'s `LIKE '%~' || pk` idempotence check being
  ASCII-case-insensitive and treating `_`/`%` in an id as wildcards (R5).
  Pre-existing on `main`, unrelated to this card, needs a pathological
  name/id to trigger.
- A row whose real name genuinely ends in `"~" + its own id` is stripped
  (R5). Inherent to the suffix-strip design; the alternative is a separate
  `retired_name` column, which is disproportionate here.

## Conclusion

**Safe to merge.** The original change is well-targeted and its two most
debatable decisions — the additive migration and the unfiltered `ReadLookup`
for brands — are both right, and both verified against this codebase's own
precedent and machinery rather than against the diff's description of them.
The one real defect (R1: four join-based display readers, including the
fiscal-register page and the inventory page that the blocking FK itself
implies) was small and squarely in scope, and is fixed on this branch with its
own red-then-green regression test. Full gate re-run clean afterwards.

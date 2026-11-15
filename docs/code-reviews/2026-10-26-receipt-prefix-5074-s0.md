# Review: receipt prefixes never reused, provenance in the sale's transaction (ut-docs#5074 S0 + S0b)

- **Date**: 2026-10-26
- **Branch**: `fix/5074-s0-receipt-prefix`
- **Card**: ut-docs#5074 (part of; design note `ut-docs/reference/design-5074-mirror-sales-to-replicas.md` §1.6 items 1–2, §3.6, §14 S0/S0b). Ships with the mirror off; needed for a several-tills shop onboarding 2026-10-28.
- **Reviewer**: independent read-only subagent on a different model (Fable).

## The bug (verified in code)

`chooseTillNo` (`internal/pages/sync_api.go`, after ut-docs#5058 UT-5 / PR #2165)
numbered a new till `max(len(ListTills)+1, every T<n>- on a tills row or a sale) + 1`.
A revoked till's row is deleted (`TillsRepo.DeleteTill`) and a withdrawn
enrolment's too, so their numbers came back as soon as none of their sales
were on the main till: add till 2, revoke it, add another → **T2- again**. Also
missed: sales moved to `sales_archive` by "Clear transaction history", and on a
till promoted to main till its own `sync.receipt_prefix` (kept by
`ClearReplicaIdentity`) and every number the old main till had granted. Two tills
on one prefix mint the same `receipt_no`; the journal replay hits
`UNIQUE(sales.receipt_no)`, which is not quarantined, so that till's sync to the
main till stays refused (and a UT-5 re-join fails).

## What shipped

- **S0** — `TillsRepo.EnrolTill` (`internal/data/tills_receipt_prefix.go`)
  inserts the till row and grants its number in one transaction (write lock first:
  `_txlock=immediate`), so concurrent enrolments serialize. Number = one past
  the max of: the shop setting `shop.till_no_high_water` (new; raised in the same
  transaction; not per-till, so it travels in the admin bundle and a promoted till
  continues it), every `T<n>-` on a tills row, in `sales`, in `sales_archive`, this
  till's own `sync.receipt_prefix`, and the till count (the old rule, a floor for
  pre-092 tills). UT-5's re-join rule is unchanged: a requested own prefix is kept
  when nothing shows it in use; one above the record raises the record.
  `chooseTillNo`, `SetReceiptPrefix`, `InsertTillWithRole` removed (no leftovers);
  `InsertTill` delegates to `EnrolTill`. The `till_enrolled` audit now records
  `till_no` and `requested_prefix`. An allocation error now fails the enrolment
  (500, nothing written) instead of enrolling a till with no recorded prefix.
- **No migration.** The record is a settings row; existing tills, prefixes and
  receipts are untouched. The first enrolment after upgrade derives the floor
  from what is on disk.
- **S0b** — `SaleInput.TillID`/`CreatedAt` → `InsertSaleParams.TillID`/`CompletedAt`:
  the journal replay's `CompleteSale` INSERT carries `till_id`, origin
  `created_at` and its `local_date`; `completed_at` stays the replay time (as
  before). `SetSaleProvenance` kept as a fallback whose UPDATE now matches no row
  when provenance is already right (fires no UPDATE trigger — matters for the
  mirror's M1 triggers).

## Tests (real migrated temp DBs, real enrol/revoke/withdraw routes)

TDD: `internal/pages/receipt_prefix_5074_test.go` written first; on the old code
6 of 7 failed with the real numbers (revoke → T2-, newest revoked → T3-, withdrawn
→ T2-, restart → T3-, archive → T2- want T7-, promoted → T2- want T6-).
`sale_provenance_5074_test.go` failed with `["ins  <replay time>", "upd till-2
<origin>"]`, want one insert carrying both. All pass after the fix.
Data tests: high water recorded and never lowered by revoke or a re-join,
invalid/main-till prefixes ignored, 12 concurrent enrolments distinct, an
existing shop (pre-092 tills, sales under T2-/T3-) numbers T4- with every receipt
and till row unchanged, the record arrives on a replica through the real admin
bundle (`DumpAdmin` → `ApplyAdminWithResult`). Hand-rolled `sales` schemas in four
`internal/pos` tests gained the real `till_id` column; `newTillsTestDB` now uses a
migrated DB.

Not mutation-checked: the concurrency test against a non-transactional variant.

## Findings (independent review)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Minor | The re-join rule trusts the requested prefix: a device that was T3 at another shop could claim a revoked till's free T3 here. Inherent to UT-5 (re-join to a new main till). | Accepted; `till_enrolled` audit now records `till_no` and `requested_prefix` so a reclaimed number is visible. |
| 2 | Minor | Promote on a stale admin bundle (a till enrolled on the old main after the last pull) can regrant that till's number. | Accepted (narrow window, mirror lag); belt-and-braces unique index on `tills.receipt_prefix` when a migration is next opened. Deferred. |
| 3 | Minor | Restoring an older backup on the main till lowers the record and roster. | Pre-existing class; deferred (restore could warn when the backup's record is below the live one). |
| 4 | Nit | The admin-generation assertion did not prove the record travels. | Fixed: bundle round-trip test. |
| 5 | Nit | `manifest.json` surface hash edited. | Written by `scripts/ci/update-docs-shots-surface-hash.sh` (no template, CSS, JS or rendered string changed; only Go handler internals); `guard-docs-shots.sh` passes. |
| 6 | Nit | `updated_at` bound as `time.Time`. | Same as `ClearReplicaIdentity`; no change. |

Reviewer verified: one-transaction allocation under BEGIN IMMEDIATE, settings
not cached by `settings.Store`, the key not in `PerTillSettingPrefixes`, nothing
inside `CompleteSale`'s transaction or its post-commit hooks reads the sale row's
`created_at`/`local_date`/`till_id`, `SetSaleProvenance` has one caller, no SQL
outside `internal/data`.

## Gate

`gofmt`, `go build ./...`, `go vet ./...`, `go test ./...` all green;
`guard-data-access.sh`, `guard-i18n.sh`, `guard-deadcode-baseline.sh`,
`guard-docs-shots.sh` pass; the other `ci.yml` guards pass or fail identically on
`origin/main` locally (environment: shellcheck version, store-private, a few
`_test.sh` self-tests). `golangci-lint` not run locally (binary built with Go 1.26;
CI runs it). Nothing visual changed; no screen looked at.

## Verdict

Safe to merge. No cloud dependency, no migration, no UI.

## For the mirror lane (M1)

- `mirror_sales.receipt_no` is not in the prefix scan (design §3.6): the
  isolation guard forbids the identifier outside `sales_mirror_repo.go`, so M1
  should expose a prefix query there and `receiptPrefixesInUse` should union it.
- A journaled sale is now one INSERT with final provenance; `SetSaleProvenance`
  no longer updates a correct row, so the `_sale_upd` trigger does not fire twice.

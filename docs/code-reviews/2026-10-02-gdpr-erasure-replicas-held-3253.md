# 2026-10-02 — GDPR erasure reaches parked baskets and LAN replicas (ut-docs#3253)

**Author:** pipeline lane `lane:cloud-24`. **Reviewer:** independent subagent
on a different model from the author, fresh context, plus a scoped second pass
over the fixes.

## Change
- `POSRepo.EraseCustomer` → `stripCustomerFromHeldSales` removes
  `customer_id`/`customer_name` from `held_sales` and `held_sales_archive`
  payloads. A label equal to the customer's name becomes the park's local
  clock time. Live rows bump `updated_at` (ADR-0093 write-through ordering).
- Replica admin sync (`adminTables` `customers`): when a row has to be
  retired in place because local sales pin it (FK-blocked), it becomes an
  anonymous shell (`scrubOnRetire`: name `''`, contact data and `loyalty_no`
  NULL). The `onPrune` hook strips the replica's parked baskets on both the
  hard-delete branch and the retire branch, without bumping `updated_at`
  (ReconcileWithPrimary compares it).
- `SearchCustomers` / `LookupCustomer` skip the shell (`name <> ''`).
- The erase handler, and a replica's admin pull, detach an erased customer
  from the open cashier and kiosk baskets
  (`internal/pages/erased_customer_basket.go`).
- Help: `web/help/en/display.md` item 16. Manifest: hash-only refresh
  (`Docs-Shots-Unchanged: true`). The help prose change and the backend-only
  `internal/pages` edits change no screenshot pixel.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | The live Engine basket kept the erased customer, so the next Hold re-wrote it into `held_sales` | **Fixed**: `forgetCustomerOnBaskets` / `forgetCustomersGoneSince`, with handler and unit tests |
| 2 | minor | No assertion that a repeat pull is a no-op; hard-delete `onPrune` branch untested | **Fixed**: generation assertions + `TestAdminApply_ErasedCustomerWithoutSatelliteHistoryStripsParkedBasket` |
| 3 | minor | Replica sales keep `customer_id` → a later upload FK-quarantines on the primary | **Dismissed**: `applyJournal` sets no `CustomerID` (sync_sales.go), confirmed by the reviewer |
| 4 | minor | Label scrub is exact-match only ("Anna – 2 lattes" keeps the name) | **Accepted residual**: a typed label is free text |
| 5 | nit | "byte-for-byte" comment overstated (json.Marshal re-orders keys and re-escapes `<>&`) | **Fixed**: comment |

Verified OK by the reviewer: the `IS NOT ''` / `IS NOT NULL` pending clause
is idempotent; `loyalty_no` is skipped from the `~id` mangle;
`held_sales_archive` has an implicit rowid; rows are drained before the
UPDATEs; ReconcileWithPrimary is unaffected.

## TDD
All new tests fail on the unfixed code. Re-verified by stashing the fix:
`TestEraseCustomer_StripsCustomerFromParkedBaskets`,
`TestAdminApply_ErasedCustomerPinnedBySatelliteSaleBecomesAnonymousShell`,
`TestEraseCustomer_DetachesCustomerFromOpenBaskets`.

## Out of scope
Plugins get no erasure signal. Filed as ut-docs#3435 (Architect/ADR).

## Verdict
APPROVE after fixes.

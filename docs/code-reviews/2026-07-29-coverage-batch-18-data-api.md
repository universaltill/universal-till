# Test coverage batch 18: data-management / GDPR admin API

2026-07-29

`internal/pages/data_api.go` — manager-gated, destructive/compliance
admin tooling: transaction-history reset (typed "RESET" confirmation),
GDPR customer search/erasure, and obsolete-catalog preview/cleanup
(typed "CLEANUP" confirmation). Previously zero coverage.

The underlying business logic (what "obsolete" means, what erasure
does to linked sales, what reset clears) is already covered at the
repository layer in `internal/data/reset_test.go`
(`TestResetTransactionHistoryClearsSalesKeepsCatalog`,
`TestEraseCustomer`, `TestCleanupObsoleteItems`). This batch is
deliberately a thin HTTP-layer test — manager gating, confirm-string
exactness, status codes, JSON response shape — not a re-derivation of
those repo-level rules.

## What's covered

- All 5 endpoints refuse with 403 + `success:false` when not a manager
  (table-driven over the full endpoint list, `UT_AUTH` not disabled).
- `reset-transactions`: missing/wrong confirm value rejected with 400;
  a correct `RESET` clears sales, leaves the catalog untouched, and
  returns the exact expected message.
- GDPR `customers` search and `erase`: missing id, unknown id (404),
  and a real erasure.
- `obsolete-items` preview and `cleanup-catalog`: missing/wrong confirm
  value rejected; a correct `CLEANUP` removes only the obsolete item and
  leaves active catalog items intact.

## Schema fix

`seedForPages`'s shared test fixture (`internal/pages/ui_smoke_test.go`)
had drifted from production on the `customers` table — missing
`email`/`address`/`created_at` (required by `SearchCustomers`'s
`COALESCE(email,'')`) and carrying an extra `is_active` column that
doesn't exist in `internal/db/migrations/001_init.sql`. Fixed to match
production exactly (confirmed nothing else in the package referenced
the phantom `is_active` column). Two entirely missing tables needed by
this batch's handlers were added, copied column-for-column from the
real migrations: `held_sales` (002) and `price_history` (001). A
comment was added above the fixture explaining these three tables must
stay in sync with the migrations they mirror, since a future silent
drift here would make tests pass against a schema that doesn't match
production.

## Independent review (opus) — three real gaps closed

1. **Mis-named exactness test.** `TestCleanupCatalog_RequiresExactConfirmString`
   only tested a *missing* confirm value — it never proved the "CLEANUP"
   comparison is actually exact (case-sensitive, no fuzzy match), despite
   its name. Added a `confirm=cleanup` (lowercase) case, matching the
   sibling reset test's stronger pattern.
2. **Reset test didn't confirm the catalog survives at the HTTP layer.**
   The repo-level test already proves this, but since this action is
   irreversible in production, the review flagged it as cheap and
   worth asserting again at the handler boundary — added.
3. **Brittle message assertion.** `strings.Contains(message, "1")` would
   pass on "10" or "21" sales cleared; tightened to an exact string match
   against the real `fmt.Sprintf` output, since exactly one sale is seeded.

The seed copy of `price_history` intentionally omits the FK and
item/variant XOR `CHECK` constraint present in production — noted as
harmless for this batch (no test inserts a row that would violate it)
but worth remembering if a future test does.

## Verification

`go build ./...`, `go test ./...`, `scripts/ci/guard-data-access.sh`,
`scripts/ci/guard-i18n.sh` — all pass.

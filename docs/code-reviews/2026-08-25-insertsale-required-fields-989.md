# Code review: `InsertSale` required-field guard (ut-docs#989)

**Date:** 2026-08-25
**Card:** universaltill/ut-docs#989 — "universal-till: InsertSale/InsertSaleParams
should reject silently-omitted required fields"
**Branch:** `feat/989-insertsale-required-fields`
**Complexity:** easy (build: Sonnet inline; review: Sonnet, fresh context,
isolated worktree)

## What shipped

Follow-up to ut-docs#976 (`InsertSaleParams` struct refactor). That
refactor's own review found that a struct literal lets a genuinely-required
field silently default to its Go zero value if a future caller omits it —
whereas the old positional signature forced every value to be supplied.
Concretely, several `sales` columns are `TEXT NOT NULL` with a SQL-level
`DEFAULT`, but that default only applies when a column is *omitted* from
the `INSERT` — binding it explicitly to `""` (which an omitted struct
field does) writes the empty string straight past it, with no error.

This card adds `InsertSaleParams.validateRequired()`, called at the top of
`InsertSale`, which rejects a call with a clear, field-naming error if any
of `SaleID`, `ReceiptNo`, `SaleType`, `Currency`, `CreatedAt`, `SyncStatus`,
or `TenderType` is left at its zero value. Everything else
(`RegisterID`, `CashierID`, `CustomerID`, `TableID`, `Note`, `OrderType`,
the sync-retry fields, `ServiceCharge`/`ServiceChargeTaxBasisBP`) is
legitimately optional and stays unguarded, per the existing test suite's
deliberate reliance on omitting them.

New test `TestPOSRepo_InsertSale_RejectsMissingRequiredFields`
(table-driven, one subtest per required field) covers both the rejection
path and a sanity check that omitting only the legitimately-optional
fields still succeeds.

## Independent review

Spawned a Sonnet subagent, fresh context, isolated git worktree
(`isolation: "worktree"`), briefed with the diff scope, the card's
requirement, and the repository-pattern rules from `CLAUDE.md`.

**Verdict: yes-with-fixes-needed (non-blocking), safe to merge.**

The reviewer independently re-verified the TDD claim itself — reverted the
guard's call site, confirmed `TestPOSRepo_InsertSale_RejectsMissingRequiredFields`
failed with the exact "want error, got nil" message for all six fields it
originally covered, then restored the guard and confirmed it passed again
(actual before/after output captured in the review). It also cross-checked
every `InsertSaleParams{...}` call site in the repo (production
`internal/pos/sales.go` plus every test literal) against the required-field
list, and checked the list itself against the `sales` table's actual
`NOT NULL` columns and their default/bind behavior in `internal/db/migrations/`.

### Findings and disposition

1. **Non-blocking — fixed in this branch.** `tender_type` is also
   `TEXT NOT NULL DEFAULT 'unknown'` and bound directly (no `nullIfEmpty`
   indirection) — the identical silent-empty-string risk class this card
   exists to close — but was missing from the original required-field list.
   Confirmed safe to add: production (`deriveTenderType` in
   `internal/pos/sales.go`) always returns a non-empty value, and every
   existing test call site already sets `TenderType` explicitly. Added
   `TenderType` to `validateRequired()` and a matching subtest.
2. **Non-blocking / nitpick — fixed in this branch.** The doc comment above
   `validateRequired()` referenced a stale identifier
   (`requiredInsertSaleFields`) that doesn't exist in the code. Reworded to
   match the actual method name and explain *why* each field is required
   (binds directly into a `NOT NULL` column, bypassing the SQL-level
   default).

No other findings. The required-field list (as revised to include
`TenderType`) was assessed as correctly scoped — neither over-broad (every
listed field is genuinely non-optional per the schema) nor missing any
other `NOT NULL` text column with the same bind pattern.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l` clean.
- Full `go test ./...` green (whole repo, not just the touched packages).
- `bash scripts/ci/guard-data-access.sh` and the other 15 CI-blocking
  guards from `universal-till/CLAUDE.md`'s "Before committing" list all
  pass (unaffected by this backend-only change, run in full per the
  standing gate discipline).
- Repo-wide grep confirms every `InsertSaleParams{...}` call site (7
  pre-existing + the new test) sets all seven now-required fields; no
  existing code path can trip the new guard.

## Safe-to-merge verdict

**Yes.** Both review findings were small and fixed in-branch; no blocking
findings; independent TDD re-verification confirmed genuine, not
decorative.

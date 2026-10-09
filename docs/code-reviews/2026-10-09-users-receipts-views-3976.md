# Review — core read views `users.list.v1` and `sales.receipts.v1` (ut-docs#3976)

**Date:** 2026-10-09 · **Lane:** cloud-54 · **Author model:** Opus 5.5 · **Reviewer model:** Fable (independent, fresh context)

## What shipped

- `users.list.v1` (new permission class `view:users`): id, display name and
  active for every till user, ordered by display name. The `system` service
  identity is skipped, as on the Users page. No PIN hash, username or role is
  ever selected.
- `sales.receipts.v1` (`view:sales`): one business date's completed receipts
  (sales and returns), with lines, payments (applied amount and tip only) and
  `fiscal_signed`. Arguments are `days_ago` 0–31, `offset` and `limit` 1–100.
  The business day starts at `reports.business_day_start`, the same shift
  `sales.by_day.v1` buckets with. Each page runs three queries, never N+1.
- The install prompt gets its own consent line for `view:users`
  (`plugins.permissions.desc.view_users` in en/ar/fa/tr, with de/es/pt pack
  PRs in the same cycle).
- Contract: ut-docs `reference/contracts/plugin-views.md` 1.2.0 and
  `reference/plugin-host-functions.md`.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `ORDER BY s.created_at` sorted text. Rows hold RFC3339 (`T…Z`, every live sale path) and the space form (schema default, older rows), so a mixed day was not oldest first. | **Fixed**: `ORDER BY datetime(s.created_at), s.id`. New regression case in `TestSalesReceiptsView` (failed first with `s1 s2 s5 s-rfc`). |
| 2 | minor | `datetime(created_at)` in the WHERE clause can't use `idx_sales_created`, so each page scans `sales`. | **Accepted**: same idiom as `SalesByDay`, a till-sized table, and the 5 s deadline. A sargable prefix filter is a perf follow-up only if a real till shows it. |
| 3 | minor | The contract didn't say what form `created_at` takes. | **Fixed**: it names RFC3339 and the legacy space form. |
| 4 | nit | `docs/plugin_guidelines.md` listed only three view classes. | **Fixed**. |
| 5 | nit | No review record. | This file. |
| 6 | nit | A `business_day_start` inside a DST gap is ambiguous (Go and SQLite pick different offsets). | **Accepted**: no code; both views agree on every other day. |

## Verified

- The reviewer re-ran TDD by breaking code and rerunning the tests. Dropping the `fiscal_receipt_evidence` EXISTS, changing the window's `<` to `<=`, or dropping
  the `system` filter each made a test fail. All were restored afterwards.
- The reviewer checked the codebase's `sale_type` values (only `sale` and `return`) and `status` values (only
  `completed` and `voided`). Held sales live in `held_sales`, so nothing is dropped or wrongly included.
- Privacy: no card, terminal, voucher, payment reference or customer field.
  The test checks the marshalled bytes.
- The data tests also pass under TZ America/New_York, Asia/Kolkata and Pacific/Auckland.
- Gates: gofmt, build, vet, `go test -race ./internal/data/`, the related
  plugins/pages tests under `-race`, full `internal/plugins` and
  `internal/pages` without race (as CI runs them), golangci-lint, and every
  `ci.yml` build-job guard.
- No UI page changed apart from one consent line on the existing install
  prompt, which renders as text under the permission badges. Not screenshotted.

**Verdict:** safe to merge.

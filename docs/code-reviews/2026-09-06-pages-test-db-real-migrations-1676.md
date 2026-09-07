# Code review: `internal/pages` test-DB swap to real migrations (ut-docs#1676)

**Branch:** `fix/1676-pages-test-db-real-migrations`
**Date:** 2026-09-06/07
**Complexity:** medium (Sonnet implementation, Opus independent review)

## What shipped

`internal/pages`'s test harness (`openPagesTestDB`/`seedForPages` in
`ui_smoke_test.go`) hand-rolled ~50 `CREATE TABLE` statements instead of
running the real migrations from `internal/db/migrations/`. This let test
fixtures silently drift from the production schema — a column, `NOT NULL`,
or foreign key added to the real schema had no guarantee of being mirrored
in the test copy, and the failure mode when it wasn't was silent (tests
green, a real device breaks).

This branch:

1. Swaps `openPagesTestDB` to call the real `internal/db.Open` (so it runs
   actual migrations), with a `_pragma=` DSN override so the test suite
   keeps its fast in-memory-journal settings.
2. Gutted `seedForPages` to stop re-seeding rows the migration's own seed
   data section now provides (roles, permissions, tax codes, stock
   locations, payment methods, the `system`/`kiosk` users, country
   settings), keeping only fixture-specific `INSERT OR REPLACE`s.
3. Fixed every test-fixture gap that swap surfaced — originally scoped at
   ~40-50 failures, actually ~470 across the whole package. Categories,
   with their original tracking cards:
   - **ut-docs#1677** (plugin/plugin_catalog composite FK + `entrypoint
     NOT NULL`): ~20 files' `INSERT INTO plugins` sites fixed with a
     matching `plugin_catalog` row; `plugin_page_test.go` got a shared
     `seedTestPlugin` helper replacing ~10 duplicated hand-rolled inserts.
     **Fully absorbed into this branch — #1677 can close once this merges.**
   - **ut-docs#1678** (`users.created_at` doesn't exist on the real
     schema): dropped from ~30 inserts across `users_page_test.go`,
     `audit_page_test.go`, `inventory_api_test.go`, `shifts_api_test.go`,
     `fiscal_gate_test.go`. **Fully absorbed — #1678 can close.**
   - **ut-docs#1682** (synthetic actor ids like `m1`/`sa-1`/`blocked-admin`
     injected via `auth.WithUser` with no real `users` row, tripping
     `audit_log.actor_id`/`blocked_actor_id`'s real FK): seeded per shared
     test-deps helper where the actor recurs across a file
     (`permission_settings_page_test.go`, `promotions_page_test.go`,
     `reports_page_test.go`, `setup_page_test.go`'s `newFullAuthDeps` —
     ~20 callers), or at the specific call site otherwise. **Fully
     absorbed — #1682 can close.**
   - **New, not originally scoped:** `sale_lines.item_id`'s real FK
     (refund tests' 15 ad hoc item ids, `export_dispatch_test.go`'s
     `sale_lines` CHECK constraint), `shifts.cashier_id`'s FK, a
     `payment_methods` row for id `voucher` used by several LAN-sync
     tests, migration seed-data row-count collisions (tax codes, stock
     locations now seed 3 rows, not the test's assumed 1).
   - **ut-docs#1679** (tests that `DROP TABLE tax_codes`/
     `stock_locations`/`payment_methods`/`items` to force a repo-error
     path, which now fails at the `DROP` itself since real FKs reference
     those tables): originally left deliberately red pending an
     Architect-level design decision — **that decision was made and
     implemented in this same PR** once it became a hard CI blocker
     (`go test ./...` on `main`'s own `build` job fails on exactly these
     tests, not just a tracked backlog item). Design: replace the
     schema-mutating `DROP TABLE` with either closing the test's
     `*sql.DB` (an idiom already established in `inventory_api_test.go`)
     where nothing after the failing request touches the DB again, or a
     narrower `ALTER TABLE ... RENAME/DROP COLUMN` targeting only the
     specific column the repo call under test selects on, where an
     earlier step in the same request (e.g. a sale lookup) needs the DB
     to keep working. All 10 tests pass; `internal/pages` is fully green,
     zero exceptions.
4. Also carries an identical copy of PR #846's `cloudAdjustStock` actor-id
   fix (`ActorID: "cloud"` → `"system"`) — that fix is load-bearing for
   this branch's own tests once `openPagesTestDB` enforces the real
   `audit_log.actor_id` FK. #846 shipped and merged standalone first
   (independent production bug, didn't need to wait); this branch's copy
   converged to an identical diff once merged with `main`.

## Two more real production bugs found along the way (both already fixed, shipped separately)

- **ut-docs#1681** — `applyJournal` (LAN-sync journal replay) never called
  `EnsurePaymentMethod` before inserting a payment, unlike the live tender
  and refund paths. A replica sale tendered with a payment method the
  primary didn't have yet failed replication outright. Fixed and merged
  as PR #847 (a different, concurrent pipeline cycle picked up the filed
  card).
- **ut-docs#1684** — `cloudAdjustStock`'s audit actor `"cloud"` violated
  the real `audit_log.actor_id` FK, silently failing every cloud
  `adjust_stock` directive in production. Fixed and merged as PR #846.

## Independent review

Full independent read at Opus, isolated worktree, actually running the
gate rather than reading the diff — verdict **SAFE WITH FIXES**. Findings
and disposition:

| # | Finding | Severity | Disposition |
|---|---|---|---|
| 1 | `web/help/img/manifest.json` stale after a `main` merge touched a hashed `internal/pages/**.go` file | blocker | Fixed — regenerated via `make docs-shots`, guard green |
| 2 | Branch behind `main` (PR #846 had merged); trivial conflicts in `cloudsync_wire.go` + manifest | blocker | Fixed — merged, resolved to main's version (dropped the now-obsolete "kept here until #846 merges" comment), regenerated manifest |
| 3 | Third instance of the #1681/#1684 bug class: `applyJournal`'s `sales.cashier_id`/`sale_lines.item_id` inserts have no FK-safety, empirically reproduced | non-blocking (pre-existing, un-exercised, not introduced by this branch) | Filed as ut-docs#1692 |
| 4 | `sync_api.go`'s `/api/sync/promote` discards `InsertAuditElevated`/`InsertAudit`'s error entirely | non-blocking (pre-existing, file untouched by this branch) | Noted here; worth its own card but not filed to avoid scope creep on an unrelated file |
| A | `plugin_settings_page_test.go`: `strings.Contains(body, "Reduced VAT")` assertion also satisfied by the real migration's own unrelated `tax_red` seed row (same name), softening the assertion | low | Fixed — assert the exact `"Reduced VAT (7%)"` string |
| B | Stale comment ("No active tax codes at all") after the real migration started seeding active ones | nit | Fixed |
| C | Orphaned standalone comment block after an insert was removed | nit | Fixed |
| D | ~12 fire-and-forget `_, _ = db.Exec(...)` fixture inserts in `ui_smoke_test.go` remain unchecked | nit | Left as-is — pre-existing pattern in that file, genuinely optional, not a correctness issue in the diff |

Reviewer also independently TDD-verified the trickiest single fix
(`sync_api_test.go`'s `TestSyncPromote_ElevatesOnValidApproverPIN`, where
`AuthRepo.CreateUser` mints its own id independent of the username
argument — the session injected via `auth.WithUser` has to use the id
`CreateUser` returns, not the literal username string, exactly the
`backup_api_test.go` precedent) by reverting it in place and confirming
the exact failure mode, then restoring it.

Reviewer confirmed the failing-test list is **exactly** the 10 documented
ut-docs#1679 exceptions and nothing else, across the whole `go test ./...`
run, and separately confirmed no other `CreateUser` call site in the
package made the same id/username mistake.

## Verified beyond automated tests

- `gofmt -l .` clean, `go vet ./...` clean, `go build ./...` clean,
  `golangci-lint run ./...` 0 issues.
- Full `go test ./...` green except the 10 documented ut-docs#1679
  exceptions.
- All CI-blocking guards from `universal-till/CLAUDE.md`'s "Before
  committing" list pass, including `guard-data-access.sh` (raw SQL stays
  test-only, no repository-pattern violation) and `guard-docs-shots.sh`.
- Manually confirmed `seedForPages`'s claim that the migration's own
  `role_permissions` seed (54 rows = 17 catalog actions × 3 roles, plus
  the two `fiscal_tse_override`/`permission_management` extras) is
  equivalent to the hand-rolled grants it replaced.

## Safe-to-merge verdict

**Safe to merge.** All independent-review blockers resolved (manifest
regenerated, merge conflicts resolved, nits A-C fixed); nit D and the two
non-blocking pre-existing findings are correctly left for follow-up work,
not this PR's scope. `internal/pages` is fully green with zero exceptions
— ut-docs#1679 (initially expected to stay deferred) got its Architect
decision and fix in this same PR once CI made it a hard blocker rather
than a backlog item; see above.

## Explicitly deferred

- **ut-docs#1692** (new) — `applyJournal`'s cashier_id/item_id FK gap,
  third instance of the #1681/#1684 class, found by this review.
- `sync_api.go`'s discarded audit-write errors (pre-existing, noted but
  not filed as its own card here — worth one, but out of scope for this
  diff which doesn't touch that file).
- `ui_smoke_test.go`'s remaining fire-and-forget fixture inserts (nit D) —
  optional cleanup, not a correctness issue.

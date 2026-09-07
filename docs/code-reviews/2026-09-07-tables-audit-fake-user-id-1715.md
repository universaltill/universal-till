# ut-docs#1715 — tables_page_test.go's audit-adjacent tests used a fake user id

Date: 2026-09-07
Branch: `fix/1715-tables-audit-fake-user-id`
Reviewer: independent subagent, fresh context, different model (Sonnet) —
`complexity:easy` per the scrum-master skill's model routing.

## Report

Found during the independent review of ut-docs#1393
(universal-till#868, `docs/code-reviews/2026-09-07-manual-free-table-1393.md`)
— pre-existing in `internal/pages/tables_page_test.go`, not introduced by
that change.

## Root cause

`audit_log.actor_id` carries `FOREIGN KEY (actor_id) REFERENCES users(id)`
(`internal/db/migrations/001_init.sql`), and `PRAGMA foreign_keys = ON` is
set permanently (`internal/db/db.go`). `tables_page_test.go`'s tests for
`table_create`/`table_update`/`table_move`/`table_activate`/
`table_deactivate` used an ad-hoc `auth.User{ID: "m1", Role: "manager", ...}`
with no matching row in `users`. `registerTables`'s `audit` closure called
`posRepo.InsertAudit(...)` and discarded its error (`_ = posRepo.InsertAudit(...)`),
so the FK violation on `actor_id="m1"` failed **silently** — these tests
never actually verified an audit row lands, only that the HTTP response
looked right.

`TestTablesPage_Release` was already fixed (a prior, unrelated change) to
use a real seeded user, which is how the gap surfaced.

## Change

- `TestTablesPage_CreateEditPositionDeactivateAndRender` (the test that
  exercises all five actions named in the ticket: create/update/move/
  activate/deactivate) now seeds a real manager via
  `data.NewAuthRepo(d.Db).CreateUser(...)`, same pattern as
  `TestTablesPage_Release`, and asserts a `table_create` `audit_log` row
  exists after creating a table.
- `TestTablesPageCreate_TapToPlacePosition` and
  `TestTablesStatePartial_FreeTodayOccupiedViaTableID` were also switched
  to a real seeded manager — both exercise a successful `table_create`
  through the same fake-actor path, so both were silently hitting the same
  FK failure. This is a scope decision beyond the ticket's literal wording;
  the independent review specifically checked every remaining fake-actor
  (`"m1"`/`"c1"`) test in the file against the handler flow and confirmed
  no other test with an unseeded actor still hides a real audit-write bug
  (every other one is refused before `audit(...)` is ever reached — see
  Finding 3 below).
- `internal/pages/tables_page.go`'s `audit` closure now logs
  (`log.Printf`) when `InsertAudit` fails, instead of silently discarding
  the error — mirroring the existing "log loudly on a swallowed
  audit/claim-release error" precedent in `internal/pages/hold_api.go`.
  A real production audit-write failure was previously invisible even to
  an operator reading the logs.

No behavior change to the shipped `registerTables` handlers beyond the
added log line; otherwise test-only.

## What the independent review found

Verdict: **SAFE TO MERGE**, no blockers.

1. **Observation** — the `log.Printf` addition correctly mirrors
   `hold_api.go`'s existing precedent (stdlib `"log"`, fire-and-forget).
   Consistent, not an inconsistency, and `golangci-lint` is clean on it.
2. **Observation, non-blocking** — ~73 other `InsertAudit` call sites
   across the rest of `internal/pages` remain untouched (no logging
   added). Correctly out of scope here — a repo-wide sweep would be the
   "broader audit-logging redesign" the ticket explicitly marks a
   non-goal — flagged only as a known remaining gap, not a defect.
3. **Verified, no gap** — every other fake-actor (`"m1"`/`"c1"`) test in
   the file was traced against the handler flow and confirmed to only
   ever reach `audit(...)` on a path that's already refused first
   (`requireManager`, `requirePrimary`, or a not-found lookup) — so the
   PR's choice of exactly 3 tests to fix is complete, not partial.
4. **Verified** — SQL/schema correctness: the new assertion's column
   names and bound `entity_id` match `audit_log`'s real schema and the
   handler's actual `audit(...)` call; no leaked `*sql.Rows`, no
   flakiness, no cross-test collision risk (each test gets its own fresh
   temp-file DB).
5. **Verified** — no i18n/money/repository-pattern/offline-first/kiosk-
   engine violations; `guard-data-access.sh` passes.

**TDD claim independently re-verified**: the reviewer temporarily reverted
just the manager-fixture change in `TestTablesPage_CreateEditPositionDeactivateAndRender`
back to the fake `auth.User{ID: "m1"}`, confirmed it now fails with
`create must write an audit_log row: sql: no rows in result set` (and the
new log line fires with the real FK-violation error), then restored the
file to the reviewed diff exactly (verified byte-for-byte).

## Verification

- `gofmt -l .` — clean.
- `go build ./...` — clean.
- `go vet ./internal/pages/...` — clean.
- `go test ./internal/pages/...` — all pass.
- `go test ./...` (full repo) — all pass.
- `golangci-lint run ./...` — 0 issues.
- `scripts/ci/guard-data-access.sh` — pass.
- Independent reviewer additionally ran `scripts/ci/guard-kiosk-engine.sh`
  (pass, unaffected by this change).

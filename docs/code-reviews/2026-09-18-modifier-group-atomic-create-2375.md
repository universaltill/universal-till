# Code review: `ModifierRepo.CreateGroupWithOptions` — one transaction for group+link+options

**Date:** 2026-09-18
**Card:** ut-docs#2375, a follow-up from the ut-docs#2322 review (finding 2;
see `docs/code-reviews/2026-09-17-modifier-groups-cloudsync-hook.md`).
**Complexity:** easy (Dev inline on Sonnet, review via a fresh-context
Sonnet subagent, per model routing).

## What shipped

`data.ModifierRepo.CreateGroup` wraps its own group+link inserts in one
transaction, but a caller creating a group **with options**
(`cloudUpsertModifierGroup`) had to call `CreateOption` once per option as
separate, non-transactional statements after it. If an option insert failed
partway (a real infra error — `SQLITE_BUSY` under a concurrent sale write,
disk I/O — not a validation failure, since options are pre-validated), the
group was left half-created; ut-docs#2322's hook worked around this with a
compensating `DeleteGroup` rollback on failure.

- `internal/data/modifier_repo.go`: new
  `ModifierRepo.CreateGroupWithOptions(ctx, groupID, itemID, name, required,
  minSelect, maxSelect, sortOrder, options []ModifierOption) (string,
  error)` — the group insert, the optional item-link insert, and every
  option insert now run inside ONE `*sql.Tx`, matching the existing
  transactional shape already used by `SetCategoryModifierGroups` in the
  same file. `itemID == ""` creates a shop-wide, unlinked group (ADR-0101),
  same as `CreateGroup` itself.
- `internal/pages/cloudsync_wire.go`: `cloudUpsertModifierGroup` now builds
  a `[]data.ModifierOption` slice and calls `CreateGroupWithOptions` once,
  instead of `CreateGroup` → `LinkGroupToItem` → a `CreateOption` loop →
  the compensating `rollBack`/`DeleteGroup` closure. The pre-validation loop
  (option name required, price delta non-negative) is unchanged and still
  runs before the repo call.
- `internal/data/modifier_repo_test.go`: three new repo-level tests —
  `TestModifierRepo_CreateGroupWithOptions_WritesGroupLinkAndOptionsTogether`,
  `_NoItemCreatesStandaloneGroup`, and
  `_FailureRollsBackEverything` (a SQLite `BEFORE INSERT` trigger forces one
  option insert to fail; asserts the group, link and every option row are
  all absent afterward).

The existing end-to-end test in `internal/pages/cloudsync_wire_test.go`,
`TestCloudUpsertModifierGroup_OptionFailureRollsBackTheGroup`, is unchanged
and still exercises the same property through the real transaction instead
of the old compensating delete.

**Deliberate scope cut**: the local admin creator
(`internal/pages/catalog/handlers.go`) is untouched — it creates a group and
its options as two separate form submissions today, never "create with
options" in one request, so the atomicity gap the issue named doesn't apply
there yet. Noted in the issue as a possible future beneficiary, not part of
this ask.

## Independent review

Fresh-context Sonnet subagent, isolated in its own worktree, diff read cold
with no access to the Dev reasoning.

**TDD re-verification (the "prove the test isn't a false pass" check).**
Temporarily changed the group insert inside `CreateGroupWithOptions` from
`tx.ExecContext` to `r.db.ExecContext` (committing it immediately, outside
the transaction) while leaving the link/option inserts on the tx. Re-ran
`TestModifierRepo_CreateGroupWithOptions_FailureRollsBackEverything`:
**failed** — `failure must roll back everything, got groups=1 links=0
options=0`, i.e. it caught the broken atomicity precisely. Restored the
real code: full `ModifierRepo` suite green again. The test is load-bearing.

**Checks, all pass:**
- No raw SQL outside `internal/data`/`internal/db`
  (`scripts/ci/guard-data-access.sh` clean; `cloudsync_wire.go` contains no
  SQL text, only the repo call).
- Every statement parameterized (`?` placeholders) — no SQL injection
  surface.
- Validation unchanged: `CreateGroupWithOptions` reproduces `CreateGroup`'s
  id/name checks and `CreateOption`'s per-option name/non-negative-price
  checks; `cloudUpsertModifierGroup`'s own pre-validation loop still runs
  before the call.
- Every write inside the `BeginTx`/`Commit` block uses `tx.ExecContext` —
  no statement slips through on `r.db` directly.
- `defer tx.Rollback()` matches the existing `SetCategoryModifierGroups`
  precedent in the same file, including the `//nolint:errcheck // no-op
  after Commit` comment.
- Caller's option-slice construction
  (`cloudsync_wire.go`) reproduces the old loop's trim/price/`SortOrder = i`
  exactly — no behavior drift.
- No dead code: `log` still used elsewhere in the file; `DeleteGroup` is
  still called from the admin delete-group handler — only the compensating
  call inside `cloudUpsertModifierGroup` was removed.
- No real client/shop name in test data (generic names only: Extras,
  Cheese, Bacon, Sauces, Ketchup).

Also ran clean: `go build ./...`, `go vet ./...`, `gofmt -l` on the three
touched files, `golangci-lint run ./internal/data/... ./internal/pages/...`
(0 issues), and the full `-run 'ModifierRepo|CloudUpsertModifierGroup'`
suite in both packages.

**No bugs or regressions found.** One non-blocking observation: the new
option insert doesn't set `is_active` explicitly — unchanged from the old
`CreateOption`, since the column defaults to `1` in the schema
(`internal/db/migrations/001_init.sql`).

## Verified beyond automated tests

Full repo test suite (`go test ./...`) run clean on the change before
review, and the CI-blocking `guard-data-access.sh` guard run directly.

## Verdict

**Safe to merge.** No deferred items.

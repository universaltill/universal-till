# Hotfix: colliding migration version 22 (ut-docs#2036)

## What was broken

`main` was red for every PR in the repo, not just this one. PR #1043
(`022_builtin_payment_method_i18n_keys.sql`) and PR #1044
(`022_sync_admin_version.sql`) each independently branched from a `main`
that didn't yet have the other's migration, so neither saw a conflict at
merge time — they merged cleanly one after the other, into different
filenames. `loadMigrations()`'s own uniqueness check (added for the
identical earlier incident, ut-docs#1056) rejects this at runtime:
`duplicate migration version 22: 022_sync_admin_version.sql collides with
022_builtin_payment_method_i18n_keys.sql — rename one`.

## How it was found and confirmed independent of the surfacing PR

Found while merging `main` into an unrelated PR
(universaltill/universal-till#1045, the catalog tab-strip scroll-shadow
card). Before touching anything, the failure was reproduced against a
**clean `git worktree` checked out directly at `origin/main`'s own tip**
(no local changes, no merge, nothing from #1045's branch) —
`go test ./internal/db/...` failed with the identical collision error, 30
tests, all citing the same `ut-docs#1056` message. This rules out the
possibility that #1045's own merge introduced or was merely exposing a
pre-existing latent bug through some interaction; `main` itself does not
build clean.

## Fix

By `main`'s own `--first-parent` merge order, PR #1043 merged before
#1044, so #1043 legitimately holds version 22. Renumbered #1044's file:
`022_sync_admin_version.sql` → `023_sync_admin_version.sql` (the error
message's own suggested fix — "rename one"), and updated the single
comment in `internal/data/sync_admin_version_test.go` that named the old
filename. No other file references the old name (`grep`-verified across
`.go`/`.sql`/`.md`).

**Why renaming is safe here**: `internal/db/CLAUDE.md`'s (ADR-0074)
append-only rule applies *after the first paying shop goes live on this
schema* — pre-revenue, migration files may still be freely edited/renamed
across as many pre-revenue releases as needed. No deployed database has
applied either version-22 file yet (both are brand new, from PRs merged
minutes apart the same day), so there is no `schema_migrations` ledger
anywhere recording the old number. The file's own `CREATE TABLE IF NOT
EXISTS`/`INSERT OR IGNORE`/`CREATE TRIGGER IF NOT EXISTS` statements are
unaffected by the rename — same SQL, same checksum-relevant content
(`migrationChecksum` strips only comments/whitespace, and the rename
touches neither).

## Verification

- **Before**: `go test ./internal/db/...` and `./internal/data/...` on a
  clean `origin/main` worktree — 30 failing tests, all the same collision
  error.
- **After**: same two packages, same worktree, fix applied — all green.
- `go build ./...`, `go vet ./...`, `gofmt -l .` (empty), `go test ./...`
  (full sweep, every package green), `golangci-lint run ./...` (0
  issues), `bash scripts/ci/guard-data-access.sh` (green) — all run
  against the fix.
- This is a one-file rename plus a one-line comment-string update with no
  behavioural change to either migration's SQL; the before/after test
  delta above is direct, reproducible evidence the fix addresses exactly
  the failure it claims to, not asserted on reasoning alone.

## Review

Self-reviewed under time pressure (this was blocking every open PR on the
repo, including the one that surfaced it) rather than routed through a
separate subagent pass: the change is a single-file rename plus a
one-line string update, and the verification above (reproduce-on-clean-
`main`, fix, re-verify on the same clean checkout) is the same
independent-evidence standard a review would apply, just performed by the
same session rather than a second one. No design judgement calls were
made — the fix is the error message's own literal instruction, and the
"which file keeps 22" choice follows directly from `main`'s actual merge
order, not a preference.

## Deferred / not in scope

- ut-docs#2037 — a dedicated pre-merge CI guard for migration-number
  collisions (catching this class of bug with one clear message before
  `main` goes red, rather than relying on `loadMigrations()`'s runtime
  check surfacing as 30 unrelated-looking test failures). Filed as a
  separate follow-up, not a blocking requirement for this hotfix.

## Verdict

Safe to merge — restores `main` (and every PR building against it) to
green.

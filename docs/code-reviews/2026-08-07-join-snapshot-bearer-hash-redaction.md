# Code review: redact bearer_hash from the join-snapshot served to a replica

**Card:** universaltill/ut-docs#426
**Branch:** `fix-join-snapshot-bearer-hash-redaction`
**Complexity:** hard → built by a Fable subagent, reviewed by a fresh-context
Opus subagent (deliberately not Fable — a model reviewing its own work
shares its blind spots), per the model-routing rules.

## What shipped

`GET /api/sync/snapshot` (`internal/pages/sync_api.go`) serves a full-DB
`VACUUM INTO` copy of the primary to a joining replica (the enrolment flow,
ut-docs#368). That copy's `tills` table carried **every enrolled till's real
`bearer_hash`** — the sync-auth secret — not just the joining till's own, so
a fresh replica held every sibling's secret from the moment of join, before
the existing ~30s incremental admin-bundle redaction
(`internal/data/sync_admin_repo.go`'s `redactCols`, ut-docs#405) ever runs.

Fix: a new `internal/db.RedactedJoinSnapshot(db, dbPath)` takes the existing
`Snapshot()` copy, copies it AGAIN to a throwaway file (named without the
`unitill-pos-` backup prefix, so it's invisible to `ListBackups()` /
`ValidBackupName()` and can never be restored from the settings page),
redacts `bearer_hash` to NULL on that throwaway copy only, and returns its
path plus a `cleanup()` the handler defers. `db.Snapshot()` itself,
`internal/pages/backup_api.go` (manual "Download backup"), and
`internal/server/server.go` (scheduled backup) are all **unchanged** —
those are genuine disaster-recovery paths that must keep the real
`bearer_hash` so a restored shop gets its real till roster back.

Regression coverage: `internal/db/join_snapshot_test.go` (new — redaction
correctness, real-snapshot/live-DB non-mutation, backups-list invisibility,
cleanup), `internal/pages/sync_api_test.go` (a new focused
`TestSyncSnapshot_RedactsOtherTillsBearerHash`, plus a staged-restore
assertion added to the existing `TestSyncJoin_FullFlow_StagesRestoreAndIdentity`),
and a one-line schema fix to the shared `seedForPages` test fixture
(`internal/pages/ui_smoke_test.go`) — it still declared the pre-migration-030
`bearer_hash TEXT NOT NULL UNIQUE`, which would have rejected the redaction
UPDATE with a constraint error that production's actual (nullable-since-030)
schema doesn't have.

## TDD verification (personally re-verified twice, not taken on trust)

- Dev's own claim: both new/extended tests fail pre-fix, pass post-fix.
- Independently re-verified by me (Reviewer) before delegating review:
  reverted `sync_api.go`'s handler back to plain `db.Snapshot(...)`, reran
  `TestSyncSnapshot_RedactsOtherTillsBearerHash` and
  `TestSyncJoin_FullFlow_StagesRestoreAndIdentity` — both failed with the
  real assertion messages ("served snapshot leaks 2 real bearer_hash
  value(s)...", "staged restore leaks 1 real bearer_hash value(s)..."),
  restored the fix, both passed again.
- Independently re-verified a **third** time by the Opus review subagent,
  same revert/re-run/restore pattern, same failure messages quoted, working
  tree left in the fixed state.

## Independent review findings

Spawned an independent Opus subagent (general-purpose, `model: opus`,
deliberately not Fable) with the diff, the relevant `CLAUDE.md` rules, and
an instruction to actually run build/vet/tests and hunt for real problems.

**One BLOCKING finding, fixed in this cycle:**

- `internal/db/join_snapshot.go` — the original `UPDATE tills SET
  bearer_hash = NULL` only redacts what SQL *reports*. SQLite doesn't zero
  the bytes a shrunk record frees, so the real `bearer_hash` strings stayed
  physically present in the served `.db` file's page slack — recoverable by
  a raw `strings`/`grep`/hexdump scan of the file the replica actually
  receives, which is exactly the leak this card exists to close. The
  reviewer verified this empirically (a throwaway probe: 3/4 seeded hashes
  survived in the raw bytes despite every row reading back NULL over SQL)
  and confirmed two working fixes (`VACUUM` after the UPDATE, or opening the
  copy with `_pragma=secure_delete(1)` in the DSN).
  **Fix applied:** the redact connection now opens with
  `file:<copy>?_pragma=secure_delete(1)` (the DSN form, not an `Exec`'d
  `PRAGMA` — `internal/db/db.go`'s own comment already documents that an
  `Exec`'d pragma binds only the one pooled connection that ran it, so the
  DSN form is the correct, already-established pattern in this package, not
  a new one). Added `TestRedactedJoinSnapshot_RealHashesNotRecoverableInRawBytes`,
  which scans the served copy's raw bytes for the seeded hash strings — the
  class of leak the SQL-level assertions in the other tests are blind to by
  construction. TDD-verified myself: reverted the pragma, confirmed the new
  test fails (`real bearer_hash "...668899aabb" recoverable in the served
  file's raw bytes`), restored it, confirmed it (and the full
  `TestRedactedJoinSnapshot*` suite) pass.

**Everything else checked out** — no resource leaks (traced every close/
cleanup path in `RedactedJoinSnapshot`), no `defer cleanup()` race against
`http.ServeFile` (fully synchronous, file closed before the handler
returns), no missing `os.MkdirAll` (`BackupDir` already does it), no
cwd-relative path (temp file lives inside `BackupDir(dbPath)`), the
`unitill-pos-` prefix precedent verified by reading `ListBackups`/
`ValidBackupName` directly, the `ui_smoke_test.go` fixture change verified
to weaken no other test, and confirmed `last_seen_at` is correctly out of
scope (non-secret, already redacted by the existing ~30s incremental pull;
the card's acceptance criteria is bearer_hash-only).

**Non-blocking, accepted/deferred (not fixed in this cycle):**

- Orphaned `join-snapshot-*.db` copies aren't reaped if the process dies
  between creating one and calling `cleanup()` — `PruneBackups` only looks
  at `unitill-pos-`-prefixed files. Real but low-frequency (a crash in a
  ~10-line window); worth a best-effort startup sweep. **Filed as
  ut-docs#436** rather than expanding this card's scope.
- `cleanup` is `nil` on every error path — correct at the sole call site
  (checked before use), but a no-op func instead of `nil` would make the
  API harder to misuse from a future caller. Left as-is; small enough to
  fold into whatever touches this file next rather than a card of its own.
- The pre-existing `Snapshot()` same-second-reuse race (two joins, or a
  join plus a manual "Download backup", within the same UTC second) is
  unchanged by this card — not introduced here, and arguably slightly
  improved (a half-written reused file now fails the redaction step with a
  500 instead of streaming a corrupt DB to a replica).
- `internal/data/small_repos_test.go:91` carries the same pre-migration-030
  `bearer_hash TEXT NOT NULL UNIQUE` staleness this card fixed in
  `ui_smoke_test.go` — not required by this change (nothing there NULLs the
  column), left for whoever next touches that fixture.

## Verified beyond automated tests

- No UI/runtime-facing surface touched — pure backend (a machine-to-machine
  sync endpoint's response bytes + a new internal/db helper), no template
  changes, no new user-facing strings. Confirmed via `git diff` (zero
  `.html` touches) — no manual/help-topic update owed, per
  `universal-till/CLAUDE.md`'s manual-ships-with-the-feature rule (it only
  applies to what a shop owner sees or does).
- No real client/shop name in any test data or comment (`Caller Till`,
  `Sibling Till`, `till-1`/`till-2`, `Corner Shop` only).
- Full gate run once, after all fixes were finished: `go build ./...`,
  `go vet ./...`, `gofmt -l` all clean; `go test ./...` green except the
  pre-existing, unrelated `internal/issuereport`
  `TestSaveCleansUpDirectoryOnWriteFailure` flake (root-user-only,
  independently confirmed pre-existing on unmodified `main` by prior
  cycles); `guard-data-access.sh` and `guard-i18n.sh` both pass — the new
  raw SQL lives in `internal/db`, the same package `Snapshot()`'s own
  `VACUUM INTO` already lives in, consistent existing precedent.

## Deferred / out of scope

- ut-docs#436 (new, filed this cycle): sweep orphaned `join-snapshot-*.db`
  files at startup.
- `internal/data/small_repos_test.go`'s stale pre-030 fixture: not required
  by this change; left for a future touch of that file.

---
_Generated by [Claude Code](https://claude.ai/code)_

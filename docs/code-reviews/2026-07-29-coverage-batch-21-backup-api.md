# Test coverage batch 21: local backup/restore admin API

2026-07-29

`internal/pages/backup_api.go` — manager-gated local backup tooling:
create a real VACUUM-INTO snapshot, download it, save a copy to the
OS user's Downloads folder (for the desktop app's WebView, which can't
handle HTTP attachment downloads), and stage a restore (applied on next
process start, requires typing "RESTORE"). Previously zero coverage.

The backup *mechanics* (`Snapshot`, `ListBackups`, `ValidBackupName`,
`PruneBackups`, `StageRestore`, `PendingRestore`) are already covered
at the `internal/db` layer (`internal/db/backup_test.go`). Unlike most
other page-test batches this session, this one operates on a REAL
on-disk sqlite database via `db.Open` (the actual production
`internal/db` package) rather than the usual in-memory `seedForPages`
fixture — necessary, not optional, since `Snapshot`/`ListBackups`/
`StageRestore` all key off the DB's file path on disk, not just the
`*sql.DB` handle.

## What's covered

- All 4 endpoints refuse with 403 when not a manager.
- `backup/now`: a real snapshot file appears on disk, `HX-Refresh:
  true` is set, and a `backup_created` audit row is written.
- `listBackupsForUI` (the page-local formatter, called from
  `settings_page.go`): `.db` suffix, "X.X MB" size format, non-empty
  date, exercised against a real snapshot.
- `backup/download/{name}`: a path-traversal-shaped name (URL-encoded
  `../../etc/passwd`) rejected with 400, a well-formed but
  non-existent name → 404, and a real download with the correct
  `Content-Disposition` header and real file bytes in the body.
- `backup/save-copy/{name}`: path-traversal rejected; a real snapshot
  really gets copied to disk, verified by redirecting
  `os.UserHomeDir()` via `t.Setenv("HOME", …)` into a test-controlled
  directory (never touches the real developer's Downloads folder).
- `backup/restore`: wrong confirmation → 400, `PendingRestore` stays
  false; a lowercase `confirm=restore` (case-insensitive per the
  handler's `strings.ToUpper`) → 200, `PendingRestore` becomes true,
  and a `restore_staged` audit row is written.
- `copyBackupTo` — the helper explicitly split out "so it's testable
  without writing to the real Downloads folder" per its own doc
  comment — now has a direct unit test independent of the HTTP layer.

## Independent review (opus) — verified empirically, two cheap adds applied

The review specifically probed whether the path-traversal test could be
passing for the wrong reason (Go's mux redirecting/404ing on the `%2F`
before ever reaching the handler, rather than `ValidBackupName`
actually rejecting it). It built the same mux and traced the request:
`%2F` survives mux routing (no clean-path redirect), `r.PathValue`
decodes it to `../../etc/passwd`, and `ValidBackupName` rejects it via
its real `filepath.Base(name) != name` check — confirmed the test
exercises the actual guard, not a routing accident.

Also independently confirmed: `t.Setenv("HOME", …)` is honored by
`os.UserHomeDir()` on the platforms this runs on, no test uses
`t.Parallel()` (consistent with using `t.Setenv`, which panics under
parallel execution), and the diff-based `createRealSnapshot` helper has
no ordering race since every test uses its own fresh temp DB and empty
backup directory.

Two cheap gaps closed: the restore path's `restore_staged` audit row
(the destructive action) was previously unasserted — only
`backup_created` was checked; added. And `copyBackupTo` — despite
existing specifically to be independently testable — had no direct
test, only indirect coverage through the HTTP save-copy path; added.
A one-line comment was added to `createRealSnapshot` noting `db.Snapshot`
dedups within the same wall-clock second (a footgun for any future test
that calls it twice in quick succession, though none currently does).

## Verification

`go build ./...`, `go test ./...`, `scripts/ci/guard-data-access.sh`,
`scripts/ci/guard-i18n.sh` — all pass.

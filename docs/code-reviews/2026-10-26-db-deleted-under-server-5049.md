# Review: till server keeps serving after its data folder is deleted (ut-docs#5049)

Scope: `internal/db/db.go`, new `internal/db/watch.go`, `internal/app/app.go` (watchdog wiring),
`packaging/macos/uninstall-unitill.command`, the DMG uninstaller heredoc in `packaging/macos/make-dmg.sh`,
`packaging/linux/uninstall-unitill.sh`. Tests: `internal/db/deleted_file_test.go`, `internal/db/watch_test.go`.

## What shipped
1. `openRaw` creates the database file itself and opens the pool with `mode=rw` (not for `:memory:`). The pool can
   no longer create a database: after the file is unlinked, a new connection fails with "unable to open database
   file" instead of silently opening an empty, table-less database (the `no such table: settings` flood).
2. `db.FileWatch` re-stats the database and `.unitill.lock` every 30 s and compares them with `os.SameFile`. A
   deleted or replaced file stops the server (`stopBg`) and `app.Run` returns a clear error naming the file. A
   transient stat error (EACCES, EIO, stale mount) stops it only after 3 failed checks in a row. Building the watch
   is never fatal.
3. The macOS (folder + DMG) and portable-Linux uninstallers quit the app, `pkill` this user's `unitill-pos`, wait up
   to 15 s, then `-9` and 5 s more, and refuse to delete anything while it still runs.

## Independent review (Opus subagent; code built by a Sonnet subagent)
Verdict: safe to merge. Checked and ruled out: `mode=rw`/pre-create breaking any `Open` caller (every "does the DB
exist" check — legacy-data migration, pending restore, backup assets — runs before `Open`); any runtime path that
renames or replaces the DB or lock file (restore and join only stage files applied at boot; `Release` never unlinks
the lock), so the watchdog has no false positive from the till's own operations; data race on the stored error;
goroutine/wg ordering; heredoc quoting.

Findings, all fixed in this branch:
- minor — Windows: `os.Stat` loads the file identity lazily, so the first check compared the replacement with
  itself. Fixed: `os.SameFile(fi, fi)` right after each stat in `NewFileWatch`.
- minor — any stat error stopped a healthy till. Fixed: only not-exist/mismatch stop at once; other errors after 3
  consecutive checks (`TestFileWatch_RunToleratesTransientStatErrors`, `TestFileWatch_CheckNotExistIsNotTransient`).
- minor — `pgrep`/`pkill` matched other users' processes (a `.deb` service under user `pos` made the portable
  uninstaller refuse). Fixed: `-u "$(id -u)"`.
- nit — no-op branch in an injected-stat test closure and an ignored error in a test. Fixed.

Accepted:
- No `internal/app` test of the wiring: booting `Run` is not feasible in a unit test; the watch logic is covered in
  `internal/db`, the wiring by build/vet and the existing `internal/app` suite.
- Under a supervisor that restarts on exit, the restart after a deliberate folder delete creates a fresh, empty
  till — the same as the old code's next boot.

## TDD
`TestOpen_DeletedFileDoesNotRecreatePhantomDB` re-verified by the reviewer in an isolated worktree: with `db.go`
reverted it fails with `a phantom empty database was created: SQL logic error: no such table: settings (1)`;
restored, it passes.

## Verification
`gofmt -l .` empty, `go build ./...`, `go vet`, `GOOS=windows go vet ./internal/db/`, `golangci-lint` 0 issues,
shellcheck clean on the three scripts and on the extracted DMG heredoc, every guard in `ci.yml`'s `build` job
(local-only failures: shellcheck 0.11 vs CI's pinned 0.9, and the winres step needing `RUNNER_TEMP`), full
`go test ./...`. The uninstall abort path was checked by hand on the Linux script with stubbed `pgrep`/`pkill`;
not run on a real Mac.

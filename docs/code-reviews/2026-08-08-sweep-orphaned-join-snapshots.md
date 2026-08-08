# Code review: sweep orphaned join-snapshot files at startup

**Card:** universaltill/ut-docs#436
**Date:** 2026-08-08
**Complexity:** easy — Dev at Sonnet (inline), Review at a fresh-context Sonnet subagent, per this pipeline's model-routing rule.

## What shipped

`internal/db.RedactedJoinSnapshot` (ut-docs#426) creates a throwaway,
bearer_hash-redacted `join-snapshot-*.db` copy in the backup directory while
serving a joining replica, then calls its own `cleanup()` closure once served.
If the process crashes or is killed in the narrow window between
`os.CreateTemp` and `cleanup()` running, the copy is orphaned — nothing else
reaps it, since `ListBackups`/`PruneBackups`/`ValidBackupName` deliberately
only match the `unitill-pos-` prefix (so a join-snapshot copy stays invisible
to the settings-page backups list).

This change adds:

- `internal/db.SweepOrphanedJoinSnapshots(dbPath string) (int, error)` —
  removes `join-snapshot-*.db` files (and `-wal`/`-shm` sidecars) from the
  backup directory that are at least `joinSnapshotOrphanAge` (5 minutes) old.
  A real request completes in well under a second, so 5 minutes is generous
  headroom against ever touching a copy genuinely mid-flight. Real backups
  (`unitill-pos-*.db`) are a disjoint namespace and are never touched.
  Per-file removal errors are swallowed (same best-effort spirit as
  `RedactedJoinSnapshot`'s own `cleanup()`); only a directory-list failure is
  returned.
- A call site in `internal/app.Run()`, right after `db.ApplyPendingRestore`
  and before `db.Open` — a pure filesystem operation needing no live DB
  connection, same shape as `ApplyPendingRestore`. Logged via
  `log.Warnf`/`log.Infof`, **never** returned as a fatal error — startup must
  never block on housekeeping (offline-first).
- Three regression tests in `internal/db/join_snapshot_test.go`:
  `TestSweepOrphanedJoinSnapshots_RemovesStaleOrphan`,
  `TestSweepOrphanedJoinSnapshots_LeavesFreshCopyAlone`,
  `TestSweepOrphanedJoinSnapshots_NeverTouchesRealBackups`.

## Independent review

Run as a fresh-context Sonnet subagent (complexity:easy → same-tier review,
different instance, per the scrum-master skill's model routing) in an
isolated git worktree. It read the diff cold, ran the build/vet/test/guard
gate itself, and reported:

- **Verdict: safe to merge**, one should-fix, one accepted nit.
- **Should-fix (fixed):** `removed` was incremented unconditionally after
  `os.Remove(path)`, whose error was discarded — so a failed primary-file
  removal (permission denied, read-only volume) would still be counted and
  logged as "removed," making the boot log line inaccurate. Fixed: `removed`
  now only increments when the primary `.db` removal actually succeeds;
  sidecar (`-wal`/`-shm`) removal errors stay swallowed, matching
  `RedactedJoinSnapshot`'s own `cleanup()`. A file that fails to remove is
  retried on the next boot's sweep since its mtime is unchanged — nothing
  gets permanently stuck either way, this was a diagnostics-accuracy fix,
  not a correctness bug.
- **Accepted nit (no change):** `TestSweepOrphanedJoinSnapshots_LeavesFreshCopyAlone`,
  taken in isolation, would also pass against a no-op sweep — it only proves
  age-gating in combination with `TestSweepOrphanedJoinSnapshots_RemovesStaleOrphan`.
  The pair together does prove it; noted for anyone touching either test
  independently later.
- Confirmed: glob match is exclusive to `RedactedJoinSnapshot`'s own
  `os.CreateTemp(dir, "join-snapshot-*.db")` naming, disjoint from
  `backupPrefix` (`unitill-pos-`) and `ApplyPendingRestore`'s
  `pre-restore-*.db`; `BackupDir` already `os.MkdirAll`s so no missing-dir
  bug; no cwd-relative path shortcut (goes through `BackupDir(dbPath)` like
  every other backup-dir consumer); boot placement is sequential with no
  concurrent writer into the backup dir at that point; no SQL, no UI string,
  no network — all standing repo rules N/A as expected for a pure
  filesystem-hygiene fix.
- Ran `go build`, `go vet`, `gofmt -l`, the targeted tests with `-race`, the
  full `internal/db`/`internal/app` package suites, and both
  `guard-data-access.sh`/`guard-kiosk-engine.sh` itself — all clean.

## Verified beyond automated tests (Reviewer, personally)

- **TDD re-verification**: reverted `internal/db/join_snapshot.go` to its
  pre-fix state (the commit before this branch), confirmed the three new
  tests fail to even compile (`undefined: SweepOrphanedJoinSnapshots` — the
  clearest possible proof they exercise genuinely new behavior, not a
  tautology), then restored the fix and confirmed all three pass again.
  Done atomically in one shell sequence (no turn boundary between revert and
  restore, so no risk of a stop-hook-forced commit capturing the reverted
  state — ut-docs#386).
- Re-ran the full gate after applying the reviewer's should-fix:
  `go build ./...`, `go test ./internal/db/... ./internal/app/... -race`,
  `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-kiosk-engine.sh`
  — all clean.
- Full repo gate (`go test ./... -race`, both guards) was already run clean
  once, before the reviewer's should-fix, covering every other package; the
  should-fix touched only `internal/db/join_snapshot.go`'s counting logic,
  so the targeted re-run above is the correct scope per this pipeline's
  "finish editing, then gate" sequencing rule — not a full second full-repo
  run.

## Non-goals / deferred (per BA/Architect scoping, unchanged)

- No periodic re-sweep at runtime — the crash window is narrow and every
  successful join already cleans up via `cleanup()`; one pass at boot is
  sufficient.
- No settings-page/UI change — join-snapshot copies are deliberately
  invisible to the operator (ut-docs#426) and stay that way.
- No manual/help-topic update — nothing a shop owner sees or does changed;
  this is invisible backend housekeeping.

## Safe-to-merge verdict

Yes. No blockers. Build/vet/test/race/guards all clean; independent review
found one real (cosmetic) issue, which is fixed and re-verified; TDD claim
personally re-verified fail→pass.

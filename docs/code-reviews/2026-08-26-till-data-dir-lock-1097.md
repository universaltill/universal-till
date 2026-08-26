# Code review: exclusive data-directory lock (ut-docs#1097)

**Branch:** `fix/1097-data-dir-lock`
**Author:** autonomous SDLC pipeline (Dev: Sonnet subagent; Review: Opus
subagent, independent, isolated worktree, different model from the author)
**Scope shipped here:** the server-side half of ut-docs#1097 only — a
process-level exclusive lock on the till's data directory. The issue's two
other acceptance criteria (desktop-shell retry probe, no silent port
fallback) are **deliberately NOT in this branch**; see "Deferred" below.
`#1097` therefore stays **open**.

## The incident this closes

`cmd/unitill-desktop` decides whether a till is already running with a
single 1.5s `/healthz` probe of `127.0.0.1:8080` (`tillAlreadyRunning`,
`desktop.go:69`). A restart window, a slow boot, or a shell launched a beat
too early all read as "nothing is listening", so the shell spawned a second
`unitill-pos` against the **same SQLite data directory** as the
systemd-managed service. The duplicate grabbed `:8080`; the real service
lost the bind and slid to `:8081` via `internal/server.listenWithFallback`.
Two live writers on one database for ~19 minutes, announced only by one
`log.Printf` line.

A port probe answers "is something listening on this port". It can never
answer "does something already own this data" — which is the question that
decides whether it is safe to start.

## What shipped

- **`internal/db/lock.go`** — `AcquireDataDirLock(dbPath)` opens/creates
  `.unitill.lock` beside the database file and takes a non-blocking
  **exclusive OS advisory lock** on it, returning the sentinel
  `ErrDataDirLocked` when another process already holds it.
  `(*DataDirLock).Release()` closes the handle, which is what actually
  drops the OS lock. Not a PID file: an OS lock is released by the kernel
  the instant the holder dies, cleanly or by `SIGKILL`, with no staleness
  or PID-reuse heuristic to get wrong in exactly the crash case that
  matters most.
- **`internal/db/lock_unix.go`** (`linux || darwin`) — `unix.Flock(fd,
  LOCK_EX|LOCK_NB)`; **`lock_windows.go`** — `LockFileEx(...,
  LOCKFILE_EXCLUSIVE_LOCK|LOCKFILE_FAIL_IMMEDIATELY, ...)`;
  **`lock_other.go`** — compile-anywhere no-op stub for GOOS values this
  module does not ship to.
- **`internal/app/app.go`** — `Run` acquires the lock immediately after
  `config.Init()` and **before** `paths.MigrateLegacyData`,
  `db.ApplyPendingRestore`, `db.SweepOrphanedJoinSnapshots` and `db.Open`,
  refusing to boot with a message a human can act on. `defer
  dataDirLock.Release()` is registered before `defer database.Close()`, so
  LIFO ordering releases the lock strictly **after** the database closes.
- **`go.mod`** — `golang.org/x/sys` reclassified indirect → direct.
- Tests: 4 in `internal/db/lock_test.go`, 2 in
  `internal/app/data_dir_lock_test.go`.

## TDD claim re-verified personally

Not taken on faith. I neutralised **only the wiring** in `internal/app/app.go`
(the `AcquireDataDirLock` call and its `defer Release()`), leaving
`internal/db/lock.go` fully intact, and re-ran
`TestRun_RefusesSecondInstanceAgainstSameDataDirectory`:

```
--- FAIL: TestRun_RefusesSecondInstanceAgainstSameDataDirectory (30.02s)
    Run did not return within 30s against an already-locked data directory
```

The log from that failing run shows exactly the ut-docs#1097 incident
reproduced in miniature — the unwired `Run` booted a **second** server
against the already-locked directory and reported `port 127.0.0.1:0 was
busy — listening on 127.0.0.1:42527 instead`. Restoring the wiring turned
it green again. The test genuinely tests the wiring.

## Findings

### F1 — `lock_other.go`'s rationale comment was factually wrong (medium, fixed)

The file claimed it was the implementation android and ios compile, and used
"mobile runs everything in one process" as the *justification* for the
no-op. That is not what the build constraints do. Go treats `GOOS=android`
as also satisfying the `linux` tag and `GOOS=ios` as also satisfying
`darwin`, so **both mobile targets compile `lock_unix.go` and get the real
flock**. Verified, not assumed:

```
$ GOOS=android go list -f '{{.GoFiles}}' ./internal/db | tr ' ' '\n' | grep lock
lock.go
lock_unix.go
$ GOOS=ios ...      → lock.go lock_unix.go
$ GOOS=windows ...  → lock.go lock_windows.go
$ GOOS=freebsd ...  → lock.go lock_other.go
```

Behaviourally the *outcome* is fine — better than the comment claimed — and
I checked that the real lock does not misfire on mobile: `mobile.Stop()`
blocks until `app.Run` has fully returned (its deferred `Release` included)
before a later `Start` can run, and `Start` is idempotent while a server is
genuinely up, so a background/foreground cycle cannot spuriously self-refuse.
But a load-bearing comment that misstates which file a platform builds is
the kind of thing a future change reasons from and gets wrong. Comment
rewritten to state what actually happens, why the real lock on mobile is
correct rather than merely harmless, and the one-line command to re-verify.

### F2 — `TestRun_AcquiresLockAndStillBootsNormally` synchronised on a fixed sleep (medium, fixed)

The happy-path test did `time.Sleep(500 * time.Millisecond)` and then
cancelled, commenting "give it a moment to reach a real listen bind". It has
no way to know it did. This repo already solved that exact problem: the
`pagesInit` seam in `app.go` exists so a test can observe boot reaching
`pages.Init` deterministically, and `TestRun_WaitsForAsyncWorkBeforeClosing
Database` in `app_test.go` uses it with a comment explicitly saying why
("safe to trigger shutdown now, deterministically, regardless of how long
boot took to get here"). A 500ms guess on a loaded CI runner is a latent
flake, and cancelling mid-boot risks `plugins.Init(ctx, …)` returning
`ctx.Canceled` and failing the test for an unrelated reason.

Both tests now use a shared `observePagesInit(t)` helper over the same seam.
This also **strengthened the refusal test**: instead of asserting "returned
in under 5 seconds" (a wall-clock proxy for "stopped early"), it now asserts
`pages.Init` was **never reached** — a direct observation of where `Run`
stopped. Added a closing assertion to the happy-path test that the lock is
genuinely free after `Run` returns, so a `Release` regression can never pass
as "boots fine".

### F3 — `provision.go`'s lock-free path was correct but undocumented (medium, fixed)

`internal/app/provision.go` (`provision-desktop-kiosk-defaults`) calls
`db.Open(cfg.DBPath)` on the same data directory and does **not** take the
lock. I traced the real install sequencing rather than trusting the doc
comment: `packaging/scripts/postinstall.sh` runs `systemctl restart
unitill-pos.service` at **line 73**, and only invokes the provisioning
subcommand at **line 213**. So on a real `.deb` install the service is
already up and already holds the lock when provisioning runs.

Current behaviour is therefore **correct as shipped** — had the author
"consistently" locked here too, every desktop-kiosk-overlay install would
have failed. But nothing said so, and the asymmetry reads as an oversight
someone will helpfully "fix". Added a comment recording the sequencing, why
a short single-transaction settings write is not what the lock guards
against, and the condition under which the reasoning stops holding.

### F4 — Lock scope and `UT_DB_PATH` (low, documented, no code change)

The lock keys on `filepath.Dir(dbPath)`, not `paths.DataDir()`. Under
`UT_DB_PATH` (used by the e2e CI job) those differ. I checked this against
the package's own conventions before treating it as a defect: `backup.go`,
`replica.go` and `db.go` **all** key their per-till artefacts off
`filepath.Dir(dbPath)`. Keying the lock the same way is internally
consistent and follows the database — the resource whose concurrent writers
caused the incident — rather than a directory that under an override may
hold neither the DB nor its backups. Left as-is; the reasoning is now in the
`dataDirLockName` doc comment so the next reader does not have to redo it.

### F5 — `Release()` deliberately does not unlink (low, documented, no code change)

Correct as written, and worth stating: deleting the lock file would create
the one race an OS lock otherwise avoids (a holder locking an unlinked inode
while a newcomer locks a freshly created file — both succeed). Reasoning
added to `Release`'s doc comment.

### F6 — The stated reason for deferring criterion 2 does not hold (medium, NOT fixed, raised)

The deferral was justified by the GTK3 build gap (ut-docs#1071). I checked
that claim rather than repeating it — and it is **half right**.

*Confirmed:* GTK3/WebKit headers are genuinely absent here.
`pkg-config --exists gtk-3.0` → exit 1; `go build -tags desktop
./cmd/unitill-desktop` → `Package 'gtk+-3.0' … not found`. Criterion 1 (the
shell's retry probe, `cmd/unitill-desktop/desktop.go`) is genuinely
unbuildable and untestable in this sandbox, so deferring it is right.

*Not confirmed:* criterion 2 — "the primary service never silently falls
back to another port" — lives in **`internal/server/server.go`**
(`listenWithFallback`, lines 263–270/343–369), plain Go with no build tag
and no GTK anywhere near it. It builds and tests fine here.
`internal/server/listen_test.go` already covers it. The GTK gap is simply
not the reason that one is missing.

I chose not to fix it in this review pass, and that is a scoping judgement
rather than an oversight: `listenWithFallback` has three live consumers with
different needs (`mobile.Start` picks a free port then binds later and
`mobile.go:218` explicitly names the fallback as its accepted race
tolerance; the desktop shell's own `freePort` comment relies on it; a
genuinely-different second till on one machine is the case it was written
for). Changing the bind contract is a design decision with its own tests and
its own issue, not something a reviewer should slip into a lock PR. But the
*stated* justification for its absence was wrong and is corrected here and
in the PR body.

Note also that this PR meaningfully defuses criterion 2 without touching it:
with the lock in place, the fallback can no longer be reached by a duplicate
on the *same data*, which is the only variant that caused harm.

### F7 — Merged-state behaviour change worth stating plainly (medium, NOT fixed, raised)

In the exact incident timing, the loser of the race is now whichever process
comes **second**, and that can be the systemd service: if the desktop shell
wins the restart window, its child takes the lock and the real service now
**refuses to start** (systemd will retry) instead of quietly running on
`:8081`. Separately, if the shell's child dies instantly on
`ErrDataDirLocked`, `desktop.go`'s 10s dial loop (lines 163–170) still opens
a WebView at a dead address — it never checks whether the child exited.

This is the correct trade — a refusal that says why beats two silent writers
on one database — but it is a real change in failure shape, and it is
precisely what the deferred shell work must finish. It strengthens the case
for keeping `#1097` open rather than closing it here.

### F8 — `go.mod` diff (clean)

`git diff origin/main -- go.mod go.sum`: `golang.org/x/sys` moved indirect →
direct, nothing else, `go.sum` untouched. Correct — `lock_unix.go` and
`lock_windows.go` import it directly. Verified there is no `vendor/` tree to
update. `go mod tidy` additionally wants to promote `golang.org/x/net`
(directly imported by `internal/pages/elevation_test.go`), but that drift is
**pre-existing on `main`** and unrelated; deliberately left out to keep this
diff minimal.

### F9 — Secrets / PII (clean)

Scanned the diff for credentials, keys, emails, hostnames and real
shop/client names. Only `127.0.0.1` loopback addresses in tests. Nothing to
report.

### Checked and found nothing wrong

- **Can two processes both succeed?** No. `flock(LOCK_EX|LOCK_NB)` is
  per-open-file-description, so a second `OpenFile`+`Flock` fails with
  `EWOULDBLOCK` even inside the *same* process. Verified with two real OS
  processes, not just unit tests (below).
- **Is the lock path unique per data directory?** Yes —
  `filepath.Join(filepath.Dir(dbPath), ".unitill.lock")`, never a global
  path. `TestAcquireDataDirLock_DifferentDirectoriesDoNotContend` covers it.
- **Does `Release()` really release the OS lock?** Yes — closing the fd
  drops both `flock` and `LockFileEx`. Verified live by restarting.
- **TOCTOU in acquire?** No. `MkdirAll` → `OpenFile` → `Flock`: a racing
  process interleaving anywhere in that window still contends on the
  `Flock`, which is the only step that confers ownership.
- **Missing `os.MkdirAll` (this pipeline's recurring bug class)?** Not
  present — `AcquireDataDirLock` creates the directory itself and
  `TestAcquireDataDirLock_CreatesMissingDataDirectory` covers a fresh
  install. It runs before `db.Open`, which does its own `MkdirAll`, so the
  ordering could not have relied on that.
- **Anything touching the data directory before the lock?** Walked `Run`
  line by line. `godotenv.Load` reads `pos.env` (not the data dir);
  `logging.Init` writes to stdout; `config.Init` resolves paths without
  creating or writing them. All four data-directory operations
  (`MigrateLegacyData`, `ApplyPendingRestore`, `SweepOrphanedJoinSnapshots`,
  `Open`) are after the lock, in that order. `main.go` has exactly two entry
  points, `app.Run` and `ProvisionDesktopKioskDefaults` (F3).
- **Does anything delete `.unitill.lock`?** No. `SweepOrphanedJoinSnapshots`
  operates only inside `backups/` and only matches `join-snapshot-*.db`;
  nothing else removes files at the data-directory root.
- **`EINTR` on `flock`?** Considered and deliberately not "fixed". Go
  installs its signal handlers with `SA_RESTART` and `flock(2)` is
  kernel-restartable, so `EINTR` cannot realistically surface; adding an
  untestable retry loop would be noise. Recording it so the next reviewer
  need not re-derive it.
- **i18n:** the refusal text is a Go `error` surfaced on stderr/journal at
  boot, not a template string or a Go-side menu label. `guard-i18n.sh`
  passes. No `web/help/` topic is affected (no screen, no user action
  added), and `guard-help-topics.sh` passes. No README claim goes stale.

## Verification performed

Everything below was run by me in an isolated worktree, not taken from the
author's report.

- `gofmt -l .` → clean · `go build ./...` → clean · `go vet ./...` → clean
- `go test ./internal/db/... ./internal/app/...` → **ok**
- Full suite (`go list ./...` minus `internal/plugins`, 57 packages,
  `-count=1`) → **ok**, no failures
- New tests run with `-count=5` for flake exposure → **ok**
- All 27 CI-blocking guards in `.github/workflows/ci.yml`'s `build` job,
  individually → **27/27 PASS**
- Cross-compile of all four lock variants: `GOOS=windows`, `GOOS=darwin`,
  `GOOS=freebsd` (the `lock_other.go` path), plus `GOOS=android go vet` →
  all clean
- **Beyond automated tests — two real OS processes against one data
  directory.** Built `unitill-pos` and ran it for real:
  - First instance boots, `listening on 127.0.0.1:18080`, `.unitill.lock`
    created containing `pid=<first>`.
  - Second instance, same `UT_DATA_DIR`, different port → **exit code 1**,
    `another Universal Till server is already running against <dir> —
    refusing to start a second instance against the same data
    (ut-docs#1097): data directory is already locked by another running
    instance`. It never bound a port and never opened the database.
  - First killed → third instance starts cleanly. Clean handover works.
  - **`SIGKILL` (crash, no defers run) on the holder** → the leftover
    `.unitill.lock` does **not** block the next start; the successor boots
    normally. This is the specific case a PID file gets wrong, and it is the
    reason the OS-lock design is right.

## Deferred (and why `#1097` must stay open)

Not in this branch:

1. `cmd/unitill-desktop` retrying its `tillAlreadyRunning` probe over a few
   seconds before deciding to spawn — genuinely blocked here by the missing
   GTK3/WebKit dev headers (ut-docs#1071), confirmed by me, not assumed.
2. The primary service never silently falling back to another port — **not**
   blocked by GTK (F6); deferred on scoping grounds, with reasons.

Two of `#1097`'s acceptance criteria are unmet, so this PR **must not close
it**. Recommendation: keep `#1097` open, re-scoped to the desktop-shell
retry probe plus the port-fallback contract, with F7's new failure shape
(systemd losing the race and restart-looping; the shell opening a WebView on
a child that already exited) written into it as concrete acceptance
criteria. The PR body says the same and carries no `Closes` line.

## Verdict

**Safe to merge.** The mechanism is correct, minimal, well-scoped and
verified with real processes including the crash path. Everything I found
was fixed in-branch; nothing outstanding is a defect in what shipped. The
merged state is a strict improvement on the incident — two writers on one
database become structurally impossible rather than merely unlikely — and
the remaining gap is stated plainly in the PR body rather than papered over
with a `Closes` line.

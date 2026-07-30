# Code review — `app.Run` joins its background services before returning

- **Date:** 2026-07-30
- **Branch/PR:** `fix/app-run-shutdown-join`
- **Author:** pipeline (sonnet)
- **Independent reviewer:** opus subagent (different model)
- **Scope:** `internal/app`, `internal/server`, `internal/updates`,
  `internal/alerts`, `internal/enroll`, `mobile/mobile_test.go`

## The bug

`internal/app/app.go`'s `Run(ctx)` starts several background goroutines —
`updates.Start`, `alerts.Start`, `enroll.Init`'s conditional registration
loop, and (via `server.Start`) a `BackgroundJobs` scheduler, a daily
local-DB-backup ticker, a related-items-rebuild ticker, and `server.Start`'s
own graceful-shutdown goroutine (`srv.Shutdown` → `supervisor.Shutdown`) —
all fire-and-forget. `Run` returned (running its deferred `database.Close()`)
without ever waiting for any of them to actually exit. A straggler could
still be reading/writing the DB, or writing sqlite `-wal`/`-shm` files in the
data directory, for a moment after `Run` had already returned and the DB
handle was closed.

Found via a `mobile/mobile_test.go` CI flake (main run 30531313594,
`TempDir RemoveAll cleanup: directory not empty`) — `PR #97` (merged
2026-07-30) diagnosed the real root cause correctly but deliberately only
mitigated the *test's* symptom (a manually-owned dir + 5s-retry `RemoveAll`),
leaving "drain before returning" as this queued follow-up. This PR is that
fix.

Also relevant and independently confirmed during review: `net/http`'s
`Server.Shutdown` docs guarantee `Serve` returns as soon as `Shutdown`
*begins* (listeners close), not after it fully completes — so
`server.Start`'s original shutdown goroutine could leave `supervisor.Shutdown`
still running after `server.Start` itself had already returned. A second,
related race this fix also had to close.

## What shipped

A `*sync.WaitGroup` threads from `app.Run` into `updates.Start`,
`alerts.Start`, `enroll.Init`, and `server.Start` (which threads it into
`BackgroundJobs.Start` and its own three internal goroutines, including the
shutdown goroutine). Every spawn site does `wg.Add(1)` synchronously before
the `go` statement and `defer wg.Done()` as the goroutine's first statement.

`app.Run` derives an independently-cancellable `bgCtx` from the caller's
`ctx` and passes `bgCtx` (not `ctx`) to every one of those calls; a single
deferred cleanup (`stopBg(); drainBackgroundServices(&wg, log,
backgroundDrainTimeout)`) runs on **every** return path from `Run`, not just
the normal caller-driven shutdown. `drainBackgroundServices` waits on the
group with a bounded, loudly-logged timeout (10s in production) before
giving up and returning anyway — a wedged service must not hang shutdown
forever.

`mobile/mobile_test.go`'s manually-owned-dir + 5s-retry `RemoveAll`
workaround from PR #97 is gone; plain `t.TempDir()` is safe again.

## Independent review — two real rounds

**Round 1 verdict: SAFE TO MERGE WITH FIXES.** The reviewer verified the core
`wg.Add`/`wg.Done` mechanics were race-correct (every `Add` before its `go`
statement, every `Done` deferred first-line) and that the Serve/Shutdown race
was genuinely closed on the *normal* cancel path — but found two real gaps by
actually running mutation probes, not just reading the diff:

- **F1/F2 (should-fix, correctness):** `Run`'s early error returns
  (`plugins.Init`, `marketplace.NewCatalogRepository`, and — proven with a
  temporary probe test — `server.Start`'s own listen failure) all bypassed
  the drain entirely, because at that point nothing had told the
  already-started background goroutines (e.g. enroll's registration loop) to
  stop. The reviewer's probe showed this meant a **guaranteed** 10s stall (a
  spurious "still running" error log) followed by closing the DB with
  goroutines still live — the exact bug this PR exists to fix, still present
  on every non-happy-path return.
- **F3 (should-fix, test-integrity):** all three `internal/server` join
  assertions were **vacuous** — one-directional ("wg drains within N
  seconds of cancel"), which an empty/never-`Add`-ed `WaitGroup` satisfies
  instantly. The reviewer proved this by deleting all six `wg.Add`/`wg.Done`
  pairs from `server.go` and showing the three tests still passed — meaning
  the package that owns the Serge/Shutdown-race fix (the whole point of this
  diff) had zero real test protection.
- **F4 (should-fix, comment accuracy):** the `mobile_test.go` comment
  overclaimed that a clean local run "proves the join works" — the reviewer
  showed 40 clean `-race -count=40` runs *with the fix removed*, since the
  original bug was a rare, timing-dependent flake, not a guaranteed
  reproduction.
- **F5 (should-fix, scope accuracy):** the `wg` doc comment's "every
  background goroutine" was not literally true — `internal/plugins.Supervisor`'s
  `monitorProcess` goroutines were already an acknowledged gap, but the
  reviewer also found the wasm runtime's per-plugin event-channel drainer
  (`internal/plugins/wasm_runtime.go`, `for ev := range ch`) has no
  shutdown-triggered exit at all (its channel only closes on the next
  `Sync`/reload) — independently confirmed by reading `ipc.go`'s
  `ResetSubscribers` and its only two call sites before accepting the claim.
- F6–F8: nitpicks (QUEUE.md wording, a slow test-failure path, a
  leaked-but-harmless goroutine on the timeout branch).

**All five should-fixes applied, re-verified with the reviewer's own method
(deliberately reintroduce each bug, confirm the specific test now fails with
the exact predicted message, restore the fix, confirm green again) before
this record was written — not taken on the reviewer's word:**

- F1/F2: `Run` now derives `bgCtx` and defers `stopBg()`+drain on every
  return path. New test `TestRun_JoinsBackgroundGoroutinesOnEarlyServerError`
  drives a real `Run()` (never cancelling its own ctx) into an unbindable
  `UT_LISTEN_ADDR`, with `enroll.Init`'s background loop genuinely live
  (config's default marketplace endpoint), and asserts `Run` returns in
  well under 3s. Reverted to the pre-fix shape → the test failed exactly as
  the reviewer's probe predicted: `Run took 10.145339385s ... apparently
  only stopped by the 10s drain timeout`. Restored → passes in 0.69s.
- F3: added the missing "must NOT return before cancel" half to all three
  `internal/server` join assertions. Reintroduced the reviewer's exact
  mutation (stripped all 6 `wg.Add`/`wg.Done` pairs) → all three tests now
  fail with the expected "not tracked" messages. Restored → green.
- F4: comment reworded to state plainly that a clean run is a regression
  canary, not proof by itself.
- F5: `app.go`'s doc comment now names both excluded gaps
  (`monitorProcess`, the wasm event drainer) and why each is excluded;
  logged as its own `ut-docs/QUEUE.md` follow-up rather than silently
  dropped.
- F7/F8 (nitpicks): `enroll`'s join test now calls
  `srv.CloseClientConnections()` ahead of `srv.Close()` so a regression
  fails fast instead of stalling ~15s; `drainBackgroundServices`'s doc notes
  the intentionally-leaked, harmless `wg.Wait()` goroutine on timeout.

## Verified beyond the reviewer's own probes

- `go build ./... && go vet ./...` clean.
- `go test -race` green across `internal/app`, `internal/server`,
  `internal/updates`, `internal/alerts`, `internal/enroll`, `mobile`.
- `go test ./mobile/... -race -count=15` clean (reviewer independently ran
  `-count=10` and `-count=40`, also clean).
- Full `go test ./...`: one failure, `internal/issuereport`'s
  `TestSaveCleansUpDirectoryOnWriteFailure` ("expected Save to fail on a
  read-only bundle directory") — confirmed pre-existing and unrelated by
  running it against unmodified `main` in this same container (root can
  write through a `0500` dir, so the permission-based test can't fail the
  way it expects here regardless of this diff). This branch does not touch
  `internal/issuereport`.
- `bash scripts/ci/guard-data-access.sh`, `guard-i18n.sh`,
  `guard-emoji-font.sh`, `guard-kiosk-launch-flags.sh`,
  `guard-webkit-version.sh` — all green (none of this diff's surface is
  relevant to the latter three, run anyway for completeness).
- Full Playwright e2e suite (`e2e/`, both projects, 20 specs): 19/20 green;
  the one failure (`catalog-image-to-till.spec.ts`, a thumbnail
  `naturalWidth`/`complete` image-decode timing assertion) reproduces
  identically against unmodified `main` in this container — pre-existing,
  unrelated to this diff (confirmed via `git stash`).
- No SQL/money/i18n/plugin-signing surface touched (`guard-data-access.sh`
  passing plus a manual diff read confirms); no new file writes anywhere in
  the diff, so neither of this pipeline's two recurring bug classes
  (missing `os.MkdirAll`, cwd-relative path instead of `paths.Data`) apply.
  No real client/shop name, no secret-shaped literal anywhere in the diff.

## Explicitly out of scope, logged as follow-ups (`ut-docs/QUEUE.md`)

- `internal/plugins.Supervisor`'s `monitorProcess` goroutines: `Shutdown`
  cancels the process context but doesn't join the corresponding
  `cmd.Wait()` goroutine. Architecturally separate (native plugin-process
  supervision, ADR-0001), a bigger diff on its own.
- The wasm runtime's per-plugin event-channel drainer
  (`internal/plugins/wasm_runtime.go`): its channel is only closed by the
  next `Sync`/reload, never by shutdown/ctx-cancel — confirmed by reading
  `ipc.go`. Registering it on the same `wg` today would make every drain
  time out, so it's correctly excluded rather than silently ignored.

## Verdict

**SAFE TO MERGE.** All should-fix findings applied and independently
re-verified via mutation (break it, confirm the specific test fails with the
predicted message, fix it, confirm green) rather than taken on either
party's word. Full gate green modulo two confirmed-pre-existing,
unrelated-to-this-diff environment issues.

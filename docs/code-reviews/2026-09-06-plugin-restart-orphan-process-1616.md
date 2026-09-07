# Code review: stop hardware-plugin processes before a self-exec restart

- **Card:** universaltill/ut-docs#1616
- **PR:** universaltill/universal-till (branch `fix/1616-plugin-restart-orphan-process`)
- **Complexity:** medium — build at Sonnet (inline), review at Opus (fresh-context subagent, isolated worktree)
- **Date:** 2026-09-06

## What shipped

`internal/plugins.Supervisor` starts hardware-plugin child processes
(printer/scale/cash-drawer) via `exec.CommandContext`. A self-exec restart
(`internal/procrestart.Restart()` — ut-docs#1550 — and the pre-existing
`internal/selfupdate.Apply()`) replaces the **parent's** process image in
place (same PID) but does nothing to the **children**: they are not
reparented, cancelled, or signalled. Left alone, a hardware-plugin process
started before a restart kept running unmanaged after it, and the freshly
restarted image's `AutoStartPlugins` could then spawn a **second** instance
of the same plugin, contending for the same physical device.

Fix: a `beforeRestart` hook (`SetBeforeRestart(fn func(context.Context))`)
added independently to both `internal/procrestart` and `internal/selfupdate`
(deliberately not shared — these two packages are already an intentional
duplicate of each other, per `procrestart.go`'s own package doc), wired by
`internal/app.Run` — the only place holding the `Supervisor` — to a
5-second-bounded `supervisor.Shutdown(ctx)`. `Supervisor` itself is
unmodified; `Shutdown` already did exactly the right thing (stop every
process, cancel its context — which makes `exec.CommandContext`'s own
machinery kill the OS process — wait bounded for every monitor goroutine)
and is already covered by `TestSupervisor_Shutdown`.

## Independent review (Opus, isolated worktree) — two rounds

**Round 1** (fresh Opus subagent, `isolation: "worktree"`, reviewing the
first commit): verified the core design sound and the TDD claim genuine —
independently reverted just the production-code changes (keeping the new
tests), confirmed `undefined: SetBeforeRestart` compile failures, then ran a
**mutation test** (moved the hook call to *after* `reexecFn`, and separately
no-opped it) and confirmed the new ordering tests correctly fail; restored
and confirmed green again. Ran the full `-race` suite (incl. the ~10-minute
`internal/plugins` package) and `-race -count=50/20` on the two directly
touched packages — no flake.

Found **8 findings**, one marked "must fix" and three "medium":

| # | Severity | Finding |
|---|---|---|
| F5 | Must fix | `procrestart.go`'s package doc still described the plugin-survives-restart gap as *not* covered, contradicting the fix it sits next to. |
| F3 | Medium | `Restart()` unconditionally ran `beforeRestart` (killing plugins) even on Windows, where `reexecFn` always fails and no restart ever actually happens — a genuinely new, unrecoverable-until-manual-restart failure mode, and a direct contradiction of `internal/pages/pairing_join.go`'s documented "logged no-op, never a crash" claim for an unsupported platform. |
| F1 | Medium | The hook ran *sequentially after* the `reexecDelay` sleep in both packages, making total delay-to-reexec additive (`reexecDelay + hookDuration`) instead of the original flat `reexecDelay` — risking `web/ui/partials/pairing_wait.html`'s first health-probe timing (tuned to `reexecDelay` alone, with an explicit comment about why) bouncing the operator to `/login` on the **pre-join database** if a plugin was slow to stop. |
| F2 | Low/Medium | `internal/app.Run` wired `selfupdate.SetBeforeRestart` *after* `pagesInit(...)`, which starts `pages.StartAutoUpdateScheduler` — an unattended goroutine that can call `selfupdate.Apply()` on its own 30s ticker — with no happens-before edge to the later `SetBeforeRestart` write. A real (if narrow) data race, invisible to `-race` in practice given the timing, but a real bug. |
| F6 | Minor | Nothing tested that the `app.go` wiring specifically connects to `Supervisor.Shutdown` — only that *some* hook fired, provable entirely inside the procrestart/selfupdate packages' own tests. |
| F4 | Medium, deferred | No compensation if `reexecFn` itself fails *after* plugins are already stopped — the process keeps running with every hardware plugin dead and no path back except a full manual restart. Genuinely out of scope for this card (new failure-recovery behavior, not the orphan-process bug); **filed as universaltill/ut-docs#1621**. |
| F7 | Informational | `Shutdown`'s 5s bound is nearly-but-not-strictly total (its opening lock isn't ctx-aware); accepted, bounded in practice by existing `internal/plugins` invariants (ut-docs#502). |
| F8 | Informational | The stop is a hard SIGKILL-via-context-cancellation, not a graceful SIGTERM-then-wait — consistent with what ordinary app shutdown already does (`internal/server/server.go`), not a new failure mode, but worth stating explicitly rather than leaving implicit (the original issue asked this question directly). Stated here: **this fix does not attempt a graceful stop** — a device mid-transaction (e.g. a scale reading in flight) is killed the same way ordinary shutdown already kills it today; no new risk relative to existing behavior, and out of scope per the issue's own non-goal ("this is not urgent/blocking"). |

Also surfaced (informational, not a regression from this diff): the macOS
`.app` update path (`selfupdate.applyMacApp`) restarts via a detached
helper that kills only the app process by name, never reaching the
`beforeRestart` hook at all — so the ut-docs#1616 gap remains open on macOS
`.app` installs specifically. Pre-existing, not made worse by this change;
**filed as universaltill/ut-docs#1622**.

**Fixes applied** (second commit) for F5, F3, F1, F2, F6 — all real
regressions this diff itself introduced or a real test gap at the exact
seam under review, so fixed in-scope rather than deferred:

- F5: package doc rewritten to state the fix and point at the wiring.
- F3: the hook is now skipped entirely when `!Supported()` (Windows);
  confirmed `selfupdate.Apply()` needed no equivalent fix — it already
  returns `ErrUnsupported` ~130 lines before ever reaching the goroutine.
- F1: `beforeRestart` now starts **concurrently** with the `reexecDelay`
  sleep in both packages (a `done` channel joins them), so the common case
  (no plugin, or a fast stop) adds no extra delay — total is
  `max(reexecDelay, hookDuration)`, not the sum. Hook-before-reexec
  ordering is preserved (channel close/receive establishes
  happens-before).
- F2: `plugins.NewSupervisor` construction and both `SetBeforeRestart`
  calls hoisted above `pagesInit(...)` in `app.go`, so the hooks are always
  registered before anything that could call `Restart()`/`Apply()` exists.
- F6: extracted `stopPluginsBeforeRestart(ps pluginShutdowner, log
  *logging.Logger) func(context.Context)` behind a minimal
  `pluginShutdowner` interface, with `TestStopPluginsBeforeRestart_
  CallsShutdownBounded` and `..._SurvivesShutdownError` proving the wiring
  is genuinely `Shutdown`, bounded to ≤5s, and error-safe.

**Round 2** (fresh Opus subagent, isolated worktree, scoped strictly to
verifying F5/F3/F1/F2/F6 against the second commit — not a full re-review):
verdict **all five FIXED**, confirmed independently by inspection and
`-race -count=100` reruns of the directly touched packages, plus
`GOOS=windows go build/vet` to confirm the Windows path still compiles.
Found one new, purely cosmetic issue: the `beforeRestart` var doc comments
and two test doc comments still said the hook runs "synchronously ...
inside the delayed goroutine," stale wording from before the
concurrent-with-the-sleep redesign; also noted no test asserted the hook
*starts* concurrently (only that it *finishes* before reexec), so a silent
regression back to sequential would not have been caught. Both fixed in a
third commit: corrected the four stale comments, and added
`TestRestartHookStartsConcurrentlyNotAfterSleep` /
`TestApplyHookStartsConcurrentlyNotAfterSleep`, which fail if the hook
start is ever moved back to after the sleep.

## Verification beyond automated tests

- TDD re-verified independently (not taken on the implementer's word): a
  real revert-then-restore in an isolated worktree, plus a mutation test
  moving the hook call past `reexecFn` — both rounds confirmed the new
  tests are genuinely behavioral, not tautological.
- Cross-platform: `GOOS=windows go build ./...` and `GOOS=darwin go build
  ./...` both clean; the Windows behavioral claim (F3) verified by full
  code-path inspection since `supported` is a build-tag `const`, not
  runtime-switchable in this environment.
- `go test -race` run at `-count=1` (full suite incl. `internal/plugins`,
  ~10 min), `-count=50`/`-count=100` on the two directly touched packages
  (no flake), and a final `-count=1` pass on `procrestart`/`selfupdate`/
  `app` after every subsequent fix.
- `golangci-lint run` clean on all three touched packages; full `go test
  ./...` (every package in the repo) green with no regressions.
- Guards run: `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-i18n.sh` — all pass (diff touches no
  SQL, no UI, no user-facing strings, confirmed by grep over the added
  lines, not just assumed from the task description).
- No real client/shop name or secret-shaped literal in the diff (none
  expected — pure internal process-lifecycle wiring — checked anyway).

## Deferred (new Backlog cards, not silently dropped)

- **universaltill/ut-docs#1621** — F4: no compensation when `reexecFn`
  itself fails after plugins are already stopped.
- **universaltill/ut-docs#1622** — the macOS `.app` update path
  (`selfupdate.applyMacApp`) never reaches the `beforeRestart` hook, so the
  orphan-plugin gap this card fixes remains open specifically for macOS
  `.app` installs.

## Verdict

**Safe to merge.** Both acceptance criteria from ut-docs#1616 are met: (a)
no duplicate/orphaned hardware-plugin process survives a self-exec restart
on the platforms where a restart actually happens, and (b) both
`internal/selfupdate` and `internal/procrestart` call sites are covered.
`Supervisor` itself was correctly left untouched. Two rounds of independent
Opus review, each earned by real findings in the round before it, with
every finding either fixed and re-verified or explicitly deferred to a
tracked card — nothing silently accepted or silently dropped.

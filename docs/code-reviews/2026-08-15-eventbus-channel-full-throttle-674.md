# Code review — throttle EventBus's unbounded "channel full" diagnostic (ut-docs#674)

**Date:** 2026-08-15
**Branch:** `investigate/ci-double-run-contention-674` → `main`
**Repo:** universaltill/universal-till
**Author (Dev):** Claude (Sonnet), autonomous SDLC pipeline
**Reviewer:** Claude (Opus), independent subagent, isolated worktree
**Complexity:** medium

## What this ticket asked

ut-docs#674: a CI run failed with a 600s hang in `internal/plugins`
(goroutines blocked on `internal/logging`'s output mutex and
`database/sql`'s connection opener) when the `build` job ran `go test ./...`
twice in one job. The ticket's acceptance criteria: either reproduce the
double-run contention and fix the real root cause, or confirm it doesn't
reproduce and document that — and never invoke `go test ./...` (or any
subset containing `internal/plugins`) twice in one CI job again without one
of those two outcomes.

## What was found and shipped

Investigating the reported symptoms, `EventBus.publish()`'s "channel full"
(dropped event) path in `internal/plugins/ipc.go` turned out to perform a
**synchronous SQLite audit-log INSERT and a raw, unsynchronized `fmt.Printf`
to stdout, on every single dropped event, completely unthrottled**.
`TestPublish_NeverPanicsRacingManagerReload` (in `publish_reload_race_test.go`)
deliberately hammers `Publish()` in a tight loop for its whole run with
nothing draining the subscriber channel — that's the test's own point,
proving an undisciplined publisher can't panic the bus — and was measured
producing **~16,700** of each write in under 16 seconds, serialized through
one SQLite connection and the shared stdout mutex.

Shipped: `EventBus.shouldWarnChannelFull(pluginID)`, a per-plugin,
one-second throttle (own dedicated mutex/map on `EventBus`, deliberately
separate from `eb.mu` since `publish()` holds `eb.mu.RLock()` across its
whole dispatch loop). The "channel full" branch now performs the audit
write + print only when the throttle allows it; the coalesced audit
row/log line says explicitly that further drops within the window were
folded into it, so a reader of `audit_log` can't misread "no dropped row"
as "was delivered."

Two new tests in `internal/plugins/channel_full_throttle_test.go`:
- `TestShouldWarnChannelFull_Throttles` — unit test of the throttle logic
  directly (first call warns, rapid repeats don't, a call after the window
  elapses warns again, independent windows per plugin ID).
- `TestEventBus_Publish_ChannelFullDiagnosticThrottled` — through the real
  `Publish()` path: 400 publishes against an undrained, capacity-100
  channel collapse to exactly 1 "dropped" audit row within one window, and
  a publish after the window elapses produces exactly a 2nd row.

## Independent verification (re-run personally by the reviewer, not taken on report)

The reviewer reverted just the `if eb.shouldWarnChannelFull(...)` guard in
`publish()` (keeping the new test file) and re-ran the new integration test
— **failed pre-fix at 300 dropped rows for 400 publishes**, **passed
post-fix at 1**. Separately re-measured `TestPublish_NeverPanicsRacingManagerReload`
pre/post-fix (own numbers, not the Dev report's): **15.86s / 16,729 warning
lines pre-fix**, **9.50–9.68s / 73 warning lines post-fix** (at the
initially-shipped 100ms interval, since revised to 1s per a should-fix
finding below — re-verified again after that change, see "Post-review
changes").

**Important negative result, surfaced by review, not by the original
investigation:** the reviewer ran two full `go test ./...` invocations
**in parallel** — the closest re-creation of the reported "twice in one
job" scenario available in this sandbox — both pre-fix and post-fix.
**Neither hung, and the fix made no measurable difference to that specific
scenario** (106–108s pre-fix vs. 111–115s post-fix for the `internal/plugins`
package, within run-to-run noise). This session's own investigation (one
full-suite baseline run, then the final gate run after the fix — effectively
a second, later full-suite run) also never reproduced a hang, in either
state.

**Conclusion, stated plainly: this ships a real, independently-confirmed
fix for a genuine unbounded resource amplifier, but it is not confirmed to
be the root cause of the original CI incident.** No one — this investigation
or the independent reviewer — could reproduce that incident, in several run
patterns, in this environment. Per the ticket's own acceptance criteria
("If not reproducible, downgrade this to 'unexplained one-off flake,
mitigated by scope-narrowing' and close with that note"), this closes on
that branch, with the amplifier fix shipped as a worthwhile hardening found
along the way — not a triumphant "root cause found and fixed" claim the
evidence doesn't support. Full reasoning and the reviewer's own numbers are
posted on the issue.

## Post-review changes (all applied, not just flagged)

The reviewer's read found no blockers, three should-fix items, and three
nits. Every should-fix was actioned:

1. **Overstated causal claim in code comments.** The `channelFullWarnInterval`
   doc comment and the new test file's doc comment both originally asserted
   this flood "is a real, reproducible source of" the CI incident. Reworded
   throughout (`ipc.go`, `channel_full_throttle_test.go`, and
   `.github/workflows/ci.yml`'s own comment on the scope-narrowing mitigation,
   which previously claimed the double-run failure was contention "rather
   than any actual bug in `internal/plugins`" — no longer accurate, since
   there was a real bug, now fixed, just not confirmed as *this* incident's
   cause) to state the finding and its actual evidentiary status honestly.
2. **Throttled audit rows were silently misleading.** Applied the reviewer's
   own worktree fix verbatim: the coalesced audit row's `data_json` and the
   stdout line now both say "(further drops within 1s coalesced/suppressed)"
   instead of a plain "channel full" that would read as one single drop.
3. **100ms was unjustified and too aggressive for a permanently-wedged
   production plugin** (~864,000 audit rows/day at 10/s indefinitely).
   Raised `channelFullWarnInterval` from 100ms to 1s (~86,400 rows/day
   worst case) — a judgement call documented as such in the comment, not
   presented as a precisely-derived number. Re-ran every affected test and
   the full gate after this change (see below) — all still green.
4. **Nit (test strength):** `TestEventBus_Publish_ChannelFullDiagnosticThrottled`
   originally only asserted `dropped <= 50` when the real throttled behavior
   through the real `Publish()` path was always exactly 1 row (all 400
   publishes land in well under one window) — the bound gave no real signal.
   Tightened to assert exactly 1 row for the burst, *and* added a second
   phase that sleeps past the window and publishes again, asserting a 2nd
   row appears — this now also proves the throttle resets rather than
   permanently silencing a plugin after its first drop (previously only the
   unit test covered window-expiry).

Not changed (reviewer's own read, left as accepted trade-offs, not blockers):
throttle keys on `pluginID` only, not `(pluginID, eventType)` — consistent
with the log message's own granularity, and a plugin subscribed to several
noisy event types is already an edge case; lazy-init of `dropWarnedAt`
inside `shouldWarnChannelFull` rather than `NewEventBus` — required, since
the unit test constructs `&EventBus{}` directly, and is safe under its own
mutex.

## Correctness analysis (reviewer's own checks, all passed)

- **Reentrancy: genuinely safe.** `shouldWarnChannelFull` touches only the
  new `dropWarnMu`, never `eb.mu`; lock order is uniformly
  `eb.mu.RLock → dropWarnMu`, never the reverse, and `dropWarnMu` has
  exactly one use site.
- **No data races** — new tests and the reload-race test both clean under
  `-race`.
- **No other consumer depends on one-row-per-drop.** Repo-wide grep for
  `audit_log` readers with `action='event_dispatch'`: only
  `internal/plugins/ipc_test.go` (asserts `enqueued`/`denied`/`error` rows
  exist — unaffected by throttling the `dropped` case). The fiscal/DSFinV-K
  export path (`internal/data/export_repo.go`) never touches `audit_log` at
  all; `HasAuditEntry` is only used for sale-scoped fiscal actions unrelated
  to plugin event dispatch. No compliance/GoBD exposure from coalescing
  these diagnostic rows.
- **Repository pattern respected** — no new SQL; reuses the existing
  `auditDispatchWithDB` → `data.NewPluginRepo(db).InsertAuditRaw` path.
- **i18n non-issue** — the only string is a backend `fmt.Printf` diagnostic
  to stdout, not template/JS-set UI text.
- **No new dependency** — `golang.org/x/time/rate` was considered and
  rejected (not already in `go.mod`; a 12-line hand-rolled throttle is the
  right call under ADR-0003's minimal-dependency posture, not an
  under-engineered shortcut).
- Both of this pipeline's recurring bug classes (missing `os.MkdirAll` on a
  file-write handler; a cwd-relative path where `paths.Data(...)` belongs)
  genuinely don't apply — the diff has no file writes and no path
  construction at all.

## Gates run

`go build ./...` clean · `go vet ./...` clean · `gofmt -l` clean on both
changed/new files · targeted tests
(`TestShouldWarnChannelFull_Throttles`, `TestEventBus_Publish_ChannelFullDiagnosticThrottled`,
`TestPublish_NeverPanicsRacingManagerReload` incl. `-race`) green ·
`go test ./internal/plugins/...` full package green (84.4s) ·
`go test ./...` full repo, 37 packages, zero failures (run before and after
the post-review changes) · `guard-data-access.sh` ✓ · `guard-kiosk-engine.sh`
✓ · `guard-plugin-menu-read.sh` ✓ · `guard-i18n.sh` ✓.

CLAUDE.md compliance: no SQL outside `internal/data`/`internal/db`, no new
user-facing strings, no money/offline-first/kiosk/plugin-signing surface
touched — each genuinely assessed. No user-visible behaviour change, so no
help-topic/README/ADR obligation.

## Non-blocking notes (no action taken)

- The throttle's window is per-plugin, not per-(plugin, event type) — see
  "Not changed" above.
- `internal/plugins -race` already sits close to its own 600s timeout
  ceiling per ut-docs#753 (filed separately, still open in Backlog) —
  unrelated to this diff, noted for awareness since this ticket is about
  the same package's CI timing.

## Safe-to-merge verdict

**Yes.** Independent review found no blockers; all three should-fix items
were applied and re-verified; the TDD claim was independently reproduced
(fails pre-fix at 300 rows, passes post-fix at 1). The ticket's headline
premise (this fixes the specific double-run CI hang) is **not** confirmed —
documented honestly above and on the issue — but the shipped change is a
real, worthwhile fix for a genuine unbounded resource amplifier, verified
safe on its own merits.

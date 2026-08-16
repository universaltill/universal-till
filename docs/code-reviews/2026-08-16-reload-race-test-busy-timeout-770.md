# Code review — TestPublish_NeverPanicsRacingManagerReload SQLITE_BUSY flake (ut-docs#770)

**Date:** 2026-08-16
**Card:** universaltill/ut-docs#770 (p2, `complexity:medium`)
**Branch:** `fix/reload-race-test-busy-timeout-770`
**Dev:** inline (Scrum Master pipeline cycle, Sonnet — medium-tier build model)
**Reviewer:** independent subagent, Opus (medium-tier review model), spawned
with `isolation: "worktree"`

## What shipped

`TestPublish_NeverPanicsRacingManagerReload` (in
`internal/plugins/publish_reload_race_test.go`) intermittently failed CI
with `database is locked (SQLITE_BUSY)` from `Reload`'s own write, under
sustained contention from the test's own publisher goroutine — a second,
independent race from the one ut-docs#750 already fixed in this same test
(`sql: database is closed`, from an undrained goroutine; that fix is
already on `main` and unaffected by this change).

- `internal/plugins/publish_reload_race_test.go`: added one call,
  `db.SetMaxOpenConns(1)`, right after `db := managerTestDB(t)`, plus an
  explanatory comment. `managerTestDB` opens a file-backed (not
  `:memory:`) SQLite DB whose DSN already sets `busy_timeout(5000)` +
  `journal_mode(WAL)` + `_txlock=immediate`, but with an unbounded
  connection pool — under real concurrent writers on a loaded/shared CI
  runner, SQLite's real-time busy handler can still be starved past its
  5s budget. Pinning the pool to one connection makes `database/sql`
  itself serialize the test's two goroutines' queries, structurally
  removing the inter-connection lock contention rather than relying on
  the busy handler to always win the race. Same fix pattern already
  established in this package (`shutdown_drain_test.go`) and in
  `internal/pos` (`sales_test.go`, `offline_resilience_test.go`).
  Scoped to this one test's local `db` handle — the shared
  `managerTestDB` helper (used by 12 files) is untouched, so no other
  test's concurrency assumptions change.

## Independent review (Opus, fresh context, worktree-isolated) — 0 blockers, 1 fixed, 2 follow-ups filed

**Verdict: safe to merge.** Full verbatim writeup on the issue; summary:

- **Deadlock hazard independently traced, not taken on trust.** The
  reviewer read both goroutines' actual code paths (`EventBus.publish`,
  `Manager.Reload` → `WasmRuntime.Sync` → `ResetSubscribers`,
  `subscribe()`) and confirmed no path acquires `eb.mu` while holding a
  pooled connection open — `publish()` takes the lock first and only then
  does DB work under it; every DB call on the Reload path fully releases
  its connection (via `defer rows.Close()` or plain `ExecContext`, no
  `Tx`) before `ResetSubscribers()` takes `eb.mu.Lock`. No cycle, no
  deadlock risk today.
- **Fixed — comment understated the real invariant.** The original
  comment said "neither goroutine holds a Tx open"; the actual invariant
  is broader (an open `*sql.Rows` pins a connection exactly like a `Tx`
  does). Rewrote the comment to name the real condition and spell out
  what would break it (a future `Sync` wrapped in a `Tx`, or a repo
  method returning `Rows` held across `ResetSubscribers`) and how it
  would surface (this package's `-race` suite timing out, not a clear
  failure) — same fix, applied and re-verified (build/vet/target test all
  green after the edit).
- **Empirical, not just reasoned:** target test `-race -count=50` passed
  clean both before (baseline, line reverted, 204.0s) and after (175.6s)
  — the fix is ~14% *faster*, not slower: removing real SQLite lock
  contention outweighs the pool-serialization cost, so it adds no new
  timeout risk. Full `internal/plugins/... -race` suite: green, 515.2s.
  `gofmt -l`: clean. All three required guards
  (`guard-data-access`/`guard-kiosk-engine`/`guard-plugin-menu-read`):
  pass.
- **Scoping independently confirmed, not just asserted:** all 12
  `managerTestDB` call sites get independent `*sql.DB` handles from a
  fresh `t.TempDir()` per call; the sibling test in the same file
  (`TestPublish_BlockingHandlerDoesNotWedgeBus`) is untouched; no
  `t.Parallel()` anywhere in the package.
- **TDD claim honestly re-verified, not fabricated:** confirmed the
  implementer's own finding that a clean local revert-then-rerun does
  NOT reproduce the original CI failure (50/50 pass with the fix
  reverted) — this is a genuinely load-dependent flake, not a
  deterministic one, and the review says so plainly rather than
  inventing a reproduction. Independently confirmed the fix is not a
  no-op by the measured runtime shift above.
- **Confirmed the fix doesn't weaken the regression guard it lives in:**
  the panic under test is an in-memory `eb.mu`-vs-channel-close race, not
  a DB race; the publisher now holds `eb.mu.RLock` *longer* (blocking in
  the pool while holding it), so `ResetSubscribers` contends *more*, not
  less.
- **Recurring bug classes checked, both N/A confirmed by grep, not
  skipped:** no `os.MkdirAll`/file-write pattern in the diff; no
  `paths.Data(...)`/cwd-relative-path pattern. No secret-shaped literal,
  no real client/shop name. Diff is exactly one `_test.go` file — no
  production code, no UI, no i18n key, no help topic — so the
  UI/manual-doc review requirements genuinely don't apply.

**Two non-blocking follow-ups, filed as new Backlog cards rather than
actioned here (real future work, out of scope for this fix):**
- Whether production `Manager.Reload` can itself return SQLITE_BUSY under
  a busy publisher (surfaces via a failed plugin install/enable/disable,
  not a failed checkout — offline-first unaffected, but a real gap if the
  original CI symptom reflects genuine production contention).
- `internal/plugins -race` running ~515s against Go's default 600s
  per-package timeout — thin margin on a loaded shared runner, and a
  plausible source of unrelated flaky-test reports on its own.

## Verification beyond the automated suite

- Reviewer ran the actual code (build, vet, target test at `-count=50`
  both with and without the fix, full package `-race` suite, gofmt,
  all 3 guards) inside an isolated worktree — not just a diff read —
  and independently traced the deadlock-safety argument through the real
  call graph rather than accepting the implementer's comment as given.
- No UI/driven-browser check — correctly out of scope; confirmed by the
  reviewer that the diff touches no production/UI code.

## Safe-to-merge verdict

Yes.

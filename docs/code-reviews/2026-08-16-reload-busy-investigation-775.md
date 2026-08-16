# Code review — can production `Manager.Reload` hit SQLITE_BUSY under a busy publisher? (ut-docs#775)

**Date:** 2026-08-16
**Card:** universaltill/ut-docs#775 (investigation-shaped, follow-up from ut-docs#770's review)
**Branch:** `fix/reload-busy-investigation-775`
**Reviewer:** independent reviewer, Opus, fresh context, worktree-isolated
(did not write the change under review)

## What shipped (as handed to review)

An investigation card, so the deliverable is a conclusion plus the evidence
for it — no production behaviour change was proposed.

- `internal/data/plugin_repo.go` — documentation-only comment block above
  `SyncPluginPaymentMethods` recording the locking analysis. No logic change.
- `internal/plugins/reload_busy_production_test.go` (new) —
  `TestReload_SurvivesRealisticPublisherContention`, racing `Manager.Reload`
  against a plugin-event publisher goroutine on an unpinned, production-DSN
  connection pool.

**Claimed conclusion:** no production code change needed.
`SyncPluginPaymentMethods` runs three sequential autocommit `ExecContext`
calls with no explicit `Tx`, so it never hits the SHARED→RESERVED
lock-promotion gap that `_txlock=immediate` (ut-docs#311) exists to close;
`busy_timeout(5000)` covers it via ordinary busy-handler retry.

## Verdict

**Safe to merge.** The conclusion is correct and is now supported by
evidence that actually supports it. One substantive finding (the shipped
test did not exercise contention at all, and the comment over-claimed what
had been proven) was found and **fixed on this branch**; details below.

## Independent verification

### 1. The locking argument — CONFIRMED, traced through real code

Read `internal/db/db.go`, `internal/data/plugin_repo.go`'s
`SyncPluginPaymentMethods`, and `internal/plugins/plugins.go`'s
`Manager.Reload` rather than trusting the new comment.

- `internal/db.Open` builds one DSN,
  `...busy_timeout(5000)&journal_mode(WAL)&_txlock=immediate`, and every
  handle in the process comes from it. `_txlock=immediate` is a DSN-level
  option, so it applies to *every* `Begin`/`BeginTx` on that handle — the
  claim that a hypothetical future `Tx`-wrap of this method would inherit
  the protection is correct.
- `SyncPluginPaymentMethods` is three bare `r.db.ExecContext` calls. No
  `Begin`, no `Tx`, no read-then-write inside one transaction. Each is a
  fresh autocommit write with no already-held SHARED lock to promote from,
  so it takes the ordinary lock-acquisition path where the busy handler
  runs and `busy_timeout` applies in full. The reasoning holds.
- `Manager.Reload` → two reads (`loadInstalled`, `loadMenuEntries`, both
  plain `QueryContext` with `defer rows.Close()`), then
  `SyncPluginPaymentMethods`, then `warnPaymentMethodAnomalies` and
  `Wasm.Sync`. Nothing wraps the sequence in a transaction, so there is no
  cross-statement lock held across the write.

### 2. Mutation re-runs — all three performed personally

| Mutation | Expected | Observed |
|---|---|---|
| `Manager.Reload` always returns an error | test fails | **FAILS** at `reload 0` — not a tautology, errors are not swallowed |
| `SyncPluginPaymentMethods` wrapped in `BeginTx` → `SELECT` → `UPDATE` (the literal bug shape), DSN unchanged | test passes | **PASSES**, `-race -count=5` |
| *(added by review)* same `Tx`-wrap **and** `_txlock=immediate` deleted from the DSN | test should fail if it has discriminating power | **STILL PASSES** — see finding below |

All mutations reverted; `git diff` confirmed empty against the WIP commit
before any further work.

### 3. Finding (medium, **fixed on this branch**) — the shipped test proved nothing

The third mutation is the one the original investigation did not run, and
it is decisive. Deleting `_txlock=immediate` while keeping the
read-then-write `Tx` shape **did not** make
`TestReload_SurvivesRealisticPublisherContention` fail.

The DSN mutation is genuinely effective — `internal/db`'s
`TestConcurrentWriterWaitsInsteadOfInstantBusy` catches it immediately:

```
concurrent write failed after 1.384281ms (want a wait then success;
instant SQLITE_BUSY is the pre-fix bug ...): database is locked (5) (SQLITE_BUSY)
```

So the new test was green for the wrong reason. Instrumenting it
(throwaway probe, deleted) showed why:

```
PROBE: 20 reloads in 11.88ms (slowest single reload 3.44ms);
       publishes ok=1 err=0; audit rows=2
```

The whole `reloadCount` loop finishes in ~12ms, while the publisher slept
50ms between `Publish` calls. **Exactly one publish overlapped the entire
run.** The test was green because nothing ever contended — not because
contention was survived. Its own doc comment described a "realistic publish
cadence… deliberately pessimistic stand-in"; measured, the cadence made the
competing writer effectively absent.

Consequently the comment in `plugin_repo.go` over-claimed. Its statement
that the `Tx`-wrap mutation stayed clean *"because BEGIN IMMEDIATE pre-empts
the promotion path entirely"* attributed the green result to a mechanism the
experiment never isolated; the result is identical with that mechanism
removed. The `"10/10 clean at a realistic publish cadence"` line described
one publish per run.

**Fixes applied (both files):**

- Publisher is now **unthrottled**. Measured effect at the same wall-clock
  cost: **153 publishes across 20 reloads under `-race`** (vs 1), tens of
  thousands without `-race`, and `Reload` observably parks in the busy
  handler — slowest single reload 1.01s at 10,477 publishes, 2.01s at
  140,325 — rather than erroring. This is real contention, and the
  conclusion survives it.
- Added a **contention floor**: the test now fails if the publisher
  completed fewer than `reloadCount` publishes, so it cannot silently decay
  back into a no-op. Verified by re-introducing the 50ms sleep — the floor
  trips with a clear diagnostic (`publisher only completed 3 publishes
  across 20 reloads`).
- Publisher errors are now **counted and asserted zero** instead of being
  discarded into `_, _ =`. This answers #775 from the losing writer's side
  too: 0 errors across ~140k publishes.
- Rewrote both comment blocks to state what was actually demonstrated, and
  added an explicit scope caveat that this test is **not** a regression test
  for `_txlock=immediate` (`internal/db`'s
  `TestConcurrentWriterWaitsInsteadOfInstantBusy` is), so neither is retired
  on the strength of the other.

Net effect on the card's conclusion: **unchanged, and now actually
evidenced.** No production code change is needed.

### 4. Gate — all green

| Check | Result |
|---|---|
| `go build ./...` | pass |
| `go vet ./...` | pass |
| `go test ./...` (full suite) | pass |
| `gofmt -l .` | 9 pre-existing files flagged, **none from this diff**; both changed files clean |
| `guard-data-access.sh` | pass |
| `guard-kiosk-engine.sh` | pass |
| `guard-plugin-menu-read.sh` | pass |
| `guard-i18n.sh`, `guard-compliance-claims.sh`, `guard-help-topics.sh` | pass (run to confirm the backend-only claim) |
| `internal/plugins -race` timing | see below |

### 5. Recurring bug classes — N/A by inspection, confirmed

- **File-write handler missing `os.MkdirAll`** — N/A. The diff adds no file
  writes; grep over added lines for `os.Create`/`os.WriteFile`/`os.OpenFile`
  returns nothing. (For completeness, `internal/db.Open` already does
  `os.MkdirAll` on the data dir, and the test's DB lives under `t.TempDir()`.)
- **cwd-relative path where `paths.Data(...)` belongs** — N/A. No path
  construction added; the test DB path comes from `t.TempDir()` via
  `openMarketplaceInstallerDB`.
- **Real client/shop name** — none. Identifiers are `com.test.reloadbusy` /
  `ReloadBusy`.
- **Secret-shaped literal** — none.

### 6. Scope discipline — confirmed

- No change to `completeTender` or any checkout path.
- No connection-pool-size change (`SetMaxOpenConns`/`SetMaxIdleConns` appear
  in the diff only inside comments).
- No `Tx`-wrapping of `SyncPluginPaymentMethods` in the real diff — the
  `BeginTx` occurrences are comment prose only. Verified by grepping added
  code lines.
- No UI/i18n/help-topic/compliance surface touched. Backend-only confirmed,
  not assumed: no files under `web/`, no locale keys, and
  `guard-help-topics.sh` passes (no new page route to claim, so
  `reference/ux-guidelines.md` and `web/help/` correctly do not apply).

### 7. Timing — no regression risk to ut-docs#753/#776

`internal/plugins -race` measured on this branch. The change costs ~2s: the
target test went from 16.6s to 18.8s for `-count=10` (~1.7s/run to ~1.9s/run)
while carrying 153x more contention. That is well inside the headroom
between the ~515-540s baseline flagged in ut-docs#753/#776 and the 600s
concern threshold. **This diff is not what would tip that ticket over**;
fixing the baseline remains out of scope here.

## Observations noted, not fixed (out of scope — candidates for a Backlog card)

1. **Reload latency under pathological publish rates.** With an unthrottled
   publisher (~140k publishes), a single `Manager.Reload` was observed
   waiting up to **~2.0s** in the busy handler. It never errored — the
   `busy_timeout` budget is 5s — but the margin is ~2.5x, not orders of
   magnitude. A real shop's event rate is far below this, so there is no
   production risk today; worth a card only if plugin event volume is ever
   expected to grow substantially, since `Reload` runs on
   install/enable/disable/uninstall and a multi-second stall there would be
   user-visible on the plugins screen.
2. **Pre-existing `gofmt` drift** in 9 files unrelated to this change
   (`internal/data/install_status_repo.go`, `internal/pages/users_page.go`,
   `internal/plugins/marketplace/client.go`, and others). Untouched here to
   keep the diff scoped; a cleanup card would let CI enforce `gofmt` cleanly.

## Files

- `internal/data/plugin_repo.go` — comment block above
  `SyncPluginPaymentMethods` (documentation only, corrected during review)
- `internal/plugins/reload_busy_production_test.go` —
  `TestReload_SurvivesRealisticPublisherContention` (contention made real
  during review; floor + publisher-error assertions added)

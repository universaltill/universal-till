# Review — pull tick: no write-back after promote (ut-docs#5903)

Branch `fix/5903-pull-tick-replica-guard`. Author: Sonnet dev subagent
(design by an Opus architect subagent). Reviewer: Opus subagent, fresh context.
Tester: Sonnet subagent, separate from the author.

## What shipped

- `internal/data/replica_guard.go`: `ErrNotReplica` (generic message) and
  `requireReplica(ctx, q)`, reading `sync.primary_url` on the caller's
  transaction. `applyAdmin` (#5896) now uses it.
- `SettingsRepo.SetIfReplica` / `IsReplica` (+ `settings.Store`
  passthroughs): upserts several keys in one transaction only while the till
  is still a replica.
- `SyncStockRepo.ApplyStockLevels(ctx, bundle, locID, version)`: replica
  check, every correction and `sync.stock_version` in one transaction —
  all or nothing.
- `syncPullTick`: `rememberOwnTillRole`, the pull bookkeeping
  (`sync.pull_version`/`last_pull_at`), the contact stamps on the unchanged
  path (`recordMainContactIfReplica`) and the plugin `sync.*` keys go
  through `SetIfReplica`; the plugin converge re-checks `IsReplica` right
  before it starts. Any `ErrNotReplica` logs the existing INFO skip line and
  ends the tick.

## Findings (round 1, Opus)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | medium | `PromotedMidPullUnchanged_5903` reached the role guard first, so the contact/plugin/stock guards were untested (mutation passed) | fixed — `promoteKeepingRole` helper; mutating the contact guard now fails the test |
| 2 | medium | extended `_5896` test stopped at the role write, losing #5896's applyAdmin coverage (mutation passed) | fixed — same helper; new `PromotedMidPullRoleRefused_5903` covers the role refusal; disabling applyAdmin's check now fails `_5896` |
| 3 | low | `recordMainContactIfReplica` dropped `MarkMainContact` silently on a real DB error; stale doc comment | fixed — WARN + still marks contact; comment corrected |
| 4 | low | "apply failed after %d corrections" always printed 0 | fixed — "apply failed, no corrections applied" |
| 5 | low | stock apply now holds the write lock for the whole loop | accepted for correctness; deferred → ut-docs#6035 |
| 6 | low | lan-sync.md overclaimed "writes nothing" (asset sync, audit row window) | fixed — residuals named; deferred → ut-docs#5921, ut-docs#6034 |
| 7 | nit | silent return after the plugin `stillReplica` check | accepted (the stock pull's own guard ends the tick) |

Fixes re-verified by the orchestrator: targeted `go test ./internal/data/
./internal/pages/ -run 'Promoted|SetIfReplica|ApplyStockLevels|TillRole|SyncPull|SyncAdmin|MainContact'` ok; gofmt, vet clean.

## Extra verification (Tester)

Each guard reverted one by one: `requireReplica` no-op fails 5 tests; the
SetIfReplica in-tx check, the ApplyStockLevels check, the unguarded role
write and the stock page's skip each fail their tests; all pass on restore.
Targeted `-race` run green (data 13s, pages 145s). Backend only — no UI run.
Known gap: the bookkeeping-pair and plugin-key guards only matter if a
promote lands between two writes in one tick; not drivable through the
test primary's hooks (defense in depth).

## Pre-flight

Pre-flight: help topic n/a (no user-visible change); reference/architecture
updated (ut-docs `architecture/lan-sync.md` "Promoting a replica");
en.json n/a; README n/a; ADR n/a (reuses #5896's pattern); follow-ups
deferred → ut-docs#5921, #5922, #5923, #6034, #6035.

Verdict: approve.

# Review: pull tick writes nothing after a committed promote (ut-docs#5903)

**Date:** 2026-11-08 · **Author model:** Sonnet (Dev) ·
**Reviewer model:** _(Reviewer to fill)_ · **Branch:** `fix/5903-pull-tick-promote-race`

Follow-up to ut-docs#5896 (review findings 1 and 2 of
`2026-11-08-admin-apply-replica-guard.md`). No ADR: the same in-tx
`sync.primary_url` check under `_txlock=immediate` is reused; ADR-0011 D4
promote semantics are unchanged.

## What shipped

- `internal/data/replica_guard.go` (new): `ErrNotReplica` moved here (generic
  message) and the shared in-tx check `requireReplica`. `applyAdmin` now calls
  it instead of its inline read.
- `SettingsRepo.SetManyIfReplica` (+ `settings.Store` passthrough): writes a
  key set in one transaction, `requireReplica` first, else `ErrNotReplica` and
  nothing written.
- `SyncStockRepo.ApplyStockLevels(ctx, bundle, locationID, version)`: now one
  transaction: replica check, the local diff, every correction movement
  (`RecordStockMovement` with the caller's tx) and `sync.stock_version`.
  All or nothing; `ErrNotReplica` after a promote. `DumpStock` wraps a
  `dumpStock(ctx, queryer)` so the diff runs inside the locked tx.
- `internal/pages/till_role.go`: `ownTillRoleChange` (decision only);
  `rememberOwnTillRole` keeps its behaviour for the make-satellite path but
  `syncPullTick` no longer calls it.
- `internal/pages/sync_admin.go`:
  - `syncPullTick`: one guarded write of `sync.last_contact_at`,
    `sync.last_pull_ok_at`, the changed role and, on a changed answer,
    `sync.pull_version` / `sync.last_pull_at`, on both the changed and the
    unchanged path. On `ErrNotReplica` the tick stops (INFO, via
    `promotedMidPull`) before main-contact side effects, plugin sync and stock
    pull. `MarkMainContact` / `primaryContactOK` / audit / refresh run after
    the write.
  - A cheap `sync.primary_url` re-read before the stock GET.
  - `syncPullPlugins`' `sync.plugins_*` writes go through `SetManyIfReplica`.
  - Stock apply: `ErrNotReplica` is the INFO skip, other errors the existing
    ERROR path.
- Tests: `replica_guard_5903_test.go`, `sync_stock_repo_5903_test.go`
  (data); pages tests for the unchanged-answer, plugin-step, stock-step races
  and a stock control; `TestSyncPullTick_PromotedMidPullIsSkipped_5896`
  extended with `sync.till_role` (and a non-vacuity assertion that the primary
  reports a valid role). The `pullTestPrimary` harness gained `tillID`,
  `onPlugins`, `onStock`, `adminHave`, `hit(path)`.

## Behaviour notes

- A tick whose admin apply **fails** no longer remembers the role that tick
  (it used to, before the apply); the next successful tick writes it.
- Post-apply race (promote commits between the apply commit and the guarded
  write): `refresh` still runs (the apply committed); the `admin_pulled` audit
  row and the "applied" INFO line are skipped.
- `ApplyStockLevels` holds the SQLite write lock for the diff plus the
  corrections (was: per-movement locks). Reviewer: confirm `busy_timeout`
  comfortably exceeds a first-sync-sized bundle.
- Out of scope, follow-up cards: F1 asset sync/pruner after promote; F2
  `sync.*` resurrection by sales push, closes push and link client; F3 sales
  mirror page apply has no replica check. `convergePluginSet`'s
  install/uninstall stay non-transactional (only its settings writes are
  guarded).

## Findings

_Independent review pending (Reviewer to fill)._

| # | Severity | Finding | Outcome |
|---|---|---|---|
| | | | |

## Verification

_Dev: see the scratchpad notes for fail-first evidence and gate output; Tester
and Reviewer to record theirs here._

Pre-flight: help topic n/a (no user-visible behaviour); reference docs → ut-docs `architecture/lan-sync.md` sentences proposed in the Dev notes, separate ut-docs PR; README n/a; en.json n/a; migration n/a; follow-up work F1-F3 (cards to be filed).

**Verdict:** _pending._

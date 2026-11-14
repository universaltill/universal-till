# Review — a promoted replica keeps and uploads the icons the cloud never received (ut-docs#6240)

**Date:** 2026-11-14 · **Author:** Dev (Sonnet) · **Reviewer:** independent Opus review · **Tester:** separate agent

## What changed

- `internal/data/cloud_outbox_promote.go` (new):
  - `promotedUploadFields` holds the item and category `icon` entries. The next promoted field (ut-docs#6081)
    adds a row here.
  - `PromotedUploadFields()` exists for catalogpull's pin test.
  - `CloudOutboxRepo.MarkUnsentPromotedFields` runs the contract 1.2.0 "promoting a field to cloud-mastered"
    mark-and-seed step:
    - It marks each linked row whose local value is set and differs from its base. A missing or null base key
      counts as the cloud default.
    - The marks are gated exactly like migration 103's triggers, with `EnsureOpenMark` semantics.
    - It then sets a missing base key to the cloud default. NULL and non-JSON bases are left to `seedBases`.
  - `SettingsRepo.PromoteReplica` runs the identity clear and the marking in one transaction.
- `internal/data/settings_repo.go`: `ClearReplicaIdentity` becomes a wrapper over
  `clearReplicaIdentity(ctx, inTx)`. The callback runs after the `sync.%` delete and before `Commit`, so the
  trigger gate already sees the cleared identity. A callback error rolls back the whole promotion.
- `internal/pages/sync_api.go`: `POST /api/sync/promote` calls `PromoteReplica`. Its handler comment is updated.
- `internal/catalogpull/base.go`: the `tillOwnedFields` comment now says a promoted field also gets a row in
  `data.promotedUploadFields`.
- Tests:
  - `internal/data/cloud_outbox_promote_6240_test.go`: marks, skips (icon empty, unlinked, base equal, NULL or
    non-JSON base, row gone), one open mark, base-key seeding, modes (till, unset, suppressed, adopting),
    rollback, idempotence.
  - `internal/catalogpull/promote_icon_6240_test.go`: promote → pull keeps the icon → push sends `""` → icon;
    a control test that loses the icon without promotion; `promotedUploadFields` ⊆ `uploadFields`, ∉ `tillOwnedFields`.
  - `internal/pages/sync_promote_icons_6240_test.go`: the route marks; till and unset mode return 200 with the
    audit entry and no marks; a marking failure gives the localized 500 and the till stays a replica.
- ut-docs: `architecture/lan-sync.md` "Promoting a replica"; `reference/contracts/cloud-catalog-changes.md`
  1.2.0 recipe note (till-internal, no version bump).
- No migration (103 stays frozen, ADR-0100), no en.json key, no UI.

This corrects the #6071 record's "a replica never uploads"
(`2026-11-14-pull-icon-mastered-6071.md`, Known limits): once a replica is promoted, it uploads.

## Known limits

- A replica's `cloud_links` bases and catalogue cursor are frozen at join time. A promotion mark therefore uploads
  every field that drifted since the join, not just the icon.
  - Usually this is an accepted no-op: the cloud already holds the value.
  - If the cloud changed one of those fields after the main till's last sync, the whole change loses a `value`
    conflict and the icon falls back to `""`. The cloud's `CatalogConflict` logs it.
  - Deferred to ut-docs#6241 (re-base before marking) with ut-docs#6242 (inherited outbox/cursor).
- A promoted replica's cached `entitlement.catalog_master` comes from the join snapshot. If the main till moved
  to cloud mode after the join, promotion marks nothing until the mode is re-cached (ut-docs#6243).

## Review

Independent Opus review (a different model from the Sonnet author): **approve with changes**.

- **F1 medium: accepted, deferred.** The stale join-time bases widen each promotion mark to every drifted field.
  A whole-change conflict then reverts the icon. It is the same class as #6071 F4, but more likely on a
  long-joined replica. It is not fixable cheaply here and is carried in ut-docs#6241 with the scenario.
- **F2 low: fixed.** The `sync.primary_url` clause of the mark SQL was untested: a mutant that drops it
  survived. Added `TestMarkUnsentPromotedFields_NoMarksOnReplica_6240`; the orchestrator re-ran the mutant (clause deleted) and it now fails.
- **F3 low: fixed.** The `ClearReplicaIdentity` headline still read "promotes a replica", although it
  has no production caller left. Reword it to point at `PromoteReplica`, so a future promote path cannot skip
  the marks.
- **F4 low: fixed.** The lan-sync.md wording said "the cloud never received" (the code actually marks
  every icon that differs from the join-time base) and "first pull keeps, next upload sends" (the upload runs
  first on the tick). Reworded.
- **F5 low: fixed.** The `docs/data-model.md` recipe paragraph (:831-838) did not say that promotion
  re-runs the step.
- **F6 nit: accepted.** A JSON-`null` base icon is marked here but not by 103. The till never writes one.
- **F7 nit: fixed.** The stale `linkRow` test comment; the idempotence test did not compare the bases.

Checked:
- The gate is byte-equivalent to the 103 triggers' WHEN clause and is evaluated after the delete in the same
  transaction.
- `err` is propagated to the trace, and the rollback leaves `sync.primary_url`, `till.name` and
  `lan_discovery.till_id` intact (tested).
- `_txlock=immediate` still serialises promotion against `applyAdmin`'s ErrNotReplica check.
- At most one open mark per row (`ux_cloud_outbox_open`). An inherited sealed mark makes the new mark wait for
  its answer.
- The COALESCE form makes the order of the mark and seed statements irrelevant.
- `created_at` has the same format as the triggers.
- Only constant identifiers are interpolated into the SQL.
- No file writes, no cwd-relative paths.

Extra verification by the reviewer:
- build, vet, gofmt, and golangci-lint (data, pages, catalogpull: 0 issues);
- the affected tests plus the migration 103 / `TestShippedMigrationsUnchanged` / #6071 tests;
- `guard-data-access.sh` and `guard-fixed-dates.sh`;
- TDD re-checked in a throwaway worktree with the callback stubbed: 4 data, 1 catalogpull and 2 pages tests fail
  for the stated reason and pass restored.

**Tester:** PASS (separate Opus agent). Mutation run in a scratch copy: 11 of 12 breakages turn a test red; the
12th (the `sync.primary_url` clause) is F2, closed by the new replica test. AC1–AC7 are covered (AC1/AC2 end to end
through `newPushHarness`). `-race`, one package at a time: data has one failure, the timing-budget
`TestStockUploadPendingAndHasSendable_Fast…` (268ms against 100ms). It fails identically with the change reverted,
so it predates this change. catalogpull and pages hit the 15m local timeout with no failure and no race; their
6240/promote/103/6071 `-race` subsets pass. CI runs the full suites. Driven run skipped: no UI, and the handler
tests drive the real mux over a real migrated DB.

Pre-flight: review record → this file (+ ut-docs `code-reviews/2026-11-14-promote-replica-icon-marks-6240.md` for the doc PR); reference pages → ut-docs `architecture/lan-sync.md`, contracts changes 1.2.0
note, `docs/data-model.md` updated; help topic n/a (no visible step or setting: icons stop
disappearing); en.json n/a; ADR n/a (applies the 1.2.0 recipe at one more point; ADR-0162 §4/§5, ADR-0100,
ADR-0003 respected); follow-ups → ut-docs#6241 (NG1 + F1 scenario), ut-docs#6242 (NG2), ut-docs#6243 (NG3 incl. (b))

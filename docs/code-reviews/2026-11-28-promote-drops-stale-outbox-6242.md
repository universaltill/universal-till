# Review: a promoted replica drops the inherited open outbox marks and push hashes (ut-docs#6242)

Date: 2026-11-28. Lane: `lane:cloud-41`. Author: Sonnet 5.5 (Dev). Reviewer: Opus 5.5, independent,
fresh context. Follow-up of #6240 (universal-till#2553).

## What shipped

- `SettingsRepo.PromoteReplica` (`internal/data/cloud_outbox_promote.go`). In the one promotion
  transaction, and only if the till really was a replica, it now does two things before #6240's
  `MarkUnsentPromotedFields`:
  - drops every open `cloud_outbox` mark (new `CloudOutboxRepo.DropInheritedOpenMarks`);
  - drops `cloudsync.snapshot_hash` and `cloudsync.order_tracking_hash`, a subset of `db.RestoreDropKeys`
    that a test pins.
- "Really was a replica" means `TRIM(sync.primary_url)` was non-empty, read inside the transaction
  before the `sync.%` delete. `clearReplicaIdentity` in `settings_repo.go` passes it on as `wasReplica`.
- Promotion keeps:
  - sealed marks, because the cloud replays a `change_id` idempotently and the answer re-bases the row;
  - `cloud_links`, the only map from local ids to cloud ids;
  - the catalogue pull cursor, because an old cursor cannot miss a change. A tombstone is covered too.
- Why the drop is needed: a replica's open marks are all inherited from the main till's join snapshot.
  Every `cloud_outbox` trigger (migrations 084, 102, 103) and every Go writer is main-till only. Kept,
  the marks froze drifted fields on the pull, uploaded a stale before/after, and conflict-logged rows.
  In the ABA case that last step became a lost update.
- Docs: `docs/data-model.md` (the cloud_outbox section) and ut-docs `architecture/lan-sync.md` (the
  promotion paragraph).
- The join snapshot itself is not changed. Promotion is the one place that also covers replicas already
  in the field.
- No migration, UI or i18n.

## Tests (TDD)

- `internal/data/cloud_outbox_promote_6242_test.go` has 8 tests. `internal/catalogpull/promote_outbox_6242_test.go`
  has 5.
- 7 of them were red first. Dev saw each fail with the stated error, and the Reviewer saw the same
  again with the production files reverted:
  - `open marks = [item:i1 category:c1 tax_code:t1], want []`
  - `cloudsync.snapshot_hash must be dropped`
  - `sent the stale after:120 change`
  - `err = <nil>, want the drop failure`
- The rest are regression guards and pass both ways: links and cursor kept, a non-replica keeps its
  own marks, the subset of `RestoreDropKeys`, old cursor, sealed replay, and a control.
- Tester mutants were all killed: a no-op drop, a skipped hash drop, and `wasReplica` forced to true.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| minor-1 | minor | Docs and the comment said "the main uploads them itself" as though always true. A main that edited while cut off from the cloud and died before uploading loses that edit with its mark. | **Fixed.** The trade-off is now stated in the code comment, `data-model.md` and `lan-sync.md`. Recovering such drift is ut-docs#6241's re-base. |
| nit-1 | nit | The dropped-mark count is thrown away (no log). | Accepted. Diagnosis is still possible from the conflict log and cursor. |
| nit-2 | nit | The two settings deletes are a per-key loop. | Accepted. Cosmetic. |
| nit-3 | nit | No regression test for a double promote or a whitespace-only primary URL. | Accepted. The Reviewer's throwaway probe proved both paths. `_txlock=immediate` serialises two promotes (`internal/db/db.go:101`). |

Hunted and found sound:
- a real main's marks are safe on a non-replica, a racing double promote, or the handler's 409 check
  outside the transaction;
- a replica never writes open marks;
- one transaction with rollback (the bearer-file clear runs only after commit);
- the ordering against #6240's icon marks (084's partial unique index allows a fresh open mark next to a
  sealed one);
- the `os.MkdirAll` and `paths.Data` bugs do not apply, because the diff writes no file.

## Extra verification

- **Tester driven run:** `POST /api/sync/promote` through the real handler, on a real migrated SQLite
  database seeded as a joined replica, returned 200:
  - the open marks were dropped, and #6240 reopened a fresh icon mark;
  - the sealed mark was kept;
  - both hashes were dropped;
  - the cursor, `next_steps` and the 3 `cloud_links` were unchanged.
- **Full gate passed:** gofmt, build, vet, golangci-lint (0 issues), shellcheck (0 issues), and all 72
  `build`-job guards.
- **Gate under `go test -race ./...`:** 125 packages ok. The 9 failures were timeouts or timing tests on
  the loaded 4-CPU container. Each passed on a rerun with `-count=1`, either under `-race` or without it
  for the packages `ci.yml` already runs without `-race` (pages, plugins, data):
  - `internal/db` times out under `-race`. It is untouched by this change and ok without `-race`.
  - `internal/data`'s one race failure, `TestStockUploadPendingAndHasSendable_FastWithRandomCloudIDsAndUnlinkedMarks`
    (a 100 ms budget), reproduces on origin/main.

Pre-flight: till user-visible behaviour n/a (no UI; promotion is a staff API with unchanged responses);
`web/help/` n/a; `reference/` → ut-docs `architecture/lan-sync.md` updated; `docs/data-model.md` updated;
README n/a; en.json n/a; migrations n/a; ADR n/a (implementation of ADR-0162 §5 / the existing
promotion section); follow-ups → ut-docs#7183 (links minted on the main after the join never reach the
replica), ut-docs#6241 (re-base unsent drift; minor-1 residual).

## Verdict

Ship. minor-1 fixed; nits accepted.

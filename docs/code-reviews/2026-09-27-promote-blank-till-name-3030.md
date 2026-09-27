# Review — promoting a replica with a blank till name (ut-docs#3030)

- **Date:** 2026-09-27
- **Card:** universaltill/ut-docs#3030 (follow-up to #3025)
- **Branch:** `fix/3030-promote-blank-till-name`
- **Author:** Sonnet dev subagent. **Reviewer:** Opus 5.5, fresh context (complexity:easy routing).

## What shipped

`data.SettingsRepo.ClearReplicaIdentity` is the promote path behind
`POST /api/sync/promote`. It now always writes the trimmed `sync.till_name`
into `till.name`, **including a blank one**. Previously a blank or missing
`sync.till_name` skipped the write. That left the shop-wide `till.name`
holding the OLD main till's name, which had been synced down. So the
promoted till reported that name to the cloud (`enroll.DeviceName`), and
re-pairing the old main till under its own name was refused as "already
taken".

Now the promoted till falls back to its translated default name. Every
reader goes through `tillNameOrDefault`, which trims. `enroll.DeviceName`
returns "", and `cloudsync` already maps "" to "Till" for the heartbeat.

This is Architect option (a) from the card, with one refinement from the
review: the till name is set **blank** instead of the row being
**deleted**. See finding 1.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The first cut deleted the `till.name` row. Shop-wide settings reach joined tills only as upserts: `ApplyAdmin` never prunes `settings` (`internal/data/sync_admin_repo.go`). A till later re-pointed at the promoted one without a re-join would therefore keep the old main till's name. | **Fixed.** A blank value is upserted in the same transaction, so it propagates the same way any other shop-wide setting does. The tests assert blank rather than absent. |
| 2 | nit | A lone `if` sat inside an `else` block. | **Fixed.** Finding 1's change makes the write unconditional, so the branch is gone. |
| 3 | process | No review record existed yet. | This file. |

The review checked the following and found no problem:
- **Callers:** there is one caller (`internal/pages/sync_api.go`), and it returns 409 unless the till is a replica.
- **Setup detection:** it keys off `setup.completed`, not `till.name`.
- **Receipts:** they don't read `till.name`.
- **Settings cache:** it covers only the barcode key.
- **Admin generation:** the migration-023 trigger bumps it.
- **Rest of the diff:** no migrations, no new strings, SQL stays in `internal/data`.

## Verification

- **TDD, re-verified by the reviewer and again by the orchestrator after the finding-1 change.** With `internal/data/settings_repo.go` reverted to `main`, three checks fail:
  - `TestSettingsRepo_ClearReplicaIdentityKeepsOwnTillName`, the subtests "no sync.till_name…" and "whitespace-only…", fail with `expected till.name blank, got val="Front Counter"`.
  - `TestSyncPromote_BlankTillNameClearsOldMainName` fails with `expected till.name set blank after promote, got val="Front Counter"`.

  With the fix restored, all of them pass.
- The new handler test goes through the real mux. After the promote it asserts: `till.name` is blank, `tillNameOrDefault` returns `setup.till_name.default`, `enroll.DeviceName` returns "", and `sync.primary_url` is cleared.
- **Full gate after the last edit:**
  - `gofmt -l .` produces no output.
  - `go build ./...`, `go vet ./...` and `go test ./...` all pass.
  - `golangci-lint run ./...` reports 0 issues.
  - These guards pass: `guard-data-access`, `guard-i18n`, `guard-kiosk-engine`, `guard-core-neutral`, `guard-compliance-claims` and `guard-help-topics`.
- **No UI or help surface changed.** `web/help` has no text about promoting a till, so there is no visual check and no screenshot.

## Verdict

Safe to merge.

## Deferred

None.

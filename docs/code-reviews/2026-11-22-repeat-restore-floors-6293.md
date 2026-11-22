# Review: a second restore no longer re-issues numbers sold since the first (ut-docs#6293)

Date: 2026-11-22. Lane `lane:cloud-41`. Follow-up of the #5068 round-2 review, finding R3.

## What shipped

- `internal/db/restore_floors.go` (new): `readLiveNumbers` reads the running
  till's restore floors (`restore.receipt_floor`, `restore.receipt_floor_prefix`,
  `restore.z_floor.<kind>`) and its real highest receipt counter (sales,
  sales_archive, the `number_series` counter of ut-docs#6663) and highest Z per
  kind. `mergeRestoreFloors` max-merges them into the staged restore's floor
  keys. A floor is only ever raised.
- `keepLiveCloudIdentity` (`internal/db/backup.go`) calls it inside the same
  staged-file transaction, after the identity rewrite, and only when the live DB
  was readable. The receipt prefix used is the one the restored till will have
  (so a kept replacement-main-till prefix wins). A live or staged floor counts
  only for that prefix. `restore.marker` and `restore.jump_token` stay the
  backup's. LAN join is untouched.
- Failure handling never refuses a restore: an unreadable live DB keeps the
  backup's floors (the #5069 rule); an unreadable sales/report table carries the
  floors and whatever maxima could be read, with a warning.
- The db-side maxima SQL duplicates `data.maxReceiptSeq` + the counter (db cannot
  import data). `TestLiveNumberMaximaMirrorData` pins it to what
  `JumpNumbersAfterRestore` computes, and `TestRestoreFloorKeysMirrorData` pins
  the key names.
- Help: `web/help/en/backups.md` says the numbers are never used twice "also
  when you restore the same or an older backup again" (English only).

## TDD

`TestRepeatRestoreNeverReissuesNumbers` (restore, sell, close two same-day Zs,
restore the same backup again) failed on `main` with "next receipt 11001
re-issues a number up to 11002" and "next eod Z 5 re-issues a number up to 6".
It passes with the fix. Re-verified independently by the reviewer (stubbing
the merge call) and by the orchestrator for the review fix below.

## Review

- Dev: Sonnet. Independent review: Opus 5.5, fresh context, separate worktree.
  Verdict: **ship**. No blocker, high or medium finding.
- **L1 (low, fixed):** `liveReceiptMax` threw away the sales maximum when the
  counter read failed (and vice versa). Now each readable source counts and the
  errors are joined into the warning. `TestLiveReceiptMaxKeepsReadableSources`
  failed without the fix (`liveReceiptMax = 0, want 42000`) and passes with it.
- **L2 (low, deferred → ut-docs#6906):** a huge `receipt_no` or a floor near
  MaxInt64 overflows the jump to a negative floor, which is then ignored. Already
  reachable from a backup file before this change.
- Nits accepted: the help clause does not hold when the live DB is unreadable
  (the backup's floors are then kept, as for the identity); the live DB is opened
  twice during staging (harmless).
- Checked and sound: all values bound, LIKE escaped, floors parsed with
  `ParseInt` and ≥0 only; no string-built SQL beyond the fixed table names; no
  new file writes or paths; no migration; no new UI string.

## Tester

PASS (Opus 5.5, separate worktree). A built till binary plus a throwaway
harness: on `main` the second restore handed out receipt 000011001 and Z 5
again; with the fix it carried floors 11003 / Z 6 and continued at 000022001 /
Z 9. Seven deliberate mutants of the merge (no-op, min instead of max, ignore
each live source, accept another prefix's floor) each failed at least one
test. `go test -race ./internal/db/...` hit go's 10-minute timeout on the
4-core sandbox with no race reported; CI does not race `internal/db` and the
new code has no concurrency (accepted gap). Tester note on the restore marker's
`last_z` after a repeat restore → ut-docs#6906.

Gates: `gofmt -l .` empty, `go build ./...`, `go vet`, `go test ./internal/db/...
./internal/data/... ./internal/app/... ./internal/enroll/...` ok,
`golangci-lint run ./...` 0 issues, guards data-access, help-topics, fixed-dates,
i18n, compliance-claims pass.

Pre-flight: help topic `web/help/en/backups.md` updated; docs-shots n/a (no
screen change); `reference/` n/a (no page describes the jump); README n/a;
en.json/en.meta.json n/a (no key); ADR n/a (ADR-0166 §D and ADR-0199 unchanged
in substance); ut-cloud mirrors n/a; follow-ups filed as ut-docs#6906.

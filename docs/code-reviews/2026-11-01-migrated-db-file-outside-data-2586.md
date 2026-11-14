# Review: tests outside internal/data open the pre-migrated template (ut-docs#2586)

Date: 2026-11-01 · Branch: `test/2586-migrated-db-file` · Lane: `lane:cloud-41`
Author: Opus 5.5 (orchestrator, inline) · Reviewer: Sonnet 5.5, fresh context

## What shipped

Test-only. The helpers and test sites that opened a fresh SQLite file with
`db.Open(filepath.Join(t.TempDir(), name))`, replaying every migration on
each open, now use `db.Open(testsupport.MigratedDBFile(t, name))` (#2196's
template copy). That covers the 10 files the card names, plus 5 more that
the review found:

- `internal/server/server_test.go`: the telemetry test and every daily-backup
  test's `dbPath`.
- `internal/discovery`, `internal/app/provision_test.go` (helper),
  `internal/settings` (×2), `internal/cloudsync` (×2), `internal/pages/catalog`
  (×2), `internal/pages/common`, `internal/plugins/payment_key_validation_test.go`,
  `internal/pos` (×2).
- `internal/app/{customer_phone,item_sku}_backfill_boot_test.go`.

Kept fresh on purpose:
- `TestOpenWithRetry_SucceedsFirstTryAgainstAnAlreadyMigratedDatabase`
  simulates the service's own migration run.
- The seed-then-reopen tests (`demo_*`, `recovery_mode`,
  `printer_charset_boot`, `wasm_*`, `installer_marketplace`) were left alone.

There is no import cycle: `testsupport` depends only on `db`, `clock`,
`logging`, `paths`, `secrets`, `pihealth` and `fxlevel`. None of those is a
package under test here.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Nit | Five more plain "migrated DB" helpers outside the card's list. | Fixed in this branch. |
| 2 | Info | The template shares migration-seeded values across copies (091's `stock_upload_state.stream_id`, migration-time `now`). | Accepted. No converted test opens two migrated DBs and compares them (the reviewer counted calls). |
| 3 | Info | Server `dbPath` is also used for `config.DBPath`, `ListBackups`, `BackupDir`, `Snapshot` and `Dir(dbPath)/backups`. | Holds. Each copy sits alone in its own `t.TempDir()`. |

## Verified

- `gofmt -l internal/` printed nothing, and `go vet` was clean on all nine packages.
- `go test -count=1` passed on server, discovery, app, settings, cloudsync,
  pages/catalog, pages/common and pos. The changed plugins tests passed
  (`-run 'PaymentKey|Validate|FiscalSign'`).
- Timing for the same 8 packages, non-race, run one after the other on this
  container:

  | | main | branch |
  |---|---|---|
  | cloudsync | 65.2 s | 8.5 s |
  | pages/common | 19.1 s | 2.1 s |
  | pos | 17.3 s | 5.0 s |
  | pages/catalog | 14.6 s | 10.9 s |
  | settings | 11.4 s | 1.1 s |
  | app | 10.5 s | 8.8 s |
  | server | 8.9 s | 3.3 s |
  | discovery | 3.8 s | 4.2 s |
  | **sum** | **~151 s** | **~44 s** |

- Under `-race` on `main`, `internal/pos` took 429 s, and `internal/plugins`
  hit the 10-minute test timeout. The panic was in `openRealDB`'s migration
  replay, run while a reviewer's tests were also running. The full
  `internal/plugins` package timed out with and without this change in that
  contended run, so its whole-package -race time is not measured here.
  `-race` is a separate CI leg.
- No TDD claim to re-verify, because nothing in the product's behaviour changed.

## Verdict

Safe to merge. Deferred: the optional repo-wide guard that would extend
`internal/data/migrated_template_guard_test.go` (the card's optional
criterion) is not built.

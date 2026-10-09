# Review: re-run the phone_e164 back-fill on a country change (ut-docs#3992)

Date: 2026-10-09 · Lane: `lane:cloud-54` · Built by Sonnet, reviewed by Opus 5.5 (fresh context).

## What shipped

- `internal/pages/customer_phone_backfill_country.go`: `startCustomerPhoneE164BackfillForCountryChange`
  runs `BackfillCustomerPhoneE164` in the background on `d.AsyncWork` (2-minute
  timeout, best-effort, never on the request path).
- Called after the new country is persisted, on a real change, by every main-till
  writer of `store.country`: `/api/settings/save`, `/api/settings/upsert`, the
  cloud `set_setting` directive, the LAN `sync_settings` endpoint, and the setup
  wizard (which runs after first boot, when the boot back-fill saw no country).
- `internal/data/customer_phone_repo.go`: a run whose region is out of date now
  stops with `ErrPhoneE164RegionChanged`, a quiet stop. The check runs inside the
  write transaction (BEGIN IMMEDIATE via `_txlock=immediate`):
  - before each chunk write, `store.country` and the region marker must still
    match the run's region;
  - before a reset, `store.country` must still match.
  Without this check, an older run could write old-region values, or reset over a
  newer run's marker, and those values would stay until the next boot.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | An older run could still reset after a newer one (country X→A→B quickly), leaving A-values with country B until the next boot. | **Fixed.** The reset and write transactions re-read `store.country`. Test `TestBackfillPhoneE164_OutdatedCountryNeitherResetsNorWrites` fails without the fix. |
| 2 | minor | A replica's country changes come from the write-through mirror or the admin pull, so no re-run is added there. The main's values do reach the replica through the pull (`phone_e164` is not in `skipCols`), and the replica recomputes at its next boot. | **Accepted.** The boot comment now says so. The remaining gap is a newer replica following an older main without the column; it lasts until the replica reboots and the lookup still matches. |
| 3 | minor | Shutdown: the run uses `context.Background()` with a 2-minute timeout. After the bounded `AsyncWork` drain it can log "database is closed". | **Accepted.** Same pattern as the print and invoice async work, and each chunk is its own transaction, so no data is at risk. |
| 4 | nit | The LAN `sync_settings` and setup-wizard call sites have no test. Save, upsert and the cloud directive are tested. | **Accepted.** These are one-line calls at the same spot as the tested `queueBasePluginsForCountryChange`. |

The reviewer also checked:
- locking (`_txlock=immediate`, WAL, `busy_timeout`);
- the settings cache, which does not cache the marker or the country;
- WaitGroup use (`Add` before `go`, deferred `Done`, panics recovered; the race run was clean);
- that no SQL sits outside `internal/data`, no user-facing strings were added, and there are no file writes.

## Verification

- TDD was re-verified by the reviewer, red to green:
  - With the call sites removed, the three pages tests fail (`phone_e164 after DE -> GB = "+492079460958", want +442079460958`).
  - With the marker check removed, `TestWritePhoneE164Chunk_StaleRegionWritesNothing` fails.
  - With the finding-1 check removed, the new data test fails (`reset err = <nil>, want ErrPhoneE164RegionChanged`).
- Commands run:
  - `go build ./...` and `go vet` on data, app and pages;
  - `go test ./internal/data/ -run 'Phone|Caller'`;
  - `go test ./internal/app/ -run Phone`;
  - `go test -race ./internal/pages/ -run 'RebackfillsCustomerPhone|CountryChange|BasePlugin|CountryUnchanged'`;
  - `guard-data-access.sh`;
  - `gofmt -l`.
- golangci-lint was not run locally: the installed binary was built with Go 1.25 and the repo targets 1.27. CI runs it.
- Backend-only change: there is no UI surface and no help topic to update.

**Verdict:** safe to merge.

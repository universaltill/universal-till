# Review — setup wizard without import / sample data; import welcome page (ut-docs#3709)

**Date:** 2026-10-05 · **Author:** Opus 5.5 (dev subagents) · **Reviewer:** Fable 5.1 (independent, read-only)

## What shipped
- Setup wizard: the sample-data checkbox and the whole "restore from another POS" step (in-form section, out-of-form upload/preview, script, restore-only Alpine state) are gone; 7 steps (PIN = 6, done = 7). The handler ignores `demo_data` / `restore_choice` / `staged_import_id`.
- Finishing the wizard lands on `/import?welcome=1`, except a German TSE rejection, which still goes to `/?tse_setup=rejected`.
- `/import`: a welcome block with three option cards (import a file, load sample data, skip → start selling / add items by hand). There is always a Sample data card: a Load button when no sample data is loaded, otherwise a "loaded" note linking to Settings → Data. On a satellite the card shows the "use the main till" notice instead.
- `POST /api/import/sample-data`:
  - gated by `import_export`; cashier gets 403; denied in demo mode;
  - refused on a satellite (409, same as an import commit);
  - idempotent: "loaded" means both items and customers/promos are present, so a half-finished load can be retried;
  - serialised by `sampleDataMu`, so concurrent taps write one audit row; audit `sample_data_loaded`.
- No leftovers:
  - `wizard=1` import preview branch, `commitStagedImportForSetup`, `?staged_id` on GET `/import`;
  - the anonymous first-boot exemption and the `optionalAuth` middleware tier for `/api/import`;
  - the restore-prompt-deferred flow (Settings → Data block, `dismiss-restore-prompt`, lock entry);
  - the locale keys used only by these; help text in en/de/ar/fa/tr; README.
- Welcome-card copy shortened by the orchestrator after the UX look: at 1024×600 the long text pushed the buttons below the fold. The detailed explanations stay in the import and sample cards below.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| S1 | should-fix | `optionalAuth("/api/import")` existed only for the deleted anonymous wizard preview. An expired-session htmx POST then got a silent text 403 instead of the #2144 `HX-Redirect` to /login. | **Fixed** — tier removed. `TestImportRequiresASession` (htmx no-session / expired → 401 + `HX-Redirect`, non-htmx JSON 401, signed-in passes); seen failing first. |
| S2 | should-fix | Sample-data seeding had no satellite gate. Items, customers and promos are shop-wide, so the next admin pull would revert them silently. | **Fixed** — 409 `import.error.replica_use_primary` fragment; GET card shows the notice and no button. `TestImportSampleData_RefusedOnReplica`; the orchestrator re-verified it fails with the gate disabled (200 vs 409). |
| S3 | should-fix | Partial failure (catalogue seeded, customers/promos failed) → the retry said "already loaded" forever. | **Fixed** — `sampleDataState` requires both halves. `TestImportSampleData_RetryAfterPartialLoadSeedsTheRest`; seen failing first. |
| N1 | nit | A double tap could audit twice. | **Fixed** — `sampleDataMu` (waits rather than rejecting, so no new message key); `TestImportSampleData_ConcurrentLoadsAuditOnce` failed with the lock removed. |
| N2 | nit | Stale comments pointing at removed restore-resume code. | **Fixed** — reworded in settings.html, settings_page.go, voucher_import.html, setup_base_plugins_test.go. |
| N3 | nit | "Welcome once" is query-param only (Back / bookmark re-shows it). | Accepted — harmless, matches the design. |
| N4 | nit | The new e2e SAMPLE-badge assertion wasn't run by the reviewer. | Verified by the orchestrator: full auth e2e project 44/44. |
| — | info | `guard-deadcode-baseline.sh` fails on `internal/logging/file.go`. | Pre-existing on `origin/main`, unrelated to this diff. |

## Verified beyond automated tests
- Driven runs on a fresh install: wizard shows 7 dots and lands on `/import?welcome=1`. Load sample data → "52 sample items…", and `/items` shows SAMPLE badges. Screenshots at 1024×600 and 360×740 in en + fa were looked at. After the copy fix, all three option buttons are above the fold at 1024×600 (en and fa).
- e2e: auth project 44/44 after the fixes; default-project import specs (friendly errors, view-catalog shell, items-shell dialog) 9/9.
- `make docs-shots` regenerated; `guard-docs-shots.sh` fresh.
- Go: build / vet / gofmt clean; `go test` pages, auth, data, httpx green; guards i18n, data-access, help-topics, help-drift, no-showmodal green.

## Not verified
Real touch hardware, dark theme, German rendering (the de pack lands after core).

## Verdict
Safe to merge.

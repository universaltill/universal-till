# Review: API-source import rows (ut-docs#4429, ADR-0161)

- **Date:** 2026-10-15
- **Card:** ut-docs#4429, step 1 of the Epos Now migration epic ut-docs#4426
- **Built by:** Opus 5.5 (dev subagent), plus orchestrator fixes
- **Reviewed by:** Fable (independent subagent, its own worktree)
- **Design:** ut-docs ADR-0161

## What shipped

- `import` manifest entries may declare `"source":"api"` (`internal/plugins/manifest.go`, validated at `PersistManifest` and `Rollback`). `POST /api/data/import` runs such an entry with no file and `mode=preview|commit` (`internal/pages/import_api_source.go`). File entries are unchanged; the multipart reader moved to `readImportMultipart`.
- New host function `import_rows_submit` (`internal/plugins/wasm_import_rows.go`, SDK `ImportRowsSubmit`):
  - bound to the hop-0 `import.requested.ask` of the same plugin;
  - resolved entities only;
  - 500 rows per call, 20,000 rows and 32 MiB per run;
  - a run that breaks a cap is refused whatever the plugin answers.
- New neutral package `internal/catsync`:
  - parses and validates the ADR-0051 item rows;
  - linked rows are updated;
  - unlinked rows are linked by barcode, then SKU, or else created;
  - stock goes through an `import_sync` stock movement;
  - preview writes nothing;
  - each row has its own savepoint;
  - commits run in chunks of 100 processed rows.
- Migration `080_import_source_links.sql`: checksum pinned, never LAN-synced (`nonAdminTables`).
- Repository additions: `catalog_sync_repo.go` and `import_source_links_repo.go`. Shared category and tax helpers, so both importers use one rule.
- Audit: one `import_sync` entry per committed run, holding the plugin, source and counts. A run that stops part-way is audited with `partial: true`.
- **ut-cloud** (`feat/4429-signer-import-source`): `CanonicalEntry.Source` mirrors the new field, so the marketplace signer keeps it.

## Findings and outcome

| # | Severity | Finding | Outcome |
|---|---|---|---|
| O1 | Major (found by the orchestrator in Dev's report) | One transaction for the whole commit held SQLite's write lock for a 20k-row run, longer than the 5 s busy timeout, so a sale at the counter would fail. | **Fixed:** chunked commits. ADR-0161 §3 amended. Tests `TestRun_CommitsInChunks` and `TestRun_FailedRunKeepsCommittedChunksAndHealsOnReRun`. |
| O2 | Major (orchestrator) | Category and tax ids were cached across a row savepoint rollback, so after one failed row every later row in that category failed. | **Fixed:** caches are cleared on rollback. `TestRun_FailedRowDoesNotPoisonCategoryOrTaxCache` failed first ("could not apply" on both rows). |
| R1 | Major | Chunks counted only *applied* rows. The DSN is `_txlock=immediate`, so an all-unchanged re-run (the steady state of a mirror) held the write lock for the whole run. | **Fixed:** chunks count processed rows. `TestRun_UnchangedReRunStillCommitsInChunks` failed first (1 chunk, want 3). |
| R2 | Major | A rolled-back row kept its in-run barcode, SKU and item claims, which made false "more than one row" issues. | **Fixed:** `releaseClaims` on rollback. `TestRun_FailedRowReleasesItsClaims` failed first. |
| R3 | Minor | A barcode added on update was inserted as a second primary. | **Fixed:** primary only when the item has none. `TestRun_AddedBarcodeOnUpdateIsNotASecondPrimary`: the first version passed without the fix (the new barcode was not first); rewritten, then confirmed failing without the fix ("primary barcodes = 2"). |
| R4 | Minor | Apply errors logged the barcode text. | **Fixed:** only the error type is logged. |
| R5 | Minor | `cleanText` rejected ZWNJ/ZWJ, which correct Persian spelling and emoji need. | **Fixed:** they are allowed; bidi overrides are still refused. `TestRun_NameAllowsZWNJButNotBidiOverride` confirmed failing without the fix. |
| R6 | Minor | No UI and no help topic. | **Accepted:** this card is the mechanism only. The operator screen and its help topic are ut-docs#4431, which is blocked on this card. |
| N1 | Nit | `FindTaxCodeByRate` was dead code. | Removed. |
| N2 | Nit | `samples` came back as `null` when empty. | Fixed: now `[]`. |
| N3 | Nit | A preview failure said "could not be applied". | Fixed: separate preview wording. |
| N4 | Nit | `normalizeBarcodes` reads the symbology settings once per row. | Deferred. It runs before the transaction (no lock held); per-run caching is a follow-up. |

## Verified beyond automated tests

- **Mutation checks by the orchestrator:**
  - disabling the per-call cap made `TestImportRowSink_EntityGateAndCaps` and `TestImportRowsSubmit_RealWasmModule` fail;
  - ignoring price changes made `TestRun_UpdateLinkedItem` and the end-to-end real-WASM handler test fail.
- **The reviewer independently confirmed:**
  - the plugin-trust boundaries: no `source` on the wire, the entity gate, the per-run sink isolation, and the strip of a config-smuggled `source`;
  - input validation of price, VAT, stock, text, barcodes and category depth;
  - the LAN-sync classification;
  - the WASM ABI bounds;
  - no new file writes and no cwd-relative paths.
- **No UI is touched,** so there was no visual check. The UX phase was skipped by design: the API returns English JSON like the rest of `/api/data/*`, and there are no locale keys.

## Gate

The rerun after the fixes:
- `go build ./...` and `go vet ./internal/...` pass.
- Full `go test ./...` passes (no `-race` on plugins/pages, as in CI).
- `go test -race ./internal/catsync` passes.
- The data-access, core-neutral and sdk-hostfns guards pass.
- Locally, `guard-deadcode-baseline` and `guard-store-private` fail on files this branch does not touch: a macOS awk/tooling difference. Both are green on Linux CI for `main`, so the PR's CI is authoritative.

## Verdict

Safe to merge once CI is green. Merge order: universal-till first, then ut-cloud. ut-cloud's manifest-contract job reads universal-till `main`, so it goes green once this merges.

## Deferred

- N4: per-run cache of the barcode symbology settings (perf only).
- Customers: ut-docs#4437. Epos Now plugin: ut-docs#4430. Till connect screen: ut-docs#4431.

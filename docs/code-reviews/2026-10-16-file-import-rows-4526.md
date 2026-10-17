# Review — file-import plugins may submit rows (ut-docs#4526)

- **Date:** 2026-10-16 · **Lane:** lane:local · **Built by:** Opus 5.5 · **Reviewed by:** Fable 5.1 (independent security review), plus the author's own review recorded in ut-docs (`code-reviews/2026-10-16-file-rows-import-contract-4526.md`)
- **Design:** ADR-0164 §2 "file-rows contract" (amendment in universaltill/ut-docs#4524), ADR-0161 §2 amended.
- **Scope:**
  - **Manifest:** `"source": "file"` entries with `file_formats` and `file_headers` (2–16 columns, at least one that isn't a neutral catalogue column).
  - **Sandbox:** during a file-rows run only `log_write`, `import_file_size/read/close` and `import_rows_submit` work; every other host function returns -2, enforced fail-closed by a runtime-package test. 32 MiB per file; 500 rows per call, 20,000 rows and 32 MiB per run.
  - **Rows:** the host stamps the source; catsync validates; `external_id` required; one `import_sync` audit entry naming the file's SHA-256.
  - **Access:** managers only; commits on the main till only.
  - **Detection:** `catimport.HeaderRow`/`HeadersClaim`/`NeutralHeader`. An ambiguous claim goes to the standard importer.
  - **Route:** `POST /api/import/from-file` commits the staged preview; only the operator who previewed it can commit it.
  - **SDK:** `ImportRequest`, `FileImportHostFunctions`, `FakeHost.FileImport`.
  - Four `import.file_plugin.*` keys; a help bullet in 5 locales.

## Verified
- root, sdk/plugin and catimport `go build/vet/test`, golangci-lint v2.14.0 (0 issues), gofmt.
- A real wasip1 guest in `TestFileImportSandbox_RealWasmModule`.
- Removing the sandbox check or the header match fails the tests.
- Import e2e specs 16/16, run alone.
- CI build-job guards pass. Six fail only on macOS tools and pass on Linux; the shellcheck version check needs 0.9.0, and no `.sh` file changed.
- **docs-shots:** branch and clean HEAD in the same amd64 container (Chromium 141) gave no differing PNG, so only the manifest's surface and catalog text hashes changed.
- The reviewer verified that the sandbox is an allow-list at runtime, that -3 → -2 broke nothing, and that authZ, CSRF and demo/kiosk handling are correct.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | ut-cloud `CanonicalEntry` lacks `file_headers`, so a marketplace-signed file-rows plugin is refused at install (fails closed) | ut-cloud mirror PR right after this merges (its contract test compares against universal-till main); required before any Epos Now file plugin |
| 2 | Low/Med | Claims made only of neutral columns shadow the standard importer | Fixed: `NeutralHeader` (reuses catimport's synonyms); a claim needs at least one vendor-specific column. Two claimants go to the standard importer (a chooser is a possible follow-up) |
| 3 | Low | Staged copy owned per entry, not per operator | Fixed: the session user is in the owner |
| 4 | Low | A double-tap consumed the staged copy | Fixed: restaged on busy |
| 5 | Low | XLSX header sniff before the 32 MiB cap; ZIP magic regardless of extension | Fixed: cap first; workbook only for `.xlsx`/`.xlsm` (catimport API change, so v0.2.0 tag) |
| 6 | Low | FakeHost closes returned 0 in the sandbox | Fixed: -2 |

**After merge:**
- tag `catimport/v0.2.0` and `sdk/plugin/v1.1.0`;
- the ut-cloud `CanonicalEntry.FileHeaders` mirror;
- language-pack PRs for the 4 keys.

**Verdict:** safe to merge.

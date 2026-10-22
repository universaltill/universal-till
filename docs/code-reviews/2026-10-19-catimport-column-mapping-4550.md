# Review: catimport column mapping for generic files (ut-docs#4550)

Date: 2026-10-19 · Lane: `lane:cloud-41` · Author: Opus 5.5 · Reviewer: Fable 5.1 (independent subagent, one round)

## What shipped

`catimport` (the shared parser module, ADR-0164 §1) now takes a shop's own
column mapping for a generic CSV/XLSX file. This is the parser half of
ADR-0164 §4's "generic header mapping".

- `Mapping` maps a neutral field to the file's header. `""` means the
  field isn't read.
- New API: `MappableFields`, `ValidateMapping` and `IsGenericFormat`.
- `ParseMapped` / `ParseXLSXMapped` apply the mapping. `Parse` /
  `ParseXLSX` delegate to them with a nil mapping, so their behaviour is
  unchanged.
- A bad mapping gets a `*MappingError` with a stable code:
  `unknown_field`, `header_not_found`, `header_ambiguous`,
  `header_reused` or `not_generic`.
- A mapped column feeds only its own field. A field whose synonym column
  the mapping took falls back to its next synonym.
- Known exports (Loyverse, Square, SumUp, Shopify) refuse a mapping.
- The XLSX merged-cell guard covers mapped columns.

Released as `catimport/v0.3.0`. ut-cloud's `require` is bumped in the same
cycle. The card was split: #4682 (streaming XLSX iterator) and #4683
(ut-cloud `PUT …/mapping`).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | When the mapping took a synonym-matched column, that field was dropped and never re-resolved. Example: `Title,Name,Price` with `{description: Title}` validated, then failed with `ErrNoNameColumn`. | **Fixed.** The synonyms are re-matched with the claimed columns blanked. Test: `TestParseMapped_EvictedSynonymReResolves`; it fails on the pre-fix code (verified). |
| 2 | major (pre-existing) | XLSX: `HeaderRow` reads the first non-blank row, while `ParseXLSX` reads `rows[0]`. | **Deferred to #4684.** It fails closed (`header_not_found`), never with wrong data. The fix touches the merged-cell rectangle's row numbering. |
| 3 | major (pre-existing) | CSV: `HeaderRow` strips the BOM before reading and uses `LazyQuotes`; `Parse` does neither. | **Deferred to #4684.** It fails closed, and the file fails in `Parse` without a mapping too. |
| 4 | minor | `ValidateMapping` reports only the first problem. | **Accepted.** The order is deterministic, so a mapping UI can show one problem at a time. Revisit with #4683 if the screen needs all of them. |
| 5 | minor | A consumer would have to hard-code which formats count as generic. | **Fixed.** `IsGenericFormat` is exported. |
| 6 | minor | An untrusted header was echoed whole in `Error()`. | **Fixed.** Headers over 1 KiB are refused as `header_not_found`, and error fields are truncated to 64 bytes. Test: `TestValidateMapping_OversizedHeader`. |
| 7 | nit | Tests were missing for finding 1, for generic-ERP and for an oversized header. | **Fixed.** Tests added. `TestParseMapped_NilMappingIsParse` stays as a smoke test; the existing suite is what pins nil-mapping identity. |
| 8 | nit | `shopify_type` isn't in `formatOnlyFields`. | **Accepted.** Adding it would change `NeutralHeader("Type")`, which is part of plugin `file_headers` claim validation, so it is out of scope here. The test exempts it by name. |
| 9 | process | The change needs a tag and a ut-cloud pin bump. | Done in this cycle (`catimport/v0.3.0`). |

## Verified

- `cd catimport && go vet ./... && go test -race ./...` passes, `golangci-lint` reports 0 issues, and `guard-catimport-module.sh` is green.
- TDD claims were re-verified inline: with the column-claim rule reverted, `TestParseMapped_MappedColumnLeavesItsSynonymField` fails. With the pre-fix `mapping.go`, `TestParseMapped_EvictedSynonymReResolves` fails. Both pass once restored.
- No UI, locale, migration or till-side caller changed. The till still calls `Parse` / `ParseXLSX`.

## Verdict

Safe to merge.

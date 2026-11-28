# Review record: pin the mode-entry compared fields to catalogpull.uploadFields (ut-docs#7214)

Reviewer: Opus 5.5 independent subagent (Dev was Sonnet 5.5), plus the orchestrator's read of the diff and a separate Opus Tester.
Pre-flight: walked (BA/Architect notes, Dev, Review, fix round, Tester). User-visible behaviour: none (backend sync bookkeeping only); `web/help/` n/a; en.json n/a; ADR n/a (ADR-0162 §6 unchanged); no migration.

## Change
- `internal/data/catalog_mode_entry_marks.go`: new exported `ModeEntryCompareFields` (item, category; `uploadFields` order). `otherFieldsMatchBase` iterates it instead of the local `vals` map, so a listed field with no local value is an error (fail closed), not silently uncompared. `icon` is now compared too (it is always the skipped field today, but a second promoted field must still compare it), normalised like catalogpull's `iconOut` (`modeEntryIcon`: an id failing `iconid.ValidFormat` counts as "").
- `internal/catalogpull/mode_entry_fields_7214_test.go`: `TestModeEntryCompareFieldsMatchUploadFields_7214` pins each entity's list to `uploadFields`, and every `data.PromotedUploadFieldKeys` entity must have a list.
- `internal/data/catalog_mode_entry_compare_7214_test.go`: icon compared when skip is another field; an invalid stored icon counts as none (item and category); an unknown listed field errors.

## Findings
1. Low, fixed: icon was compared raw while catalogpull `readLocal` sends `iconOut(icon)`. Once a second field is promoted, a row with a legacy invalid icon would never be marked and the pull would reset the new field. Now `modeEntryIcon`, with a test (mutation of either branch back to raw fails it).
2. Low, accepted: the missing-value error rolls back the whole `SetManyMarkingCatalogModeEntry` write, so `cacheEntitlement` keeps raising a problem each tick. That is a programmer error only, caught in CI by the pin test and `UnknownListedFieldErrors_7214`; fail-closed beats the #7214 silent skip. Commented at the error.
3. Nit, accepted: the pin is order-sensitive (`reflect.DeepEqual`), stricter than needed but matches the doc comment.
- Info: the net-quantity halves are still compared one at a time where `changedFields` drops both when the base lacks either; errs toward not marking; pre-existing.

## Verification
- TDD: both new tests failed first (data: ok=true want false; catalogpull: did not compile).
- Mutation (scratch copy): dropping icon / barcodes from the lists, raw icon on item or category, removing the missing-value error, reverting the fix, a field only in uploadFields or only in the list, a reorder — each fails a test.
- Gate, go1.27.1: gofmt, build, vet clean; golangci-lint 0 issues; `internal/data` and `internal/catalogpull` full suites pass; `-race` on the 7214/6243/6240/icon/outbox/promote subset passes (full `-race` of catalogpull exceeds 30 min on this box). 197 of 200 build-job guard commands pass; the 3 others fail identically on origin/main here (no shellcheck binary, catimport tags not fetched) or were flaky under load and passed on re-run (`play-upload_test`). `internal/cloudsync` `TestChannelOrderVendoredSchemaIsCurrent` fails pre-existing (ut-docs#7213).
- Out of scope, per the card: merging the mark SQL with `MarkUnsentPromotedFields` waits on ut-docs#6242.

## Verdict
Approve (fixes in).

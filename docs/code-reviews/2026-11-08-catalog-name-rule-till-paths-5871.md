# Review — till directive and catalogue-pull paths apply the single-line name rule (ut-docs#5871)

**Date:** 2026-11-08 · **Branch:** `fix/5871-catalog-name-rule`
**Author model:** Sonnet (dev subagent) · **Tester + Reviewer:** Opus 5.5 (independent subagents; review in a separate worktree)

Pre-flight: web/help n/a (no new screen or flow; the till's admin entry points already refused these names with `common.error.name_invalid_characters`, only my.-originated writes change) · reference/ updated (ut-docs `reference/coding-standards.md` §4, `reference/contracts/cloud-catalog-feed.md` 1.1.1 note + changelog, same session) · README n/a · en.json/i18n-meta n/a (no keys) · mirrors n/a (`internal/data/singleline_name.go`, mirrored by ut-cloud, is unchanged) · ADR n/a · review record: this file · follow-ups: #5908 (legacy CSV import reason), #5909 (agentaction `cleanText`).

## What shipped
- New `internal/data/received_name.go`: `checkReceivedName(stored *string, received string)` — trims both sides; an unchanged stored name passes (legacy tolerance), anything else goes through `CheckSingleLineName`. Lives outside `singleline_name.go` because ut-cloud's mirror test parses that file.
- The check runs inside the tx, after the row loads and before the first write, on every catalogue write path my. reaches: items (`saveItemTx`, `CreateItemTx`, `CatalogRepo.CreateItem` — the `create_item` directive's path the card missed — and `SetItemName`, now one tx and storing the trimmed name, item or variant fall-through), categories (`saveCategoryTx`, `UpdateCategory` now in a tx, `UpdateCategoryPartial`, `CreateCategoryWithColor` delegating to the checked admin variant), modifier groups/options (`saveGroupTx`, `replaceOptionsTx` per option id, `CreateGroupWithOptions`), option sets/values (`saveOptionSetTx`, `replaceOptionSetValuesTx` per value id), variants (`saveVariantTx`), tax codes (`SaveTaxCodeTx`).
- `normalizeOptionSetValues` still refuses control characters even when unchanged (#3319 parking); that error now wraps both `ErrOptionSetValueInvalid` and `ErrNameInvalidCharacters`.
- `cloudRenameItem` and `cloudCreateItem` trim the received name, so dedupe, audit row and result message use the stored value.
- Catalogue pull needs no new code: a refused row's savepoint rolls back, the previous version is kept and the failure lands in `cloud_apply_failures`.

## Tests
- The three "Unchecked/StillAccepts" regression tests in `singleline_name_paths_test.go` are replaced by "Checked" tests ranging `catimport/catalogrules/namecases` (item, category, group, `CreateGroupWithOptions`, option set, variant, tax code), each asserting a refused call writes nothing.
- `TestSingleLineName_ReceivedNameLegacyTolerance` (13 update paths: unchanged bad name applies, changed-to-bad refused, padded stored name tolerated), `TestCheckReceivedName_*`, `TestRetireOptionSet_LegacyBadNameStillRetires_5871`.
- catalogpull: `TestPullRefusesABadNameKeepsPreviousVersion`, `TestPullItemNamesRangeNameCases`, `TestPullOptionSetBadNewValueRefusedWholeRow`, `TestPullResendsStoredLegacyBadNameUnchanged`, and (Tester-added) `TestPullRefusesBadNamesOnEveryNamedKind`.
- pages: `TestCloudRenameItem/SaveItem/CreateItem_RefusesBadName` against a migrated DB (no write, no audit row on refusal).
- `TestSingleLineName_EnrolmentRegisterRefused` (#5862) untouched.

## Review findings
- **F1 (medium) — fixed.** `RetireOptionSetTx` writes a till-generated freed name `name + " (id)"` through `saveOptionSetTx`, which now ran the received-name check; a legacy bad-named in-use set could not be retired, so a pull tombstone was refused and the set stayed active on the till. Fix: unexported `ownName` flag on `OptionSetSave` set only by `RetireOptionSetTx`; regression test red ("name contains a line break…") → green.
- **F2 (low) — deferred → #5908.** Till legacy CSV import previews a refused name as OK, then fails it at commit with generic `item_failed`.
- **F3 (low) — deferred → #5909.** agentaction `cleanText` lets U+2028/2029 and U+E0100–E01EF through; the batch fails with a raw English error.
- **F4 (info) — accepted.** For an unchanged stored control-character option-set value (no path could store one) the admin message becomes `common.error.name_invalid_characters` instead of `catalog.option_sets.value_invalid`; both translated, not a 500.
- **F5 (info) — fixed.** `cloudCreateItem` now trims before dedupe/audit.
- **Orchestrator (low) — fixed.** Test literals carried raw invisible characters (U+202E/U+202D/U+200B) in Go source; rewritten as `\u` escapes so the source shows no bidi trickery.

## Verification
- Tester mutation runs (17 mutants, one per checked path, incl. the tolerance helper): each turned at least one test red; the pages directive tests fail on their own mutants. `git diff` md5 identical before/after.
- Reviewer re-verified personally in a separate worktree: `CreateItem` check removed → `ItemDirectivePathsChecked/CreateItem` red (`err = <nil>, want ErrNameInvalidCharacters`); `replaceOptionSetValuesTx` check removed → `SaveOptionSetChecked/SaveOptionSet_new_value` red; restored → green, files `cmp`-identical.
- Gates: gofmt clean, `go build ./...`, `go vet`, `go test` data/catalogpull/pages/pos, golangci-lint 0 issues, guard-data-access, guard-fixed-dates and the other ci.yml guards pass. `guard-deadcode-baseline.sh` (deadcode built with go1.26 vs go1.27 module) and `guard-shellcheck-version.sh` (no shellcheck binary) could not run in the sandbox; no `.sh` changed and no exported symbol removed — CI runs them.
- No driven app run: no UI; directive dispatch (hook error → `failed`) is unchanged generic code, hooks exercised directly against a real migrated DB.

## Verdict
Approve (one medium finding fixed in review, regression-tested).

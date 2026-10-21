# Review — catsync: in-run claim before the link check (ut-docs#4740)

- **Date:** 2026-10-21
- **Branch:** `fix/4740-catsync-claim-before-link`
- **Author:** Sonnet 5.5 (Dev subagent); **reviewer:** Opus 5.5, fresh context.

## What shipped

`internal/catsync/catsync.go` `plan()`: for an unlinked row whose code match
finds item `cand`, the in-run `claimedItem` check now runs before
`ExternalIDFor`. Before, on a commit, an earlier row of the same run had
already written its link under `Source`, so a later row matching the same
item by barcode or SKU reported `item_linked_elsewhere`; a preview had
written nothing and reported `item_already_matched`. Both modes now report
`item_already_matched`. A link that predates the run still reports
`item_linked_elsewhere` (`TestRun_AlreadyLinkedAndApplyFailedCodes`).

Test: `TestRun_SecondRowMatchingClaimedItemIsAlreadyMatchedInPreviewAndCommit`
runs row 1 (barcode match) and row 2 (SKU match, same item) in preview and
commit on fresh DBs.

## TDD re-verified

Orchestrator reverted the fix, ran the test: fails in commit mode with
`IssueCode:item_linked_elsewhere IssueArgs:[p-1], want one "item_already_matched"`.
Restored: passes. The reviewer reproduced the same in a throwaway copy.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Low | Row Y adopts item C via a LegacySource link while C already carries a Source link from an earlier run to record X; a later row Z code-matching C used to report `item_linked_elsewhere(X)`, now `item_already_matched`. | Accepted: still an issue in both modes, now the same in both; only the record id drops out of the message. |
| 2 | Info | Apply-failed rows release their claims on commit only (preview never applies), so a later row may proceed on commit but not in preview. | Accepted: pre-existing and inherent to apply failures; not touched here. |
| 3 | Info | The #4734 record listed this gap as accepted (finding 7). | Note added there pointing here. |

Paths checked by the reviewer: linked path (unchanged; post-branch claim
check still covers it), LegacySource adoption (now consistent too), name
fallback (`match()` already skips claimed items), chunk boundaries
(`claimedItem` is runner-wide and survives `commitOpen`).

## Gate

`gofmt -l .` empty; `go build ./...`; `go vet ./internal/catsync/`;
`go test -race ./internal/catsync/ ./internal/data/...` ok;
`golangci-lint run ./internal/catsync/...` 0 issues; 52 of 54 `ci.yml`
guards pass locally — `guard-deadcode-baseline.sh` (local deadcode binary
built with Go 1.26 vs go1.27 module) and `guard-shellcheck-version.sh` (no
shellcheck in the container) can't run here and are left to CI. No UI, help
or locale text changes: `import.issue.item_already_matched` already reads
correctly for the new behaviour.

**Verdict:** safe to merge.

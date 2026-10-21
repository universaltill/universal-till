# 2026-10-21 — catsync: exact-name fallback across import entries (ut-docs#4734)

## What shipped
- `internal/catsync`: an unlinked row with **neither barcode nor SKU** that
  the barcode/SKU steps can't place matches an item by exact name — only an
  item a sibling entry of the same plugin linked (`<plugin>#<other entry>`
  or the bare-id legacy source), not yet linked under the row's own source,
  and only when exactly one candidate is left (ADR-0161 Amendment 2).
- Decided per run: `snapshotNameFallback` reads the candidates once, before
  the first transaction, so preview and commit agree. Never a name candidate:
  an item another row of the run claims, names by barcode/SKU, or adopts
  through its legacy link. Two or more code-less rows of one name → no
  name match for any of them.
- `internal/data.ImportSourceLinkRepo.SiblingItemsByName`: one query per
  run, name → item ids. Prefix test is a `[p#, p$)` range on the source
  index (no LIKE, so `_`/`%` in plugin ids are literal). No migration.

## Review
Built by Sonnet; round 1 reviewed by Opus (fresh context, isolated
worktree), fix round written inline by Opus and reviewed by Sonnet.

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | High | A new code-less row listed first took the item a later row adopts through its legacy link; that row then failed `item_already_matched` on every run. Reproduced. | Fixed: legacy-adopted items reserved in the snapshot; test `NeverTakesLegacyAdoptedItem` (renamed upstream row, so the name count can't mask it — verified by mutating out `reserved`). |
| 2 | High | Preview vs commit disagreed on two same-name code-less rows (or an earlier in-run claim): the per-row query hid items linked under own source mid-commit. Reproduced. | Fixed: per-run snapshot + `codelessNames` count + claimed-item filter; tests `SameNameTwiceInRun`, `SkipsItemClaimedInRun`, both modes. |
| 3 | High (perf) | Per-row query scanned every link of the plugin inside the IMMEDIATE write tx (100 lookups vs 20k links = 2.4 s, against a sale's 5 s busy timeout). | Fixed: one query per run through the pool, before the first tx. |
| 4 | Low-Med | A code-less row could take an item a later row names by barcode/SKU. | Fixed: those items are reserved too; test `NeverTakesItemALaterRowMatchesByCode` (fails with `reserved` mutated out). |
| 5 | Low | Test gaps: `pp#a` check ran in the wrong order; price update unasserted; no `p"#a`/`p$a` boundary case. | Fixed. |
| 6 | Low | `codelessNames` also counts rows already linked under own source, so a re-sync with a linked "Tea" plus a new code-less "Tea" never name-matches. | Accepted: conservative, matches the ADR wording ("two or more code-less rows of one name"); preview and commit agree. |
| 7 | Low | Pre-existing: a mid-run claimed item reports `item_linked_elsewhere` in commit vs `item_already_matched` in preview. | Accepted, not introduced here; row is an issue either way. Fixed later by ut-docs#4740 (`2026-10-21-catsync-claim-before-link-4740.md`). |
| 8 | Info | Snapshot can go stale if an item is renamed mid-run. | Accepted: deletion is handled by `SyncItemState`; one commit per entry at a time. |

## Verified
- TDD: the fallback tests failed with "new" counts before the fix (re-checked
  by the round-1 reviewer by reverting the block); the three round-2 tests
  failed before the snapshot fix; mutation of `reserved` fails the two
  reservation tests.
- Gate: gofmt, `go build ./...`, golangci-lint 0 issues, data-access guard,
  `go test` for catsync, data, pages (+ subpackages), plugins.
- No UI, locale, help or migration change; `web/help` describes only the file
  importer's matching, so no manual update.

## Verdict
Safe to merge.

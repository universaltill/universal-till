# Review — import link source per entry (ut-docs#4727)

**Date:** 2026-10-21 · **Lane:** lane:cloud-41 · **Built by:** Sonnet subagent ·
**Reviewed by:** Opus 5.5 (orchestrator), different model from the author.
**ADR:** ADR-0161 amendment (2026-10-21), ut-docs branch `adr-0161-amendment-4727`.

## What shipped
- `internal/pages/import_api_source.go`: links are stamped
  `importLinkSource(plugin, entry) = "<plugin id>#<entry key>"`, no longer the
  bare plugin id. The audit `source` uses the same value. An API entry and a
  file-rows entry of one plugin therefore keep separate link namespaces: the
  second entry matches the first's items by barcode, then SKU, and adds its
  own link instead of hitting `item_linked_elsewhere`.
- `internal/catsync`: `Options.LegacySource` (the page passes the bare plugin
  id). A row with no link under `Source` is looked up there. A hit counts as
  linked and is re-stamped under `Source` on commit (`relink`; an otherwise
  unchanged row reports `updated` with change `link` once). The fallback is
  never used for the linked-elsewhere refusal. Old rows stay. There is no
  migration.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | The `Options.Source` doc comment still said "the importing plugin's id". | Fixed. |
| 2 | minor | A row whose candidate item carries a bare legacy link to a *different* external id of the same entry is no longer refused. The fallback skips the refusal on purpose, because a bare link may belong to the other entry. | Accepted. The ADR amendment records it. It is rare, only affects tills that imported before this release, and the cost is one item linked twice. |
| 3 | minor | Items with neither barcode nor SKU still have no cross-entry match (no name matching), so a second entry duplicates them on its first run. | Accepted, out of scope (the card's ask covers barcode/SKU). A Backlog card is filed. |

No file writes or paths were added, so the MkdirAll and paths.Data checks don't apply. No raw SQL was added outside `internal/data`. No i18n, UI or pixel change, so the docs-shots surface hash was refreshed with `update-docs-shots-surface-hash.sh` (`Docs-Shots-Unchanged: true`).

## TDD re-verified by the reviewer
- Reverting `catsync.go` (keeping only the new field) makes `TestRun_LegacySourceLinkIsAdopted` fail with `preview: {New:1 …}`, which is the duplicate. With the fix restored it passes.
- Reverting `import_api_source.go` to the bare plugin id makes `TestFileRowsImport_JSONAPI_PreviewThenCommit` fail with `links = 0, want 2 under the per-entry source`. With the fix restored it passes.
- `TestRun_TwoSourcesShareItemByBarcode` and `TestRun_LegacyLinkNeverLinkedElsewhere` are regression guards. They pass on the old catsync because the bug was in the page's source value.

## Gates
`gofmt`, `go build ./...`, `go vet`, `go test` for catsync, pages and data,
`golangci-lint` (0 issues), and the guard-data-access, core-neutral,
docs-shots and i18n guards all pass. In this container, `go test ./...` timed out in the untouched `internal/plugins` (wasm, 10 min cap). CI is the judge.

## Verdict
Safe to merge. User manual: no topic describes cross-entry behaviour. A sentence is proposed on ut-docs#4729, which already edits the `catalog` topic and regenerates its shots.

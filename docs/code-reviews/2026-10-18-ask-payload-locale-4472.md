# Review: import/export ask payloads carry the operator's locale (ut-docs#4472)

Date: 2026-10-18 · Lane: `lane:cloud-41` · Complexity: easy
Built by: Sonnet (Dev subagent) · Reviewed by: Opus 5.5 (independent subagent, fresh context, in a separate worktree)

## What shipped
- `exportRequestPayload`, `importRequestPayload` (`internal/pages/data_api.go`),
  `apiImportRequestPayload` (`import_api_source.go`) and
  `fileRowsRequestPayload` (`import_file_rows.go`) gain `locale`. It is filled
  at every construction site by the new `pluginAskLocale(r)`:
  `httpx.RequestLocale(r)` (no cookie side effect on an API POST) matched onto
  an installed locale with `httpx.MatchLocale`, else the shop default.
- SDK `plugin.ImportRequest` gains `Locale`. The README documents it, including
  that an older till sends it empty, so fall back to `en`.
- ut-docs `reference/plugin-manifest.md` and `reference/plugin-host-functions.md`
  document the field (ut-docs branch `docs/4472-ask-payload-locale`).
- The Epos Now plugin answering in that locale is split to ut-docs#4648
  (`blocked:dep` on this change).

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `locale` was the raw `?lang=` value (`DE`, `xx` or a 4 KB string reached the plugin as typed), so the SDK's "BCP-47 like de" promise couldn't be relied on. | **Fixed:** `pluginAskLocale` matches onto an installed locale, else the shop default. `TestPluginAskLocale_MatchesInstalledLocale` covers `TR-tr`→`tr`, `fa_IR`→`fa`, `xx`/`<script>`/empty→`en`. The plugin-view asks (`plugin_view.go`) still send `ResolveLocale` unmatched. That predates this change, is out of scope, and their JSON encoding and manager gate keep it harmless. |
| 2 | nit | The import payloads had no field pin, so the SDK's `ImportRequest` could drift unnoticed. Only export had one. | **Fixed:** `TestImportRequestPayloads_PinnedFields` pins all three import payloads. |
| 3 | process | No review record. | This file. |

Construction sites: the reviewer grepped every `AskPlugin` call and every struct
literal. The only export/import asks are `data_api.go` (export),
`import_dispatch.go` (file import) and `import_api_source.go` (api and
file-rows), and each sets `Locale`.

## TDD verification
- Dev: the four handler tests and the export pinned-field test failed before the
  change (`payload lacks locale`).
- Reviewer, in a separate worktree: reverted the production files to `origin/main`.
  All four `*_PayloadCarriesLocale` tests and `TestExportRequestPayloadSchema_PinnedFields`
  failed, and the SDK test failed to build (`req.Locale undefined`). Restored, everything passed.
- After the fix for finding 1, the orchestrator swapped the matcher back to the raw
  `RequestLocale`. `TestPluginAskLocale_*` failed (`got "TR-tr", want "tr"`, etc.),
  and so did the handler tests (`"locale":"tr-TR"`). Restored, everything passed.
  The handler tests send `?lang=tr-TR`, so they cover the matching end to end.
  `de` is not a core locale (it ships as a language pack), so the tests use `tr`.

## Gates
`gofmt -l .` clean · `go build ./...` · `go vet ./internal/pages/` ·
`go test ./...` and `sdk/plugin` `go test ./...` · `golangci-lint run ./...` 0 issues ·
every `guard-*.sh` in `ci.yml` passes locally, except two that are
environmental here and don't touch this diff: `guard-deadcode-baseline.sh`
(the local `deadcode` binary was built with go1.26 and the repo needs go1.27)
and `guard-shellcheck-version.sh` (no `shellcheck` in this container). CI
runs both.

## Not covered
No UI surface changed, so there was no visual check. No e2e: the change is a
payload field, covered at the handler layer.

## Verdict
Safe to merge.

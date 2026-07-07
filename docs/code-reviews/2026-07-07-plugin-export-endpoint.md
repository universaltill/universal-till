# Code Review — Plugin export endpoint + UI action

- **Date:** 2026-07-07
- **Branch:** `feature/plugin-export-endpoint`
- **Author/Reviewer:** Claude Opus 4.8
- **Scope:** `internal/plugins/exporter.go` (traversal guard), `internal/pages/plugin_api.go`
  (handler + route), `internal/pages/plugin_export_test.go`,
  `internal/plugins/exporter_test.go` (traversal test),
  `web/ui/pages/plugins.html` (Export action).

## Summary

Exposes the offline plugin export (added previously as a library `Exporter`) through
the POS admin UI, symmetric with the existing manual **import** (`POST
/api/plugins/import-from-file`). An operator can now download an installed plugin as a
`.tar.gz` bundle and side-load it on another, possibly offline, till.

- **Endpoint:** `GET /api/plugins/{id}/export?version=<v>` (`handleExportPlugin`) —
  exports via `plugins.NewExporter("./data/plugins")` to a temp file, then streams it
  as an `attachment` (`Content-Disposition`, `Content-Length`, `X-Bundle-SHA256`) and
  removes the temp file. Version comes from the query string since a plugin may have
  multiple installed versions; the plugins page passes `p.currentVersion`.
- **UI:** an **Export** action (a `download` anchor) in each installed plugin's action
  row in `plugins.html`, gated on `st.installed && p.currentVersion`, matching the
  sibling Rollback/Uninstall buttons.
- **Hardening:** `Exporter.Export` now rejects path traversal — the resolved
  `{base}/{id}/{version}` must stay under the base dir — because `id`/`version` now
  arrive from an HTTP request. Covered by `TestExport_RejectsPathTraversal`.

## Notes / decisions

- **Reuses the accepted side-loading surface.** Manual file import already exists and
  is wired, so exposing export (which is *read-only* — it only packages what is already
  installed) introduces no new policy: it is strictly less privileged than the existing
  import.
- **Base dir `./data/plugins`** is hardcoded to match `handleImportFromFile`; both share
  the same limitation. Making it configurable is a separate cleanup (would also let the
  handler be unit-tested without touching the working tree).
- **Handler test scope.** The handler's own logic is argument validation; the
  archive/round-trip behaviour is covered by the `internal/plugins` exporter tests
  (round-trip, validation, missing-manifest, traversal). `TestHandleExportPlugin_RequiresVersion`
  asserts the 400 branch, which returns before any filesystem write (so no repo
  pollution). The deeper 404/stream paths are exercised through the exporter unit tests.
- **`d *common.Deps`** is unused by the handler (it resolves the base dir itself) but
  kept for signature parity with the sibling `handleXxx(d)` handlers.
- **Label i18n.** "Export" is a raw label to match the adjacent raw buttons
  (Disable/Enable/Rollback/Uninstall); string-externalization is documented-only, not
  gated, and adding a lone locale key here would be inconsistent with its neighbours.

## Verification

`go build ./...`, `go vet`, `bash scripts/ci/guard-data-access.sh`, and `go test ./...`
all green. New tests: `TestExport_RejectsPathTraversal`,
`TestHandleExportPlugin_RequiresVersion`; existing plugins-page render test still green.

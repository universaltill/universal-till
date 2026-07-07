# Code Review — Plugin offline export (bundle) + round-trip

- **Date:** 2026-07-07
- **Branch:** `feature/plugin-offline-export`
- **Author/Reviewer:** Claude Opus 4.8
- **Scope:** new `internal/plugins/exporter.go` (+ `exporter_test.go`).

## Summary

Adds the **export** half of offline plugin bundles (epic 1-1 AC3). The **import**
half already existed (`internal/plugins/importer.go` — extraction with
path-traversal guards, signature verification, disk budget, DB persist), but there
was no way to *produce* a bundle. `Exporter` packages an installed plugin directory
(`{baseDir}/{id}/{version}`) into a `.tar.gz` rooted at the plugin dir, so
`manifest.json` sits at the archive root — exactly the layout `Importer.Import`
expects. Result: an operator can export a plugin on one till and import it on an
**offline** till (device provisioning / migration) with no marketplace connectivity.

- `Exporter.Export(ctx, req)` validates id/version/dest, requires `manifest.json`
  in the source dir, walks the tree, and writes a gzip-compressed tar. It hashes the
  compressed bytes on the way out (via `io.MultiWriter`) so `ExportResult` carries
  the bundle `Size` + `SHA256` with no second read.
- Symmetric to the existing `Importer`: same base-dir convention, same archive
  format (`ImportFormatTarGz`), round-trips straight back through `Import`.

## Notes / decisions

- **Entry types.** Only directories and regular files are emitted; symlinks/devices
  are skipped rather than written as headers the importer would ignore. This also
  sidesteps exporting anything that could re-materialize outside the target on import
  (the importer additionally enforces its own path-traversal guard).
- **No DB / no SQL.** Export is a filesystem operation; the exporter holds no DB
  handle, so the data-access guard is trivially satisfied. It exports whatever is on
  disk under the plugin dir (the install layout); it does not re-query install state.
- **Signatures.** The exported `manifest.json` retains whatever signature it was
  installed with, so a signature-checking import re-verifies against the marketplace
  key. The round-trip test uses `SkipSignature` (offline/dev) with a nil verifier;
  production import against a configured verifier is unchanged existing behavior.
- **Partial-file cleanup.** Writers are closed in order and the destination is
  removed on any archive error (`firstErr` collects the first failure across the
  walk + both `Close()`s).
- **`ctx`** is currently unused but kept to mirror `Importer.Import` and to allow
  cancellation if export grows to stream large trees.

## Integration boundary (follow-up, intentionally out of scope)

Like the existing `Importer`, `Exporter` is a **library** capability — neither is
wired to an admin endpoint or CLI yet. A thin handler/CLI to trigger export+import
(and, later, multi-plugin bundles) is a natural follow-up; this change delivers the
missing library half so the round-trip exists and is tested.

## Verification

`go build ./...`, `go vet`, `bash scripts/ci/guard-data-access.sh`, and
`go test ./...` all green. Tests: `TestExportImportRoundTrip` (export → import on a
fresh base dir + DB; files + contents intact; DB row persisted; recorded checksum
matches the file), `TestExport_Validation`, `TestExport_MissingManifest`.

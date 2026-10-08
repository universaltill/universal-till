# Code review — plugin view file uploads + upload_open/read/close (ut-docs#3793)

- **Date:** 2026-10-08 · **Lane:** lane:cloud-24 · **Card:** universaltill/ut-docs#3793 (ADR-0121 build 4b, part of epic #2848; on the path to #2851)
- **Author:** Opus 5.5 dev subagent · **Reviewer:** Fable (independent, different model), plus the orchestrator's own re-verification

## What shipped

- `form` field kind `file` in plugin view documents. It is allowed only on a page entry that declares
  `config.upload_max_mb` (an integer 1–32). That value is validated at ParseManifest, PersistManifest
  and Rollback, and read back through `data.PageEntryRow.UploadMaxMB`.
- Multipart action posts are streamed with `r.MultipartReader` and never spooled by `mime/multipart`.
  Limits:
  - the whole post is bounded by `http.MaxBytesReader`;
  - at most 4 files, one per field;
  - each file is capped on bytes actually written, and an over-cap file's field goes to `invalid`.
- Each staged file reaches the plugin in `upload_handles` as `{field, handle, filename, size, content_type}`.
  `content_type` is sniffed. `filename` is a base name with control and format characters stripped.
- Host functions `upload_open` / `upload_read` / `upload_close` (`internal/plugins/wasm_upload.go`),
  following the `import_file_*` precedent. Tokens are 128-bit, from crypto/rand, and bound to the plugin.
  There are caps of 8 staged uploads and 8 open handles per plugin.
- Every staged file is deleted on whichever of these comes first:
  - `upload_close`;
  - the end of the `ui.action.ask`;
  - the end of the job it started (finish, deadline, abandon or panic);
  - plugin unload, disable or reload (`uploads.CloseAll` on both the Sync drop path and the reload path);
  - every error path in the handler.
- Docs: `docs/plugin_guidelines.md`, the user manual `web/help/en/plugins.md` (one sentence on
  file fields; `make docs-shots` re-run, no pixel change), and ut-docs `reference/plugin-views.md`,
  `plugin-host-functions.md` and `plugin-manifest.md` (separate ut-docs PR).

## Findings (Fable review)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | No permanent test for the `MaxBytesReader` whole-post overflow path | **Fixed.** Added the "body past the whole-post bound" subtest (5 MB on a 1 MB entry → 400, plugin not asked, nothing staged). |
| 2 | minor | `uploadFilename` kept Unicode format characters (U+202E, ZWJ), so a label could display as another name | **Fixed.** `Cf` is stripped too; `TestPluginViewUpload_FilenameLabel_3793` added. |
| 3 | minor | The user manual didn't mention plugin pages asking for a file | **Fixed.** One sentence in `web/help/en/plugins.md`; docs-shots regenerated. |
| 4 | nit | Windows: a read in flight while an unload removes the file can leave one `ut-view-upload-*` temp file (no boot sweep) | **Accepted.** Same as the existing `ut-import-*` staging. |
| 5 | nit | `CloseAll` keeps the handle counter, while `importFiles` resets it | **Accepted.** Never reusing a handle number is strictly safer. |

The reviewer checked and passed these: temp-file ownership on every path, token scoping (another
plugin's token → -1, malformed → -4, cap → -6, tested with a real wasip1 guest), the size cap, no
stdlib spool, install validation (`1.5`, `"8"`, `0`, `33`, `true`, `null`, `1e9` all refused),
the template, logical CSS, no new i18n strings, and SQL only in `internal/data`.

Found by the gate after review: `uploadRegistry.count` was used only by tests, and
`guard-deadcode-baseline` flagged it. It moved into the test file.

## Verified beyond the reviewer

- I re-checked the TDD claims myself by reverting each fix and running its test:
  - removing `ReleaseUploads` after the ask makes `TestPluginViewUpload_PayloadAndCleanupAfterAsk_3793`
    fail with "staged upload temp files left behind";
  - removing the `Cf` strip makes `TestPluginViewUpload_FilenameLabel_3793` fail on U+202E and U+200D;
  - with both restored, both tests pass.
- Gate:
  - gofmt clean; `go build ./...`, `go vet ./...` and `golangci-lint run ./...` (v2.14.0 rebuilt for
    go1.27) all clean, 0 issues;
  - `go test ./...` passes;
  - all 42 guards in `ci.yml`'s build job pass, except two that are environmental:
    - shellcheck is not installed here, and no shell script changed;
    - deadcode can't analyse `cmd/unitill-desktop` without GTK headers. Its only remaining hits are
      `internal/logging` functions reached from that root.
- Not driven in a browser: no shipped plugin has a file field yet (the first is #3873's identify
  seam). The rendered form (`enctype`, `hx-encoding`, `<input type="file">`) is asserted in
  `TestPluginViewUpload_RenderFileField_3793`.

## Verdict

Safe to merge once CI is green.

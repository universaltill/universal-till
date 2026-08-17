# Review: `import_dispatch` stages an uploaded file exactly once, not twice (ut-docs#613)

**Card:** universaltill/ut-docs#613 — "import_dispatch: uploaded file is
staged to disk twice (ParseMultipartForm + a second io.Copy)"
**Complexity:** medium. **Build:** Sonnet (inline). **Review:** Opus
(fresh-context subagent, independent of the build, isolated worktree).

## What was asked

`POST /api/data/import`'s handler (`internal/pages/import_dispatch.go`)
called `r.ParseMultipartForm(4 << 20)` — net/http's own spool of the whole
upload, to memory or (past 4MiB) to its own `multipart-*` temp file — and
then made a **second**, separate `os.CreateTemp` + `io.Copy` to build the
plugin-facing staged file. Peak disk (or RAM, if `TMPDIR` is tmpfs) was up
to 2x the upload size, up to 2GB at the current 1GB cap — undercutting the
ADR-0001 amendment's "never buffered/staged more than once" intent on the
low-power till hardware this endpoint targets.

The card suggested checking whether the pre-existing `POST /api/import`
(`internal/pages/import_page.go`, the core CSV/.bkp importer) shared the
same shape.

## Scope verification (BA step)

Inspected `import_page.go` directly: it calls `ParseMultipartForm(20<<20)`
then passes the `multipart.File` straight into `sniffZipUpload` /
`catimport.Parse` / `catimport.ParseBkp` — no `os.CreateTemp`, no second
`io.Copy` anywhere in the file. It does **not** share the bug and was left
untouched. The independent review re-checked this with its own grep and
confirmed the call was correct.

## Fix

Replaced `ParseMultipartForm` + `FormFile` + `FormValue` with a direct
`r.MultipartReader()` loop: the `file` part streams straight into the one
`os.CreateTemp` staged file (same `io.Copy`-through-`io.LimitReader`
streaming pattern `catimport.ParseBkp` already uses), and the small
`entry_key`/`entities` fields are buffered as their parts are encountered
— field order on the wire is caller-controlled, so every existing
validation (installed-entries check, entry_key resolution, entities/
permission-grant filtering) still runs only after all parts are consumed,
preserving the original order of checks and every existing error
string/status code.

## TDD

Regression test written first
(`TestImportDispatch_StagesUploadExactlyOnce`, asserting directly on
`mime/multipart`'s own `multipart-*` temp-file naming convention for a
5MiB upload — past the old 4MiB in-memory threshold), confirmed failing
against the pre-fix handler with a real assertion failure (`upload was
staged via mime/multipart's own spool too`, not a compile error), then the
fix applied and the test confirmed passing. All 13 pre-existing
`import_dispatch_test.go` tests were re-run unmodified and stayed green
throughout — same status codes, same error strings.

## Independent review (Opus, fresh context, isolated worktree)

Verdict: **FAIL on first pass** — two regressions in the part-loop
rewrite, both real, both fixed before merge.

1. **BLOCKER — a second `file` part orphaned the first staged temp file
   permanently.** The loop overwrote `tmpPath` on every `file` part; the
   cleanup defer only ever removed the *last* one. Proven with a scratch
   test: a two-`file`-part request returned 200 and leaked one
   `ut-import-*.upload` forever — a real disk-accumulation risk on the
   constrained till hardware this issue exists to protect, and the same
   scratch test passed against the pre-fix code, confirming it was a
   regression introduced by the rewrite, not pre-existing. **Fixed:** a
   second `file` part is now rejected outright (`400 "invalid upload"`)
   instead of silently accepted or replaced.
2. **SHOULD-FIX — a filename-less field named `file` was accepted as the
   upload.** The switch keyed on `part.FormName()` alone, never
   `part.FileName()`; pre-fix, `r.FormFile("file")` required an actual
   file part (mime/multipart only populates the `.File` map for parts
   with a filename). Proven: pre-fix a plain `file` text field 400'd with
   `"file required"`; post-fix it 200'd and dispatched with
   `file_name":"."` — garbage input for any plugin doing extension
   sniffing. **Fixed:** the file-part match now requires
   `part.FileName() != ""`; a filename-less `file` field falls through to
   the default (ignored) case, same as before.
3. **Nit (accepted, not fixed):** `entry_key`/`entities` are no longer
   readable from the URL query string — `r.FormValue` merged query and
   body, `MultipartReader` reads only the body. No in-repo caller relies
   on this; body-only is the saner contract for a POST endpoint. Recorded
   here as the "documented behavior change" the reviewer asked for.
4. **Nit (accepted, not fixed):** an over-long `entry_key`/`entities`
   value is truncated at `importFormOverhead` (1MiB) rather than
   rejected outright, which could surface as a confusing 404 instead of a
   clear error. Negligible in practice — no real caller sends anything
   close to that length for a lookup key.
5. **Nit (fixed):** the regression test globbed the shared OS temp dir,
   which could race concurrent `go test ./...` packages that also spool
   multipart uploads. Fixed by scoping the test to a private `TMPDIR`
   (`t.Setenv("TMPDIR", t.TempDir())`), which `os.TempDir()` respects on
   every call.

Two new regression tests were added for findings 1 and 2
(`TestImportDispatch_DuplicateFilePartDoesNotLeak`,
`TestImportDispatch_FilenamelessFieldNamedFileRejected`), each confirmed
failing against the pre-fix-for-these-findings code (real leak/real 200,
not compile errors) before the fixes landed, then confirmed passing after.

Verified clean by the reviewer, independent of the build:
- Every `multipart.Part` closed on all branches; the `staged` flag flips
  only after `OpenImportFile` returns nil, matching that function's
  documented ownership-transfer contract
  (`internal/plugins/wasm_import_file.go`) — no double-remove, no race
  (all locals are per-request).
- `os.MkdirAll` — N/A: `os.CreateTemp("", ...)` resolves to
  `os.TempDir()`, which the OS guarantees exists; unchanged from pre-fix.
- `paths.Data(...)` — N/A: a transient per-request spool deleted before
  the response, not persistent app data.
- No `.html`/`web/` file in the diff — ux-guidelines and help-topic
  checks correctly skipped; no i18n keys needed (every `dataAPIRespond`
  literal in the new code is byte-identical to a pre-existing one in this
  same file, matching that file's established plain-English API-error
  convention).
- No real client/shop name, no literal secret — only the synthetic
  `com.t.imp1` plugin ID already used by the existing suite.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...` — clean.
- `go test ./internal/pages/... -run TestImportDispatch -v` — all 15
  tests pass (13 pre-existing + 2 new from the review round).
- `go test ./internal/pages/...` (full package, no filter) — clean.
- `go test ./...` (whole repo, all 38 tested packages) — clean, run twice
  (once after the initial fix, once after the review-round fixes).
- `bash scripts/ci/guard-data-access.sh`,
  `bash scripts/ci/guard-i18n.sh`,
  `bash scripts/ci/guard-kiosk-engine.sh`,
  `bash scripts/ci/guard-plugin-menu-read.sh` — all clean, re-run after
  the review-round fixes.
- TDD claims independently re-verified: the original double-staging
  regression test was reverted and re-run by the reviewer in an isolated
  worktree (`git worktree`, per ut-docs#386) and confirmed a genuine
  assertion failure quoting the leaked `multipart-*` path, then restored
  and confirmed passing again. The two review-round regression tests were
  each confirmed failing against the pre-fix-for-that-finding code before
  their fixes landed.

## Safe-to-merge verdict

**Safe to merge.** The core single-staging fix was correct on the first
pass; the review round found two real regressions the rewrite introduced
(one leak-class blocker, one validation gap), both fixed and covered by
new regression tests, with the full gate re-run clean afterward. Two nits
(query-string form values, silent long-value truncation) are recorded
above as accepted, out of scope for this card.

# Review: sweep leftover staged-upload temp files (ut-docs#3955)

Branch `fix/3955-sweep-staged-upload-temp`. Author: Opus 5.5 (lane:cloud-24).
Reviewer: Fable, fresh context, in a separate worktree (reviewer ≠ author
model; the card is `complexity:easy`, built inline on the session model).

## What shipped

- New package `internal/stagedupload` owns the temp-file name patterns the
  till stages operator uploads under (`ut-view-upload-*.upload`,
  `ut-import-*.upload`, `ut-import-stage-*.upload`) and
  `PruneOlderThan(cutoff)`: removes only regular files matching those
  patterns in `os.TempDir()` (never dirs, never symlinks; a file that
  vanished meanwhile is not an error).
- The four `os.CreateTemp("", …)` call sites in `internal/pages` use the
  package's constants, so the names the sweep matches can't drift
  (`TestPatternsMatchCreateTempNames`).
- `internal/housekeeping`: new `staged_uploads` sweep and retention-table
  row. Housekeeping runs ~2 min after boot, then daily, so this covers the
  card's "at startup and in the housekeeping pass".
- Manual: one bullet under "Automatic clean-up" in
  `web/help/{en,de,tr,fa,ar}/backups.md`.

**Deviation from the card: 24 h, not 1 h.** The card's premise ("1 h is
longer than the longest legitimate lifetime") is wrong for import previews:
`restageCatalogUpload` restarts a staged copy's 1 h TTL without touching the
file's mtime, and the registry prune is lazy, so a copy can still be taken
after 1 h by mtime. A 1 h sweep would break a preview committed 90 min later.
24 h is far past every live use; a preview kept beyond that is already past
its TTL and fails cleanly with `import.error.stage_expired`.

## Findings (Fable)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | The three `internal/server` `TestRunHousekeeping_*` tests call `runHousekeeping` without redirecting `stagedupload.Dir`, so they swept the real system temp dir | Fixed: `isolateStagedUploads` helper in all three; verified with an old canary file in `TMPDIR` that survives the run |
| 2 | minor | `MaxAge` comment claimed no live owner can hold a file 24 h; a lazily-unpruned, already-expired preview can | Fixed: comment states the real guarantee (only an expired preview, which fails cleanly) |
| 3 | minor | DE bullet "ausging" colloquial; the manual uses "ausgeschaltet" | Fixed: "ausgeschaltet wurde oder abgestürzt ist" |
| 4 | nit | `ut-bkp-*.db` / `ut-bkp-docs-*.zip` (catimport .bkp import) leak the same way and are larger | Deferred: ut-docs#3985 |
| 5 | nit | Sweep compares mtime with wall clock; a Pi with no RTC that steps its clock >24 h within 2 min of boot could lose a live file | Accepted: same exposure as the existing issue-report/update sweeps |
| 6 | nit | `TestPackageNeverTouchesTheDatabase` doesn't follow imports transitively | Accepted: `stagedupload` imports only the standard library |

Reviewer confirmed: no pattern over-match (`.upload.keep`, `.csv`,
`ut-bkp-*`, other programs' files rejected and tested); symlinks/dirs
skipped; races with a live request harmless; several tills sharing one temp
dir covered by the same bound; tests fail on broken code.

## Verified beyond automated tests

- TDD: removing the `KindStagedUploads` line from `housekeeping.Run` fails
  `TestRun_RemovesOnlyWhatTheRetentionTableAllows` ("staged_uploads removed
  0, want 1"); restored, it passes. Mutations in `PruneOlderThan` (drop the
  regular-file check; drop the age check) each fail `stagedupload` tests.
- Gate: `go build ./...`, `go vet`, `gofmt -l`, `go test ./...` (the
  `internal/pages`/`internal/plugins` packages re-run alone with a longer
  timeout), every guard in `ci.yml`'s build job. Environment-only failures
  here: no `shellcheck` binary, `deadcode`/`golangci-lint` built with an older
  Go than the module's 1.27 — CI runs them.
- `docs-shots`: the `internal/pages` edits change only the `CreateTemp`
  pattern argument (no rendered pixel), so the surface hash was refreshed
  with `update-docs-shots-surface-hash.sh` (`Docs-Shots-Unchanged: true`).
- No UI surface changed apart from the manual text; no screenshots needed.

## Verdict

Safe to merge after fixes 1–3 (done).

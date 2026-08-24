# Code review: http.Error raw-leak sweep, increment 1 (ut-docs#924)

## What shipped

ut-docs#924 tracks the remaining ~67 `http.Error(w, err.Error(), status)`
raw-error-leak call sites across `internal/pages` (the same defect class
ut-docs#316/#893 already fixed 26+ instances of) — too large for one PR, so
the card's own acceptance criteria calls for splitting it. This is
**increment 1**: 7 call sites across 5 files, the smallest, most contained
slice (each file had 1-2 sites; the remaining ~9 files with ~53 sites are
deliberately deferred — see below).

Files touched: `internal/pages/external_api.go`, `import_page.go`,
`issue_report_page.go`, `backup_api.go` (2 sites), `settings_page.go`
(2 sites). Each site now routes through `common.LogAndLocalizedError` —
the same helper `catalog/handlers.go`/`plugin_api.go`/`sync_api.go` already
use — logging the real error server-side and showing the operator a
translated message instead.

Locale keys: 4 existing keys reused (`ext.error.unreachable`,
`import.export_save_failed`, `issuereport.save_error`,
`settings.error.save_failed`) where the call site is provably the same
failure an existing sibling code path already surfaces with that key. 2 new
keys added to all four locale files (`en`/`ar`/`fa`/`tr`):
`settings.backup.stage_failed` (restore-staging failure) and
`settings.backup.download_failed` (added after review — see below).

7 new regression tests (one per call site), each forcing a **real**
failure (a dropped DB table, a regular file blocking a directory
`MkdirAll` needs, a NUL byte in a plugin route, a validly-named but
nonexistent backup file) and asserting both that the localized message
appears and that the specific raw error fragment does not.

## Independent review

Opus, fresh context, isolated git worktree (complexity:medium →
Sonnet-builds/Opus-reviews per the model-routing rubric). Verdict:
**safe to merge**, one should-fix (fixed), one nit (fixed).

**Should-fix, fixed**: `backup_api.go`'s download handler mapped its
`db.BackupDir` failure to `settings.backup.failed` ("Backup failed") —
the same string the *create-a-backup* handler uses, and semantically wrong
for a download failure (an operator downloading an existing snapshot
would read "Backup failed" as their backup having been lost, not that the
download itself failed). Worse, the sibling `save-copy` handler's
*identical* `db.BackupDir` failure already used a different, correct key
(`settings.backup.save_failed`) — same error, same file, two different
messages. Added `settings.backup.download_failed` ("Could not download
the backup.") to all 4 locales and repointed the download handler at it.

**Nit, fixed**: `backup_api_test.go`'s
`TestRestoreBackup_MissingSnapshotIsLocalized` had a dead `_ = dbPath`
assignment (needed by a sibling test, not this one) — reduced to
`mux, _, _ := newBackupTestDeps(t)`.

**Independently re-verified**, not taken on trust:
- Every one of the 5 touched source files' revert produced the exact
  raw-error string the corresponding test's assertion is written to catch
  (pasted verbatim in the review transcript) — including a deliberately
  *finer* check the brief didn't ask for: `settings_page.go` has two
  independent call sites in one test function that `t.Fatalf`s on its
  first failing assertion, so a whole-file revert only ever exercised the
  first (telemetry) site. The reviewer additionally reverted *only* the
  second (fiscal) hunk in isolation and confirmed that site's test also
  fails correctly on its own.
- All 4 locale files: valid JSON, exact key-set parity, correct sort
  position for both new keys.
- The `web/help/img/manifest.json` diff is a single-line hash refresh with
  no `.png` diffs — confirming the docs-shots re-run genuinely changed no
  pixels (4 of the 5 touched files register a screenshotted route, so
  their whole-file hash is in-scope for the guard regardless).
- No manual/help-topic update owed: every touched route is under `/api/`
  (denylisted from route-coverage), no page, capability or screen changed
  — only the text of failure-path messages that already existed as
  generic (untranslated) errors before.
- No secrets, no real client/shop names.
- The two recurring bug classes this pipeline watches for (`os.MkdirAll`
  missing on a file-write handler; a cwd-relative path where
  `paths.Data(...)` belongs) — both clean; the one cwd-relative default in
  this area (`issuereport.PendingDir`) is already overridden at startup by
  `internal/pages/init.go` via `paths.Data(...)`, untouched by this diff.
- Test isolation: the two `DROP TABLE`-based tests each run against a
  fresh on-disk temp-file DB (`t.TempDir()`), not a shared DSN — no
  cross-test poisoning.

**Process note surfaced by review, actioned**: the review's worktree
branched from a slightly stale base; the fix branch has since been rebased
onto current `main` (which gained ut-docs#942's PR in the interim) and
every check below re-run post-rebase, including a fresh locale key-parity
check (1583 keys, zero drift, in all four locales).

## Verified beyond automated tests

- `gofmt -l .`, `go build ./...`, `go vet ./internal/pages/...` — clean.
- Full CI-blocking guard suite (all 16 guards in `.github/workflows/ci.yml`'s
  `build` job, including a real `make docs-shots` run for the docs-shots
  guard) — green, both pre- and post-rebase.
- Full test suite matching CI's actual invocation —
  `go test $(go list ./... | grep -v '/internal/plugins$')` plus
  `go test -timeout 20m ./internal/plugins` — all green, post-rebase.
- TDD claim re-verified independently twice: once by the Dev step (revert
  all 5 files, confirm all 7 tests fail with the right symptom, restore),
  once by the Reviewer step in an isolated worktree (same check, plus the
  extra per-hunk isolation check on `settings_page.go` above).

## Explicitly deferred (not fixed here, tracked separately)

1. **Remaining #924 scope** — ~9 files / ~53 sites not touched by this
   increment. Follow-up backlog cards opened for the next increment(s),
   grouped by subsystem (see the cards linked from #924's closing comment).
2. **Audit log raw error text** (`backup_api.go`'s restore handler still
   records `err.Error()` verbatim in the admin-visible audit payload) and
   **`common.LogAndLocalizedError`'s use of stdlib `log.Printf`** (bypasses
   the app's leveled/structured logger — a property inherited from #316's
   original helper, not introduced here) — both real, both pre-existing
   across every #316/#893/#924 call site, neither a regression from this
   diff. New backlog card opened to decide and fix centrally rather than
   patch site-by-site.
3. **API envelope** (`{"data": …, "error": null}`) — these `/api/...`
   error bodies are `text/plain` from `http.Error`, pre-existing at every
   one of these 7 sites before this diff too. Noted for the same follow-up
   card as #2 rather than blocking this increment on it.
4. **`ut-plugin-language-{de,es}` packs** — the two new locale keys need
   the standard follow-up there; `lang-pack-drift` is advisory-only on this
   PR (only blocking on push to `main`) and the German pack is already
   deep in a known, separately-tracked gap (ut-docs#297, ~917 keys behind)
   — this PR does not materially change that already-red state.

## Safe-to-merge verdict

Safe to merge. Should-fix and nit from independent review both fixed and
re-verified; all CI-blocking guards green; full test suite green
(matching CI's real invocation) both before and after a rebase onto
current `main`; TDD claim re-verified independently twice, including a
finer per-hunk check the initial brief didn't request.

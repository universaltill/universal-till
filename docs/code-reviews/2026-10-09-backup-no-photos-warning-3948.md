# Review: Back up now warns when the photos could not be added (ut-docs#3948)

Date: 2026-10-09 · Branch: `fix/3948-backup-no-photos-warning` · Built by Sonnet, reviewed independently by Opus 5.5 (fresh context, own worktree).

## What shipped

- `POST /api/backup/now` (`internal/pages/backup_api.go`): when
  `SnapshotWithAssets` returns a path **and** an error (DB snapshot kept,
  photos failed), the response is now a warning span
  (`row-warn-icon` ⚠ + `settings.backup.done_no_photos`) instead of
  "✓ Backup created". Prune, the `backup_created` audit row with
  `photos_error`, and `X-UT-Response: ok` (Backup region refresh) are
  unchanged. Total failure (no path) still answers 500 as before.
- Test seam `var snapshotWithAssets = db.SnapshotWithAssets` (package
  precedent: `discoveryBrowse`).
- New key `settings.backup.done_no_photos` in en/ar/fa/tr; de/es/pt follow
  in their `ut-plugin-language-*` packs.
- Manual: step 1 of `web/help/{en,de,ar,fa,tr}/backups.md` explains the
  warning (same list item — no structural drift).
- Review fix: the three backup-now messages are HTML-escaped with
  `template.HTMLEscapeString`, matching the repo convention.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Low | Translated text written into HTML unescaped (new line and the neighbouring ✓/✗ lines); a pack string with `<`/`&` would inject raw markup. | **Fixed** — all three lines escaped. |
| 2 | Low | "photos" understates that the receipt logo is also missing. | Accepted — "photos" is the help page's existing term. |
| 3 | Low | Pre-uninstall snapshot (`cmd/unitill-uninstall`) and daily auto-snapshot (`internal/server`) still ignore the photos error. | Deferred → ut-docs#3991. |
| 4 | Low | Fake calls `t.Fatalf` inside the injected func. | Accepted — runs on the test goroutine via `ServeHTTP`. |

Reviewer also traced: the warning survives `UT.refreshRegion` (keeps
`#backup-msg` innerHTML like the ✓) and the elevation-approved retry
(`elevation-done` → `refreshRegion(#backup-msg)`); RTL uses logical CSS
only; no `t.Parallel()` in the package, seam restored via `t.Cleanup`;
ar/fa/tr strings and de/ar/fa/tr help sentences match their files' terms.

## Verified

- TDD: `TestBackupNow_PhotosFailureShowsWarningNotSuccess` failed before
  the fix (`expected the no-photos warning text, got: <span>✓ Backup
  created</span>`), re-verified independently by the reviewer by reverting
  only the handler block; passes with it.
- `go build ./...`, `go vet`, `gofmt`, backup tests, full `go test ./...`,
  `golangci-lint`, i18n / help-drift / help-topics / compliance /
  competitor-naming and other guards.
- Not driven in a browser: forcing the photo step to fail in a running
  till needs a fault the container (root) can't produce via permissions;
  the rendered fragment is asserted at handler level and the refresh path
  was traced in code. CI's Playwright run covers the unchanged ✓ path.

**Verdict:** safe to merge.

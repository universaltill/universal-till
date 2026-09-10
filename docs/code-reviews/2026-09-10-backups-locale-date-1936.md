# Code review — settings backup-file list date not locale-aware (ut-docs#1936)

**Date:** 2026-09-10
**Branch:** `fix/1936-backups-locale-date`
**Card:** ut-docs#1936 — Settings backup-file list date (`.backups[].Date`) is still a hardcoded raw format, not locale-aware — follow-up to #1894/#1632/#1130
**Reviewer:** independent fresh-context Sonnet subagent (`complexity:easy`, per the model-routing table), no sight of the implementer's reasoning
**Verdict:** PASS — safe to merge. No findings.

## What shipped

`internal/pages/backup_api.go`'s `listBackupsForUI` set
`Date: b.ModTime.Format("2006-01-02 15:04")` — a hardcoded Go layout,
ignoring locale entirely. This was the exact site #1894 named as a
*separate, still-open gap* in its own review (ut-docs#1936 was filed from
that miscited old comment: `backup_api.go`'s function had been cited as
the already-correct reference implementation for the reset-archives table
next to it, which is how the drift surfaced).

Fix: `listBackupsForUI` now takes a `locale string` parameter and formats
via `httpx.FormatDateTime(b.ModTime.Local(), locale)` — the same function
the reset-batches list a few lines away in `settings_page.go` already
uses. The one production call site (`settings_page.go`'s `registerSettings`
handler) already resolves `locale := httpx.ResolveLocale(w, r)` earlier in
the same function, so the change is a straight parameter thread, no new
resolution logic.

New regression test `TestListBackupsForUI_DateIsLocaleAware` (alongside
the existing `TestListBackupsForUI_FormatsRealSnapshots`, updated for the
new signature) pins a real on-disk snapshot, formats it through both `en`
and `de-DE`, and asserts: the old ISO-ish `YYYY-MM-DD ` shape is gone for
both, the two locales' outputs actually disagree (de-DE dot-separated vs.
en slash-separated — proving the locale argument is wired through, not
silently ignored), and each matches `httpx.FormatDateTime` called
directly. Same pattern #1894/#1632 established.

`web/help/img/manifest.json` was regenerated via `make docs-shots` (112/112
screenshots passed) — only the `surface_sha256` changed, no screenshot
pixels differ, since neither `backup_api.go` nor `settings_page.go`'s
edited lines are user-visible text changes and the docs-shots fixture's
settings page doesn't populate a real snapshot row.

## Independent review — findings and disposition

None. The reviewing subagent independently ran `go build ./...`, `go vet
./...`, `gofmt -l` on the changed files, and the targeted test file, and
confirmed:
- the only production call site compiles against the new signature (grepped
  for other callers — none),
- `dateLayout`'s default branch handles an unrecognized/empty locale
  gracefully (no panic risk), moot in practice since `ResolveLocale` never
  returns empty,
- no repository-pattern/SQL, money-type, or i18n-hardcoded-string
  violations (doesn't touch `internal/data`, no money, no new
  user-facing string — a Go-side format-function swap only, so no
  `web/locales/en.json` key needed),
- the manifest diff is the guard's expected, correct output for a
  non-test `.go` file under `internal/pages/` registering a screenshotted
  route, not a stray edit,
- the new test genuinely proves the fix rather than false-passing: the
  ISO-shape regex would catch a reverted fix, and the en/de-DE
  disagreement check additionally catches a fix that accepts `locale` but
  silently ignores it.

## What was verified beyond automated tests

- `gofmt -l .` — clean.
- `go build ./...` — clean.
- `go test ./...` — full suite green (`internal/pages` 192.753s, `internal/data`
  59.113s, `internal/db` 11.920s, `internal/plugins` 86.104s, all other
  packages green).
- `golangci-lint run ./...` — 0 issues.
- Every CI-blocking guard in `.github/workflows/ci.yml`'s `build` job run
  locally: `guard-data-access`, `guard-kiosk-engine`,
  `guard-plugin-menu-read`, `guard-page-http-error`, `guard-i18n`,
  `guard-compliance-claims`, `guard-docs-shots` (after `make docs-shots`
  regen), `guard-help-topics`, `guard-help-drift` (pre-existing, tracked
  drift only — ut-docs#1962/#1973, unrelated to this change),
  `guard-webkit-version`, `guard-kiosk-launch-flags`,
  `guard-android-status-address`, `guard-android-i18n`, `guard-emoji-font`,
  `guard-htmx-loaded`, `guard-autofill-suppression`,
  `guard-e2e-fixtures-import`, `check-brand-assets`,
  `guard-makefile-version` — all green.
- `shellcheck` not available in this session's environment (no `.sh` files
  touched by this change, so this is a no-op gap, not a skipped check on
  anything this PR modifies — CI will still run it).
- Independent reviewer re-derived and re-ran the scoped tests personally
  rather than trusting the diff (see above).

## Follow-up

None new. This closes the last of the three follow-ups #1894's own review
filed (ut-docs#1938 — `fiscal_device.html` — remains separately open,
unrelated site).

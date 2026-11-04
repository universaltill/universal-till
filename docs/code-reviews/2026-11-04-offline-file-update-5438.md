# Review - offline (USB / file) till update (ut-docs#5438, ADR-0178 amendment)

- **Date:** 2026-11-04 - **Card:** universaltill/ut-docs#5438
- **Built by:** Dev (this record is the Dev part; Tester and Reviewer sections are added by those phases)
- **Design:** ut-docs ADR-0178 amendment of 2026-11-04 (branch `docs/5438-adr-0178-offline-file-install`)

## What shipped
- `internal/selfupdate/verified.go`: `compiledReleaseKeys` and `verifySums` (bytes in) split out of
  `verifiedChecksums`; `verifySums` holds the only `releasesig.Verify` call in the package
  (`TestOneReleasesigVerifyCall`). `verifiedChecksums` keeps its signature and behaviour.
- `internal/selfupdate/selfupdate.go`: `installArchive` (extract -> swap -> smoke run -> rollback -> pending
  -> restart) and `serverWebBase` moved out of `applyVersion`; `ErrBusy`; `pending.go` `ErrCannotStart`
  (matched through `errors.Is`, message text unchanged).
- `internal/selfupdate/fromfile.go`: `FileInstallAvailability`, `BeginFileInstall` (holds `applyMu` before any
  upload byte is read), `FileInstall.Install` (availability -> name/version -> signature -> signed SHA-256 ->
  `installArchive`), name rule `unitill-pos_<v>_<goos>_<goarch>.tar.gz`, anti-downgrade, disk cap.
- `internal/pages/update_file_api.go`: `POST /api/update/upload` (`plugin_management`, demo-mode denied),
  streamed `MultipartReader` into a private dir, inline `role="alert"` refusals with `data-code`, audit rows
  `update_installed_from_file` and `update_file_refused`, `HX-Trigger: ut:update-installing`.
- Settings -> Software update -> "Install from a file" (three file inputs, UX R-UX1..R-UX9), CSS, 22 i18n keys
  x 4 core locales (en, tr, fa, ar) + `en.meta.json`, `web/public/update-restart.js` extracted from `base.html`.
- Help: new section in `web/help/{en,de,tr,fa,ar}/updates.md`; `RELEASING.md`, `docs/arch/desktop-app.md`,
  `README.md`.

## Security notes for the Reviewer
- Nothing is extracted, swapped or executed before the signature AND the SHA-256 for that exact file name pass.
- Unarmed or unusable key stamp: refused before the body is read (fail closed); online dormant mode is unchanged.
- The archive name must match the regex exactly; it is the only file-name sanitiser (`part.FileName()` is not trusted
  beyond that), and the handler never calls `part.Close()` on a refused archive (it would drain the body).
- No `ParseMultipartForm` (would spill outside the private dir).

## Tests
`internal/selfupdate/fromfile_test.go`, `internal/pages/update_file_api_test.go` (see the Dev notes for the list).
Docs-shots: the surface hash was refreshed with `update-docs-shots-surface-hash.sh` (no screenshot of the Settings
update card exists; no pixel of a screenshotted page changes). Commit with `Docs-Shots-Unchanged: true`.

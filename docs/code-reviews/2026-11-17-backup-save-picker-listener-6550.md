# Review: Android "Save to Downloads" picker survives boosted navigation (ut-docs#6550)

Branch `fix/6550-backup-save-picker-listener`. Dev: Sonnet; reviewer: Opus (different model) plus the orchestrator's own read of the diff.

Pre-flight: till user-visible behaviour -> `web/help/en/backups.md` still accurate (no "press again" wording anywhere in web/help), docs-shots n/a (no visual change), `reference/` n/a, README n/a; new `en.json` key -> `en.meta.json` updated (English only, lang packs via Weblate, ADR-0160); plan/price n/a; platform claim n/a; ADR n/a (follows the existing shell/bridge pattern, no new mechanism or JavascriptInterface surface); plugin/manifest n/a; review record -> this file; follow-ups -> deferred items below.

## What shipped
Root cause: the bridge script in `settings.html` added a document click listener that the shell's `dropPageListeners()` removes on every boosted swap, while its run-once guard `window.utBackupSaveResult` outlived it. From the second Settings visit the button was a plain htmx POST and the server answered "press again".
Fix: delegated capture click handler and `window.utBackupSaveResult` moved to `web/public/inline-actions.js` (loaded once per document, outside `#ut-page`); messages read at click time from `data-picker-*` on `#backup-dl-msg`; the button carries `data-backup-native` when `hostIsAndroid`, so a missing bridge gets a clear client message instead of a server dead end; the server's `errUseNativePicker` now maps to new key `settings.backup.save_failed_picker_unavailable` (old key removed). Kotlin untouched.

## Findings
| # | Sev | Finding | Disposition |
|---|---|---|---|
| 1 | Low | `hostIsAndroid` means the server runs on Android, not that the viewer is the app; a LAN browser without the bridge sees "This app cannot open the file picker ... or download from another device". Misleading wording, not a regression (old text was also a dead end). | Accepted; deferred (wording / browser-download follow-up, noted on ut-docs#6550) |
| 2 | Low | `TestSettingsPageFlagsNativePickerFromHost` is a source-text check, not a render test. | Accepted: the e2e spec is the real regression test (the comment says so); a render test is a nice-to-have |
| 3 | Low | `MainActivity.kt` `saveBackupCopy` returns silently when `isValidName` fails (existing code). | Out of scope; deferred |
| - | None | Kotlin contract `utBackupSaveResult(code, name)` and codes saved/cancelled/failed/no_space match; no double handling with htmx (stopPropagation in capture); escaping via html/template + textContent; old key unreferenced in tree. | Verified |

## Extra verification
- Tester (independent): spec red on original code (2 expected calls, 1 received; unavailable-message test fails), green on the fix. Reviewer re-ran in a throwaway worktree: same red, and `--repeat-each=3` 12/12 green.
- `go build ./...`, `go vet ./internal/pages`, `go test ./internal/pages -run 'Backup|Picker|Settings'` pass; guards (inline handlers, i18n, i18n-meta) pass.
- Device: bug reproduced on the installed app (0.36.1); fix NOT verified on device. The release signing key is not available locally and a debug-signed APK cannot update the release-signed install without an uninstall (data loss), so no uninstall was done.

## Verdict
Approve. The fix reaches Android only via a new APK (the till is embedded in the AAR); no release is cut here.

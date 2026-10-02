# Code review — iOS bug-report Record no longer crashes the app (ut-docs#3354)

- **Date:** 2026-10-02
- **Ticket:** ut-docs#3354 (`complexity:medium`, p1, owner's iPhone report)
- **Branch:** `fix/3354-ios-microphone-usage`
- **Author:** Opus 5.5 (lane:cloud-24 build routine).
- **Reviewer:** independent fresh-context Fable subagent (`MODEL-ROUTING.md`
  medium tier).
- **Verdict: SAFE TO MERGE** after both should-fix findings were fixed in
  this branch. A device tap on the iPhone is still owed (see below).

## Root cause

The voice-note recorder in `web/ui/partials/bugreport_panel.html` calls
`getUserMedia({ audio: true })`. The iOS shell's Info.plist
(`ios/project.yml`) declared `NSCameraUsageDescription` but no
`NSMicrophoneUsageDescription`. `TillWebView.swift` grants the till origin in
`requestMediaCapturePermissionFor`, after which WebKit calls
`AVCaptureDevice requestAccessForMediaType:` directly (the reviewer checked
WebKit's `UserMediaPermissionRequestManagerProxy` on `main` and the iOS 15
branch: no usage-string check on that path), so TCC terminates the app on the
first Record tap.

## What shipped

- `ios/project.yml`: `NSMicrophoneUsageDescription`; translated in
  `ios/UniversalTill/{en,tr,ar,fa}.lproj/InfoPlist.strings`.
- `scripts/ci/guard-ios-usage-descriptions.sh` (+ `_test.sh`, wired into
  `ci.yml` next to the Android manifest guard): every web `getUserMedia`
  call needs the matching plist key (audio → mic, video → camera, non-literal
  constraints → both; `web/public/vendor/` excluded), and every declared
  `NS*UsageDescription` must be translated in every `.lproj`.
- `bugreport_panel.html`: `pickMimeType` returns `null` when the recorder
  supports none of the candidates; the voice button is then disabled up front
  with the existing `issuereport.voice_unsupported` text and the microphone
  is never requested. The screen recorder keeps its old `video/webm` fallback.
- `e2e/tests/bugreport-voice-mime-3354.spec.ts`: MP4-only recorder → button
  disabled with the unsupported text and `getUserMedia` never called; a
  WebM-capable browser keeps the button live.
- Manual: `web/help/{en,de,tr,ar,fa}/bug-reporting.md` gain an iPhone/iPad
  paragraph (mic prompt and re-enable path, older iOS can't record voice
  notes, no screen recording or screenshots in the iOS app yet).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | iOS 15–18 WKWebView's MediaRecorder writes MP4 only; the panel built a WebM recorder anyway, so after granting the mic the user got "Couldn't access the microphone." and the help over-promised. | Fixed: voice button disabled up front with "can't record audio" text; help says older iOS can't record voice notes. Recording MP4 there needs the bundle to stop hard-coding `audio.webm` (`internal/issuereport/bundle.go`, cloud sync), which is a follow-up Backlog card. |
| 2 | should-fix | Help implied screenshots work on iPhone (the Android paragraph above says they do); iOS WKWebView has no `getDisplayMedia`. | Fixed in all five locales. The screenshot bridge itself is ut-docs#3355. |
| 3 | nit | Guard regex: `video: false` would still demand the camera key; a comment with `getUserMedia()` counts as non-literal. | Accepted: no false positive today, and both keys are declared. |
| 4 | nit | `.lproj` regex rejects a translation containing `\"`. | Accepted: none exist. |
| 5 | nit | project.yml comment could point at the delegate grant. | Covered by this record. |
| 6 | pre-existing | iOS app ships no `de.lproj`, so German operators get the English prompt. | Out of scope. Noted on the close-out. |

## Verified

- TDD: guard fails on `main`'s tree naming `bugreport_panel.html:189` and
  passes with the key (reviewer re-ran it in a throwaway worktree). The new
  e2e spec fails against the old panel JS (`toBeDisabled` received enabled)
  and passes with the fix.
- Guards: ios-usage-descriptions (+test, 9/9), help-drift, help-topics, i18n,
  compliance-claims, competitor-naming, no-showmodal; `shellcheck` on the new
  scripts; `go build ./...`; `go test ./internal/manual ./internal/pages`;
  e2e `bugreport-panel.spec.ts` + the new spec (19/19 at the time).
- Looked at: the panel at 1280×800, light theme, English, with the voice
  button in its disabled state. Not looked at: RTL, dark theme, a real
  iPhone.
- **Not verified:** a real iPhone tap. ios-ci builds the shell on the
  simulator but can't exercise TCC. The card's close-out asks for one device
  tap on the next TestFlight build.

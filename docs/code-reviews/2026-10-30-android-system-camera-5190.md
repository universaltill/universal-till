# Android: item photo opens the system camera (ut-docs#5190)

Author model: Sonnet 5.5. Reviewer: Opus 5.5 (different model).

## What shipped
- Android shell: `onShowFileChooser` launches the system camera for a file
  input with `capture` + image types (`CaptureLogic.wantsCamera`), asking
  `CAMERA` on first use only; refused / no camera app / empty result falls back
  to the document picker or returns nothing, so the callback is always
  resolved once. The photo goes to a private cache file (`captures/`) the
  camera app reaches through `CaptureFileProvider` (own authority, one path,
  `exported=false`).
- Page (`catalog.html`): inside the Android app (`window.AndroidKiosk`) Take a
  Photo clicks the capture input instead of opening the in-page viewfinder;
  desktop browsers keep the viewfinder (#1472). The returned photo is shrunk
  to a 1024px JPEG (the viewfinder path always did; a camera photo is 8MP+,
  over the server's 6MP limit) and saved at once like iOS, the background
  offer first.
- Manual (en, de) and `android/README.md` updated.

## What it exposes
A second FileProvider exposing only the app's cache `captures/` directory;
camera permission requested on first use (already declared in the manifest).

## Findings (Opus review, all fixed)
- HIGH full-resolution camera photo would be refused by the server: shrunk in
  the page before upload (e2e proves the upload is <= 1024px; fails without it).
- MEDIUM the photo no longer saved at once on Android (#3133 behaviour):
  `saveIOSPick()` now also for the Android app.
- LOW e2e only proved the click: added a case that returns a 3264x2448 photo,
  checks the offer, then the upload request and the size.
- LOW a camera app that answers OK without writing the file: now only a
  non-empty file is handed over.
- Help text read badly mid-sentence: moved to its own sentence.

## Verified
`CaptureLogicTest` (JVM), full Kotlin unit tests, `compileArm64DebugKotlin`;
e2e: new spec (3 cases; the Android click case and the shrink case each shown
to fail with the fix reverted) plus the 1472, 4096, 3127 and image-to-till
specs (44 passed); go build/vet, catalog + pages Go tests, guard-i18n,
guard-help-topics, guard-help-drift.

NOT verified: a real camera on the tablet. That needs the APK built by CI
(release workflow) and installed; the owner's tablet is the device test.
Deferred (not in this change): iPhone/iPad may hit the same 6MP limit (no
resize there); permanent camera-permission refusal falls back silently;
lock-task device-owner mode may block the camera app (same as the picker).

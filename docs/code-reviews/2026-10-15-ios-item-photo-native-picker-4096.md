# Review: iPhone/iPad item photo uses the system picker (ut-docs#4096)

- **Date:** 2026-10-15
- **Card:** ut-docs#4096
- **Reviewed by:** Fable (independent, two passes)

## Background

The iPhone/iPad app's in-page viewfinder for item photos showed a black box that then went blank. Device diagnosis on the owner's iPhone 17 Pro Max, through Web Inspector and a LAN test till:
- **iPhone Safari over HTTPS:** the viewfinder works.
- **Plain http:** no `mediaDevices`, so the page falls back to the camera input, as designed.
- **The app:** the black-then-blank preview reproduces there only.

A WKWebView `mediaTypesRequiringUserActionForPlayback` fix was drafted. The owner then reported that the sell-screen camera works in the app, which contradicts that theory, so the fix was dropped unshipped.

Product owner decision: on iPhone/iPad, item photos use the system's own picker.

## What shipped

- **`web/ui/pages/catalog.html`:**
  - **Detection:** `appleTouch` is an `iPhone|iPad|iPod` user agent, or a `Macintosh` user agent with `maxTouchPoints > 1` (iPadOS desktop-class).
  - **Buttons:** on those devices the item image tab shows one full-width **Add photo** button. It opens the iOS menu: Photo Library / Take Photo / Choose File. "Take a Photo" is hidden (kept in the DOM for the cutout's Retake, which then opens the system camera directly).
  - **Auto-save:** a picked or taken photo saves at once, with the background-removal offer first when available, like the viewfinder's Capture. A native app has no second "Upload Image" tap to forget.
- **Unchanged:** Android and desktop keep both buttons and the viewfinder (#1472).
- **New key `catalog.add_photo`:** en, ar, fa and tr in core. The de/es/pt packs follow in pack PRs.
- **Help:** `web/help/{en,de,ar,fa,tr}/catalog.md` gained one sentence.
- **iOS camera permission text:** now "…scan barcodes and take item photos" in `ios/project.yml` and the 4 `InfoPlist.strings`; `ios/README.md` updated.

## Findings and outcome

| # | Severity | Finding | Outcome |
|---|---|---|---|
| R1 | Medium | On iOS the picked photo needed an Upload Image tap; closing the item lost it, and the help implied otherwise | **Fixed:** auto-save on iOS picks. e2e "a picked photo saves without an Upload Image tap" and "a photo from the camera (Retake path) saves the same way" failed first. |
| R2 | Low | The iOS camera permission prompt said "scan barcodes" only | **Fixed** in all 4 iOS locales; `guard-ios-usage-descriptions` passes |
| R3 | Low | No e2e drove a file through the capture input | **Fixed** by the R1 tests (`setInputFiles` on `#image-file-camera`) |
| R4 | Low | `ios/README.md` mentioned only the barcode viewfinder | **Fixed** |
| R0 | (first pass, on the dropped WKWebView fix) | `.audio` would be narrower than `[]`; guard gaps | **Moot:** the change was dropped. Kept on the card in case identify ever needs it. |

## Verified beyond automated tests

- **Real iPhone Safari over HTTPS, driven via Web Inspector:**
  - before the one-button change: Take a Photo made 0 getUserMedia calls, opened the capture input once, and the viewfinder stayed hidden;
  - after it: Take a Photo hidden, one **Add photo** button 406 px wide (the full row at a 440 px viewport).
- **By the owner on the same iPhone:** the camera and the iOS picker menu (Photo Library / Take Photo / Choose File) open as described.
- **Not verified:** the installed TestFlight app. That needs a new TestFlight build after merge, and the card stays open for that check.

## Gate

- Playwright: new spec (iPhone, iPad desktop-UA, Mac control) plus the catalog-camera-viewfinder-1472, catalog-image-to-till and photo-cutout-3127 specs: **28 passed**.
- Guards pass: i18n, help-drift, help-topics, no-inline-handlers, competitor-naming, compliance-claims, ios-usage-descriptions (+ test), docs-shots (surface hash refreshed: desktop pixels unchanged, iOS-only branch).

## Verdict

Safe to merge. After merge, the de/es/pt pack PRs add `catalog.add_photo`.

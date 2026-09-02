# 2026-09-02 — Android screenshot + camera/mic permission bridge (ut-docs#1435)

## What shipped

**Deliberately narrowed scope** (see "What was deferred" below) for the
Android till app's bug-report panel bug: screenshot and screen recording
said "not available here," and the camera never opened, because (1)
Android's WebView implements no `getDisplayMedia` at all, and (2) the
`WebChromeClient` had no `onPermissionRequest` override — its platform
default is an unconditional `deny()` — so every `getUserMedia` call (the
panel's voice note, an in-page camera) was silently refused, with no OS
permission dialog ever shown, and no CAMERA/RECORD_AUDIO permission was
even declared.

This change:

- **Native screenshot bridge** — `KioskBridge.captureScreenshot()`
  (`MainActivity.kt`), a synchronous `@JavascriptInterface` method that
  captures the WebView's current content via `PixelCopy` of the Activity
  `Window` (API 26+, clipped to the WebView's own bounds) or
  `View.draw` onto a software canvas (API 24-25, this app's `minSdk`),
  returned as a `data:image/png;base64,...` URL (`""` on any failure —
  nothing throws across the bridge). Sync-over-async via a
  `CountDownLatch` (5s timeout) since a JS bridge call has no
  promise/callback channel but the capture itself is UI-thread/async.
- **`WebChromeClient.onPermissionRequest`** — grants only for the till's
  own origin (`request.origin.authority == allowedHost`, the same
  comparison `shouldOverrideUrlLoading` already uses and — independently
  re-derived in review — the only one of `.host`/`.authority` that can
  ever actually match, since `allowedHost` holds `host:port`), backed by
  real runtime `CAMERA`/`RECORD_AUDIO` permission requests via a
  `RequestMultiplePermissions` launcher. Every branch (null `allowedHost`,
  null origin, authority mismatch, ungrantable resource, one in-flight
  request already pending) resolves the `PermissionRequest` exactly once,
  fail-closed.
- **Manifest**: `CAMERA`, `RECORD_AUDIO`, `MODIFY_AUDIO_SETTINGS` —
  deliberately **no** `<uses-feature android.hardware.camera>`, so a till
  running on camera-less hardware stays installable.
- **`bugreport_panel.html`**: the screenshot button prefers
  `window.AndroidKiosk.captureScreenshot` when present; the two
  pre-existing `getDisplayMedia` branches are untouched (proven by the new
  e2e spec's desktop-path test). The voice note needed **no JS change** —
  it already called `getUserMedia({audio:true})` correctly; it was only
  ever blocked by the native `deny()` default.
- **New e2e**: `e2e/tests/android-screenshot-bridge-1435.spec.ts` — stubs
  `window.AndroidKiosk`, drives the real panel JS for both bridge outcomes
  (data URL, `""`, a throwing bridge) plus the untouched desktop path.
- **Docs**: `web/help/{en,fa,tr,ar}/bug-reporting.md` (all four locales —
  see review finding below) and `android/README.md`.

## What was deferred (two new Backlog cards)

Filed as new `pipeline`/`status:backlog`/`complexity:hard` cards in
`ut-docs`, each explicit that it builds on this cycle's
`onPermissionRequest`/`MEDIA_PERMISSIONS`/`mediaPermissionLauncher`
plumbing and the `captureScreenshot` bridge pattern, not re-doing it:

- **Screen recording via MediaProjection** — a much larger, separate
  mechanism (consent dialog, `mediaProjection` foreground service type,
  its own encoder pipeline).
- **Catalog in-page camera viewfinder** — the CAMERA half of this cycle's
  permission plumbing has no consumer yet; building the viewfinder UI
  (layout, facing-mode, RTL, no-camera fallback) is a separate UX-sized
  feature.

`ut-docs#1435` itself stays open, unassigned, back in `status:ready`, with
its body/acceptance-criteria updated to strike what shipped and point at
the two follow-ups — the original card's "camera never opens" and "screen
recording" causes are only partially closed.

## Independent review (Opus, different model from the Fable-authored
first draft and the Sonnet orchestrator that revised it)

Full pass: hand-traced every line of the new Kotlin against real Android
API signatures (no SDK/gomobile in this sandbox to compile it — same
constraint as the 2026-08-29 kiosk-lock-task precedent, and this repo's
`ci.yml` `build` job doesn't compile Android at all, only `release.yml`
does, so this review's own care is the only thing standing between a
subtle Kotlin mistake and it surfacing at the next release cut) — no
compile-breaking errors found. Ran all 18 CI-blocking guards, `gofmt`,
`go build ./...`, and the real e2e suite (19/19 including 4 new specs) as
part of the review itself, not just re-reading the Dev subagent's claims.

**Verdict: SHIP AS-IS (no blockers)**, with 3 should-fix items — all fixed
in this same cycle before merge:

1. **Manual updated in `en` only.** `fa`/`tr`/`ar` copies of
   `bug-reporting.md` all exist and hadn't been touched, unlike this
   repo's usual convention of updating all four locales together. Fixed:
   added the equivalent paragraph, translated, to all three; regenerated
   `make docs-shots` afterward (`guard-docs-shots` failed until this ran —
   editing tracked help markdown changes the surface hash it checks).
2. **`pendingMediaRequest` could wedge permanently.** If
   `mediaPermissionLauncher.launch(...)` itself threw (e.g. the Activity
   finishing/destroyed), the `PermissionRequest` already stored in
   `pendingMediaRequest` would never be resolved (a WebView contract
   violation) *and* every subsequent `getUserMedia` would hit the
   "one in-flight request" branch and be denied for the rest of the
   process — silently reproducing this exact ticket's original symptom.
   Fixed: wrapped the `launch()` call in try/catch, clearing the pending
   reference and `deny()`ing on failure.
3. **"so the sale screen never janks" overstated.** True for the Android
   UI thread (PNG encoding happens off it), but the bridge call itself is
   synchronous from the page's own JS main thread, which does block for
   the capture's duration (normally sub-second, up to the 5s timeout in
   the pathological case). Fixed: corrected the KDoc and
   `android/README.md` wording rather than changing the behavior — the
   timeout itself is an appropriate safety margin, not something to
   shrink.

Also applied one nice-to-have: a `Bitmap` allocated just before
`PixelCopy.request` itself threw (before scheduling its callback, so
`captured` is guaranteed still unset) is now recycled in the `catch`
block instead of left for GC. Two nice-to-haves were deliberately **not**
applied: downscaling/JPEG re-encoding (no evidence yet that a full-res PNG
is actually slow on the target device; the server's 32MB cap isn't at
risk) and returning `""` unconditionally on API 24-25 (would make the
fallback path never actually produce a screenshot, defeating its own
purpose — the existing "may render less faithfully" caveat already
documents the real, bounded limitation).

## What was verified beyond automated guards

- Re-ran the full `e2e/` default-project suite (293 specs) after the
  review's fixes: 292 passed, 1 pre-existing failure
  (`settings-pos-notice-918.spec.ts`'s customer-search test, a
  `route.continue()`-already-handled race) reproduced identically on
  clean `main` (`git stash` + re-run) before this branch's changes existed
  — confirmed unrelated to this change, not investigated further here.
- Origin-scoping re-derived independently against `mobile/mobile.go`'s
  actual `"127.0.0.1:" + port` address format, not taken on faith from
  the diff's own comments.
- Manifest parsed programmatically (not just grepped) to confirm exactly
  three new permissions and zero `<uses-feature>` elements.

## Files

`android/app/src/main/java/com/universaltill/pos/MainActivity.kt`,
`android/app/src/main/AndroidManifest.xml`,
`web/ui/partials/bugreport_panel.html`,
`web/help/{en,fa,tr,ar}/bug-reporting.md`, `android/README.md`,
`web/help/img/manifest.json` + regenerated screenshots,
`e2e/tests/android-screenshot-bridge-1435.spec.ts`.

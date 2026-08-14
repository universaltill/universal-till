# Code review: camera barcode/QR scan on the sale screen (ut-docs#548)

**Date:** 2026-08-14
**Author (Dev):** scrum-master pipeline, Sonnet (inline)
**Reviewer:** independent Opus subagent (fresh context, different model, worktree-isolated)
**Card:** universaltill/ut-docs#548

## What shipped

Camera-based barcode/QR scanning as an alternative input mode on the
**cashier sale screen** (`web/ui/pages/index.html`), alongside the existing
wedge/HID scanner path (ut-docs#76/#423). No backend change: `/api/pos/scan`
already accepts a generic `code` field, so this is a pure client-side diff —
`web/ui/pages/index.html`, `web/public/app.js`, `web/public/app.css`,
`web/locales/{en,ar,fa,tr}.json`, `web/help/{en,ar,fa,tr}/sell.md`,
`web/help/img/**` (regenerated), `README.md`, plus a new Playwright spec.

- A 🔳 button in the scan-row, feature-detected client-side
  (`typeof window.BarcodeDetector === 'function'`) — hidden entirely on
  unsupported browsers, matching the existing AI-identify overlay's pattern
  and CSS (`.ai-identify-overlay`/`.ai-identify-panel`/`.ai-identify-status`/
  `.ai-identify-controls`, reused as-is; `#ai-identify-video`'s ID-scoped
  sizing rule was generalized to a shared `.camera-overlay-video` class so
  both overlays' `<video>` elements use it).
- `getUserMedia` + native `BarcodeDetector` (EAN-13/8, UPC-A/E, Code-128,
  QR), decoding 100% client-side — no frame or image ever leaves the device,
  a hard requirement distinct from the AI-identify feature's photo upload.
- A decoded code submits through the exact same path a wedge scan uses:
  `web/public/app.js`'s existing scan IIFE now exports
  `window.utScan = { input: scanCodeInput, submit: submit }` so the new
  camera IIFE calls the identical function, rather than duplicating the
  form-lookup/submit logic.
- Camera permission denial / no camera → inline translated
  `scan.camera.camera_error` status, never a stuck or silent overlay.
- Non-goals, split into follow-up Backlog cards during BA scoping (this
  card's own acceptance criteria named all three as separate hardware/
  platform tracks): self-order kiosk integration (#695), native Android/iOS
  camera adapter (#696), physical Pi field test (#697, `blocked:env` — needs
  a human at camera-equipped hardware).

## Independent review — findings

**BLOCKING, found and fixed:** three lifecycle races in the camera IIFE
(`web/public/app.js`), all from the same root cause — neither async
continuation (`getUserMedia().then`, `detect().then`) re-checked whether the
cashier had already closed the overlay in the meantime:

1. Closing while `getUserMedia` was still pending (slow permission prompt/
   camera start) left the resolved stream orphaned — a **live camera
   recording behind a hidden overlay**, indefinitely, on a feature whose
   entire premise is "no frame ever leaves the device." Worse variant: if a
   barcode was in view when the orphaned stream started, it would ring up a
   line onto the sale with the overlay already hidden — money-shaped, no
   visible cause.
2. An in-flight `detect()` that resolved after Close still rang up a line —
   `scanFrame`'s `if (!stream) return` guard was only at the top of the
   function, not inside the `.then`/`.catch` continuations.
3. The open button keeps focus after the click that opens the overlay;
   since the wedge-scanner `keydown` listener doesn't `preventDefault` a
   bare Enter when its buffer is empty, a stray Enter (or Space) could
   re-invoke `open()` and leak a second `getUserMedia` stream.

The reviewer proved each with throwaway Playwright probes against the real
page (not just reasoning about the code), then wrote and verified an ~8-line
fix: `open()` now no-ops if already open; the `getUserMedia().then` and both
`detect()` continuations (`.then` and `.catch`) re-check `overlay.hidden`/
`stream` before doing anything observable, stopping an orphaned stream's
tracks immediately if the overlay closed underneath them. Fix applied
verbatim, then independently re-verified in this session: reverted the fix,
confirmed three new regression tests fail with exactly the predicted
symptom (stream never stopped / line rung up after close / duplicate
`getUserMedia` call), restored the fix, confirmed all pass again.

**Non-blocking, fixed anyway (cheap):**
- `keywords: [camera]` in the ar/fa/tr help topics was left untranslated,
  against the repo's own convention (localized keyword lists, e.g.
  `web/help/ar/country-settings.md`) — now `كاميرا`/`دوربین`/`kamera`.
- README's "Barcode scanning" bullet didn't mention the camera path —
  added `(USB/Bluetooth scanner, or a device camera — no dedicated hardware
  required)`.
- Added test coverage for camera release (`stream.getTracks()[].stop()`
  actually called) — the reviewer noted no test previously observed this at
  all, which is exactly how the three races above went uncaught by the
  original test pass.

**Non-blocking, deferred (noted for follow-up, not fixed this cycle):**
- The Android native client loads the till over `http://`, an insecure
  context where `BarcodeDetector`/`getUserMedia` are unavailable — this
  button will never appear on the shipped tablet client today. Belongs with
  #696 (native adapter track); noted there.
- No frame-rate throttle on the detect loop (self-throttling via sequential
  awaits, but still pegs a core on Pi-class hardware for as long as the
  overlay is open) — worth a ~100ms delay between attempts, not correctness-
  critical.
- `codes[0]` picks one barcode when several are in frame — acceptable for
  this pass, a same-code-on-two-consecutive-frames debounce would be more
  robust.
- No Escape-to-close / focus trap on the overlay — pre-existing convention
  (the AI-identify overlay has the same gap), not a new regression.

## Verified beyond automated tests

- Full gate: `go build ./...`, `go test ./...` (all packages), and all four
  `scripts/ci/guard-*.sh` checks — data-access, kiosk-engine, plugin-menu-
  read, i18n, help-topics — pass.
- `make docs-shots` (via the pre-installed-Chromium fallback,
  `PLAYWRIGHT_BROWSERS_PATH=/opt/pw-browsers`) regenerated all 76
  screenshots; `guard-docs-shots.sh` passes. `sell.png` in every locale is
  byte-identical to before — confirms the new button renders with zero
  visual footprint when `BarcodeDetector` is unsupported (Linux CI
  Chromium), matching the AC1 requirement.
- New spec `e2e/tests/sale-screen-camera-barcode-scan-548.spec.ts` (7
  tests, all passing): unsupported-browser hiding, happy-path scan +
  wedge-scanner-still-works regression check, manual close releases the
  camera, camera-denied inline error, and the three lifecycle races above
  (each independently reproduced failing pre-fix, passing post-fix).
  Existing `sale-screen-scan-focus-search-423.spec.ts` (wedge-scanner focus
  regression) re-run and still passes — 3/3, no interference from the new
  feature.
- Confirmed insecure-context behavior directly (non-localhost `http://`
  origin): Chromium exposes neither `BarcodeDetector` nor
  `navigator.mediaDevices` there, so the button stays hidden exactly as
  intended — not a live bug, `open()`'s missing explicit
  `navigator.mediaDevices` guard is defensive-only.
- Confirmed no phone-width layout regression (320px/360px, button
  force-shown).
- No real client/shop name in any new file; no secret-shaped literal.
- Playwright browser-revision mismatch in this sandbox
  (`chromium_headless_shell-1228` wanted, `1194` present) confirmed
  pre-existing and unrelated to this diff — the existing #423 spec fails
  identically without a workaround. Verified locally via a temporary
  `executablePath` override to the pre-installed Chromium build; that
  override was never committed.

## Safe to merge

Yes, after the blocking fix above (applied, tested, TDD-verified in both
directions) and the two cheap non-blocking items folded in. Follow-up cards
for the kiosk/native/hardware split (#695/#696/#697) and the Android-
insecure-context gap already exist or are noted above.

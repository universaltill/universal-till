# Review: camera barcode scan without a native BarcodeDetector (ut-docs#696)

**Date:** 2026-10-02 · **Lane:** lane:cloud-24 · **Author model:** Opus 5.5 · **Reviewer:** Fable (independent subagent, separate worktree)

## What shipped

- Vendored decoder (ADR-0003): `web/public/vendor/barcode-detector/` holds `barcode-detector@3.2.2`'s IIFE ponyfill and `zxing-wasm@3.1.3`'s `zxing_reader.wasm`. Both are unmodified, and the reviewer re-hashed them against the npm tarballs. The folder also has MIT ×2 and zxing-cpp Apache-2.0 licences and a provenance README.
- `web/public/app.js`: the sale-screen camera-scan button always shows.
  - It uses a native `BarcodeDetector` only if `getSupportedFormats()` reports one of our formats.
  - Otherwise it lazy-loads the vendored ponyfill on the first tap. `prepareZXingModule` points `locateFile` at the till's own copy, so there is no CDN path.
  - The decoder URLs carry `?v=<content hash>`, so they get immutable caching.
  - If the decoder fails to load, the camera is released and "Camera unavailable" shows. The failed module is purged, so the next tap retries.
- CSP report-only policy: added `'wasm-unsafe-eval'`, which permits WebAssembly compilation only.
- `web/vendor_barcode_decoder_test.go`:
  - pins the wasm's SHA-256 to the one the ponyfill embeds;
  - checks that both files are in the embed FS;
  - checks that the exports `app.js` calls still exist.
- Help `sell` topic (en/de/fa/ar/tr), regenerated screenshots, CHANGELOG entry.

## Findings (Fable)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | `csp-report-only-2913.spec.ts` pinned the old policy string | fixed |
| 2 | major | docs-shots stale (the button now renders on headless Chromium) | fixed: `make docs-shots` |
| 3 | minor | Native `BarcodeDetector` with no platform service (Chromium on Android without Play services) would spin forever | fixed: `getSupportedFormats()` gate + e2e |
| 4 | minor | Decoder refetched on every load (`no-cache`, no validators on the embed FS) | fixed: `?v=` via `assetv`; e2e asserts `immutable` + `application/wasm` |
| 5 | minor | No CHANGELOG entry | fixed |
| 6 | minor | Stale comments in `index.html` | fixed |
| 7 | minor | First tap on WebKit shows "Scanning…" while ~1 MB loads; a load failure reuses "Camera unavailable" | accepted: no new locale keys in this slice; device timing is checked on ut-docs#3429 |
| 8 | nit | Retry path untested; `<script>` left in `<head>` on the 'missing' branch | fixed: e2e retry after `unroute`; `s.remove()` |
| 9 | nit | Go test's sha regex took the first match | fixed: requires exactly one distinct value |

The reviewer also read the minified ponyfill and confirmed:

- a failed instantiate is cached, so purging it is necessary;
- no CDN path remains once the overrides are set;
- the close/reopen races are covered;
- iOS and Android already grant the camera;
- the scan row still fits at 480px and 1024×600.

## Verified beyond automated tests

- TDD:
  - The two new e2e tests failed against `main`'s `app.js` (button hidden), then passed.
  - The reviewer corrupted the wasm and saw the Go test fail with the sha mismatch, then restored it and saw it pass.
- A real decode: zxing-wasm reads a canvas-drawn EAN-13 through a stubbed `getUserMedia` stream. The requests are checked to stay same-origin, and the decoder is fetched only after the tap.
- Full e2e suite: 1018 passed.
  - The CSP spec failed and is fixed (finding 1).
  - `/shifts` 360px and `tender-panel-reachable` (45 s timeout) failed only under full-suite load; both pass when re-run alone.
- Screenshots looked at: the sale screen at 1024×600 in en and in fa (RTL). The scan icon sits in the scan row with nothing clipped. 360px is covered by `phone-layout-sweep-3297`.
- **Not verified:** a real iPhone, iPad or Android device, and real camera hardware. That is ut-docs#3429 (`blocked:env`).

## Deferred

- ut-docs#3428: scan the pairing QR on the join forms with the same decoder.
- ut-docs#3429: device check, plus a native VisionKit/ML Kit bridge only if the WebView decoder falls short.

**Verdict:** safe to merge once CI is green.

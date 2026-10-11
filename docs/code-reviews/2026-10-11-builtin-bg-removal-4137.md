# Code review: built-in on-device background removal (ut-docs#4137)

- **Date:** 2026-10-11
- **Branch:** `feat/4137-builtin-bg-removal`
- **Design:** ADR-0154 (ut-docs, merged first), `architecture/ai-product-photo.md` §6a
- **Author:** Opus 5.5 (lane:sdk). **Reviewer:** Fable (independent subagent), one round.

## What shipped

- `web/public/vendor/bg-removal/`: `onnxruntime-web` 1.30.0 (`ort.wasm.bundle.min.mjs`, `ort-wasm-simd-threaded.wasm`, MIT) and `u2netp.onnx` (Apache-2.0), unmodified, with licences and a hash-pinned provenance README; `web/vendor_bg_removal_test.go` checks the hashes and that all three are embedded.
- `aiCutoutOffered` now returns the engine: `service` when the shop's rembg is configured and valid (`CanCutout()`), else `builtin`; `""` without `catalog_management`. The `photo_cutout` panel carries `data-cutout-engine` and, for `builtin`, versioned same-origin URLs of the worker, runtime, wasm and model.
- `web/public/photo-cutout.js`: the built-in path downscales to 1024 px, builds u2netp's 320×320 input (rembg's recipe: divide by the max channel value, ImageNet mean/std, planar RGB), runs the model in a per-tap module worker (`web/public/photo-cutout-worker.js`, `numThreads = 1`), scales the min-max-normalised mask back as the photo's alpha and posts the PNG to the new `POST /api/catalog/image/tile`. A background change re-tiles the cached cut-out (no second inference). The worker is terminated as soon as the mask is back and at once on Use original / Retake / a new pick.
- `POST /api/catalog/image/tile` (`internal/pages/ai_cutout.go`): same gate, body cap, header pixel check and envelope as `/cutout`; PNG with transparency only; `imaging.SquareTile`; stateless; denied in demo mode. `/cutout` unchanged (404 without a service).
- Help (`catalog.md`, `plugins.md` in en/de/tr/ar/fa), README and both `pos.env.example` files: works out of the box, rembg is optional. No new locale keys (the panel's existing strings cover the built-in path).
- e2e: `photo-cutout-builtin-4137.spec.ts` replaces `photo-cutout-off-3127.spec.ts` (whose premise, "no offer without a server", is gone); the viewfinder spec taps **Use original photo** after Capture (a manager's Capture now shows the offer first, the #3127 flow); the CSP report-only spec gains a built-in tap that must add no violation.

## Review findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | A run superseded while the photo was still being decoded could still start a worker (19 MB load + full inference, discarded), and a reset left the pending inference promise (and the 1024 px canvas) alive until the 120 s timer. | **Fixed:** `infer` checks `st.seq` before `new Worker`; `stopWorker` also calls a stored `cancelInfer` that clears the timer and rejects; at most one worker per panel. |
| 2 | minor | `run-till-cutout.sh` / `playwright.config.ts` still named the deleted off-spec. | **Fixed.** |
| 3 | minor | Comments in `photo-cutout.js` and `catalog.html` still said "only when the service is configured". | **Fixed.** |
| 4 | minor | CSP compliance of the built-in path was only argued, not exercised. | **Fixed:** new test in `csp-report-only-2913.spec.ts` (inventory diff around the tap). Verified it bites: an `eval('1')` added to the worker makes it fail with a `script-src` violation. The pre-existing `img-src blob:` report (preview/downscale `<img>`, since #3127) is excluded and left to the CSP enforcement slices. |
| 5 | nit | Worker comment vs `{type:'module'}`. | **Fixed** (comment explains the option is ignored where unsupported). |
| 6 | nit | ADR/architecture said "terminated when the panel closes". | **Fixed** in the ADR PR. |
| 7 | nit | ar/fa `catalog.md` have no step-1 "Remove background" bullet (pre-existing drift); the facts are folded into their Good-to-know bullet. | Accepted (no meaning lost). |
| 8 | nit | On Android/iOS `assetv` falls back to boot time, so the 19 MB is re-read from the loopback server after each app restart. | Accepted (same as every `/public` asset; local, cheap). |

## Verified

- TDD: the new offer tests failed against the old gate ("service-only": three failures) and pass with the fix; the tile-route tests failed (404) before the route existed.
- `go build ./...`, `go vet ./...`, `go test ./...` (one flaky setup-wizard test hit a temp-dir `disk I/O error` and passed on rerun; unrelated). Guards: i18n, data-access, help-drift, help-topics, no-inline-handlers, core-neutral, netaccess, readme-local-links, e2e-no-browser, compliance-claims, competitor-naming — all pass. `guard-deadcode-baseline` flagged only `internal/logging` (untouched; the local run skips `cmd/unitill-desktop` without GTK headers).
- Full Playwright suite: 1167 passed; 13 sale-screen failures at 4 workers all passed on rerun at 2 workers (load flakiness, nothing touching catalog photos). The rembg (`cutout` project) specs all pass unchanged (AC2).
- Visual: offer, busy ("Removing background…") and result states at 1024×600 (en/de, fa RTL) and 360 px, looked at; layout unchanged from #3127.
- Real devices, one 1024×768 photo per run, three runs each, same code path as the picker (worker + compositing + PNG encode), served locally on the device / over `adb reverse`, nothing exposed on the LAN:

| Device | Browser | Per removal | Mask |
|---|---|---|---|
| MacBook (Apple silicon) | Playwright Chromium | ~2 s | correct |
| Raspberry Pi 4 Model B (4 GB) | Chromium 149, headless | 15.5–17.5 s (model ~15–17 s) | correct (product opaque, corner clear) |
| TECLAST P50T tablet (Android 16) | Chrome 154 | 15.8–19.1 s (model 11.7–14.6 s, compositing + PNG ~4 s) | correct |

  The Pi 5 (192.168.1.163) was not measured: no SSH login from this machine. The Pi 4 is the slower board, so it is the floor. Chrome on the tablet stands in for the till app's WebView (same engine). A busy state shows throughout; "Use original photo" stays live; nothing is on the sale screen.
- Bundle impact: +18.9 MB on the till binary and therefore on the Android APK / iOS app that embed it (14.2 MB wasm, 4.6 MB model, 73 KB JS). Nothing loads before the tap.

## Not covered / deferred

- A browser test that a run superseded *during the photo decode* never starts a worker (the window is milliseconds in desktop Chromium; covered by review + code). Accepted gap.
- Speed on the Pi 4 / tablet (~16 s) is acceptable for a back-office action with a busy state, but not good: follow-up card for faster inference (keep the session for a retake, WebGPU, multi-threading under cross-origin isolation, a faster compositing path).
- The real touchscreen tap and a real product photo on the kiosk were not done by a person; the device runs above drove the same code headless/over adb.

**Verdict:** safe to merge once ADR-0154 is merged.

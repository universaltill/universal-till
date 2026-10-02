# Vendored client-side barcode decoder (ut-docs#696)

Used by `web/public/app.js`'s sale-screen camera scan only when the browser
has no native `BarcodeDetector` (WebKit — so every iPhone/iPad — and some
Android WebViews). Lazy-loaded on the first tap of the camera-scan button.
Decoding runs entirely in the page; no frame leaves the device, and the
wasm is loaded from this directory, never from a CDN (ADR-0003).

| file | source | licence |
|---|---|---|
| `ponyfill.js` | npm `barcode-detector@3.2.2`, `dist/iife/ponyfill.js`, unmodified | MIT (`LICENSE.barcode-detector`) |
| `zxing_reader.wasm` | npm `zxing-wasm@3.1.3`, `dist/reader/zxing_reader.wasm`, unmodified | MIT (`LICENSE.zxing-wasm`); compiled from zxing-cpp `a17fd9dc65d6aa0dd2f660fdfca7a6a6613d938f`, Apache-2.0 (`LICENSE.zxing-cpp`) |

`ponyfill.js` embeds the SHA-256 of the wasm it was built against
(`ZXING_WASM_SHA256`); `web/vendor_barcode_decoder_test.go`
fails if the two files here ever stop matching. To upgrade, replace both
files from the same `barcode-detector` release and its pinned `zxing-wasm`
dependency, and update the versions above.

# Vendored on-device background removal (ut-docs#4137, ADR-0154)

The built-in engine behind "Remove background" in the item, category and
variant photo pickers (`web/public/photo-cutout.js`,
`web/public/photo-cutout-worker.js`). Loaded only when the operator taps
**Remove background** while no rembg service is configured; never on page
load, never on the sale screen. Inference runs in the page (a Web Worker);
no photo leaves the device, and every file is served from this directory by
the till itself, never from a CDN (ADR-0003).

| file | source | sha256 | licence |
|---|---|---|---|
| `ort.wasm.bundle.min.mjs` | npm `onnxruntime-web@1.30.0`, `dist/ort.wasm.bundle.min.mjs`, unmodified | `11e64bd8ffe11bd1a2a2f0d6275fdfbbba7262f0b76b99b53d228a8a22ef3d90` | MIT (`LICENSE.onnxruntime-web`) |
| `ort-wasm-simd-threaded.wasm` | npm `onnxruntime-web@1.30.0`, `dist/ort-wasm-simd-threaded.wasm`, unmodified | `3398c10d07d229bd91b364548e130e0e51a8e5704b88c7c083ebbeb78842dee2` | MIT (`LICENSE.onnxruntime-web`) |
| `u2netp.onnx` | rembg model release `v0.0.0` (`github.com/danielgatis/rembg/releases/download/v0.0.0/u2netp.onnx`), unmodified; U²-Net-P weights from `github.com/xuebinqin/U-2-Net` | `309c8469258dda742793dce0ebea8e6dd393174f89934733ecc8b14c76f4ddd8` | Apache-2.0 (`LICENSE.u2net`) |

Only the WASM (CPU) build of the runtime is shipped; the WebGPU build would
add 28 MB (ADR-0154 Decision 7). `web/vendor_bg_removal_test.go` fails if a
file here stops matching the hash above. To upgrade, replace the runtime's
two files from the same `onnxruntime-web` release (the bundle loads the wasm
it was built with), update the table, and re-run the driven check in
`e2e/`.

// ut-docs#4137 (ADR-0154): the built-in background-removal engine's worker.
// web/public/photo-cutout.js starts one per "Remove background" tap and
// terminates it as soon as the mask is back (or the operator moves on), so
// neither the runtime's WASM heap nor the model stays in memory.
//
// In:  { runtime, wasm, model: same-origin URLs of the vendored files in
//        web/public/vendor/bg-removal/, input: Float32Array(3*320*320) —
//        the photo as a normalised CHW tensor }
// Out: { ok: true, mask: Float32Array(320*320) } — u2netp's first output,
//      the saliency map (higher = product) — or { ok: false, error }.
//
// Nothing here leaves the device: the runtime, the wasm and the model are
// served by the till itself (ADR-0003, no CDN). Single-threaded: the till's
// pages are not cross-origin isolated, so no SharedArrayBuffer. Started with
// {type: 'module'}, but written as a plain script using dynamic import():
// where module workers are missing the option is ignored and it still runs.
// No user-facing strings (guard-i18n).
'use strict';

var SIZE = 320;

self.onmessage = function (ev) {
  var m = ev.data || {};
  import(m.runtime).then(function (ort) {
    ort.env.wasm.numThreads = 1;
    ort.env.wasm.wasmPaths = { wasm: m.wasm };
    return fetch(m.model, { credentials: 'same-origin' }).then(function (res) {
      if (!res.ok) throw new Error('model ' + res.status);
      return res.arrayBuffer();
    }).then(function (buf) {
      return ort.InferenceSession.create(buf, { executionProviders: ['wasm'] });
    }).then(function (session) {
      var feeds = {};
      feeds[session.inputNames[0]] = new ort.Tensor('float32', m.input, [1, 3, SIZE, SIZE]);
      return session.run(feeds).then(function (out) {
        var mask = new Float32Array(out[session.outputNames[0]].data);
        if (session.release) session.release();
        self.postMessage({ ok: true, mask: mask }, [mask.buffer]);
      });
    });
  }).catch(function (e) {
    self.postMessage({ ok: false, error: String((e && e.message) || e) });
  });
};

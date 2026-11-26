// Shared camera barcode/QR reading (ut-docs#548, #696, #6960, #3428).
//
// Moved out of app.js so a standalone page (the setup wizard loads no
// app.js) can scan too. Offline-first: the browser's native BarcodeDetector
// is used when it exists, otherwise the vendored zxing-wasm decoder
// (web/public/vendor/barcode-detector/, ADR-0003) is lazy-loaded from this
// till on first use. No frame ever leaves the device.
//
// SECURITY: a decoded value can be a one-time pairing token. Nothing here
// logs, stores or renders it; this file has no console output at all.
(function () {
  'use strict';

  var DECODER_DIR = '/public/vendor/barcode-detector/';
  // Default formats = what the sell screen's wedge scanners read (#423) plus
  // qr_code. An explicit list keeps decoding the same across browsers.
  var FORMATS = ['ean_13', 'ean_8', 'upc_a', 'upc_e', 'code_128', 'qr_code'];
  // Content version of the decoder, appended so it caches immutably.
  var decoderQS = '';
  var detectors = {}; // key = formats.join(',')
  var pending = {};

  function setDecoderVersion(v) {
    if (v) decoderQS = '?v=' + encodeURIComponent(v);
  }

  function currentQS() {
    if (!decoderQS) {
      var el = document.querySelector('[data-decoder-v]');
      if (el) setDecoderVersion(el.getAttribute('data-decoder-v'));
    }
    return decoderQS;
  }

  function nativeDetector(formats) {
    // typeof-check, not `'BarcodeDetector' in window`: a test stubbing the
    // property to undefined must still read as unsupported.
    if (typeof window.BarcodeDetector !== 'function') return null;
    try {
      return new window.BarcodeDetector({ formats: formats });
    } catch (e) {
      return null; // the browser supports none of these formats
    }
  }

  function loadVendoredDetector(formats) {
    var qs = currentQS();
    return new Promise(function (resolve, reject) {
      function build() {
        var api = window.BarcodeDetectionAPI;
        // The ponyfill defaults to a CDN for its wasm; serve it from this
        // till (offline-first). prepareZXingModule resolves once the wasm is
        // instantiated, so a missing file surfaces here, not per frame.
        api.prepareZXingModule({
          overrides: { locateFile: function (path) { return DECODER_DIR + path + qs; } },
          fireImmediately: true
        }).then(function () {
          resolve(new api.BarcodeDetector({ formats: formats }));
        }, function (err) {
          try { api.purgeZXingModule(); } catch (e) { /* nothing to purge */ }
          reject(err);
        });
      }
      if (window.BarcodeDetectionAPI) { build(); return; }
      var s = document.createElement('script');
      s.src = DECODER_DIR + 'ponyfill.js' + qs;
      s.onload = function () {
        if (window.BarcodeDetectionAPI) { build(); return; }
        s.remove();
        reject(new Error('decoder missing'));
      };
      s.onerror = function () {
        s.remove(); // let the next attempt retry with a fresh <script>
        reject(new Error('decoder failed to load'));
      };
      document.head.appendChild(s);
    });
  }

  // Chromium exposes BarcodeDetector even where the platform service behind
  // it is missing (Android without Play services); every detect() then
  // rejects. getSupportedFormats() resolves to [] there, so a native
  // detector is trusted only if it reads at least one wanted format.
  function usableNativeDetector(formats) {
    var native = nativeDetector(formats);
    if (!native) return Promise.resolve(null);
    var probe = window.BarcodeDetector.getSupportedFormats;
    if (typeof probe !== 'function') return Promise.resolve(native);
    return Promise.resolve().then(function () { return probe.call(window.BarcodeDetector); })
      .then(function (supported) {
        var ok = (supported || []).some(function (f) { return formats.indexOf(f) !== -1; });
        return ok ? native : null;
      }, function () { return null; });
  }

  function getDetector(formats) {
    formats = formats || FORMATS;
    var key = formats.join(',');
    if (detectors[key]) return Promise.resolve(detectors[key]);
    if (!pending[key]) {
      pending[key] = usableNativeDetector(formats)
        .then(function (native) { return native || loadVendoredDetector(formats); })
        .then(function (d) { detectors[key] = d; return d; }, function (err) {
          pending[key] = null;
          throw err;
        });
    }
    return pending[key];
  }

  // The same lazy decoder for other camera surfaces (the plugin settings
  // page's one-time key code scan, ut-docs#6960; the sell screen).
  window.utGetBarcodeDetector = function () { return getDetector(FORMATS); };

  // ---- pairing QR payload (ut-docs#3428) --------------------------------
  // What the main till prints (sync_api.go encodeEnrollCode): base64url (no
  // padding) of {"url":"http://host:port","token":"<32 lowercase hex>"} --
  // newLongEnrolToken mints 16 random bytes hex-encoded. Raw JSON is also
  // accepted, as decodeEnrollCode does. UX pre-check only: the server
  // (decodeEnrollCode + validPrimaryBaseURL) stays the authority.
  var MAX_LEN = 1024;
  var TOKEN_RE = /^[0-9a-f]{32}$/;
  var B64URL_RE = /^[A-Za-z0-9_-]+$/;

  function decodeB64Url(s) {
    var b = s.replace(/-/g, '+').replace(/_/g, '/');
    while (b.length % 4) b += '=';
    var bin = atob(b);
    var bytes = new Uint8Array(bin.length);
    for (var i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
    return new TextDecoder('utf-8', { fatal: true }).decode(bytes);
  }

  function hostOfBaseURL(u) {
    if (typeof u !== 'string' || u.indexOf('#') !== -1 || u.indexOf('?') !== -1) return null;
    var url;
    try { url = new URL(u); } catch (e) { return null; }
    if (url.protocol !== 'http:' && url.protocol !== 'https:') return null;
    if (url.username || url.password || !url.host) return null;
    if (url.pathname !== '' && url.pathname !== '/') return null;
    return url.host;
  }

  function parse(raw) {
    if (typeof raw !== 'string') return { ok: false };
    var s = raw.trim();
    if (!s || s.length > MAX_LEN) return { ok: false };
    var obj = null;
    try {
      if (s.charAt(0) === '{') obj = JSON.parse(s);
      else if (B64URL_RE.test(s)) obj = JSON.parse(decodeB64Url(s));
    } catch (e) {
      return { ok: false };
    }
    if (!obj || typeof obj !== 'object' || Array.isArray(obj)) return { ok: false };
    if (typeof obj.token !== 'string' || !TOKEN_RE.test(obj.token)) return { ok: false };
    var host = hostOfBaseURL(obj.url);
    if (!host) return { ok: false };
    // `code` is the scanned string itself: the server re-decodes it, the
    // client never re-encodes.
    return { ok: true, code: s, host: host };
  }

  // ---- camera session ----------------------------------------------------
  // open({ video, formats, onStatus(kind), onCode(result) }) -> { close() }
  // kind: scanning | permission_denied | no_camera | busy | not_ut_code
  // A valid payload needs two identical consecutive reads (ut-docs#4342: one
  // frame can decode a partial pass as a wrong value). Closes itself first,
  // then calls onCode. Also stops on pagehide / tab hidden / video removed.
  function open(opts) {
    var video = opts.video;
    var formats = opts.formats || ['qr_code'];
    var stream = null, raf = null, closed = false, candidate = null, detector = null;

    function stopCamera() {
      if (raf) { cancelAnimationFrame(raf); raf = null; }
      if (stream) { stream.getTracks().forEach(function (t) { t.stop(); }); stream = null; }
      if (video) video.srcObject = null;
    }
    function close() {
      if (closed) return;
      closed = true;
      stopCamera();
      window.removeEventListener('pagehide', close);
      document.removeEventListener('visibilitychange', onVis);
    }
    function onVis() { if (document.hidden) close(); }
    window.addEventListener('pagehide', close);
    document.addEventListener('visibilitychange', onVis);

    function status(kind) { if (opts.onStatus) opts.onStatus(kind); }

    function frame() {
      if (closed || !stream) return;
      // The page was swapped away (htmx boost): release the camera.
      if (video && !video.isConnected) { close(); return; }
      detector.detect(video).then(function (codes) {
        if (closed || !stream) return;
        var raw = codes && codes.length ? codes[0].rawValue : '';
        if (raw) {
          var r = parse(raw);
          if (!r.ok) {
            candidate = null;
            status('not_ut_code');
          } else if (raw === candidate) {
            close();
            if (opts.onCode) opts.onCode(r);
            return;
          } else {
            candidate = raw;
            status('scanning');
          }
        }
        raf = requestAnimationFrame(frame);
      }).catch(function () {
        // A transient per-frame decode error must not end the session.
        if (closed || !stream) return;
        raf = requestAnimationFrame(frame);
      });
    }

    status('scanning');
    // Non-secure origin (plain-http LAN): mediaDevices is undefined and
    // calling it would throw before any .catch() exists.
    if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
      status('no_camera');
      return { close: close };
    }
    var ready = getDetector(formats);
    ready.catch(function () {});
    navigator.mediaDevices.getUserMedia({ video: { facingMode: 'environment' } })
      .then(function (s) {
        // Closed while the permission prompt was pending: never leave an
        // orphaned live camera behind a hidden overlay.
        if (closed) { s.getTracks().forEach(function (t) { t.stop(); }); return; }
        stream = s;
        video.srcObject = s;
        ready.then(function (d) {
          if (closed || stream !== s) return;
          detector = d;
          raf = requestAnimationFrame(frame);
        }, function () {
          if (closed || stream !== s) return;
          stopCamera(); // no decoder, no scanning: release the camera
          status('no_camera');
        });
      })
      .catch(function (err) {
        if (closed) return;
        var n = err && err.name;
        if (n === 'NotAllowedError' || n === 'SecurityError') status('permission_denied');
        else if (n === 'NotReadableError') status('busy');
        else status('no_camera');
      });
    return { close: close };
  }

  window.utQrScan = { parse: parse, open: open, setDecoderVersion: setDecoderVersion };
})();

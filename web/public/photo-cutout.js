// ut-docs#3127: "Remove background" in the item, category and variant photo
// pickers (docs repo architecture/ai-product-photo.md §6). One module, three
// thin adopters: each page renders web/ui/partials/photo_cutout.html (only
// when the viewer holds catalog_management; the engine is the shop's rembg
// service or the built-in model, ut-docs#4137) bound to its own file input, and this script
// drives it. Without that markup this file does nothing.
//
// Flow: a photo is picked (the input's own `change`, or `photo-cutout:pick`
// dispatched by a page that assigned the file itself — a DataTransfer
// assignment fires no `change`) → the panel offers Remove background / Use
// original photo, and NOTHING is sent until Remove background is tapped →
// the photo is drawn to a canvas at a 1024 px long edge (which also applies
// the EXIF orientation) and posted to POST /api/catalog/image/cutout →
// original and result side by side, background choice (tile colour / white
// / none; a change re-requests, cached per choice) → Use this puts the PNG
// into the SAME input, so the page's own save runs unchanged, and tells the
// page with `photo-cutout:accept` ({file, cutout: true|false}).
//
// Engines (ut-docs#4137, ADR-0154): the panel's data-cutout-engine says
// which. "service" posts the photo to the shop's rembg service through
// /api/catalog/image/cutout (the flow above). "builtin" — the default when
// no service is configured — cuts the photo out HERE: the 1024 px copy is
// sampled to u2netp's 320×320 input, a Web Worker (photo-cutout-worker.js,
// started per tap and terminated as soon as it answers) runs the vendored
// model, the mask is scaled back as the photo's alpha, and the till only
// squares that PNG (POST /api/catalog/image/tile). Nothing is loaded before
// the tap; a background change re-tiles the same cut-out, no second run.
//
// Errors never block: any failure shows the panel's own translated message
// (the server's text is never shown) and the original photo stays
// selected. Inline panel, no dialog, no showModal (ADR-0003: nothing here
// is on the sale path). No user-facing strings in this file — they come
// from the panel's data-msg-* attributes (guard-i18n).
(function () {
  'use strict';
  if (window.utPhotoCutout) return;

  var ENDPOINT = '/api/catalog/image/cutout';
  var TILE_ENDPOINT = '/api/catalog/image/tile';
  var MODEL_EDGE = 320; // u2netp's input
  // ImageNet normalisation, as rembg feeds u2netp.
  var MEAN = [0.485, 0.456, 0.406];
  var STD = [0.229, 0.224, 0.225];
  // Built-in run: loading ~19 MB and inferring on a Pi can take a while;
  // past this the worker is terminated and the panel says unavailable.
  var BUILTIN_TIMEOUT_MS = 120000;
  var LONG_EDGE = 1024;
  // The server allows the service 90 s (bgremove.cutoutTimeout); give the
  // whole round trip a little more, then call it unavailable.
  var TIMEOUT_MS = 100000;
  var states = new WeakMap();

  function panelFor(input) {
    if (!input || input.type !== 'file') return null;
    var panels = document.querySelectorAll('.photo-cutout');
    for (var i = 0; i < panels.length; i++) {
      var p = panels[i];
      if (p.dataset.cutoutInput && p.dataset.cutoutInput === input.id) return p;
      if (p.dataset.cutoutInputs) {
        try { if (input.matches(p.dataset.cutoutInputs)) return p; } catch (e) { /* bad selector: not ours */ }
      }
    }
    return null;
  }

  function q(panel, sel) { return panel.querySelector(sel); }
  function btn(panel, action) { return q(panel, '[data-cutout-action="' + action + '"]'); }

  function revoke(st) {
    if (!st) return;
    if (st.originalURL) URL.revokeObjectURL(st.originalURL);
    Object.keys(st.results).forEach(function (k) {
      if (st.results[k].url) URL.revokeObjectURL(st.results[k].url);
    });
    st.originalURL = null;
    st.results = {};
  }

  function setStatus(panel, level, text) {
    var el = q(panel, '[data-cutout-status]');
    if (!el) return;
    el.classList.toggle('photo-cutout-error', level === 'error');
    el.textContent = text || '';
  }

  function setBusy(panel, busy) {
    panel.setAttribute('aria-busy', busy ? 'true' : 'false');
    // "Use original photo" stays live: a slow or hung service must never
    // take away the way out (finish() aborts the request in flight).
    panel.querySelectorAll('button').forEach(function (b) {
      b.disabled = busy && b.getAttribute('data-cutout-action') !== 'original';
    });
  }

  // Back to the offer state (or fully hidden with hide=true).
  function reset(panel, hide) {
    if (typeof panel === 'string') panel = document.getElementById(panel);
    if (!panel) return;
    var st = states.get(panel);
    revoke(st);
    if (st) {
      st.seq++;
      if (st.ctl) { st.ctl.abort(); st.ctl = null; }
      stopWorker(st);
    }
    setBusy(panel, false);
    setStatus(panel, '', '');
    q(panel, '[data-cutout-compare]').hidden = true;
    q(panel, '[data-cutout-result-fig]').hidden = true;
    q(panel, '[data-cutout-bg]').hidden = true;
    btn(panel, 'remove').hidden = false;
    btn(panel, 'accept').hidden = true;
    btn(panel, 'retake').hidden = true;
    if (hide) {
      var hadFocus = panel.contains(document.activeElement);
      panel.hidden = true;
      if (hadFocus && st && st.returnFocus && st.returnFocus.focus) st.returnFocus.focus();
    }
  }

  // The form's colour, if it is one of the palette's own values (the page's
  // colour tiles carry them); anything else → "" (a white tile), which the
  // server also accepts. Never a free value — the server would 400 it.
  function tileColor(panel) {
    var id = panel.dataset.cutoutColor;
    var field = id ? document.getElementById(id) : null;
    var v = field ? String(field.value || '').trim().toLowerCase() : '';
    if (!v) return '';
    var ok = false;
    document.querySelectorAll('.item-color-tile[data-color]').forEach(function (t) {
      if ((t.getAttribute('data-color') || '').toLowerCase() === v) ok = true;
    });
    return ok ? v : '';
  }

  function offer(input, opts) {
    var panel = panelFor(input);
    if (!panel) return;
    var file = input.files && input.files[0];
    if (!file) { reset(panel, true); return; }
    var prev = states.get(panel);
    revoke(prev);
    if (prev && prev.ctl) { prev.ctl.abort(); prev.ctl = null; }
    if (prev) stopWorker(prev);
    // One panel serves every variant row: a pick left undecided on another
    // row would otherwise sit in that row's input and never upload — drop
    // it, so what the operator sees is what is pending.
    if (prev && prev.input !== input) prev.input.value = '';
    var st = {
      input: input,
      file: file,
      results: {},
      choice: 'tile',
      seq: prev ? prev.seq + 1 : 1,
      originalURL: null,
      returnFocus: document.activeElement && document.activeElement !== document.body ? document.activeElement : null,
      autoSave: !!(opts && opts.autoSave)
    };
    states.set(panel, st);
    reset(panel, false);
    var target = q(panel, '[data-cutout-target]');
    var label = input.dataset.cutoutLabel || '';
    target.textContent = label;
    target.hidden = !label;
    paintChoice(panel, 'tile');
    paintSwatch(panel);
    if (panel.dataset.cutoutPreview === '1') {
      st.originalURL = URL.createObjectURL(file);
      q(panel, '[data-cutout-original]').src = st.originalURL;
      q(panel, '[data-cutout-compare]').hidden = false;
    }
    panel.hidden = false;
    if (panel.scrollIntoView) panel.scrollIntoView({ block: 'nearest' });
  }

  // The tile-colour swatch follows the form's colour field, which the
  // operator can change after the pick (Details tab); repainted on every
  // request so it always shows the colour actually sent.
  function paintSwatch(panel) {
    var swatch = q(panel, '.photo-cutout-swatch-tile');
    if (swatch) swatch.style.setProperty('--swatch', tileColor(panel) || '#ffffff');
  }

  function paintChoice(panel, choice) {
    panel.querySelectorAll('[data-cutout-bg-choice]').forEach(function (b) {
      b.setAttribute('aria-pressed', b.getAttribute('data-cutout-bg-choice') === choice ? 'true' : 'false');
    });
  }

  // Draw the photo at a 1024 px long edge. An <img> decode applies the EXIF
  // orientation, so the canvas copy is upright and the server needs no EXIF
  // code; a 12 MP phone photo (over imaging.MaxPixels) becomes ~0.8 MP.
  function drawScaled(file) {
    return new Promise(function (resolve, reject) {
      var url = URL.createObjectURL(file);
      var img = new Image();
      img.onload = function () {
        var w = img.naturalWidth, h = img.naturalHeight;
        if (!w || !h) { URL.revokeObjectURL(url); reject(new Error('empty image')); return; }
        var scale = Math.min(1, LONG_EDGE / Math.max(w, h));
        var canvas = document.createElement('canvas');
        canvas.width = Math.max(1, Math.round(w * scale));
        canvas.height = Math.max(1, Math.round(h * scale));
        canvas.getContext('2d').drawImage(img, 0, 0, canvas.width, canvas.height);
        URL.revokeObjectURL(url);
        resolve(canvas);
      };
      img.onerror = function () { URL.revokeObjectURL(url); reject(new Error('decode failed')); };
      img.src = url;
    });
  }

  function toBlob(canvas, type) {
    return new Promise(function (resolve, reject) {
      canvas.toBlob(function (blob) {
        canvas.width = 0; canvas.height = 0;
        if (!blob) { reject(new Error('encode failed')); return; }
        resolve(blob);
      }, type, 0.9);
    });
  }

  function downscale(file) {
    return drawScaled(file).then(function (canvas) {
      return toBlob(canvas, file.type === 'image/png' ? 'image/png' : 'image/jpeg');
    });
  }

  // Ends a built-in run: terminates its worker and rejects its pending
  // inference at once (so nothing waits on the timeout holding the photo).
  function stopWorker(st) {
    if (!st) return;
    if (st.worker) { st.worker.terminate(); st.worker = null; }
    if (st.cancelInfer) { var c = st.cancelInfer; st.cancelInfer = null; c(); }
  }

  // u2netp's input: the photo stretched to 320×320, scaled by its brightest
  // channel value, then ImageNet-normalised, planar RGB (rembg's recipe).
  function modelInput(photo) {
    var c = document.createElement('canvas');
    c.width = MODEL_EDGE; c.height = MODEL_EDGE;
    var ctx = c.getContext('2d');
    ctx.drawImage(photo, 0, 0, MODEL_EDGE, MODEL_EDGE);
    var px = ctx.getImageData(0, 0, MODEL_EDGE, MODEL_EDGE).data;
    c.width = 0; c.height = 0;
    var n = MODEL_EDGE * MODEL_EDGE, peak = 1, i;
    for (i = 0; i < n * 4; i++) { if ((i & 3) !== 3 && px[i] > peak) peak = px[i]; }
    var out = new Float32Array(3 * n);
    for (i = 0; i < n; i++) {
      for (var ch = 0; ch < 3; ch++) out[ch * n + i] = (px[i * 4 + ch] / peak - MEAN[ch]) / STD[ch];
    }
    return out;
  }

  // Run the model in a fresh worker; resolves with the 320×320 saliency map.
  // seq: the run's st.seq; a run superseded (Use original photo, Retake, a
  // new pick) before the worker starts never starts one.
  function infer(panel, st, input, seq) {
    return new Promise(function (resolve, reject) {
      if (states.get(panel) !== st || st.seq !== seq) { reject(new Error('superseded')); return; }
      if (typeof WebAssembly !== 'object' || typeof Worker !== 'function') { reject(new Error('no wasm')); return; }
      var d = panel.dataset;
      var worker;
      // A plain script started as a module worker; where module workers are
      // missing the option is ignored and it runs classic (same code).
      try { worker = new Worker(d.cutoutWorker, { type: 'module' }); } catch (e) { reject(e); return; }
      stopWorker(st); // at most one run per panel
      st.worker = worker;
      var timer = setTimeout(function () { stopWorker(st); }, BUILTIN_TIMEOUT_MS);
      function done() {
        clearTimeout(timer);
        st.cancelInfer = null;
        if (st.worker === worker) { worker.terminate(); st.worker = null; }
      }
      st.cancelInfer = function () { clearTimeout(timer); reject(new Error('stopped')); };
      worker.onmessage = function (ev) {
        done();
        if (ev.data && ev.data.ok) resolve(ev.data.mask); else reject(new Error('inference failed'));
      };
      worker.onerror = function () { done(); reject(new Error('worker failed')); };
      worker.postMessage({ runtime: d.cutoutRuntime, wasm: d.cutoutWasm, model: d.cutoutModel, input: input }, [input.buffer]);
    });
  }

  // The built-in cut-out: the 1024 px photo with the model's mask, min-max
  // normalised and scaled back up, as its alpha channel. PNG blob.
  function builtinCutout(panel, st) {
    if (st.cutout) return Promise.resolve(st.cutout);
    var seq = st.seq;
    return drawScaled(st.file).then(function (photo) {
      return infer(panel, st, modelInput(photo), seq).then(function (mask) {
        var n = MODEL_EDGE * MODEL_EDGE, lo = Infinity, hi = -Infinity, i;
        for (i = 0; i < n; i++) { if (mask[i] < lo) lo = mask[i]; if (mask[i] > hi) hi = mask[i]; }
        var range = hi - lo > 1e-6 ? hi - lo : 1;
        var m = document.createElement('canvas');
        m.width = MODEL_EDGE; m.height = MODEL_EDGE;
        var mctx = m.getContext('2d');
        var md = mctx.createImageData(MODEL_EDGE, MODEL_EDGE);
        for (i = 0; i < n; i++) md.data[i * 4 + 3] = Math.round((mask[i] - lo) / range * 255);
        mctx.putImageData(md, 0, 0);
        // Scale the mask to the photo with the canvas's own smoothing.
        var w = photo.width, h = photo.height;
        var big = document.createElement('canvas');
        big.width = w; big.height = h;
        var bctx = big.getContext('2d');
        bctx.imageSmoothingEnabled = true;
        bctx.imageSmoothingQuality = 'high';
        bctx.drawImage(m, 0, 0, w, h);
        var alpha = bctx.getImageData(0, 0, w, h).data;
        m.width = 0; m.height = 0; big.width = 0; big.height = 0;
        var pctx = photo.getContext('2d');
        var pd = pctx.getImageData(0, 0, w, h);
        for (i = 3; i < pd.data.length; i += 4) pd.data[i] = alpha[i];
        pctx.putImageData(pd, 0, 0);
        return toBlob(photo, 'image/png');
      }, function (e) { photo.width = 0; photo.height = 0; throw e; });
    }).then(function (blob) { st.cutout = blob; return blob; });
  }

  function request(panel, st, choice) {
    var color = choice === 'tile' ? tileColor(panel) : '';
    var key = choice + '|' + color;
    var seq = st.seq;
    if (st.results[key]) return Promise.resolve(st.results[key]);
    var builtin = panel.dataset.cutoutEngine === 'builtin';
    var p;
    if (builtin) p = builtinCutout(panel, st);
    else p = st.photo ? Promise.resolve(st.photo) : downscale(st.file).then(function (b) { st.photo = b; return b; });
    return p.then(function (photo) {
      var fd = new FormData();
      fd.append('photo', photo, photo.type === 'image/png' ? 'photo.png' : 'photo.jpg');
      fd.append('background', choice);
      fd.append('tile_color', color);
      var ctl = window.AbortController ? new AbortController() : null;
      st.ctl = ctl;
      var timer = ctl ? setTimeout(function () { ctl.abort(); }, TIMEOUT_MS) : null;
      return fetch(builtin ? TILE_ENDPOINT : ENDPOINT, { method: 'POST', body: fd, credentials: 'same-origin', signal: ctl ? ctl.signal : undefined })
        .then(function (res) {
          if (timer) clearTimeout(timer);
          if (st.ctl === ctl) st.ctl = null;
          var type = res.headers.get('Content-Type') || '';
          if (!res.ok || type.indexOf('image/png') !== 0) {
            var err = new Error('cutout failed');
            err.status = res.status;
            throw err;
          }
          return res.blob();
        }, function (e) { if (timer) clearTimeout(timer); throw e; })
        .then(function (blob) {
          // Superseded (another pick, Retake, Use original): run() drops
          // the answer, so no object URL that nobody would revoke.
          if (states.get(panel) !== st || st.seq !== seq) return { blob: blob, url: null };
          var r = { blob: blob, url: URL.createObjectURL(blob) };
          st.results[key] = r;
          return r;
        });
    });
  }

  function run(panel, choice) {
    var st = states.get(panel);
    if (!st) return;
    // The page may have dropped the pick since (item switch, icon chosen).
    if (!st.input.files || st.input.files[0] !== st.file) { reset(panel, true); return; }
    var seq = st.seq;
    var removeBtn = btn(panel, 'remove');
    var hadFocus = panel.contains(document.activeElement) ? document.activeElement : null;
    setBusy(panel, true);
    paintChoice(panel, choice);
    paintSwatch(panel);
    setStatus(panel, '', panel.dataset.msgRemoving);
    request(panel, st, choice).then(function (r) {
      if (states.get(panel) !== st || st.seq !== seq) return; // superseded
      setBusy(panel, false);
      setStatus(panel, '', '');
      st.choice = choice;
      st.current = r;
      paintChoice(panel, choice);
      if (!st.originalURL) st.originalURL = URL.createObjectURL(st.file);
      q(panel, '[data-cutout-original]').src = st.originalURL;
      q(panel, '[data-cutout-result]').src = r.url;
      q(panel, '[data-cutout-result-fig]').hidden = false;
      q(panel, '[data-cutout-compare]').hidden = false;
      q(panel, '[data-cutout-bg]').hidden = false;
      removeBtn.hidden = true;
      btn(panel, 'accept').hidden = false;
      btn(panel, 'retake').hidden = false;
      // Keep the keyboard user where the action now is, not on a button
      // that just disappeared.
      if (hadFocus === removeBtn) btn(panel, 'accept').focus();
      else if (hadFocus && hadFocus.focus) hadFocus.focus();
    }, function (err) {
      if (states.get(panel) !== st || st.seq !== seq) return;
      setBusy(panel, false);
      paintChoice(panel, st.current ? st.choice : choice);
      setStatus(panel, 'error', err && err.status === 422 ? panel.dataset.msgNoSubject : panel.dataset.msgUnavailable);
      if (hadFocus && hadFocus.focus && !hadFocus.hidden) hadFocus.focus();
    });
  }

  function finish(panel, useCutout) {
    var st = states.get(panel);
    if (!st) { reset(panel, true); return; }
    var input = st.input;
    var file = st.file;
    if (useCutout && st.current) {
      var base = (file.name || 'photo').replace(/\.[^.]*$/, '') || 'photo';
      var png;
      try {
        png = new File([st.current.blob], base + '-cutout.png', { type: 'image/png' });
        var dt = new DataTransfer();
        dt.items.add(png);
        input.files = dt.files;
        file = png;
      } catch (e) {
        // No DataTransfer (very old WebView): the original stays selected.
        setStatus(panel, 'error', panel.dataset.msgUnavailable);
        return;
      }
    }
    var autoSave = st.autoSave;
    var cutout = !!(useCutout && st.current);
    var submit = panel.dataset.cutoutSubmit === '1';
    reset(panel, true);
    states.delete(panel);
    input.dispatchEvent(new CustomEvent('photo-cutout:accept', {
      bubbles: true,
      detail: { file: file, cutout: cutout, autoSave: autoSave }
    }));
    if (submit && input.form) {
      if (input.form.requestSubmit) input.form.requestSubmit();
      else input.form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    }
  }

  document.addEventListener('change', function (ev) {
    var t = ev.target;
    if (t && t.type === 'file' && panelFor(t)) offer(t, null);
  });
  document.addEventListener('photo-cutout:pick', function (ev) {
    var t = ev.target;
    if (t && t.type === 'file') offer(t, ev.detail || null);
  });
  document.addEventListener('click', function (ev) {
    var el = ev.target && ev.target.closest ? ev.target.closest('.photo-cutout [data-cutout-action], .photo-cutout [data-cutout-bg-choice]') : null;
    if (!el) return;
    var panel = el.closest('.photo-cutout');
    var st = states.get(panel);
    var choice = el.getAttribute('data-cutout-bg-choice');
    if (choice) {
      if (st && st.current && choice !== st.choice) run(panel, choice);
      return;
    }
    switch (el.getAttribute('data-cutout-action')) {
      case 'remove': run(panel, st && st.choice ? st.choice : 'tile'); break;
      case 'accept': finish(panel, true); break;
      case 'original': finish(panel, false); break;
      case 'retake': {
        var id = panel.dataset.cutoutRetake;
        var trigger = id ? document.getElementById(id) : (st && st.input);
        reset(panel, true);
        if (trigger) trigger.click();
        break;
      }
    }
  });

  // Pages drop a pending pick (item switch, an icon chosen, dialog reopened)
  // without any event; they call reset(id) so the offer goes with it.
  window.utPhotoCutout = {
    reset: function (panelOrId) { reset(panelOrId, true); },
    available: function (input) { return !!panelFor(input); }
  };
})();

// ut-docs#3127: "Remove background" in the item, category and variant photo
// pickers (docs repo architecture/ai-product-photo.md §6). One module, three
// thin adopters: each page renders web/ui/partials/photo_cutout.html (only
// when the shop's background-removal service is configured and the viewer
// holds catalog_management) bound to its own file input, and this script
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
// Errors never block: any failure shows the panel's own translated message
// (the server's text is never shown) and the original photo stays
// selected. Inline panel, no dialog, no showModal (ADR-0003: nothing here
// is on the sale path). No user-facing strings in this file — they come
// from the panel's data-msg-* attributes (guard-i18n).
(function () {
  'use strict';
  if (window.utPhotoCutout) return;

  var ENDPOINT = '/api/catalog/image/cutout';
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
  function downscale(file) {
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
        var png = file.type === 'image/png';
        canvas.toBlob(function (blob) {
          canvas.width = 0; canvas.height = 0;
          if (!blob) { reject(new Error('encode failed')); return; }
          resolve(blob);
        }, png ? 'image/png' : 'image/jpeg', 0.9);
      };
      img.onerror = function () { URL.revokeObjectURL(url); reject(new Error('decode failed')); };
      img.src = url;
    });
  }

  function request(panel, st, choice) {
    var color = choice === 'tile' ? tileColor(panel) : '';
    var key = choice + '|' + color;
    var seq = st.seq;
    if (st.results[key]) return Promise.resolve(st.results[key]);
    var p = st.photo ? Promise.resolve(st.photo) : downscale(st.file).then(function (b) { st.photo = b; return b; });
    return p.then(function (photo) {
      var fd = new FormData();
      fd.append('photo', photo, photo.type === 'image/png' ? 'photo.png' : 'photo.jpg');
      fd.append('background', choice);
      fd.append('tile_color', color);
      var ctl = window.AbortController ? new AbortController() : null;
      st.ctl = ctl;
      var timer = ctl ? setTimeout(function () { ctl.abort(); }, TIMEOUT_MS) : null;
      return fetch(ENDPOINT, { method: 'POST', body: fd, credentials: 'same-origin', signal: ctl ? ctl.signal : undefined })
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

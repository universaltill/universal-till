/* ut-docs#6566: behaviour of the "Which reader?" picker in the tender overlay
 * (web/ui/partials/card_reader_picker.html, GET /api/pos/card-readers).
 *
 * - The tender grid's single "Card" button (#pay-card-readers) loads the
 *   picker into #card-reader-mount and the pay grid hides until Back.
 * - Tapping a row only SELECTS it (money is involved); Confirm posts the
 *   existing /api/pos/tender with the registry row id. Rows that cannot be
 *   chosen carry aria-disabled and ignore clicks and keys.
 * - Exactly one selectable reader: the panel submits straight away.
 * - A tender answered 409 reader_required / 400-409 card_reader.* (nothing was
 *   charged) opens the picker; the generic server-error alert is withdrawn.
 * - While open, the panel refreshes every ~10 s (the provider-state cache
 *   TTL) and announces state changes once in the polite live region.
 * Click handlers only: no pointer/drag handlers, so the list scrolls under a
 * finger. No user-facing strings here (guard-i18n scans web/public/**\/*.js):
 * every text comes from the fragment's own markup / data-* attributes. */
(function () {
  'use strict';
  if (window.__utCardReaderPicker) return;
  window.__utCardReaderPicker = true;

  var REFRESH_MS = 10000;
  var timer = null;

  function byId(id) { return document.getElementById(id); }
  function fill(template) {
    var args = Array.prototype.slice.call(arguments, 1), i = 0;
    return String(template || '').replace(/%s/g, function () { return i < args.length ? args[i++] : ''; });
  }
  // The Confirm label ("Pay %s on %s"): the amount and the reader name are
  // wrapped in <bdi> so a right-to-left reader name next to a left-to-right
  // amount (or the reverse) cannot reorder the sentence. DOM nodes only: the
  // name is shop data and never goes through innerHTML (O4, #6566).
  function setConfirmLabel(btn, template, amount, name) {
    var args = [amount, name], i = 0, nodes = [];
    String(template || '').split(/(%s)/).forEach(function (part) {
      if (part === '%s') {
        var b = document.createElement('bdi');
        b.textContent = i < args.length ? args[i++] : '';
        nodes.push(b);
      } else if (part) {
        nodes.push(document.createTextNode(part));
      }
    });
    btn.replaceChildren.apply(btn, nodes);
  }
  function options(root) {
    return Array.prototype.slice.call((root || document).querySelectorAll('#card-reader-list .card-reader-option'));
  }
  function selectable(opt) { return opt.getAttribute('aria-disabled') !== 'true'; }
  function picker() { return byId('card-reader-picker'); }
  function say(text) {
    var live = byId('card-reader-live');
    if (!live || !text) return;
    live.textContent = '';
    window.setTimeout(function () { live.textContent = text; }, 30);
  }

  function select(opt) {
    var form = byId('card-reader-form');
    options().forEach(function (o) {
      var on = o === opt;
      o.setAttribute('aria-checked', on ? 'true' : 'false');
      o.tabIndex = on ? 0 : -1;
    });
    if (!form) return;
    form.elements.reader.value = opt.getAttribute('data-reader-id') || '';
    form.elements.method.value = opt.getAttribute('data-method') || '';
    var btn = byId('card-reader-confirm');
    var ext = opt.getAttribute('data-kind') === 'external';
    setExternal(ext);
    if (btn) {
      btn.disabled = false;
      if (ext) {
        btn.textContent = btn.getAttribute('data-label-external') || '';
      } else {
        var amount = btn.getAttribute('data-amount') || '';
        setConfirmLabel(btn, btn.getAttribute('data-label'), amount, opt.getAttribute('data-name') || '');
      }
    }
  }

  // ut-docs#6755: the own-terminal block (auth code, last 4, Declined) shows,
  // and its inputs are enabled (so they post), only for an external row.
  function setExternal(on) {
    var box = byId('card-reader-external');
    if (!box) return;
    box.hidden = !on;
    var eb = byId('card-reader-external-error');
    if (eb) { eb.textContent = ''; eb.hidden = true; }
    Array.prototype.forEach.call(box.querySelectorAll('input'), function (i) {
      i.disabled = !on;
      if (!on) i.value = '';
    });
  }

  // The tender footer and the Offline toggle are hidden while the picker is
  // open so the reader list gets the room (O2, #6566): see .card-reader-picking.
  function setPicking(on) {
    var ov = byId('payment-overlay');
    if (ov) ov.classList.toggle('card-reader-picking', on);
  }
  function showPanel() {
    var grid = document.querySelector('#panel-pay .pay-grid');
    var mount = byId('card-reader-mount');
    setPicking(true);
    if (grid) grid.hidden = true;
    if (mount) mount.hidden = false;
  }
  function closePanel() {
    var grid = document.querySelector('#panel-pay .pay-grid');
    var mount = byId('card-reader-mount');
    stopRefresh();
    setPicking(false);
    if (mount) { mount.replaceChildren(); mount.hidden = true; }
    if (grid) grid.hidden = false;
    var card = byId('pay-card-readers');
    if (card && card.focus) card.focus();
  }

  function startRefresh() {
    stopRefresh();
    timer = window.setInterval(refresh, REFRESH_MS);
  }
  function stopRefresh() {
    if (timer) { window.clearInterval(timer); timer = null; }
  }

  // Re-fetch the fragment and copy each row's state onto the live DOM, keeping
  // the selection; announce what changed (names/states come from the markup).
  function refresh() {
    var p = picker();
    if (!p || p.getAttribute('data-pinned') === '1') { if (!p) stopRefresh(); return; }
    var form = byId('card-reader-form');
    if (form && form.getAttribute('aria-busy') === 'true') return;
    var url = '/api/pos/card-readers?source=pos&poll=1'; // poll=1: a timer refresh, not input (idle lock)
    var m = p.getAttribute('data-method-filter');
    if (m) url += '&method=' + encodeURIComponent(m);
    fetch(url, { headers: { 'HX-Request': 'true' }, credentials: 'same-origin' }).then(function (r) {
      return r.ok ? r.text() : null;
    }).then(function (html) {
      if (!html || !picker()) return;
      var doc = new DOMParser().parseFromString(html, 'text/html');
      var tpl = (byId('card-reader-live') || {}).getAttribute && byId('card-reader-live').getAttribute('data-announce');
      var changes = [];
      options(doc).forEach(function (fresh) {
        var id = fresh.getAttribute('data-reader-id');
        var cur = options().filter(function (o) { return o.getAttribute('data-reader-id') === id; })[0];
        if (!cur) return;
        if (cur.getAttribute('data-state') !== fresh.getAttribute('data-state')) {
          var txt = fresh.querySelector('.card-reader-state-text');
          changes.push(fill(tpl, cur.getAttribute('data-name') || '', txt ? txt.textContent.trim() : ''));
        }
        cur.setAttribute('data-state', fresh.getAttribute('data-state'));
        var curState = cur.querySelector('.card-reader-state'), freshState = fresh.querySelector('.card-reader-state');
        if (curState && freshState) curState.replaceWith(freshState);
        var curReason = cur.querySelector('.card-reader-reason'), freshReason = fresh.querySelector('.card-reader-reason');
        if (curReason && freshReason) curReason.textContent = freshReason.textContent;
        if (fresh.getAttribute('aria-disabled') === 'true') {
          cur.setAttribute('aria-disabled', 'true');
          if (cur.getAttribute('aria-checked') === 'true') cur.setAttribute('aria-checked', 'false');
        } else {
          cur.removeAttribute('aria-disabled');
        }
      });
      var chosen = options().filter(function (o) { return o.getAttribute('aria-checked') === 'true'; })[0];
      var btn = byId('card-reader-confirm');
      if (!chosen && btn) btn.disabled = true;
      if (changes.length) say(changes.join('. '));
    }).catch(function () { /* offline: keep what is shown; the tender itself is never blocked by this check */ });
  }

  // A freshly swapped-in picker: show it, then auto-submit / focus the default.
  function onPickerSwapped() {
    var p = picker();
    if (!p) return;
    showPanel();
    startRefresh();
    var cbtn = byId('card-reader-confirm');
    var pre = options().filter(function (o) { return o.getAttribute('aria-checked') === 'true'; })[0];
    if (pre && pre.getAttribute('data-kind') === 'external') select(pre);
    else if (cbtn && pre) setConfirmLabel(cbtn, cbtn.getAttribute('data-label'), cbtn.getAttribute('data-amount') || '', pre.getAttribute('data-name') || '');
    var form = byId('card-reader-form');
    if (p.getAttribute('data-auto') === '1' && form) {
      var only = options().filter(selectable)[0];
      // never auto-submit an own-terminal row: the Paid step is the point (#6755)
      if (only && only.getAttribute('data-kind') === 'external') {
        select(only);
        if (only.focus) only.focus();
        return;
      }
      if (only) {
        select(only);
        var live = byId('card-reader-live');
        say(fill(live && live.getAttribute('data-auto-announce'), only.getAttribute('data-name') || ''));
        if (window.htmx) window.htmx.trigger(form, 'submit');
        return;
      }
    }
    var cur = options().filter(function (o) { return o.getAttribute('aria-checked') === 'true'; })[0] || options().filter(selectable)[0];
    if (cur && cur.focus) cur.focus();
  }

  document.addEventListener('htmx:afterSwap', function (ev) {
    var t = ev.detail && ev.detail.target;
    if (t && t.id === 'card-reader-mount') onPickerSwapped();
  });

  document.addEventListener('click', function (ev) {
    var t = ev.target;
    if (!t || !t.closest) return;
    var opt = t.closest('.card-reader-option');
    if (opt && picker() && picker().contains(opt)) {
      if (selectable(opt)) select(opt);
      return;
    }
    if (t.closest('#card-reader-back')) { closePanel(); return; }
    if (t.closest('#card-reader-external-declined')) {
      // Declined: nothing is sent or recorded; back to the pay grid.
      var live = byId('card-reader-live');
      var msg = live ? live.getAttribute('data-declined') : '';
      setExternal(false);
      closePanel();
      var lastLive = byId('pos-live') || document.querySelector('[role="status"]');
      if (lastLive && msg) { lastLive.textContent = ''; window.setTimeout(function () { lastLive.textContent = msg; }, 30); }
    }
  });

  document.addEventListener('keydown', function (ev) {
    var p = picker();
    if (!p || !p.contains(ev.target)) return;
    if (ev.key === 'Escape') { ev.preventDefault(); closePanel(); return; }
    var opt = ev.target.closest && ev.target.closest('.card-reader-option');
    if (!opt) return;
    var rtl = getComputedStyle(p).direction === 'rtl';
    var step = 0;
    if (ev.key === 'ArrowDown') step = 1;
    else if (ev.key === 'ArrowUp') step = -1;
    else if (ev.key === 'ArrowRight') step = rtl ? -1 : 1;
    else if (ev.key === 'ArrowLeft') step = rtl ? 1 : -1;
    if (!step) return;
    ev.preventDefault();
    var list = options().filter(selectable);
    if (!list.length) return;
    var i = list.indexOf(opt);
    var next = list[(i < 0 ? 0 : i + step + list.length) % list.length];
    select(next);
    next.focus();
  });

  // The Card button: while the provider states are being asked (at most a few
  // seconds, never a blocker: Back works), show a checking line with a Back.
  function showChecking(btn) {
    var mount = byId('card-reader-mount');
    if (!mount) return;
    var wrap = document.createElement('div');
    wrap.className = 'card-reader-picker';
    var line = document.createElement('p');
    line.setAttribute('role', 'status');
    line.setAttribute('data-testid', 'card-reader-checking');
    line.textContent = btn.getAttribute('data-checking') || '';
    var back = document.createElement('button');
    back.type = 'button';
    back.id = 'card-reader-back';
    back.className = 'btn secondary card-reader-back';
    back.setAttribute('data-testid', 'card-reader-back');
    back.textContent = btn.getAttribute('data-back') || '';
    wrap.appendChild(line);
    wrap.appendChild(back);
    mount.replaceChildren(wrap);
    showPanel();
  }

  // Confirm pressed: a second tap is ignored while the tender is in flight.
  document.addEventListener('htmx:beforeRequest', function (ev) {
    var el = ev.detail && ev.detail.elt;
    if (el && el.id === 'pay-card-readers') { showChecking(el); return; }
    if (!el || el.id !== 'card-reader-form') return;
    if (el.getAttribute('aria-busy') === 'true') { ev.preventDefault(); return; }
    el.setAttribute('aria-busy', 'true');
    var btn = byId('card-reader-confirm');
    if (btn) btn.disabled = true;
  });

  document.addEventListener('htmx:afterRequest', function (ev) {
    var d = ev.detail || {};
    var el = d.elt;
    if (el && el.id === 'card-reader-form') {
      el.removeAttribute('aria-busy');
      var btn = byId('card-reader-confirm');
      var chosen = options().filter(function (o) { return o.getAttribute('aria-checked') === 'true'; })[0];
      if (btn) btn.disabled = !chosen;
    }
    // A tender that needs a reader choice (nothing was charged): open the picker.
    var xhr = d.xhr;
    // ut-docs#6755: a refused auth code / last 4: keep the panel, show why.
    if (xhr && xhr.status === 422 && el && el.id === 'card-reader-form') {
      var em = null;
      try { var ej = JSON.parse(xhr.responseText); if (ej.error && ej.error.code === 'card_reader.external.invalid') em = ej.error.message; } catch (e) { em = null; }
      if (em) {
        var ab = byId('pos-alert');
        var at = ab && ab.querySelector('.notice-text');
        if (ab && at && at.textContent === (ab.dataset.msgServer || '')) ab.hidden = true;
        var eb = byId('card-reader-external-error');
        if (eb) { eb.textContent = em; eb.hidden = false; }
        say(em);
        return;
      }
    }
    if (!xhr || (xhr.status !== 409 && xhr.status !== 400 && xhr.status !== 402)) return;
    var trig = xhr.getResponseHeader && xhr.getResponseHeader('HX-Trigger');
    if (!trig || trig.indexOf('card-reader-needed') === -1) return;
    // withdraw the generic "server error" alert app.js raised for the 4xx
    var alertBox = byId('pos-alert');
    var alertText = alertBox && alertBox.querySelector('.notice-text');
    if (alertBox && alertText && alertText.textContent === (alertBox.dataset.msgServer || '')) alertBox.hidden = true;
    var info = {};
    try { info = JSON.parse(trig)['card-reader-needed'] || {}; } catch (e) { return; }
    var mount = byId('card-reader-mount');
    if (!mount || !window.htmx) return; // split tender handles its own
    window.htmx.ajax('GET', '/api/pos/card-readers?source=pos', { target: '#card-reader-mount', swap: 'innerHTML' }).then(function () {
      var msg = null;
      try { msg = JSON.parse(xhr.responseText).error.message; } catch (e) { msg = null; }
      if (msg && info.code !== 'reader_required') say(msg);
    });
  });
})();

// tour.js (ut-docs#3710): the sale screen's guided tour — a balloon with an
// arrow, anchored to one real element per step, over a dimmed backdrop with
// that element cut out and ringed. Own code (no library, no CDN: ADR-0003).
//
// Data: web/ui/partials/tour.html renders <script type="application/json"
// id="ut-tour-steps" data-start="0|1"> on the sale screen — the translated
// labels and the steps, each with an ordered list of CSS selectors
// ("targets"; the first one on screen wins, none on screen → the step is
// skipped; no targets → a centred card). data-start="1" is the server's call
// (index_page.go tourShouldStart): a signed-in operator who hasn't finished
// it yet, or /?tour=1.
//
// Rules it keeps (repo CLAUDE.md, UX notes on the card):
//  - Never modal. The balloon is a non-modal role="dialog"; the backdrop is
//    visual only (pointer-events: none), so the status bar, Lock and
//    exit-to-OS stay reachable and a stray tap never dismisses it. No
//    showModal(), no #ut-scrim.
//  - Never starts while a <dialog> (payment overlay, hold, picker …) or the
//    shared scrim is up; if one opens mid-tour (the operator went on
//    selling), the tour ends — counted as skipped.
//  - Focus goes to Next, never to an input (no on-screen keyboard pops).
//    Enter on a focused button activates it; → / ← step forward/back (mirrored
//    in RTL, like the sale screen's tab bars); Esc skips.
//  - Finishing or skipping POSTs /api/tour/done (the session user's own
//    key); a failure is ignored — offline never blocks anything.
//  - Repositions on resize, rotation and scroll; the balloon stays inside
//    the viewport. prefers-reduced-motion: no transition (app.css).
//
// A persistent-shell script (ADR-0098, loaded once from base.html's <head>
// and listed in httpx.HeadAssets): checked on load and after every htmx
// settle, so a boosted navigation back to the sale screen is covered too.
// No user-facing strings: every label comes from the JSON above.
(function () {
  'use strict';

  var READY_TIMEOUT_MS = 4000;
  var WATCH_MS = 400;
  var GAP = 14;   // target edge → balloon edge (the arrow lives here)
  var PAD = 6;    // ring padding around the target
  var EDGE = 8;   // minimum distance from the viewport edge

  var tour = null; // the running tour, or null

  function rtl() {
    return (document.documentElement.getAttribute('dir') || '').toLowerCase() === 'rtl';
  }

  function blocked() {
    if (document.querySelector('dialog[open]')) return true;
    var scrim = document.getElementById('ut-scrim');
    return !!(scrim && !scrim.hidden);
  }

  // usable: on screen right now, with real size, not off-canvas (the phone
  // drawer slides its links out horizontally rather than hiding them).
  function usable(el) {
    if (!el || !el.isConnected) return false;
    if (typeof el.checkVisibility === 'function' &&
        !el.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true })) return false;
    var cs = getComputedStyle(el);
    if (cs.display === 'none' || cs.visibility === 'hidden') return false;
    var r = el.getBoundingClientRect();
    if (r.width < 1 || r.height < 1) return false;
    var vw = document.documentElement.clientWidth;
    if (r.right <= 0 || r.left >= vw) return false;
    return true;
  }

  // resolve → an element, null for a centred step, or undefined to skip.
  function resolve(step) {
    if (!step.targets || !step.targets.length) return null;
    for (var i = 0; i < step.targets.length; i++) {
      var el;
      try { el = document.querySelector(step.targets[i]); } catch (e) { el = null; }
      if (usable(el)) return el;
    }
    return undefined;
  }

  function fmtCounter(tpl, n, total) {
    var nf;
    try { nf = new Intl.NumberFormat(document.documentElement.lang || undefined); } catch (e) { nf = null; }
    var vals = [n, total].map(function (v) { return nf ? nf.format(v) : String(v); });
    var i = 0;
    return String(tpl || '%d / %d').replace(/%d/g, function () { return vals[i++] || ''; });
  }

  function el(tag, cls, attrs) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (attrs) Object.keys(attrs).forEach(function (k) { e.setAttribute(k, attrs[k]); });
    return e;
  }

  function markDone() {
    try {
      fetch('/api/tour/done', { method: 'POST', credentials: 'same-origin', keepalive: true })
        .catch(function () {});
    } catch (e) { /* offline or no fetch: the tour just shows again next time */ }
  }

  function build(labels) {
    var ring = el('div', 'ut-tour-ring', { 'aria-hidden': 'true', 'data-testid': 'tour-ring' });
    var box = el('div', 'ut-tour-balloon', {
      role: 'dialog', 'aria-modal': 'false',
      'aria-labelledby': 'ut-tour-title', 'aria-describedby': 'ut-tour-body',
      'data-testid': 'tour-balloon'
    });
    var arrow = el('span', 'ut-tour-arrow', { 'aria-hidden': 'true' });
    var title = el('h2', 'ut-tour-title', { id: 'ut-tour-title' });
    var body = el('p', 'ut-tour-body', { id: 'ut-tour-body' });
    var foot = el('div', 'ut-tour-foot');
    var counter = el('span', 'ut-tour-counter', { 'data-testid': 'tour-counter' });
    var actions = el('div', 'ut-tour-actions');
    var skip = el('button', 'btn secondary ut-tour-skip', { type: 'button', 'data-testid': 'tour-skip' });
    var back = el('button', 'btn secondary ut-tour-back', { type: 'button', 'data-testid': 'tour-back' });
    var next = el('button', 'btn primary ut-tour-next', { type: 'button', 'data-testid': 'tour-next' });
    skip.textContent = labels.skip || '';
    back.textContent = labels.back || '';
    actions.appendChild(skip);
    actions.appendChild(back);
    actions.appendChild(next);
    foot.appendChild(counter);
    foot.appendChild(actions);
    // The content scrolls on its own (a very short screen) so the arrow,
    // outside the balloon's edge, is never clipped by that overflow.
    var content = el('div', 'ut-tour-content');
    content.appendChild(title);
    content.appendChild(body);
    content.appendChild(foot);
    box.appendChild(arrow);
    box.appendChild(content);
    document.body.appendChild(ring);
    document.body.appendChild(box);
    skip.addEventListener('click', function () { end(true); });
    back.addEventListener('click', function () { go(-1); });
    next.addEventListener('click', function () { go(1); });
    return { ring: ring, box: box, arrow: arrow, title: title, body: body,
      counter: counter, skip: skip, back: back, next: next };
  }

  function clamp(v, lo, hi) { return Math.max(lo, Math.min(hi, v)); }

  function place() {
    if (!tour) return;
    var ui = tour.ui;
    var target = tour.target;
    var vw = document.documentElement.clientWidth;
    var vh = window.innerHeight;
    var bw = ui.box.offsetWidth;
    var bh = ui.box.offsetHeight;
    var placement, left, top, ax = null, ay = null;

    if (!target) {
      ui.ring.classList.add('is-centred');
      ui.ring.style.left = (vw / 2) + 'px';
      ui.ring.style.top = (vh / 2) + 'px';
      ui.ring.style.width = '0px';
      ui.ring.style.height = '0px';
      placement = 'centre';
      left = (vw - bw) / 2;
      top = (vh - bh) / 2;
    } else {
      var r = target.getBoundingClientRect();
      var t = {
        left: Math.max(r.left - PAD, 0), top: Math.max(r.top - PAD, 0),
        right: Math.min(r.right + PAD, vw), bottom: Math.min(r.bottom + PAD, vh)
      };
      ui.ring.classList.remove('is-centred');
      ui.ring.style.left = t.left + 'px';
      ui.ring.style.top = t.top + 'px';
      ui.ring.style.width = (t.right - t.left) + 'px';
      ui.ring.style.height = (t.bottom - t.top) + 'px';
      var cx = (t.left + t.right) / 2;
      var cy = (t.top + t.bottom) / 2;
      var fits = {
        below: t.bottom + GAP + bh <= vh - EDGE,
        above: t.top - GAP - bh >= EDGE,
        right: t.right + GAP + bw <= vw - EDGE,
        left: t.left - GAP - bw >= EDGE
      };
      // Beside an item of the vertical nav rail (towards the screen's
      // middle, the inline-end side); below/above everything else — the
      // phone's horizontal top bar included.
      var nav = target.closest('.nav');
      var nr = nav && nav.getBoundingClientRect();
      var sides = rtl() ? ['left', 'right'] : ['right', 'left'];
      var order = nr && nr.height > nr.width ? sides.concat(['below', 'above']) : ['below', 'above'].concat(sides);
      placement = 'over';
      for (var i = 0; i < order.length; i++) {
        if (fits[order[i]]) { placement = order[i]; break; }
      }
      if (placement === 'below' || placement === 'above') {
        left = clamp(cx - bw / 2, EDGE, vw - bw - EDGE);
        top = placement === 'below' ? t.bottom + GAP : t.top - GAP - bh;
        ax = clamp(cx - left, 18, bw - 18);
      } else if (placement === 'right' || placement === 'left') {
        left = placement === 'right' ? t.right + GAP : t.left - GAP - bw;
        top = clamp(cy - bh / 2, EDGE, vh - bh - EDGE);
        ay = clamp(cy - top, 18, bh - 18);
      } else {
        // A target too big to sit beside (the product panel on a small
        // screen): the balloon goes inside it, near its bottom edge.
        left = clamp(cx - bw / 2, EDGE, vw - bw - EDGE);
        top = clamp(t.bottom - bh - EDGE * 2, EDGE, vh - bh - EDGE);
      }
    }
    left = clamp(left, EDGE, Math.max(EDGE, vw - bw - EDGE));
    top = clamp(top, EDGE, Math.max(EDGE, vh - bh - EDGE));
    // Physical px from getBoundingClientRect geometry: correct in RTL too.
    ui.box.style.left = Math.round(left) + 'px';
    ui.box.style.top = Math.round(top) + 'px';
    ui.box.setAttribute('data-placement', placement);
    // The arrow is a 14px square turned 45°, half under the balloon's edge
    // facing the target; its inner half blends into the balloon (same
    // background, no border).
    var A = 7;
    ui.arrow.hidden = ax === null && ay === null;
    if (placement === 'below') { ui.arrow.style.left = (ax - A) + 'px'; ui.arrow.style.top = -A + 'px'; }
    else if (placement === 'above') { ui.arrow.style.left = (ax - A) + 'px'; ui.arrow.style.top = (bh - A) + 'px'; }
    else if (placement === 'right') { ui.arrow.style.left = -A + 'px'; ui.arrow.style.top = (ay - A) + 'px'; }
    else if (placement === 'left') { ui.arrow.style.left = (bw - A) + 'px'; ui.arrow.style.top = (ay - A) + 'px'; }
  }

  var raf = 0;
  function schedulePlace() {
    if (raf) return;
    raf = requestAnimationFrame(function () { raf = 0; place(); });
  }

  function show(i, dir) {
    var steps = tour.steps;
    while (i >= 0 && i < steps.length) {
      var target = resolve(steps[i]);
      if (target !== undefined) {
        tour.index = i;
        tour.target = target;
        break;
      }
      i += dir || 1;
    }
    if (i < 0) { show(0, 1); return; }
    if (i >= steps.length) { end(false); return; }
    var step = steps[tour.index];
    var ui = tour.ui;
    var last = tour.index === steps.length - 1;
    ui.box.setAttribute('data-step', step.id || String(tour.index));
    ui.title.textContent = step.title || '';
    ui.body.textContent = step.body || '';
    ui.counter.textContent = fmtCounter(tour.labels.counter, tour.index + 1, steps.length);
    ui.back.hidden = tour.index === 0;
    ui.next.textContent = (last ? tour.labels.done : tour.labels.next) || '';
    if (tour.target) {
      var r = tour.target.getBoundingClientRect();
      if (r.top < 0 || r.bottom > window.innerHeight || r.left < 0 || r.right > document.documentElement.clientWidth) {
        tour.target.scrollIntoView({ block: 'nearest', inline: 'nearest', behavior: 'auto' });
      }
    }
    place();
    try { ui.next.focus({ preventScroll: true }); } catch (e) { ui.next.focus(); }
  }

  function go(dir) {
    if (!tour) return;
    if (dir > 0 && tour.index >= tour.steps.length - 1) { end(false); return; }
    show(tour.index + dir, dir);
  }

  // record=false: the operator was interrupted (a dialog opened, or they
  // navigated off the sale screen) — tear the balloon down but don't mark
  // the tour done, so it is offered again next time (ut-docs#3710 review:
  // only finishing or skipping counts as seen).
  function end(skipped, record) {
    if (!tour) return;
    var t = tour;
    tour = null;
    clearInterval(t.watch);
    window.removeEventListener('resize', schedulePlace);
    window.removeEventListener('orientationchange', schedulePlace);
    window.removeEventListener('scroll', schedulePlace, true);
    document.removeEventListener('keydown', onKey, true);
    t.ui.ring.remove();
    t.ui.box.remove();
    if (record !== false) markDone();
    var f = t.returnFocus;
    if (f && f.isConnected && typeof f.focus === 'function') {
      try { f.focus({ preventScroll: true }); } catch (e) { /* ignore */ }
    }
    document.dispatchEvent(new CustomEvent('ut-tour-end', { detail: { skipped: !!skipped } }));
  }

  function onKey(e) {
    if (!tour) return;
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      end(true);
      return;
    }
    // Arrows only while focus is in the balloon: elsewhere they belong to
    // the tab bars, inputs and tiles under it.
    if (!tour.ui.box.contains(document.activeElement)) return;
    var fwd = rtl() ? 'ArrowLeft' : 'ArrowRight';
    var bwd = rtl() ? 'ArrowRight' : 'ArrowLeft';
    if (e.key === fwd) { e.preventDefault(); go(1); }
    else if (e.key === bwd) { e.preventDefault(); if (tour.index > 0) go(-1); }
  }

  function watch() {
    if (!tour) return;
    // The operator went on: opened a dialog, or navigated off the sale screen.
    if (blocked() || !tour.source.isConnected) { end(true, false); return; }
    if (tour.target && !usable(tour.target)) { show(tour.index, 1); return; }
    place();
  }

  function start(cfg, source) {
    if (tour || blocked()) return false;
    var steps = (cfg.steps || []).filter(function (s) { return resolve(s) !== undefined; });
    if (!steps.length) return false;
    tour = {
      steps: steps, labels: cfg.labels || {}, index: 0, target: null, source: source,
      returnFocus: document.activeElement, ui: build(cfg.labels || {})
    };
    window.addEventListener('resize', schedulePlace);
    window.addEventListener('orientationchange', schedulePlace);
    window.addEventListener('scroll', schedulePlace, true);
    document.addEventListener('keydown', onKey, true);
    tour.watch = setInterval(watch, WATCH_MS);
    show(0, 1);
    return true;
  }

  // The sale screen fills in after first paint in places (the basket and the
  // grid fall back to an htmx load, the session chip always does): wait for
  // them — bounded — so their steps aren't skipped for being empty.
  function ready() {
    if (!document.getElementById('basket')) return false;
    if (!document.querySelector('.products:not([hx-trigger="load"])')) return false;
    var chip = document.getElementById('session-chip');
    if (chip && !chip.firstElementChild) return false;
    return !!window.Alpine;
  }

  function dropTourParam() {
    try {
      var u = new URL(window.location.href);
      if (u.searchParams.get('tour') !== '1') return;
      u.searchParams.delete('tour');
      history.replaceState(history.state, '', u.pathname + (u.search || '') + u.hash);
    } catch (e) { /* old WebView: a reload just restarts the tour */ }
  }

  function maybeStart() {
    var src = document.getElementById('ut-tour-steps');
    if (!src || src.getAttribute('data-ut-tour-seen') === '1') return;
    src.setAttribute('data-ut-tour-seen', '1');
    if (src.getAttribute('data-start') !== '1') return;
    var cfg;
    try { cfg = JSON.parse(src.textContent || '{}'); } catch (e) { return; }
    dropTourParam();
    var t0 = Date.now();
    (function wait() {
      if (!src.isConnected) return;
      var timedOut = Date.now() - t0 > READY_TIMEOUT_MS;
      if ((ready() && !blocked()) || (timedOut && !blocked())) {
        // One frame more so Alpine's x-show and the layout have settled.
        requestAnimationFrame(function () { if (src.isConnected) start(cfg, src); });
        return;
      }
      if (timedOut) return; // a dialog is still up: not this time
      setTimeout(wait, 100);
    })();
  }

  window.utTour = {
    start: function (cfg) { return start(cfg || {}, document.getElementById('ut-tour-steps') || document.body); },
    end: function () { end(true); },
    active: function () { return !!tour; }
  };

  document.addEventListener('htmx:afterSettle', maybeStart);
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', maybeStart);
  } else {
    maybeStart();
  }
})();

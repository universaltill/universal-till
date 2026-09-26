// sell-screen-watch.js (ut-docs#2765): keeps an OPEN sale screen's tile grid
// in step with catalog changes made outside this document.
//
// The grid root (.products in web/ui/partials/buttons.html) re-fetches
// itself on `buttons-changed from:body`, but that event only ever reaches
// the document whose own request answered with HX-Trigger: buttons-changed.
// A change made anywhere else -- another till or browser tab, a my./cloud
// catalog push, a main-till -> replica admin sync pull -- used to leave the
// sale screen showing (and selling from) the old grid until something
// happened to reload it.
//
// How: every grid render carries the catalog generation it was rendered at
// -- as the X-UT-Sell-Version header of a GET /ui/buttons response and, since
// ut-docs#2989 inlined the grid into GET / itself (a page response has no
// such header), as data-sell-version on the grid root; the root attribute is
// read on load and after every swap, the header on every grid request
// (sell_screen_version, migrations 042/047 -- catalog tables only, so a
// completed sale never moves it). While the page is visible, this polls
// GET /ui/buttons/version every POLL_MS (and at once when the page becomes
// visible again); when the value differs from the grid's own, it fires
// `buttons-changed` on <body>, and the grid refreshes itself the normal way
// (buttons.html's restoreGridState keeps the selected tab and search).
//
// Never in the operator's way: the refresh waits (re-checked next tick)
// while the grid is being edited (the Designer's replica, the sale screen's
// jiggle mode, a tile being dragged), while any <dialog> is open (the
// category popup, modifier/variant picker, payment overlay, hold, manager
// PIN prompt, ...), while a search query is active, or while a pointer is
// down. One request in flight at most; a failed/offline poll is silently
// ignored -- this never blocks checkout (offline-first, ADR-0003).
//
// A persistent-shell script (ADR-0098, loaded once from base.html's <head>
// and listed in httpx.HeadAssets): it only ever acts while the current page
// has a sale-screen grid, so it is a no-op everywhere else, same pattern as
// record-dialog.js / list-reorder.js. No user-facing strings.
(function () {
  'use strict';
  var POLL_MS = 5000;
  // A refresh this watcher asked for that never came back (the request
  // failed without an htmx:afterRequest, the grid was swapped away) must not
  // wedge the watcher forever.
  var REFRESH_TIMEOUT_MS = 15000;
  var FETCH_TIMEOUT_MS = 10000;
  var VERSION_URL = '/ui/buttons/version';
  var HEADER = 'X-UT-Sell-Version';

  var rendered = null;    // generation of the grid on screen, as a string
  var inFlight = false;   // a /ui/buttons/version poll is running
  var refreshAt = 0;      // when this watcher last fired buttons-changed (0 = none pending)
  var pointersDown = 0;
  var timer = null;

  // The sale screen's own grid: the at-rest root, never the Designer's
  // edit-mode replica (.products--edit / hx-vals mode=edit).
  function saleGrid() {
    var g = document.querySelector('.products[hx-get="/ui/buttons"]');
    if (!g || g.classList.contains('products--edit') || g.hasAttribute('hx-vals')) return null;
    if (document.getElementById('buttons-grid-admin')) return null;
    return g;
  }

  function busy(g) {
    if (pointersDown > 0) return true;
    if (g.querySelector('#buttons-grid.jiggle-mode, .jiggle-active, .dragging')) return true;
    if (document.querySelector('dialog[open]')) return true;
    var s = document.getElementById('products-search');
    if (s && ((s.value && s.value.trim() !== '') || s.getClientRects().length > 0)) return true;
    var st = window.utSaleGridState;
    if (st && st.q) return true;
    return false;
  }

  function isGridElt(elt) {
    if (!elt || !elt.classList || !elt.classList.contains('products')) return false;
    if (elt.classList.contains('products--edit') || elt.hasAttribute('hx-vals')) return false;
    return elt.getAttribute('hx-get') === '/ui/buttons';
  }

  // Record the version every grid render carries. The grid swaps itself out
  // (hx-swap="outerHTML"), and htmx 1.x then re-fires htmx:afterRequest on
  // the nearest still-attached ancestor with detail.elt rewritten to THAT
  // ancestor -- so the grid's requests are marked by their xhr at
  // htmx:beforeRequest (fired on the still-attached grid) and recognised by
  // it afterwards. Recording twice is harmless.
  var gridXhrs = typeof WeakSet === 'function' ? new WeakSet() : null;
  document.addEventListener('htmx:beforeRequest', function (e) {
    var d = e.detail;
    if (gridXhrs && d && d.xhr && isGridElt(d.elt)) gridXhrs.add(d.xhr);
  });
  document.addEventListener('htmx:afterRequest', function (e) {
    var d = e.detail;
    if (!gridXhrs || !d || !d.xhr || !gridXhrs.has(d.xhr)) return;
    // A failed grid render keeps refreshAt, so the retry waits
    // REFRESH_TIMEOUT_MS instead of re-rendering on every tick.
    if (!d.successful) return;
    refreshAt = 0;
    var v = d.xhr.getResponseHeader(HEADER);
    rendered = v ? String(v) : null;
  });

  // ut-docs#2989: adopt the version a grid root carries in its markup
  // (data-sell-version), once per root element -- GET /'s inline first-paint
  // grid has no response header. Later polls/refreshes update `rendered`
  // through the header path above; a root already adopted is never re-read,
  // so an old root left on screen by a failed refresh can't roll it back.
  var seededFrom = null;
  function seed() {
    var g = saleGrid();
    if (!g || g === seededFrom) return;
    var v = g.getAttribute('data-sell-version');
    if (!v) return;
    seededFrom = g;
    rendered = String(v);
  }
  document.addEventListener('htmx:afterSettle', seed);
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', seed, { once: true });
  else seed();

  function pointerDown() { pointersDown++; }
  function pointerUp() { if (pointersDown > 0) pointersDown--; }
  document.addEventListener('pointerdown', pointerDown, true);
  document.addEventListener('pointerup', pointerUp, true);
  document.addEventListener('pointercancel', pointerUp, true);
  // A pointerup that lands outside the window (or a lost capture) would
  // otherwise leave the count stuck above zero.
  window.addEventListener('blur', function () { pointersDown = 0; });

  function tick() {
    if (inFlight || document.visibilityState !== 'visible') return;
    var g = saleGrid();
    if (!g) { rendered = null; refreshAt = 0; seededFrom = null; return; }
    seed();
    if (rendered === null) return;
    if (refreshAt && Date.now() - refreshAt < REFRESH_TIMEOUT_MS) return;
    refreshAt = 0;
    if (!window.fetch || !window.htmx) return;
    inFlight = true;
    // A probe the server never answers must not stop every later one.
    var ctl = typeof AbortController === 'function' ? new AbortController() : null;
    var abortTimer = ctl ? window.setTimeout(function () { ctl.abort(); }, FETCH_TIMEOUT_MS) : null;
    // redirect: 'manual' -- an expired session answers 303 -> /login; following
    // it would fetch the login page's HTML on every tick until the idle lock
    // navigates away. The opaque redirect is simply !r.ok.
    fetch(VERSION_URL, { credentials: 'same-origin', cache: 'no-store', redirect: 'manual', headers: { Accept: 'application/json' }, signal: ctl ? ctl.signal : undefined })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (body) {
        var v = body && body.data && body.data.version;
        if (v === undefined || v === null) return;
        v = String(v);
        var grid = saleGrid();
        if (!grid || rendered === null || v === rendered) return;
        if (busy(grid)) return; // re-checked on the next tick
        refreshAt = Date.now();
        window.htmx.trigger(document.body, 'buttons-changed');
      })
      .catch(function () { /* offline or server busy: try again next tick */ })
      .then(function () { inFlight = false; if (abortTimer) window.clearTimeout(abortTimer); });
  }

  function start() {
    if (timer === null) timer = window.setInterval(tick, POLL_MS);
  }
  document.addEventListener('visibilitychange', function () {
    if (document.visibilityState === 'visible') tick();
    else pointersDown = 0;
  });
  start();
})();

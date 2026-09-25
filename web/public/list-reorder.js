// ut-docs#2699 — long-press drag-and-drop reorder for a vertical admin list
// (the /categories table and the Designer's category-management list), with
// the two non-drag paths WCAG asks for: Alt+ArrowUp / Alt+ArrowDown on a
// focused row (2.1.1) and a page-provided Move up / Move down button pair
// that calls utListReorder.move() (2.5.7). Loaded on every page from
// base.html; attribute-driven and delegated on document, so a page with no
// [data-reorder-list] never does anything here, and a list that arrives by
// htmx swap (the Designer's self-refreshing .products root) works with no
// per-page hook — same pattern as record-dialog.js.
//
// Markup contract:
//   [data-reorder-list]            the list element (a <tbody>, an <ol>);
//                                  its DIRECT children carrying
//                                  data-reorder-id are the items
//     data-reorder-url             POST target: every id, in the new order,
//                                  as repeated "ids" fields (the body shape
//                                  /api/categories/reorder and
//                                  /api/designer/categories/reorder share)
//     data-reorder-refresh         optional htmx event fired on <body> after
//                                  a save (the Designer: buttons-changed,
//                                  so the replica re-renders from the
//                                  server's truth)
//     data-reorder-live            id of a visually-hidden aria-live region
//                                  for announcements
//     data-reorder-msg             optional id of a visible message region
//                                  for a refused save (else the live region)
//     data-reorder-msg-moved       "Moved %s to position %d of %d."
//     data-reorder-msg-cancelled   announced when Escape cancels a drag
//     data-reorder-msg-failed      shown when a save is refused / fails
//   [data-reorder-id]              an item; data-reorder-name is its
//                                  human-readable name for announcements
//
// The gesture (Pointer Events + setPointerCapture, never HTML5 DnD — no
// touch path on the till's WebKitGTK, the reason ut-docs#1221 dropped it):
//   - pointerdown on an item arms a HOLD_MS timer; moving more than
//     MOVE_CANCEL_PX first cancels it, so a swipe still scrolls the list
//     (items — a <tr>'s cells — keep touch-action: pan-y; the page
//     scrolls natively);
//   - the timer firing lifts the row (.is-dragging, a short vibrate) and
//     from then on the row itself is the placeholder: it is moved in the
//     DOM each time the pointer crosses a sibling's midpoint — no floating
//     copy, and no transform on a <tr> (WebKit's support for transformed
//     table rows is unreliable). A non-passive touchmove listener stops
//     the page scrolling under the finger once armed; near the scroll
//     container's edges the list auto-scrolls;
//   - release saves (only when the order actually changed) and eats the
//     click that follows, so a drag never also opens the row's editor; a
//     short tap never arms and opens it as before;
//   - Escape (or a pointercancel) puts the rows back where the drag began.
// A refused or failed save puts the rows back to the last order the server
// accepted, fires list-reorder:reverted on the list, and shows the reason.
// Saves are chained on one promise per list so a quick run of moves can
// never land out of order on the wire.
(function () {
  'use strict';
  var HOLD_MS = 450;
  var MOVE_CANCEL_PX = 8;
  var EDGE_PX = 48;
  var EDGE_STEP = 12;

  var hold = null;   // { timer, pointerId, x, y, item, list }
  var drag = null;   // { item, list, pointerId, start: [ids], y, raf }
  var swallowClick = false;
  var clearSwallowOnUp = false; // Escape mid-drag: the button is still down
  var states = typeof WeakMap === 'function' ? new WeakMap() : null;

  function listOf(item) {
    var p = item && item.parentElement;
    return p && p.hasAttribute('data-reorder-list') ? p : null;
  }
  function itemFor(el) {
    var it = el && el.closest ? el.closest('[data-reorder-id]') : null;
    return it && listOf(it) ? it : null;
  }
  function items(list) {
    return Array.prototype.filter.call(list.children, function (c) { return c.hasAttribute('data-reorder-id'); });
  }
  function visible(list) {
    return items(list).filter(function (c) { return !c.hidden && c.getClientRects().length > 0; });
  }
  // The items one Move press / Alt+Arrow steps across, and the set
  // "Position N of M" counts: the VISIBLE items (a list's search filter
  // hides rows), so one press always changes N by exactly one and never
  // lands the row next to a row the user can't see. Only when `item`
  // itself is not rendered does it fall back to every item — then the
  // same fallback serves position(), move() and canMove() alike, so they
  // still agree. The saved order is always the FULL list (ids()): hidden
  // rows keep their places relative to each other.
  function peers(list, item) {
    var vis = visible(list);
    return vis.indexOf(item) >= 0 ? vis : items(list);
  }
  function ids(list) {
    return items(list).map(function (c) { return c.getAttribute('data-reorder-id'); });
  }
  function same(a, b) {
    if (a.length !== b.length) return false;
    for (var i = 0; i < a.length; i++) if (a[i] !== b[i]) return false;
    return true;
  }
  // The last order the server accepted, captured the first time a list is
  // touched (before any DOM move) and advanced on every successful save.
  function state(list) {
    var s = states ? states.get(list) : list.__utReorder;
    if (!s) {
      s = { pending: Promise.resolve(), good: ids(list) };
      if (states) states.set(list, s); else list.__utReorder = s;
    }
    return s;
  }

  // "%s"/"%d" filled left to right — the translated templates keep that
  // order in every core locale (guard-i18n checks the verbs match en.json).
  function format(tpl, args) {
    var i = 0;
    return String(tpl || '').replace(/%[sd]/g, function (m) { return i < args.length ? String(args[i++]) : m; });
  }

  function position(item) {
    var list = listOf(item);
    if (!list) return { index: 0, total: 0 };
    var set = peers(list, item);
    return { index: set.indexOf(item) + 1, total: set.length };
  }

  function announce(list, text) {
    var id = list.getAttribute('data-reorder-live');
    var live = id ? document.getElementById(id) : null;
    if (!live || !text) return;
    // Clear first so the same sentence twice in a row is still re-read.
    live.textContent = '';
    setTimeout(function () { live.textContent = text; }, 30);
  }
  function announceMoved(list, item) {
    var p = position(item);
    announce(list, format(list.getAttribute('data-reorder-msg-moved'),
      [item.getAttribute('data-reorder-name') || '', p.index, p.total]));
  }

  function clearFailure(list) {
    var id = list.getAttribute('data-reorder-msg');
    var box = id ? document.getElementById(id) : null;
    if (box && box.getAttribute('data-reorder-owned') === '1') {
      box.textContent = '';
      box.removeAttribute('data-reorder-owned');
      if (box.getAttribute('data-reorder-unhid') === '1') { box.hidden = true; box.removeAttribute('data-reorder-unhid'); }
    }
  }
  function showFailure(list, reason) {
    var text = list.getAttribute('data-reorder-msg-failed') || '';
    if (reason) text = text ? text + ' ' + reason : reason;
    var id = list.getAttribute('data-reorder-msg');
    var box = id ? document.getElementById(id) : null;
    if (!box) { announce(list, text); return; }
    box.textContent = '';
    var span = document.createElement('span');
    span.className = 'error';
    span.textContent = text;
    box.appendChild(span);
    box.setAttribute('data-reorder-owned', '1');
    if (box.hidden) { box.setAttribute('data-reorder-unhid', '1'); box.hidden = false; }
  }

  // Put the list's items back into `order` (ids), leaving any non-item
  // children (a no-results row) where they are.
  function applyOrder(list, order) {
    var byId = {};
    var all = items(list);
    all.forEach(function (c) { byId[c.getAttribute('data-reorder-id')] = c; });
    var anchor = all.length ? all[all.length - 1].nextSibling : null;
    var focused = document.activeElement;
    order.forEach(function (id) { if (byId[id]) list.insertBefore(byId[id], anchor); });
    if (focused && focused !== document.activeElement && document.contains(focused)) focused.focus();
  }

  // A refused body is our own server's short localized message (plain
  // text, or the Designer's <div class="error"> fragment): read it as TEXT
  // only — parsed, never injected.
  function reasonOf(res) {
    return res.text().then(function (t) {
      t = String(t || '');
      if (/^\s*</.test(t) && typeof DOMParser === 'function') {
        t = new DOMParser().parseFromString(t, 'text/html').body.textContent || '';
      }
      t = t.replace(/\s+/g, ' ').trim();
      return t.length > 300 ? '' : t;
    }, function () { return ''; });
  }

  function refreshAfterSave(list) {
    var ev = list.getAttribute('data-reorder-refresh');
    if (!ev || !window.htmx) return;
    var a = document.activeElement;
    var focusId = (a && a.id && list.contains(a)) ? a.id : '';
    if (focusId) {
      // The refresh replaces this list: put focus back on the same
      // control's fresh twin once htmx has settled it, unless something
      // else claimed focus meanwhile.
      var onSettle = function () {
        document.body.removeEventListener('htmx:afterSettle', onSettle);
        var el = document.getElementById(focusId);
        var cur = document.activeElement;
        if (el && el.getClientRects().length && (!cur || cur === document.body || !document.contains(a))) el.focus();
      };
      document.body.addEventListener('htmx:afterSettle', onSettle);
    }
    window.htmx.trigger(document.body, ev);
  }

  function persist(list) {
    var s = state(list);
    var url = list.getAttribute('data-reorder-url');
    if (!url) return s.pending;
    s.pending = s.pending.then(function () {
      if (!document.contains(list)) return;
      var now = ids(list);
      if (same(now, s.good)) return;
      var body = new URLSearchParams();
      now.forEach(function (id) { body.append('ids', id); });
      return fetch(url, { method: 'POST', body: body, credentials: 'same-origin' }).then(function (res) {
        if (res.ok) {
          s.good = now;
          clearFailure(list);
          refreshAfterSave(list);
          return;
        }
        return reasonOf(res).then(function (reason) { revert(list, reason); });
      }, function () { revert(list, ''); });
    }).catch(function () {
      // Never let one failure break every later save on this list.
    });
    return s.pending;
  }

  function revert(list, reason) {
    if (!document.contains(list)) return;
    applyOrder(list, state(list).good);
    showFailure(list, reason);
    list.dispatchEvent(new CustomEvent('list-reorder:reverted', { bubbles: true }));
  }

  // Move `item` one place among its peers (step -1 / +1), save,
  // announce. Returns whether it moved. Public: the pages' Move up /
  // Move down buttons call it.
  function move(item, step) {
    var list = listOf(item);
    if (!list || !step) return false;
    state(list);
    var vis = peers(list, item);
    var i = vis.indexOf(item);
    var j = i + (step < 0 ? -1 : 1);
    if (i < 0 || j < 0 || j >= vis.length) return false;
    var focused = document.activeElement;
    if (step < 0) list.insertBefore(item, vis[j]);
    else list.insertBefore(item, vis[j].nextSibling);
    if (focused && focused !== document.activeElement && document.contains(focused)) focused.focus();
    announceMoved(list, item);
    persist(list);
    return true;
  }
  function canMove(item, step) {
    var list = listOf(item);
    if (!list) return false;
    var vis = peers(list, item);
    var i = vis.indexOf(item);
    return i >= 0 && (step < 0 ? i > 0 : i < vis.length - 1);
  }

  // ---- pointer: long-press, then drag ----
  function clearHold() {
    if (hold) { clearTimeout(hold.timer); hold = null; }
  }
  function ignoredTarget(t) {
    return !!(t.closest && t.closest('input, select, textarea, a[href], label, form, [data-reorder-ignore]'));
  }
  function arm(h) {
    hold = null;
    if (!document.contains(h.item)) return;
    state(h.list);
    drag = { item: h.item, list: h.list, pointerId: h.pointerId, start: ids(h.list), y: h.y, raf: 0 };
    h.item.classList.add('is-dragging');
    h.list.classList.add('is-reordering');
    swallowClick = true;
    if (navigator.vibrate) { try { navigator.vibrate(10); } catch (e) { /* not allowed here */ } }
    capture();
  }
  // Moving the row in the DOM drops pointer capture, so it is re-acquired
  // after every move (same finding as app.js's utTileJiggle). The drag
  // itself ends on pointerup/pointercancel, never lostpointercapture.
  function capture() {
    if (!drag || !drag.item.setPointerCapture) return;
    try { drag.item.setPointerCapture(drag.pointerId); } catch (e) { /* keep going uncaptured */ }
  }
  function reorderAt(y) {
    var list = drag.list, item = drag.item;
    var others = visible(list).filter(function (c) { return c !== item; });
    if (!others.length) return;
    var before = null;
    for (var i = 0; i < others.length; i++) {
      var r = others[i].getBoundingClientRect();
      if (y < r.top + r.height / 2) { before = others[i]; break; }
    }
    var ref = before || others[others.length - 1].nextSibling;
    if (ref === item || item.nextSibling === ref) return;
    var focused = document.activeElement;
    list.insertBefore(item, ref);
    if (focused && focused !== document.activeElement && document.contains(focused)) focused.focus();
    capture();
  }
  function scroller(el) {
    for (var n = el.parentElement; n && n !== document.body; n = n.parentElement) {
      var oy = getComputedStyle(n).overflowY;
      if ((oy === 'auto' || oy === 'scroll') && n.scrollHeight > n.clientHeight) return n;
    }
    return document.scrollingElement || document.documentElement;
  }
  function edgeTick() {
    if (!drag) return;
    drag.raf = 0;
    var sc = scroller(drag.list);
    var top = 0, bottom = window.innerHeight;
    if (sc !== document.scrollingElement && sc !== document.documentElement) {
      var r = sc.getBoundingClientRect();
      top = Math.max(top, r.top);
      bottom = Math.min(bottom, r.bottom);
    }
    var dy = drag.y < top + EDGE_PX ? -EDGE_STEP : (drag.y > bottom - EDGE_PX ? EDGE_STEP : 0);
    if (!dy) return;
    var before = sc.scrollTop;
    sc.scrollTop += dy;
    if (sc.scrollTop === before) return;
    reorderAt(drag.y);
    drag.raf = requestAnimationFrame(edgeTick);
  }
  function endDrag(commit) {
    var d = drag;
    drag = null;
    if (!d) return;
    if (d.raf) cancelAnimationFrame(d.raf);
    d.item.classList.remove('is-dragging');
    d.list.classList.remove('is-reordering');
    if (d.item.hasPointerCapture && d.item.hasPointerCapture(d.pointerId)) {
      try { d.item.releasePointerCapture(d.pointerId); } catch (e) { /* already released */ }
    }
    if (!document.contains(d.list)) return;
    if (!commit) {
      if (!same(ids(d.list), d.start)) applyOrder(d.list, d.start);
      announce(d.list, d.list.getAttribute('data-reorder-msg-cancelled'));
      return;
    }
    if (same(ids(d.list), d.start)) return;
    announceMoved(d.list, d.item);
    persist(d.list);
  }

  document.addEventListener('pointerdown', function (e) {
    // A new gesture: whatever click the previous one owed us is not coming
    // (the browser sends none when the pressed row was moved in the DOM
    // mid-gesture), so never eat THIS gesture's click for it.
    if (!drag) { swallowClick = false; clearSwallowOnUp = false; }
    if (drag) return;
    if (e.button !== undefined && e.button !== 0) return;
    if (e.isPrimary === false) { clearHold(); return; }
    var item = itemFor(e.target);
    if (!item || ignoredTarget(e.target)) return;
    clearHold();
    var h = { pointerId: e.pointerId, x: e.clientX, y: e.clientY, item: item, list: listOf(item) };
    h.timer = setTimeout(function () { arm(h); }, HOLD_MS);
    hold = h;
  });
  document.addEventListener('pointermove', function (e) {
    if (hold && e.pointerId === hold.pointerId &&
        (Math.abs(e.clientX - hold.x) > MOVE_CANCEL_PX || Math.abs(e.clientY - hold.y) > MOVE_CANCEL_PX)) {
      clearHold();
      return;
    }
    if (!drag || e.pointerId !== drag.pointerId) return;
    if (!document.contains(drag.item)) { drag = null; return; }
    e.preventDefault();
    drag.y = e.clientY;
    reorderAt(e.clientY);
    if (!drag.raf) drag.raf = requestAnimationFrame(edgeTick);
  });
  document.addEventListener('pointerup', function (e) {
    if (hold && e.pointerId === hold.pointerId) clearHold();
    if (clearSwallowOnUp) {
      clearSwallowOnUp = false;
      setTimeout(function () { swallowClick = false; }, 400);
    }
    if (drag && e.pointerId === drag.pointerId) {
      endDrag(true);
      // The click that follows this pointerup is eaten below; if the
      // browser sends none (released off the row), don't eat a later one.
      setTimeout(function () { swallowClick = false; }, 400);
    }
  });
  document.addEventListener('pointercancel', function (e) {
    if (hold && e.pointerId === hold.pointerId) clearHold();
    if (drag && e.pointerId === drag.pointerId) { endDrag(false); swallowClick = false; }
  });
  // Once armed, the finger drags the row, not the page.
  document.addEventListener('touchmove', function (e) {
    if (drag && e.cancelable) e.preventDefault();
  }, { passive: false });
  // Android's long-press context menu / text-selection callout on a row.
  document.addEventListener('contextmenu', function (e) {
    if ((hold || drag) && itemFor(e.target)) e.preventDefault();
  });
  // Capture phase: runs before record-dialog.js's document listener and
  // the Designer row's own Alpine @click, so a drop never opens an editor.
  document.addEventListener('click', function (e) {
    if (!swallowClick) return;
    swallowClick = false;
    e.preventDefault();
    e.stopPropagation();
    e.stopImmediatePropagation();
  }, true);

  // ---- keyboard ----
  document.addEventListener('keydown', function (e) {
    if (drag && e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      endDrag(false);
      // The release that ends this gesture must not open the row's editor.
      swallowClick = true;
      clearSwallowOnUp = true;
      return;
    }
    if (!e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return;
    if (e.key !== 'ArrowUp' && e.key !== 'ArrowDown') return;
    var t = e.target;
    if (!t || !t.closest || t.closest('input, select, textarea, [contenteditable="true"], form')) return;
    var item = itemFor(t);
    if (!item) return;
    e.preventDefault();
    move(item, e.key === 'ArrowUp' ? -1 : 1);
  });

  window.utListReorder = { move: move, canMove: canMove, position: position, format: format };
})();

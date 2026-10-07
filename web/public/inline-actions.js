// ut-docs#3325 (CSP slice 2 of ut-docs#2913) — every inline event handler
// that used to live in web/ui templates, as delegated listeners instead.
//
// Why: an onclick=/oninput=/onerror=/onsubmit=/onclose= attribute is what
// CSP's `script-src-attr` blocks, and htmx's hx-on / hx-on::<event>
// attributes are compiled with `new Function()` ('unsafe-eval'). The
// report-only inventory from slice 1 (/csp-report,
// e2e/tests/csp-report-only-2913.spec.ts) listed them; ut-docs#3327 will
// enforce the policy, so none may remain. scripts/ci/guard-no-inline-
// handlers.sh keeps them out of web/ui and pins the listeners below.
//
// Loaded on every page from base.html, and by every standalone document
// (setup.html, login.html, self_order*.html, order_tracking.html) itself.
// A no-op on a page that carries none of the attributes.
//
// ---- The attributes ------------------------------------------------------
//   data-action="…"              click (was onclick= / hx-on:click=)
//   data-input / -change / -submit / -close
//                                the matching DOM event (was oninput= …)
//   data-before-request / -after-request / -before-swap /
//   -after-settle / -response-error / -send-error
//                                the htmx:<event> (was hx-on::<event>=)
//   data-fallback="hide|none|src" (+ data-fallback-src)
//                                a broken <img> (was onerror=)
//
// NEVER name a new attribute here "data-on-<anything>" (ut-docs#3325
// review): Go's html/template decides an attribute's escaping context by
// stripping a leading "data-" and then checking for an "on" prefix
// (html/template/context.go attrType) — the exact same sniff it uses for
// real onclick=/onsubmit=/... handlers. "data-on-after-request" strips to
// "on-after-request", which DOES start with "on", so any {{ template value
// }} inside that attribute's quotes gets JS-STRING escaped (JSON-quoted)
// instead of plain HTML-attribute escaped — a templated id comes out
// wrapped in literal quote characters the real DOM id never has, and every
// byId()-style lookup against it silently fails. Confirmed two live
// instances of exactly this (ut-docs#3325 tester pass): parked_orders.html's
// remember-focus/restore-focus pair (e2e/tests/held-table-move-2702.spec.ts)
// and pending_pairings.html's wrong-PIN unhide hook. Fixed by dropping the
// "on-" infix from every one of these names (data-before-request, not
// data-on-before-request) so stripping "data-" never leaves something
// starting with "on".
//
// The same sniff has a SECOND trap (ut-docs#3325 review, verified against
// Go's attrTypeMap): after stripping "data-", "action" is looked up as the
// <form action=> attribute and "fallback-src" matches the "src" heuristic,
// so data-action and data-fallback-src are URL-context attributes, not
// plain ones. For data-fallback-src that is right (it IS a URL). For
// data-action it means every {{ }} inside the value is URL-normalised:
// a space becomes %20, non-ASCII is percent-encoded, and once a `#` or `?`
// precedes the {{ }} it is FULLY percent-encoded (`,` -> %2c, `/` -> %2f,
// `:` -> %3a) -- which would break the step syntax -- while a {{ }} that
// is the very first thing in the value is run through the URL-scheme
// filter and comes out as "#ZgotmplZ" (so "close:x" from a template value
// would never work). Today's only templated data-action values are
// receipt numbers after `go:/refund/`, `go:/journal/`, `receipt-print:`
// and `receipt-ask-print:` (server-generated `T<n>-NNNNNNNNN`, so
// untouched by the normaliser). Keep it that way: never put a {{ }} after
// a `#` or `?` in a data-action, never start a data-action with one, and
// prefer carrying a templated value in a plain attribute (data-receipt,
// data-done-text, ...) read by the step over interpolating it into
// data-action. TestTemplatesDataActionTemplateValuesStayOutOfURLParts
// (internal/pages) pins the two shapes that would break.
//
// ---- The value: a tiny, fixed vocabulary — never code ---------------------
// One or more CHAINS separated by `;`. A chain is whitespace-separated
// steps `name` or `name:arg` (several args: `name:a,b`), run left to right
// against the ACTIONS registry below. A GUARD step (ok, fail, not-html …)
// returns false to skip the rest of its chain; the next chain still runs.
// So hx-on's
//     if (event.detail.successful) { UT.reload('x') }
//     else { document.getElementById('settings-save-error').hidden = false }
// is  data-after-request="ok reload:x; fail unhide:settings-save-error".
// An unknown step name stops its chain and warns once (fail closed): a typo
// must not half-run a sequence. Steps only ever take ids / keys / same-
// origin paths, so injected markup carrying these attributes can't run
// arbitrary script — there is deliberately no "call any function" step.
// Add a step here (reused by every template that needs it) rather than a
// per-template function.
//
// ---- Semantics kept from the inline handlers ------------------------------
// * `this` → the element CARRYING the attribute (ctx.el), not event.target.
// * Bubbling: an inline handler on an ancestor ran for a descendant's
//   event too (an hx-on on a <form> sees its fields' htmx events; menu.html's
//   before-swap fires on the swap TARGET inside the form). So every
//   carrying ancestor runs, innermost first — except for non-bubbling events
//   (error, close), which only ever reached the element itself. The `stop`
//   step ends that walk (journal.html's link inside a clickable row: was
//   event.stopPropagation()).
// * Capture phase on `document` for all of them: an inline handler ran at
//   the target before htmx's/Alpine's own listeners and before any
//   document-level bubble listener, and an ancestor's stopPropagation()
//   could never hide an event from it. Capture keeps both. The capture-
//   phase click swallowers in app.js / list-reorder.js (jiggle mode, a
//   drag's trailing click) call stopImmediatePropagation() and register
//   first (this file loads last), so they still win, as they did over the
//   attributes. `error` and `close` don't bubble at all, so capture is the
//   only way delegation sees them.
// * htmx 1.9 fires htmx:afterRequest on the requesting element even when
//   its own swap DETACHED it (hx-target="this" / an ancestor), and then
//   again on the nearest still-connected ancestor. hx-on's own listener saw
//   the first dispatch; a document listener can't (a detached node's events
//   never reach it). So beforeRequest records the source on htmx's detail
//   object — the same object afterRequest is dispatched with — and the
//   reconnected afterRequest replays the source's hooks first.
// * A broken image can fail before this deferred file runs; the scan at
//   the bottom applies the fallback to any <img data-fallback> that is
//   already complete with no pixels.
(function () {
  'use strict';

  function byId(id) { return id ? document.getElementById(id) : null; }
  function preventDefault(ctx) { if (ctx.event && ctx.event.cancelable) ctx.event.preventDefault(); }
  function formOf(el) {
    if (!el) return null;
    if (el.tagName === 'FORM') return el;
    return el.form || (el.closest ? el.closest('form') : null);
  }
  function xhrOf(ctx) { return ctx.detail && ctx.detail.xhr; }
  function header(ctx, name) {
    var x = xhrOf(ctx);
    try { return (x && x.getResponseHeader(name)) || ''; } catch (e) { return ''; }
  }
  // Same-origin path only ("/refund/42"), never "//host" or "javascript:".
  function safePath(p) { return typeof p === 'string' && /^\/(?![\/\\])/.test(p) ? p : null; }
  function showDialog(d) { if (d && !d.open) d.show(); }
  function closeDialog(d) { if (d && d.open) d.close(); }
  function toMinor(v) { return window.utCurrency ? window.utCurrency.toMinor(v) : v; }
  function scanFocusLater() {
    setTimeout(function () {
      var b = document.querySelector('input[name=code]');
      if (b) b.focus({ preventScroll: true });
    }, 150);
  }
  // Receipt reprint (receipt.html): the same POST as before; a 451 is the
  // customer-document gate's refusal (ADR-0124), never a printer fault, so
  // it never falls back to window.print().
  function reprint(no) {
    return fetch('/api/print/receipt/' + encodeURIComponent(no), { method: 'POST' });
  }

  // name -> function(ctx, arg, args). ctx = { el, event, detail }.
  // Returning false (guards only) skips the rest of the chain.
  var ACTIONS = {
    // ---- guards (htmx hooks) ----
    'ok': function (ctx) { return !!(ctx.detail && ctx.detail.successful); },
    'fail': function (ctx) { return !(ctx.detail && ctx.detail.successful); },
    // A real 2xx, for a form whose before-swap forces isError=false (and
    // with it `successful`) so its 4xx message can swap in (menu.html).
    'ok-2xx': function (ctx) { var x = xhrOf(ctx); return !!(x && x.status >= 200 && x.status < 300); },
    // users.html / elevation: X-UT-Response tells success from a refusal.
    'ut-ok': function (ctx) { return header(ctx, 'X-UT-Response') === 'ok'; },
    // A settings save that answered with HTML (an elevation prompt) must
    // not reload over it.
    'not-html': function (ctx) { return header(ctx, 'Content-Type').indexOf('text/html') === -1; },
    'status': function (ctx, arg) { var x = xhrOf(ctx); return !!x && String(x.status) === arg; },
    'not-status': function (ctx, arg) { var x = xhrOf(ctx); return !x || String(x.status) !== arg; },
    // A sale/hold whose response raised an error toast keeps the payment
    // overlay / table picker open so the operator sees why.
    'no-error-toast': function () {
      var t = byId('toast-message');
      return !(t && t.classList.contains('error'));
    },

    // ---- dialogs ----
    'show': function (ctx, id) { showDialog(byId(id)); },
    // Only the self-order kiosk's dialog is modal (status/lock/exit must be
    // unreachable there) — CLAUDE.md, guard-no-showmodal.sh.
    'show-modal': function (ctx, id) {
      var d = byId(id);
      if (d && !d.open) d.showModal(); // showmodal:allow self-order kiosk #selforder-modal only (show-modal step, ut-docs#2097 exemption)
    },
    // Open without leaving focus inside the dialog (menu.html's deposit
    // refund: show() would focus its first field and pop the OSK).
    'show-keep-focus': function (ctx, id) {
      var d = byId(id);
      if (!d) return;
      var prev = document.activeElement;
      showDialog(d);
      if (d.contains(document.activeElement)) {
        document.activeElement.blur();
        if (prev && prev !== document.body && prev.focus) prev.focus();
      }
    },
    'close': function (ctx, id) { closeDialog(byId(id)); },
    // Close and drop its content (self-order kiosk: the next open swaps
    // fresh markup in; stale markup must never flash).
    'close-clear': function (ctx, id) {
      var d = byId(id);
      if (!d) return;
      closeDialog(d);
      d.innerHTML = '';
    },
    // Close the dialog this control sits in. On a link (the in-shell
    // "← Catalog" back link) it also stops the navigation.
    'close-closest': function (ctx) {
      closeDialog(ctx.el.closest('dialog'));
      if (ctx.event && ctx.event.type === 'click') preventDefault(ctx);
    },

    // ---- elements ----
    'focus': function (ctx, id) { var el = byId(id); if (el) el.focus(); },
    // Back to the scan field once the tap's own focus has settled.
    'refocus-scan': function () { scanFocusLater(); },
    'hide': function (ctx, id) { var el = byId(id); if (el) el.hidden = true; },
    'unhide': function (ctx, id) { var el = byId(id); if (el) el.hidden = false; },
    'hide-display': function (ctx, id) { var el = byId(id); if (el) el.style.display = 'none'; },
    'remove': function (ctx, id) { var el = byId(id); if (el) el.remove(); },
    'empty': function (ctx, id) { var el = byId(id); if (el) el.replaceChildren(); },
    'clear-text': function (ctx, id) { var el = byId(id); if (el) el.textContent = ''; },
    'clear-value': function (ctx, id) { var el = byId(id); if (el) el.value = ''; },
    'set-value': function (ctx, arg, args) { var el = byId(args[0]); if (el) el.value = args.length > 1 ? args[1] : ''; },
    // A failed request's body as the message (store name, staff languages).
    'text-response': function (ctx, id) {
      var el = byId(id), x = xhrOf(ctx);
      if (el) el.textContent = (x && x.responseText) || '';
    },
    // The carrier's own data-error-text into #id (category popup tiles).
    'error-text': function (ctx, id) {
      var el = byId(id);
      if (el) el.textContent = ctx.el.getAttribute('data-error-text') || '';
    },
    // A visible money field mirrors its minor-unit value into the hidden
    // field that is actually POSTed (#1249: on input, so osk.js typing
    // counts). -or-empty keeps an empty optional field empty, not 0.
    'minor': function (ctx, id) { var h = byId(id); if (h) h.value = toMinor(ctx.el.value); },
    'minor-or-empty': function (ctx, id) {
      var h = byId(id);
      if (h) h.value = ctx.el.value === '' ? '' : toMinor(ctx.el.value);
    },
    // A failed toggle save puts the checkbox back.
    'toggle-checked': function (ctx) { ctx.el.checked = !ctx.el.checked; },
    // parked_orders.html: which control to focus once the re-rendered body
    // settles (set before the request, read by restore-focus).
    'remember-focus': function (ctx, arg, args) {
      var host = byId(args[0]);
      if (host && args[1]) host.dataset.focusAfter = args[1];
    },
    'restore-focus': function (ctx) {
      var id = ctx.el.dataset.focusAfter;
      if (!id) return;
      delete ctx.el.dataset.focusAfter;
      var el = byId(id);
      if (el) el.focus();
    },
    // reports.html: move active/aria-selected to the clicked tab and point
    // the panel's aria-labelledby at it (ut-docs#421).
    'activate-tab': function (ctx, panelId) {
      var list = ctx.el.closest('[role=tablist]');
      if (list) {
        Array.prototype.forEach.call(list.querySelectorAll('[role=tab]'), function (b) {
          b.classList.remove('active');
          b.setAttribute('aria-selected', 'false');
        });
      }
      ctx.el.classList.add('active');
      ctx.el.setAttribute('aria-selected', 'true');
      var panel = byId(panelId);
      if (panel) panel.setAttribute('aria-labelledby', ctx.el.id);
    },

    // ---- forms ----
    'reset-form': function (ctx) { var f = formOf(ctx.el); if (f) f.reset(); },
    'submit-form': function (ctx) { var f = formOf(ctx.el); if (f) f.submit(); },
    'request-submit': function (ctx) { var f = formOf(ctx.el); if (f) f.requestSubmit(); },
    'prevent': function (ctx) { preventDefault(ctx); },
    // A plain (non-htmx) form asking first; the question is the carrier's
    // data-confirm-text (already translated server-side).
    'confirm': function (ctx) {
      if (!window.confirm(ctx.el.getAttribute('data-confirm-text') || '')) {
        preventDefault(ctx);
        return false;
      }
    },
    // ut-docs#1540: a pairing button outside any <form> — trim, then let
    // `required` speak; an invalid name cancels the htmx request.
    'trim-validate': function (ctx, id) {
      var n = byId(id);
      if (!n) return;
      n.value = n.value.trim();
      if (!n.reportValidity()) { preventDefault(ctx); return false; }
    },

    // ---- htmx ----
    // menu.html's deposit refund: its 4xx message is meant for #id, so let
    // it swap instead of htmx dropping an error response.
    'swap-errors-into': function (ctx, id) {
      var d = ctx.detail;
      if (d && d.target && d.target.id === id) { d.shouldSwap = true; d.isError = false; }
    },
    // refresh-region:<id> names the region outright (ut-docs#2904): a
    // poll that swapped the carrier out mid-request leaves it detached,
    // and a detached element can't find its region by closest().
    'refresh-region': function (ctx, id) {
      if (!window.UT || !UT.refreshRegion) return;
      var r = id ? document.getElementById(id) : null;
      UT.refreshRegion(r || ctx.el);
    },
    'reload': function (ctx, key) { if (window.UT && UT.reload) UT.reload(key); else location.reload(); },
    'note-nav': function (ctx, key) { if (window.UT && UT.noteNav) UT.noteNav(key); },
    'ajax-get': function (ctx, arg, args) {
      var path = safePath(args[0]);
      if (path && window.htmx) window.htmx.ajax('GET', path, { target: args[1], swap: 'innerHTML' });
    },
    // elevation_prompt.html (ut-docs#795 S7, #2762): X-UT-Response
    // "elevation-prompt" is a re-prompt (the OOB swap already replaced the
    // dialog); anything else is terminal — close the dialog, and on "ok"
    // also clear a form[data-ut-reset-on-ok] around the target and refresh
    // the target's region.
    'elevation-done': function (ctx) {
      var xr = header(ctx, 'X-UT-Response');
      if (xr === 'elevation-prompt') return;
      closeDialog(ctx.el.closest('dialog'));
      if (xr !== 'ok') return;
      var t = null;
      try { t = document.querySelector(ctx.el.getAttribute('hx-target')); } catch (e) { t = null; }
      var f = t && t.closest('form[data-ut-reset-on-ok]');
      if (f) f.reset();
      if (window.UT && UT.refreshRegion) UT.refreshRegion(t);
    },

    // ---- navigation / window ----
    'go': function (ctx, path) { var p = safePath(path); if (p) window.location.href = p; },
    'reload-page': function () { location.reload(); },
    'print': function () { window.print(); },
    // Ends the bubbling walk: ancestors' data-action does not run.
    'stop': function () {},

    // ---- page functions (each a named, existing global — never a
    //      caller-chosen one) ----
    'open-payment': function () { if (window.posOpenPayment) window.posOpenPayment(); },
    'order-type-resolve': function () { if (window.posOrderTypePromptResolve) window.posOrderTypePromptResolve(); },
    'goto-modifiers': function (ctx) {
      if (window.utCatalogGoToModifiers && window.utCatalogGoToModifiers() === false) preventDefault(ctx);
    },
    'dismiss-pairing-notice': function () { if (window.utPairingNoticeDismiss) window.utPairingNoticeDismiss(); },
    'dismiss-shop-name-notice': function () { if (window.utShopNameNoticeDismiss) window.utShopNameNoticeDismiss(); }, // ut-docs#3114
    'close-install-modal': function () { if (window.closeInstallModal) window.closeInstallModal(); },
    // receipt.html's "ask" prompt: Print hides the prompt BEFORE any
    // window.print() fallback, so the question never lands on paper.
    'receipt-ask-print': function (ctx, no) {
      var el = byId('receipt-ask');
      reprint(no).then(function (r) {
        if (el) el.hidden = true;
        if (!r.ok && r.status !== 451) window.print();
      }).catch(function () {
        if (el) el.hidden = true;
        window.print();
      });
    },
    // receipt.html's Print: ✓ on success (data-done-text), disabled on the
    // 451 gate, browser print dialog on any other failure.
    'receipt-print': function (ctx, no) {
      var b = ctx.el;
      reprint(no).then(function (r) {
        if (r.ok) b.textContent = b.getAttribute('data-done-text') || b.textContent;
        else if (r.status === 451) b.disabled = true;
        else window.print();
      }).catch(function () { window.print(); });
    }
  };

  var warned = {};
  // Runs one attribute value. Returns false when a `stop` step ran.
  function run(spec, ctx) {
    var keepWalking = true;
    String(spec || '').split(';').forEach(function (chain) {
      var steps = chain.trim().split(/\s+/);
      for (var i = 0; i < steps.length; i++) {
        var step = steps[i];
        if (!step) continue;
        var c = step.indexOf(':');
        var name = c === -1 ? step : step.slice(0, c);
        var arg = c === -1 ? '' : step.slice(c + 1);
        var fn = ACTIONS[name];
        if (!fn) {
          if (!warned[name] && window.console) { warned[name] = true; console.warn('inline-actions: unknown step "' + name + '"'); }
          return;
        }
        if (name === 'stop') keepWalking = false;
        var out;
        try {
          out = fn(ctx, arg, arg === '' ? [] : arg.split(','));
        } catch (e) {
          if (window.console) console.error('inline-actions: step "' + name + '" failed', e);
          return;
        }
        if (out === false) return;
      }
    });
    return keepWalking;
  }

  // Every element from `start` up carrying `attr`, innermost first (a
  // non-bubbling event: only `start` itself).
  function dispatch(start, attr, event, detail) {
    if (!start || start.nodeType !== 1) return;
    var el = event.bubbles ? start.closest('[' + attr + ']') : (start.hasAttribute(attr) ? start : null);
    while (el) {
      if (!run(el.getAttribute(attr), { el: el, event: event, detail: detail })) return;
      if (!event.bubbles) return;
      el = el.parentElement ? el.parentElement.closest('[' + attr + ']') : null;
    }
  }

  // ---- 1. click -------------------------------------------------------------
  document.addEventListener('click', function (ev) {
    dispatch(ev.target, 'data-action', ev, null);
  }, true);

  // ---- DOM form/dialog events ----------------------------------------------
  [['input', 'data-input'], ['change', 'data-change'],
   ['submit', 'data-submit'], ['close', 'data-close']].forEach(function (pair) {
    document.addEventListener(pair[0], function (ev) {
      dispatch(ev.target, pair[1], ev, null);
    }, true);
  });

  // ---- 2. broken images -----------------------------------------------------
  // hide → visibility:hidden (keeps the tile's layout), none →
  // display:none, src → one retry with data-fallback-src (a variant's
  // missing photo falls back to its item's), then hidden.
  function imageFallback(img) {
    var mode = img.getAttribute('data-fallback');
    if (mode === 'src') {
      var alt = img.getAttribute('data-fallback-src');
      if (alt && !img.dataset.fb) { img.dataset.fb = '1'; img.src = alt; return; }
      img.style.visibility = 'hidden';
    } else if (mode === 'none') {
      img.style.display = 'none';
    } else {
      img.style.visibility = 'hidden';
    }
  }
  document.addEventListener('error', function (ev) {
    var t = ev.target;
    if (t && t.tagName === 'IMG' && t.hasAttribute('data-fallback')) imageFallback(t);
  }, true);

  // ---- 3. htmx lifecycle ----------------------------------------------------
  // htmx fires these on the requesting element (beforeSwap/afterSettle on
  // the swap target); the walk up finds the carrier either way.
  var SRC = 'utInlineActionsSource';
  function htmxHook(attr) {
    return function (ev) { dispatch(ev.target, attr, ev, ev.detail || {}); };
  }
  document.addEventListener('htmx:beforeRequest', function (ev) {
    var detail = ev.detail || {};
    // The same object htmx later dispatches htmx:afterRequest with.
    try { detail[SRC] = ev.target; } catch (e) { /* no replay for this one */ }
    dispatch(ev.target, 'data-before-request', ev, detail);
  }, true);
  document.addEventListener('htmx:afterRequest', function (ev) {
    var detail = ev.detail || {};
    var src = detail[SRC];
    // The source swapped itself out: this is htmx's re-dispatch on a
    // connected ancestor. Replay the source's own (missed) dispatch first.
    if (src && src !== ev.target && !src.isConnected) dispatch(src, 'data-after-request', ev, detail);
    dispatch(ev.target, 'data-after-request', ev, detail);
  }, true);
  document.addEventListener('htmx:beforeSwap', htmxHook('data-before-swap'), true);
  document.addEventListener('htmx:afterSettle', htmxHook('data-after-settle'), true);
  document.addEventListener('htmx:responseError', htmxHook('data-response-error'), true);
  document.addEventListener('htmx:sendError', htmxHook('data-send-error'), true);

  // Images that failed before this deferred file ran.
  Array.prototype.forEach.call(document.querySelectorAll('img[data-fallback]'), function (img) {
    if (img.complete && img.naturalWidth === 0 && img.getAttribute('src')) imageFallback(img);
  });
})();

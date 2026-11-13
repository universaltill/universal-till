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
// is  data-after-request="ok reload:x; fail unhide:settings-save-error"
// (Settings now uses `fail save-error:settings-save-error`, ut-docs#2982).
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
  // Persistent invalid state for a field and the hint it is described by;
  // cleared on the field's first change that makes it valid.
  function markInvalid(n) {
    var hint = byId(n.getAttribute('aria-describedby'));
    n.classList.add('field-invalid');
    if (hint) hint.classList.add('field-invalid');
    if (n.utInvalidWired) return;
    n.utInvalidWired = true;
    n.addEventListener('change', function () {
      if (!n.validity.valid) return;
      n.classList.remove('field-invalid');
      var h = byId(n.getAttribute('aria-describedby'));
      if (h) h.classList.remove('field-invalid');
    });
  }
  function header(ctx, name) {
    var x = xhrOf(ctx);
    try { return (x && x.getResponseHeader(name)) || ''; } catch (e) { return ''; }
  }
  // Same-origin path only ("/refund/42"), never "//host" or "javascript:".
  function safePath(p) { return typeof p === 'string' && /^\/(?![\/\\])/.test(p) ? p : null; }
  function showDialog(d) { if (d && !d.open) d.show(); }
  // ut-docs#5288: a confirm / PIN prompt (data-ut-prompt) never closes while
  // its own form has a request in flight (.htmx-request): its answer must
  // still show. Prompts only: other dialogs (self-order kiosk, modifier
  // picker) keep closing as before. The terminal after-request hook runs
  // after htmx has cleared that class on the form it closes.
  function closeDialog(d) { if (d && d.open && !(d.hasAttribute && d.hasAttribute('data-ut-prompt') && d.querySelector('form.htmx-request'))) d.close(); }
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
  // ut-docs#4714: a self-order customer's language pick (the Print button's
  // data-customer-locale) rides along so a manual print matches the
  // auto-print; the journal's reprint sends none (the shop's copy).
  function reprint(no, locale) {
    var opts = { method: 'POST' };
    if (locale) {
      opts.headers = { 'Content-Type': 'application/x-www-form-urlencoded' };
      opts.body = 'customer_locale=' + encodeURIComponent(locale);
    }
    return fetch('/api/print/receipt/' + encodeURIComponent(no), opts);
  }
  // Where the save-error step's line goes: right after the posting form;
  // a form-less carrier (a lone checkbox) uses its card.
  function saveErrorAnchor(el) {
    return formOf(el) || el.closest('.card') || el;
  }
  // That anchor's line: the card's last child, else the next sibling.
  function saveErrorLine(anchor) {
    if (anchor.classList.contains('card')) return anchor.querySelector(':scope > .save-error-inline');
    var next = anchor.nextElementSibling;
    return next && next.classList.contains('save-error-inline') ? next : null;
  }
  // A new save from the same card drops the previous attempt's reason.
  function clearSaveError(start) {
    var carrier = start && start.closest && start.closest('[data-after-request*="save-error:"]');
    if (!carrier) return;
    var line = saveErrorLine(saveErrorAnchor(carrier));
    if (line) line.remove();
  }
  // ut-docs#3247: app.js's htmx:responseError raises the generic page-wide
  // "server error" alert (#pos-alert) for every non-2xx. A step that has put
  // the server's own specific reason on screen takes it back down — but only
  // while it is still that generic message, never another alert (a network
  // error, a caller's own text) that happens to be showing.
  function hideGenericServerAlert() {
    var alertBox = byId('pos-alert');
    var alertText = alertBox && alertBox.querySelector('.notice-text');
    if (alertBox && !alertBox.hidden && alertText && alertText.textContent === (alertBox.dataset.msgServer || '')) {
      alertBox.hidden = true;
    }
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
    // refund: show() would focus its first field and pop the OSK). A
    // dialog in base.html's focus trap (ut-docs#3212) would pull focus
    // restored to the page behind straight back to that first field (the
    // native keyboard on #1248's hardware), so there focus is left on the
    // body: the next Tab enters the dialog.
    'show-keep-focus': function (ctx, id) {
      var d = byId(id);
      if (!d) return;
      var prev = document.activeElement;
      showDialog(d);
      if (d.contains(document.activeElement)) {
        document.activeElement.blur();
        var trapped = d.hasAttribute('data-ut-focus-trap') || d.hasAttribute('data-ut-escape-close');
        if (!trapped && prev && prev !== document.body && prev.focus) prev.focus();
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
    // A failed request's body as the message (store name, staff languages,
    // device profile, report retention). ut-docs#3247: a non-empty reason
    // supersedes the generic "server error" alert app.js raised for the same
    // non-2xx; an empty body leaves it up (the operator has nothing else to
    // read). ut-docs#3978: only a body the server marked X-UT-Response:
    // refused (httpx.RefuseText: already translated, for the operator) and
    // sent as text/plain counts, exactly as `save-error` requires; an
    // unmarked body is an untranslated developer string or HTML, so it is
    // never shown (the span is cleared so an older message cannot linger)
    // and the generic alert stays.
    'text-response': function (ctx, id) {
      var el = byId(id), x = xhrOf(ctx);
      var text = (x && header(ctx, 'X-UT-Response') === 'refused' &&
        header(ctx, 'Content-Type').indexOf('text/plain') === 0 && x.responseText) || '';
      if (el) el.textContent = text;
      if (el && text.trim()) hideGenericServerAlert();
    },
    // ut-docs#2982: a failed Settings save. A refusal the server marked
    // X-UT-Response: refused (httpx.RefuseText — already translated, meant
    // for the operator) goes into a role=alert line right after the form
    // that made the request (one card can hold several forms:
    // #settings-display has seven), and replaces the generic banners the
    // same response raised. Anything else — an unmarked text body is an
    // untranslated developer string ("could not save"), HTML, empty, a
    // network error — unhides #<bannerId> exactly as `unhide:<bannerId>`
    // did. That form's next request clears the line (htmx:beforeRequest
    // below).
    'save-error': function (ctx, bannerId) {
      var banner = byId(bannerId);
      var x = xhrOf(ctx);
      var text = (x && header(ctx, 'X-UT-Response') === 'refused' &&
        header(ctx, 'Content-Type').indexOf('text/plain') === 0 && (x.responseText || '').trim()) || '';
      var anchor = saveErrorAnchor(ctx.el);
      if (!text || !anchor.isConnected) {
        if (banner) banner.hidden = false;
        return;
      }
      var line = saveErrorLine(anchor);
      if (!line) {
        line = document.createElement('p');
        line.className = 'login-error save-error-inline';
        line.setAttribute('role', 'alert');
        line.setAttribute('data-testid', 'save-error-inline');
        if (anchor.classList.contains('card')) anchor.appendChild(line);
        else anchor.insertAdjacentElement('afterend', line);
      }
      line.textContent = text;
      if (banner) banner.hidden = true;
      // app.js's htmx:responseError already raised the generic "server
      // error" alert for this non-2xx; the specific reason supersedes it.
      hideGenericServerAlert();
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
    // data-confirm-text (already translated server-side). Asked in the till's
    // own dialog (till-confirm.js, ut-docs#5524): the submit is held, then
    // re-issued with requestSubmit() once confirmed -- utConfirmed lets that
    // one re-submit through. Native confirm() only if the dialog is missing.
    'confirm': function (ctx) {
      var f = ctx.el;
      if (f.utConfirmed) { f.utConfirmed = false; return; }
      var q = f.getAttribute('data-confirm-text') || '';
      if (!(window.UT && window.UT.confirm && byId('ut-confirm'))) {
        if (!window.confirm(q)) { preventDefault(ctx); return false; } // native-confirm:allow no #ut-confirm shell (login/setup/self-order pages): native fallback
        return;
      }
      preventDefault(ctx);
      var sub = ctx.event && ctx.event.submitter;
      // Verb labels / tone from the form, same attributes as the htmx path
      // (ut-docs#6148); an absent attribute keeps the shell default.
      window.UT.confirm(q, {
        opener: sub || f,
        ok: f.getAttribute('data-confirm-ok') || undefined,
        dismiss: f.getAttribute('data-confirm-dismiss') || undefined,
        tone: f.getAttribute('data-confirm-tone') === 'neutral' ? 'neutral' : undefined
      }).then(function (ok) {
        if (!ok || !f.isConnected) return;
        // The submit handler runs synchronously inside requestSubmit; a form
        // that fails validation fires none, so the flag never outlives it.
        f.utConfirmed = true;
        try {
          if (f.requestSubmit) f.requestSubmit(sub || undefined); else f.submit();
        } finally { f.utConfirmed = false; }
      });
      return false;
    },
    // ut-docs#1540: a pairing button outside any <form> — trim, then let
    // `required` speak; an invalid name cancels the htmx request.
    'trim-validate': function (ctx, id) {
      var n = byId(id);
      if (!n) return;
      n.value = n.value.trim();
      if (!n.reportValidity()) { preventDefault(ctx); return false; }
    },
    // ut-docs#4507: the same check for a field that needs no trimming (the
    // Tills page's replace-data tick box): `required` speaks, an invalid
    // field cancels the htmx request. Chain it after trim-validate.
    // ut-docs#4560: the native bubble alone may never show (WebKitGTK
    // kiosks), cancelling with nothing visible — so also mark the field and
    // the hint its aria-describedby names with .field-invalid until it
    // turns valid (the till-name field's persistent hint, ut-docs#1548).
    'validate': function (ctx, id) {
      var n = byId(id);
      if (!n) return;
      if (!n.reportValidity()) { markInvalid(n); preventDefault(ctx); return false; }
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
      var dlg = ctx.el.closest('dialog');
      // A terminal answer, not a Cancel (ut-docs#5275). Only while still
      // open: a dialog the operator already cancelled mid-request has had
      // its cleanup, and a stale flag must not outlive it.
      if (dlg && dlg.open) dlg.utAnswered = true;
      closeDialog(dlg);
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
    'dismiss-move-approval-notice': function () { if (window.utMoveApprovalNoticeDismiss) window.utMoveApprovalNoticeDismiss(); }, // ut-docs#5127
    // receipt.html's "ask" prompt: Print hides the prompt BEFORE any
    // window.print() fallback, so the question never lands on paper.
    'receipt-ask-print': function (ctx, no) {
      var el = byId('receipt-ask');
      reprint(no, ctx.el.getAttribute('data-customer-locale')).then(function (r) {
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
      reprint(no, b.getAttribute('data-customer-locale')).then(function (r) {
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

  // ---- confirm / elevation prompt cleanup (ut-docs#5275) ---------------------
  // confirm_prompt.html / elevation_prompt.html (dialog[data-ut-prompt]).
  // Escape (base.html) and Cancel both just close the dialog; this puts
  // focus back on the control that asked and removes the prompt's own
  // .elevation-hint from the hx-target. The opener is captured at request
  // start (autofocus has moved focus by the time the dialog opens). Only
  // the hint NODE is removed, never the target's innerHTML, so a real
  // answer in the target is untouched.
  function captureOpener(el, detail) {
    var from = el && el.closest ? el.closest('dialog[data-ut-prompt]') : null;
    if (from) {
      detail.utPromptFrom = from; detail.utOpener = from.utOpener || null;
      var tf = detail.target;
      detail.utHintBefore = tf && tf.querySelector ? tf.querySelector(':scope > .elevation-hint') : null;
      return;
    }
    var a = document.activeElement;
    var op = a && a !== document.body ? a : null;
    if (!op) {
      var te = detail.requestConfig && detail.requestConfig.triggeringEvent;
      op = (te && te.submitter) || el;
    }
    detail.utOpener = op;
    var tg = detail.target;
    detail.utHintBefore = tg && tg.querySelector ? tg.querySelector(':scope > .elevation-hint') : null;
  }
  var PROMPT_IDS = { 'confirm-prompt': 'confirm-modal', 'elevation-prompt': 'elevation-modal' };
  function wirePrompt(ev, detail) {
    if (detail.utPromptWired) return;
    var id = PROMPT_IDS[header({ detail: detail }, 'X-UT-Response')];
    var d = id && byId(id);
    if (!d) return;
    detail.utPromptWired = true;
    d.utOpener = detail.utOpener || null;
    d.utHint = null;
    var f = d.querySelector('form[hx-target]');
    var t = null;
    try { t = f && document.querySelector(f.getAttribute('hx-target')); } catch (e) { t = null; }
    // Only a hint THIS response placed: the request's own swap target (a
    // swap="none" form like display-mode places nothing), and a node that
    // was not already there before the request.
    if (t && t === detail.target) {
      var h = t.querySelector(':scope > .elevation-hint');
      if (h && h !== detail.utHintBefore) d.utHint = h;
    }
    var from = detail.utPromptFrom;
    if (from && from !== d && from.isConnected && from.open) {
      from.utHandedOff = true; // confirm -> elevation: replaced, not cancelled
      from.close();
    }
  }
  document.addEventListener('close', function (ev) {
    var d = ev.target;
    if (!d || !d.matches || !d.matches('dialog[data-ut-prompt]')) return;
    try {
      if (d.utAnswered || d.utHandedOff) { d.utAnswered = d.utHandedOff = false; return; }
      if (d.utHint && d.utHint.isConnected) d.utHint.remove();
      var op = d.utOpener;
      if (op && op.isConnected && !op.disabled && op.focus) op.focus({ preventScroll: true });
      d.utOpener = d.utHint = null;
    } catch (e) { /* cleanup only */ }
  }, true);

  // ---- 3. htmx lifecycle ----------------------------------------------------
  // htmx fires these on the requesting element (beforeSwap/afterSettle on
  // the swap target); the walk up finds the carrier either way.
  var SRC = 'utInlineActionsSource';
  function htmxHook(attr) {
    return function (ev) { dispatch(ev.target, attr, ev, ev.detail || {}); };
  }
  // ut-docs#5291: aria-busy on a prompt's form while its own request runs
  // (htmx adds .htmx-request and disables the buttons itself).
  function markPromptBusy(el, on) {
    var f = el && el.tagName === 'FORM' && el.closest && el.closest('dialog[data-ut-prompt]') ? el : null;
    if (!f) return;
    if (on) f.setAttribute('aria-busy', 'true'); else f.removeAttribute('aria-busy');
  }
  document.addEventListener('htmx:beforeRequest', function (ev) {
    var detail = ev.detail || {};
    try { markPromptBusy(ev.target, true); } catch (e) { /* a11y hint only */ }
    // The same object htmx later dispatches htmx:afterRequest with.
    try { detail[SRC] = ev.target; } catch (e) { /* no replay for this one */ }
    try { captureOpener(ev.target, detail); } catch (e) { /* focus return only */ }
    clearSaveError(ev.target);
    dispatch(ev.target, 'data-before-request', ev, detail);
  }, true);
  document.addEventListener('htmx:afterRequest', function (ev) {
    var detail = ev.detail || {};
    var src = detail[SRC];
    try { markPromptBusy(ev.target, false); } catch (e) { /* a11y hint only */ }
    try { wirePrompt(ev, detail); } catch (e) { /* focus return only */ }
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

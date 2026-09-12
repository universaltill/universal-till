// ut-docs#2010 — the app-wide list/edit standard's behaviour
// (ut-docs/reference/list-and-dialog-pattern.md): the full-screen record
// dialog (create + tap-to-edit), its close/Escape/discard guard, its focus
// trap, and the list header's client-side search filter. Loaded on every
// page from base.html; everything is delegated and driven by data-*
// attributes, so on a page with none of that markup nothing here ever does
// anything, and a dialog that arrives later by htmx swap (the /items rail
// embeds /categories' content, ut-docs#1950) works without a per-page hook.
//
// Attribute vocabulary (the markup side lives in web/ui/partials/
// list_header.html, record_dialog.html and the page's own rows):
//   [data-record-dialog-open="<dialog id>"]   opens that dialog in create mode
//   [data-record-open="<dialog id>"]          a row: click/tap opens edit mode,
//     prefilled from the row's data-field-<name> attributes, form.action from
//     data-record-action (REQUIRED — a row without one is a page authoring
//     bug: console.error and refuse to open, never edit the wrong record),
//     the destructive forms' action from data-record-destructive-action
//     (cleared when the row carries none)
//   [data-record-edit]                        a real button inside a row that
//     opens the same edit — the keyboard path (ut-docs#826)
//   [data-record-dialog]                      the dialog; carries
//     data-create-action, data-discard-confirm, data-record-mode (set here)
//   [data-record-dialog-title]                the <h3>; data-title-create /
//     data-title-edit
//   [data-record-dialog-destructive]          the slot hidden in create mode
//   [data-record-destructive-form]            a form inside it whose action is
//     the row's data-record-destructive-action
//   [data-record-when="name=value"]           shown only when the opened row's
//     data-field-<name> equals value (e.g. deactivate vs activate)
//   [data-record-dialog-msg]                  the in-dialog error/status
//     region (ut-docs#2020) — the aria-live="polite" node itself, cleared
//     (emptied, never just hidden — see dialogFailureFallback below and
//     app.css's ":empty" rule) on every open() so a previous refusal never
//     appears to describe a new one. A form's own confirm-before-submit is
//     hx-confirm (a real htmx attribute, not a data-record-* one this file
//     reads) — see categories.html's record_dialog_destructive slot and
//     the "no submit-confirm listener here any more" comment further down
//     for why.
//   data-error-network / data-error-server (on [data-record-dialog])
//     back dialogFailureFallback below: translated text for a failure the
//     server never got to answer meaningfully at all (network
//     unreachable, or a plain-text 403/500 app.js's own htmx:beforeSwap
//     does not force-swap) — see that function's own comment.
//   [data-list-filter="<rows selector>"]      the search input; optional
//     data-list-empty="<selector>" names the translated no-results row
//   data-record-default-action                set HERE on the dialog's form
//     the first time it is seen: the server-rendered action, captured once
//     so the create fallback never reads an action a previous open() wrote
//
// Dialogs are opened with .show(), never .showModal(): the on-screen
// keyboard (#osk, osk.js) is appended to <body>, and showModal()'s
// top-layer/inert-outside behaviour makes it unreachable on the till's own
// touchscreen (ut-docs#1385). Two things .show() therefore does not give and
// this file implements by hand: Escape-to-close and the focus trap —
// without the latter, Tab walks out from the name field into the nav rail
// and the row buttons UNDER the opaque dialog and never reaches Close/Save
// (WCAG 2.4.3/2.4.7). The trap whitelists #osk, which never takes focus
// itself but must not be fought over. Escape-to-close does NOT by itself
// settle ut-docs#1999 for this pattern (a since-corrected claim this
// comment used to make) — #1999 (coding-standards.md §10) also requires
// status/lock/exit-to-OS to stay reachable while this dialog covers the
// nav rail, which record_dialog.html's .record-dialog-status-row handles
// (ut-docs#2099); bindStatusRow() further down is this file's half of it.
// No hardcoded user-facing strings: every message comes from a data-*
// attribute the template filled from a locale key. The console.error below
// is developer-facing (a page authoring bug), not operator-facing.
(function () {
  'use strict';

  var snapshots = typeof WeakMap === 'function' ? new WeakMap() : null;
  // The element that opened each dialog, so close() can hand focus back.
  var openers = typeof WeakMap === 'function' ? new WeakMap() : null;

  function serialize(form) {
    // What the server would receive, as one comparable string.
    return new URLSearchParams(new FormData(form)).toString();
  }

  function formOf(dialog) {
    var save = dialog.querySelector('.record-dialog-save[form]');
    return save ? document.getElementById(save.getAttribute('form')) : null;
  }

  // The server-rendered action, captured ONCE (first sight) and never from
  // the live attribute afterwards — a previous open() has already
  // overwritten that (review B1: New after an edit, with the create action
  // blank, posted to the last-opened record and Save overwrote it).
  function defaultAction(form) {
    if (!form.hasAttribute('data-record-default-action')) {
      form.setAttribute('data-record-default-action', form.getAttribute('action') || '');
    }
    return form.getAttribute('data-record-default-action');
  }

  function fieldsOf(row) {
    var out = {};
    for (var i = 0; i < row.attributes.length; i++) {
      var a = row.attributes[i];
      if (a.name.indexOf('data-field-') === 0) out[a.name.slice('data-field-'.length)] = a.value;
    }
    return out;
  }

  function setField(form, name, value) {
    var el = form.elements.namedItem(name);
    if (!el) return;
    if (el.type === 'checkbox') {
      el.checked = (value === '1' || value === 'true' || value === el.value);
    } else {
      // Also covers a RadioNodeList (radios sharing a name): assigning
      // .value selects the matching radio.
      el.value = value;
    }
  }

  function firstField(form) {
    return form.querySelector(
      'input:not([type="hidden"]):not([disabled]), select:not([disabled]), textarea:not([disabled])'
    );
  }

  // The ONE discard guard, shared by every path that would drop the form's
  // current contents: Close, Escape, AND opening again over a dirty form
  // (review S1 — open() used to skip it, so New over an edit with unsaved
  // text silently emptied the field). confirm() for consistency with the
  // hx-confirm this codebase already uses everywhere else.
  function confirmDiscard(dialog) {
    var form = formOf(dialog);
    if (!(form && snapshots && snapshots.has(dialog))) return true;
    if (serialize(form) === snapshots.get(dialog)) return true;
    return window.confirm(dialog.getAttribute('data-discard-confirm') || '');
  }

  // ut-docs#2020: hx-boost reads a form's method+action ONCE, when htmx
  // first PROCESSES the element (page load, or the last time something
  // called htmx.process on it) — verified against the actual shipped
  // web/public/vendor/htmx.min.js (1.9.12)'s lt()/_t(): the action is
  // captured into a closure at that moment, not re-read fresh on every
  // submit the way a native, non-boosted <form> would. Every open() below
  // rewrites a boosted form's `action` to route it at the right row/create
  // endpoint — without this, htmx keeps submitting to whatever `action`
  // was on the form the FIRST time htmx ever saw it (its own static
  // template default), regardless of which row is actually open.
  // htmx.process() is htmx's own documented re-scan entry point for
  // exactly this "I changed an hx-* or boosted attribute by hand" case —
  // it deinits and reinstalls the element's handlers, recapturing the
  // live action. A no-op if htmx.min.js hasn't loaded (defer + script
  // order guarantees it has by the time any dialog can open) or the
  // element isn't hx-boosted at all (a future record_dialog user with no
  // htmx forms) — both harmless, so guarded rather than assumed.
  function reboost(el) {
    if (el && window.htmx && typeof window.htmx.process === 'function') window.htmx.process(el);
  }

  function open(dialog, row, opener) {
    var action = row ? row.getAttribute('data-record-action') : null;
    if (row && !action) {
      // Failing loudly beats editing the wrong record.
      console.error('record-dialog: row carries data-record-open but no data-record-action — refusing to open it', row);
      return;
    }
    if (dialog.open && !confirmDiscard(dialog)) return;

    var form = formOf(dialog);
    var title = dialog.querySelector('[data-record-dialog-title]');
    var destructive = dialog.querySelector('[data-record-dialog-destructive]');
    var mode = row ? 'edit' : 'create';
    if (form) { defaultAction(form); form.reset(); }
    // ut-docs#2020: a message left over from a previous refused save must
    // not appear to describe THIS open — every open starts clean. Emptying
    // the text is enough to hide it too: app.css's
    // ".record-dialog-msg:empty { display: none }" reacts to that on its
    // own, so there is no separate "hidden" flag to keep in sync with the
    // text — the element's own emptiness IS the visibility state.
    var msg = dialog.querySelector('[data-record-dialog-msg]');
    if (msg) msg.textContent = '';

    if (row) {
      var fields = fieldsOf(row);
      if (form) {
        Object.keys(fields).forEach(function (name) { setField(form, name, fields[name]); });
        form.setAttribute('action', action);
        reboost(form);
      }
      // Unconditional: a row with no destructive action clears it rather
      // than leaving the previous row's (review B1).
      var dAction = row.getAttribute('data-record-destructive-action');
      Array.prototype.forEach.call(dialog.querySelectorAll('[data-record-destructive-form]'), function (f) {
        if (dAction) f.setAttribute('action', dAction); else f.removeAttribute('action');
        reboost(f);
      });
      Array.prototype.forEach.call(dialog.querySelectorAll('[data-record-when]'), function (el) {
        var cond = (el.getAttribute('data-record-when') || '').split('=');
        el.hidden = !(cond.length === 2 && fields[cond[0]] === cond[1]);
      });
      if (title) title.textContent = title.getAttribute('data-title-edit') || title.textContent;
      if (destructive) destructive.hidden = false;
    } else {
      if (form) {
        form.setAttribute('action', dialog.getAttribute('data-create-action') || defaultAction(form));
        reboost(form);
      }
      if (title) title.textContent = title.getAttribute('data-title-create') || title.textContent;
      if (destructive) destructive.hidden = true;
    }

    dialog.setAttribute('data-record-mode', mode);
    if (openers && !dialog.open) openers.set(dialog, opener || document.activeElement);
    if (!dialog.open) dialog.show();
    if (form && snapshots) snapshots.set(dialog, serialize(form));
    // Focus on open is fine (it follows a deliberate tap); nothing here ever
    // focuses on page load. First ENABLED field, not the first focusable —
    // that would be the trash/close button in the head.
    var f = form && firstField(form);
    if (f) f.focus();
  }

  function close(dialog) {
    if (!confirmDiscard(dialog)) return;
    if (dialog.open) dialog.close();
    // Hand focus back to what opened the dialog (WCAG 2.4.3): the pencil
    // or New button the operator was on. Nothing is focused on the page
    // behind while the dialog is open, so this is the only way back.
    var back = openers ? openers.get(dialog) : null;
    if (openers) openers.delete(dialog);
    if (back && back !== document.body && document.contains(back) && typeof back.focus === 'function') back.focus();
  }

  // --- Focus trap --------------------------------------------------------
  // .show() leaves the page behind fully reachable by keyboard. While a
  // record dialog is open, Tab/Shift+Tab cycle inside it and a focusin
  // backstop pulls back any focus that lands outside (a stray tap on the
  // strip the OSK-shortened dialog uncovers, a script focusing something).
  // #osk is whitelisted: it is a <body> child and stays reachable by design.

  var FOCUSABLE = 'a[href], button:not([disabled]), input:not([type="hidden"]):not([disabled]), ' +
    'select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

  function focusables(dialog) {
    return Array.prototype.filter.call(dialog.querySelectorAll(FOCUSABLE), function (el) {
      // Skips anything hidden — the destructive slot in create mode, the
      // data-record-when form that did not match — which has no box.
      return el.getClientRects().length > 0;
    });
  }

  function openDialog() {
    var all = document.querySelectorAll('[data-record-dialog][open]');
    return all.length ? all[all.length - 1] : null;
  }

  function trapTab(dialog, ev) {
    var list = focusables(dialog);
    if (!list.length) { ev.preventDefault(); return; }
    var idx = list.indexOf(document.activeElement);
    if (ev.shiftKey) {
      if (idx <= 0) { ev.preventDefault(); list[list.length - 1].focus(); }
    } else if (idx === -1 || idx === list.length - 1) {
      ev.preventDefault(); list[0].focus();
    }
  }

  document.addEventListener('focusin', function (ev) {
    var dialog = openDialog();
    if (!dialog) return;                       // no dialog open: never interferes
    var t = ev.target;
    if (!t || dialog.contains(t)) return;
    var osk = document.getElementById('osk');
    if (osk && osk.contains(t)) return;
    // Another <dialog> opened ON TOP of this one owns the focus while it is
    // up — the trap must not fight it. /categories has none today, but the
    // ut-docs#2012 screens carry #plugin-install-modal, #hold-modal and
    // friends, and yanking focus out of one of those would be this trap
    // causing the very bug it exists to prevent.
    var top = t.closest ? t.closest('dialog[open]') : null;
    if (top && top !== dialog) return;
    var list = focusables(dialog);
    if (list.length) list[0].focus(); else dialog.focus();
  });

  // --- Dialog: open (create / edit), close ------------------------------

  document.addEventListener('click', function (ev) {
    var t = ev.target;
    if (!t || !t.closest) return;

    var opener = t.closest('[data-record-dialog-open]');
    if (opener) {
      var target = document.getElementById(opener.getAttribute('data-record-dialog-open'));
      if (target) { ev.preventDefault(); open(target, null, opener); }
      return;
    }

    var closer = t.closest('.record-dialog-close');
    if (closer) {
      var owner = closer.closest('[data-record-dialog]');
      if (owner) { ev.preventDefault(); close(owner); }
      return;
    }

    var row = t.closest('[data-record-open]');
    if (!row) return;
    var explicit = t.closest('[data-record-edit]');
    // A control inside the row (reorder button, a link, a form) keeps its
    // own job — only the explicit edit button or the row itself opens.
    if (!explicit && t.closest('button, a, input, select, textarea, label, form')) return;
    var dlg = document.getElementById(row.getAttribute('data-record-open'));
    if (!dlg) return;
    ev.preventDefault();
    // Focus comes back to the row's real focusable control on close (the
    // row itself is not focusable — a <tr>).
    open(dlg, row, explicit || row.querySelector('[data-record-edit]') || row);
  });

  // ut-docs#2020: a destructive form asking first (the icon-only trash
  // button has no caption to slow a mis-tap down) used to be implemented
  // HERE, via a data-record-confirm attribute + a document-level 'submit'
  // listener calling window.confirm()/preventDefault(). Once these forms
  // became hx-boosted, that raced hx-boost's own submit listener — bound
  // directly to the FORM, the event's target, it always ran (and issued
  // the request) BEFORE a listener on `document` ever saw the same event,
  // so cancelling here no longer stopped anything. Replaced by htmx's own
  // native hx-confirm attribute (see categories.html's
  // record_dialog_destructive slot), which htmx reads fresh from INSIDE
  // the same call that goes on to issue the request — no separate listener
  // to race. No listener implements this any more; kept as a comment, not
  // dead code, since a future page copying this pattern needs to know
  // hx-confirm is the mechanism, not a data-record-confirm attribute this
  // file no longer looks for.

  // ut-docs#2020 (AC5, offline-first / ADR-0003): a request that gets NO
  // coded response back at all — the network is unreachable — or a
  // response app.js's own htmx:beforeSwap does NOT force-swap (that
  // handler only ever swaps a non-2xx response that is real, non-empty
  // text/html — a plain-text 403/500, e.g. a permission gate rejecting the
  // request before categories_page.go's own mutation handler ever runs,
  // falls straight through it) must still leave the operator told
  // SOMETHING went wrong, same as any other refusal. Before this, neither
  // case showed anything at all: app.js's own #pos-alert fallback exists
  // only on the sale screen (web/ui/pages/index.html), nowhere on this
  // app-wide dialog pattern — the exact ut-docs#916 class of bug, just for
  // a dialog instead of a fragment target, and exactly the "spinner with
  // no failure path" AC5 forbids.
  //
  // Global listeners (htmx:sendError/htmx:responseError fire on
  // `document.body`, not a specific element) — MUST check ev.detail.elt is
  // actually inside the open dialog, not just that a dialog happens to be
  // open: base.html polls unrelated fragments on every page (e.g.
  // #pairing-notice, hx-trigger="load, every 30s") whose own failure has
  // nothing to do with whatever the operator is editing. Without this
  // check, review found that exact poll failing 30s after opening an
  // unrelated row stamped "something went wrong" into a dialog the
  // operator had touched nothing in. Strings come from the dialog's own
  // data-error-* attributes (record_dialog.html), filled from a locale
  // key, so this file stays locale-free like everything else here.
  function dialogFailureFallback(kind, ev) {
    var dialog = openDialog();
    if (!dialog) return;
    var elt = ev && ev.detail && ev.detail.elt;
    if (!elt || !dialog.contains(elt)) return;
    var msg = dialog.querySelector('[data-record-dialog-msg]');
    if (!msg) return;
    var text = dialog.getAttribute(kind === 'network' ? 'data-error-network' : 'data-error-server');
    if (text) msg.textContent = text;
  }
  document.body.addEventListener('htmx:sendError', function (ev) { dialogFailureFallback('network', ev); });
  document.body.addEventListener('htmx:responseError', function (ev) { dialogFailureFallback('server', ev); });

  // Escape closes and Tab cycles — bound on each dialog element, not on
  // document, so neither can ever swallow a key meant for something else
  // on the page. Flip side: both only fire while focus is INSIDE the
  // dialog, which is exactly what the focus trap guarantees.
  function bind(root) {
    var dialogs = (root || document).querySelectorAll('[data-record-dialog]');
    Array.prototype.forEach.call(dialogs, function (dialog) {
      if (dialog.hasAttribute('data-record-dialog-bound')) return;
      dialog.setAttribute('data-record-dialog-bound', '');
      dialog.addEventListener('keydown', function (ev) {
        if (ev.key === 'Tab') { trapTab(dialog, ev); return; }
        if (ev.key !== 'Escape' && ev.key !== 'Esc') return;
        ev.preventDefault();
        ev.stopPropagation();
        close(dialog);
      });
    });
  }

  // --- Status row: sync/offline indicator (ut-docs#2099) -----------------
  // [data-record-dialog-conn] (record_dialog.html's .record-dialog-status-
  // row, absent outright in self-order kiosk mode — see that file's own
  // comment) is a SECOND, independent instance of base.html's #sb-conn
  // footer chip, same data-conn-online/data-conn-offline attributes and
  // same navigator.onLine + online/offline-event logic — deliberately not
  // shared code, since base.html's own inline script looks up `#sb-conn`
  // by its one fixed id and can't see this one anyway.
  //
  // Bound ONCE globally, not per element (ut-docs#2122). The previous
  // shape mirrored bind()'s per-element bound-guard, which is harmless
  // there for a different reason than "the element persists" — the
  // [data-record-dialog] element is swapped away and recreated by the
  // /items rail exactly like this status row is (both live inside the
  // same categories.html fragment #items-panel replaces wholesale), but
  // bind()'s keydown listener is attached directly to the dialog element
  // itself, so it is garbage-collected together with that detached node
  // once nothing else references it — a per-element guard there is only
  // ever redundant, never leak-preventing. This status row's listeners
  // were instead attached to `window`, a target that outlives every
  // swap: each new [data-record-dialog-conn] element got its own fresh
  // window online/offline pair, the previous element's pair was never
  // removed, and the closure over that pair kept its now-detached element
  // alive too. A per-element guard can never stop that, because the
  // "already bound" flag lived on the very element the next swap throws
  // away. Painting every currently-present element from one shared pair
  // of listeners avoids the leak outright, the same way #sb-conn avoids
  // it by simply never being re-created.
  function paintStatusRows() {
    var online = navigator.onLine;
    var els = document.querySelectorAll('[data-record-dialog-conn]');
    Array.prototype.forEach.call(els, function (el) {
      var txt = el.querySelector('.sb-conn-text');
      var on = el.getAttribute('data-conn-online'), off = el.getAttribute('data-conn-offline');
      el.classList.toggle('is-offline', !online);
      if (txt) txt.textContent = online ? on : off;
    });
  }
  window.addEventListener('online', paintStatusRows);
  window.addEventListener('offline', paintStatusRows);

  // Kept as its own function (called from init() and the htmx:afterSwap
  // handler below, same as bind()) so a freshly-swapped-in element gets
  // its initial paint immediately rather than waiting for the next
  // online/offline event.
  function bindStatusRow() {
    paintStatusRows();
  }

  // --- List header: client-side filter ---------------------------------
  // Right for a bounded list (a few hundred rows); above that the screen
  // moves to server-side limit/offset (ut-docs#2014), not a bigger filter.

  function applyFilter(input) {
    var sel = input.getAttribute('data-list-filter');
    if (!sel) return;
    var q = (input.value || '').trim().toLocaleLowerCase();
    var rows = document.querySelectorAll(sel);
    var shown = 0;
    Array.prototype.forEach.call(rows, function (r) {
      var hit = !q || (r.textContent || '').toLocaleLowerCase().indexOf(q) !== -1;
      r.hidden = !hit;
      if (hit) shown++;
    });
    var emptySel = input.getAttribute('data-list-empty');
    var empty = emptySel ? document.querySelector(emptySel) : null;
    if (empty) empty.hidden = !(rows.length > 0 && shown === 0);
  }

  // A back/forward-cache restore can bring the field back with text in it,
  // and an htmx swap that replaces only the rows comes back unfiltered
  // while the search box still shows a query — re-apply in both cases.
  function reapplyFilters() {
    Array.prototype.forEach.call(document.querySelectorAll('[data-list-filter]'), function (input) {
      if (input.value) applyFilter(input);
    });
  }

  document.addEventListener('input', function (ev) {
    var input = ev.target && ev.target.closest ? ev.target.closest('[data-list-filter]') : null;
    if (input) applyFilter(input);
  });
  // The native clear (x) of a type=search field fires "search", not "input",
  // in some engines.
  document.addEventListener('search', function (ev) {
    var input = ev.target && ev.target.closest ? ev.target.closest('[data-list-filter]') : null;
    if (input) applyFilter(input);
  }, true);

  function init() {
    bind(document);
    bindStatusRow(document);
    reapplyFilters();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
  document.addEventListener('htmx:afterSwap', function () { bind(document); bindStatusRow(document); reapplyFilters(); });
})();

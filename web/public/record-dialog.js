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
//   [data-record-confirm="…"]                 a form that asks before submit
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
// this file implements by hand: Escape-to-close (settling ut-docs#1999) and
// the focus trap — without one, Tab walks out from the name field into the
// nav rail and the row buttons UNDER the opaque dialog and never reaches
// Close/Save (WCAG 2.4.3/2.4.7). The trap whitelists #osk, which never takes
// focus itself but must not be fought over.
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

    if (row) {
      var fields = fieldsOf(row);
      if (form) {
        Object.keys(fields).forEach(function (name) { setField(form, name, fields[name]); });
        form.setAttribute('action', action);
      }
      // Unconditional: a row with no destructive action clears it rather
      // than leaving the previous row's (review B1).
      var dAction = row.getAttribute('data-record-destructive-action');
      Array.prototype.forEach.call(dialog.querySelectorAll('[data-record-destructive-form]'), function (f) {
        if (dAction) f.setAttribute('action', dAction); else f.removeAttribute('action');
      });
      Array.prototype.forEach.call(dialog.querySelectorAll('[data-record-when]'), function (el) {
        var cond = (el.getAttribute('data-record-when') || '').split('=');
        el.hidden = !(cond.length === 2 && fields[cond[0]] === cond[1]);
      });
      if (title) title.textContent = title.getAttribute('data-title-edit') || title.textContent;
      if (destructive) destructive.hidden = false;
    } else {
      if (form) form.setAttribute('action', dialog.getAttribute('data-create-action') || defaultAction(form));
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

  // A destructive form asks first (the icon-only trash button has no
  // caption to slow a mis-tap down).
  document.addEventListener('submit', function (ev) {
    var f = ev.target;
    if (!f || !f.getAttribute || !f.hasAttribute('data-record-confirm')) return;
    if (!window.confirm(f.getAttribute('data-record-confirm') || '')) ev.preventDefault();
  });

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
    reapplyFilters();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
  document.addEventListener('htmx:afterSwap', function () { bind(document); reapplyFilters(); });
})();

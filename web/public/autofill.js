// Suppress the browser's own autofill / typed-value-history dropdown, plus
// mobile autocapitalize/autocorrect/spellcheck, on every text-entry input —
// "everywhere" (ut-docs#400): it covers the sale screen's barcode field mid
// scan, offers wrong values from unrelated fields, and on a SHARED till
// leaks one operator's typed history to the next — the login screen every
// operator handover shows is exactly the sharpest case of that last one.
//
// Central, not per-template, so a new form can't silently reintroduce it:
// this walks every <input>/<textarea> already on the page at load, anything
// htmx swaps in, and anything ANY future page or plugin-served content adds
// afterwards — via a MutationObserver, so there is no per-template call site
// to forget.
//
// Its own file, NOT part of app.js: app.js is only loaded by
// web/ui/layouts/base.html, but four page templates are whole standalone
// documents that bypass that layout and own their own <script> tags —
// web/ui/pages/login.html, setup.html, self_order.html and
// self_order_shop.html (the same class of gap ut-docs#344 hit with htmx,
// which is why guard-htmx-loaded.sh exists). Bundling this into app.js would
// have silently skipped exactly the screen every operator handover shows.
// scripts/ci/guard-autofill-suppression.sh checks every standalone document
// loads this file, the same way guard-htmx-loaded.sh checks htmx.
//
// `autocomplete="off"` alone is unreliable on some Chromium builds — a
// unique, non-standard token per field is the belt-and-braces version the
// card asked for (still spec-legal: an unrecognized token is processed as
// "off"). Elements that already declare their OWN autocomplete — e.g.
// web/ui/pages/pin.html's `current-password`/`new-password`, a deliberate
// password-manager integration for the PIN-change form — are left alone;
// this only fills the gap where nothing was set. autocapitalize/autocorrect
// are always forced off regardless, since neither conflicts with that kind
// of deliberate choice. spellcheck is forced off on <input> only — the one
// <textarea> in this codebase (the bug-report note field) is free prose the
// shop owner actually writes, so it keeps the browser's normal spellcheck;
// it still gets the autocomplete/autocapitalize/autocorrect treatment,
// since a "previous notes" history dropdown is the same privacy leak as
// anywhere else on a shared till.
(function () {
  var TEXTY_TYPES = {
    text: 1, search: 1, number: 1, tel: 1, email: 1, url: 1, password: 1,
    date: 1, time: 1, 'datetime-local': 1, month: 1, week: 1
  };
  var seen = new WeakSet();
  var counter = 0;

  function eligible(el) {
    if (el.tagName === 'TEXTAREA') return true;
    if (el.tagName !== 'INPUT') return false;
    return !!TEXTY_TYPES[(el.getAttribute('type') || 'text').toLowerCase()];
  }

  // autocomplete is a space-separated token list — a raw name/id (e.g. a
  // plugin-declared settings key, web/ui/pages/plugin_settings.html's
  // `setting_{{ .Key }}`) could contain a space or other token-breaking
  // character. Not a security issue (this only ever reaches setAttribute,
  // never parsed as markup), but a stray space would turn one intended
  // token into two.
  function autocompleteKey(el) {
    var raw = el.getAttribute('name') || el.id || ('f' + (counter++));
    return raw.replace(/[^A-Za-z0-9_-]/g, '-');
  }

  function suppress(el) {
    if (!eligible(el) || seen.has(el)) return;
    seen.add(el);
    if (el.hasAttribute('data-allow-autofill')) return; // explicit, reviewed opt-out
    if (!el.getAttribute('autocomplete')) {
      el.setAttribute('autocomplete', 'off-' + autocompleteKey(el));
    }
    el.setAttribute('autocapitalize', 'off');
    el.setAttribute('autocorrect', 'off');
    if (el.tagName !== 'TEXTAREA') el.setAttribute('spellcheck', 'false');
  }

  function sweep(root) {
    var els = (root || document).querySelectorAll('input, textarea');
    for (var i = 0; i < els.length; i++) suppress(els[i]);
  }

  function onReady(fn) {
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', fn, { once: true });
    } else {
      fn();
    }
  }

  onReady(function () { sweep(document); });
  // htmx swaps content in and out of the same page (basket updates, page
  // navigation under hx-boost, …) without a full reload.
  document.addEventListener('htmx:afterSwap', function () { sweep(document); });

  if (window.MutationObserver) {
    new MutationObserver(function (mutations) {
      for (var i = 0; i < mutations.length; i++) {
        var added = mutations[i].addedNodes;
        for (var j = 0; j < added.length; j++) {
          var node = added[j];
          if (node.nodeType !== 1) continue; // element nodes only
          if (eligible(node)) suppress(node);
          if (node.querySelectorAll) sweep(node);
        }
      }
    }).observe(document.documentElement, { childList: true, subtree: true });
  }
})();

/* Tills & devices in the Settings tree (ut-docs#7173).
 *
 * - A lazy section (a Settings card holding [data-tills-body]) loads its
 *   fragment the first time the card is shown (settings.html dispatches
 *   ut:section-shown on the card) and again after tills-changed, never
 *   before: /settings makes no network call for these sections (ADR-0003).
 * - One Save on This till submits whichever of its forms changed.
 * - A removal asks for the manager PIN in an inline panel under its button
 *   (no confirm, no dialog), checks it, then posts each removal to its own
 *   route one after another, so every local removal is exactly the existing
 *   revoke.
 * - The tree's count tag on Old entries follows the list without a reload.
 *
 * No user-facing text lives here: every string comes from a data-* attribute
 * the server rendered already translated.
 */
(function () {
  'use strict';

  var UT = window.UT = window.UT || {};

  function bodies(card) { return Array.prototype.slice.call(card.querySelectorAll('[data-tills-body]')); }
  function lazyCards() { return Array.prototype.slice.call(document.querySelectorAll('#settings-grid > .card')).filter(function (c) { return bodies(c).length; }); }

  function load(body) {
    if (!window.htmx) return;
    body.setAttribute('data-requested', '1');
    window.htmx.trigger(body, 'ut:load-section');
  }

  function liveFor(el) {
    var card = el && el.closest ? el.closest('#settings-grid > .card') : null;
    return (card && card.querySelector('[data-tills-live]')) || document.querySelector('#settings-tills-old [data-tills-live]');
  }
  function say(el, text) {
    var live = liveFor(el);
    if (live) live.textContent = text || '';
  }

  // Shown for the first time: load every fragment in the card once.
  document.addEventListener('ut:section-shown', function (ev) {
    var card = ev.target;
    if (!card || !card.querySelectorAll) return;
    bodies(card).forEach(function (b) { if (!b.hasAttribute('data-requested')) load(b); });
  }, true);
  // The listener above is on document (capture) because the event does not
  // bubble: capture still reaches it.

  // A first-load deep link (/settings#settings-tills-old, or an old /tills?tab=
  // redirect) shows its card before this deferred script has registered the
  // listener, so the event above was already lost. On init, load the lazy
  // card that is visible right now (ut-docs#7173).
  function loadVisible() {
    lazyCards().forEach(function (c) {
      if (c.classList.contains('settings-section-hidden')) return;
      bodies(c).forEach(function (b) { if (!b.hasAttribute('data-requested')) load(b); });
    });
  }
  UT.loadVisibleTills = loadVisible;
  // After DOMContentLoaded (so htmx, also deferred, has processed the page and
  // its ut:load-section trigger exists): a timeout runs after htmx's own handler.
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', function () { setTimeout(loadVisible, 0); });
  else setTimeout(loadVisible, 0);

  // Something changed (a removal, a role change): reload the sections that
  // are already loaded, so the list, the banner and the count agree.
  var focusAfter = null;
  document.body.addEventListener('tills-changed', function () {
    lazyCards().forEach(function (c) {
      bodies(c).forEach(function (b) {
        if (b.hasAttribute('data-requested')) {
          // A manager mid-approval in the pairing box is never interrupted by a
          // push (ut-docs#7097 AC16): leave that section as it is.
          if (b.querySelector('#pending-pairings, #reader-requests') && window.utPairingBusy && window.utPairingBusy()) return;
          if (!c.classList.contains('settings-section-hidden')) focusAfter = c;
          load(b);
        }
      });
    });
  });

  // After a fragment lands: keep the tree's count tag right and, if the
  // person's button just disappeared, put focus on the section heading.
  document.body.addEventListener('htmx:afterSwap', function (ev) {
    var t = ev.detail && ev.detail.target;
    if (!t || !t.hasAttribute || !t.hasAttribute('data-tills-body')) return;
    t.setAttribute('data-loaded', '1');
    var holder = t.querySelector('[data-old-count]');
    if (holder) setCount(parseInt(holder.getAttribute('data-old-count'), 10) || 0);
    var card = t.closest('#settings-grid > .card');
    if (focusAfter && card === focusAfter) {
      focusAfter = null;
      var h = card.querySelector('h2');
      var active = document.activeElement;
      if (h && (!active || active === document.body || !card.contains(active))) {
        if (!h.hasAttribute('tabindex')) h.setAttribute('tabindex', '-1');
        h.focus();
      }
    }
  });

  function setCount(n) {
    var link = document.querySelector('#settings-tree a[data-section="settings-tills-old"]');
    if (!link) return;
    var tag = link.querySelector('[data-tree-count]');
    if (n <= 0) { if (tag) tag.remove(); return; }
    if (!tag) return; // built from the server's rendering; absent only while unknown
    var digit = tag.querySelector('[aria-hidden]');
    var words = tag.querySelector('.visually-hidden');
    var was = tag.getAttribute('data-n') || '';
    if (digit) digit.textContent = String(n);
    if (words && was) words.textContent = words.textContent.replace(was, String(n));
    tag.setAttribute('data-n', String(n));
  }

  // A successful removal button says what it did.
  document.body.addEventListener('htmx:afterRequest', function (ev) {
    var elt = ev.detail && ev.detail.elt;
    if (!elt || !elt.getAttribute || !ev.detail.successful) return;
    var done = elt.getAttribute('data-done-text');
    if (done) say(elt, done);
  });

  // One Save for the forms of This till.
  document.addEventListener('click', function (ev) {
    var btn = ev.target.closest ? ev.target.closest('[data-save-forms]') : null;
    if (!btn) return;
    ev.preventDefault();
    var forms = Array.prototype.slice.call(document.querySelectorAll(btn.getAttribute('data-save-forms')));
    var any = false;
    forms.forEach(function (f) {
      var changed = false;
      Array.prototype.forEach.call(f.elements, function (el) {
        if (el.type === 'text' && el.value !== el.defaultValue) changed = true;
        if (el.tagName === 'SELECT') {
          var opt = el.querySelector('option[selected]');
          if ((opt ? opt.value : '') !== el.value) changed = true;
        }
      });
      if (!changed) return;
      any = true;
      if (f.reportValidity && !f.reportValidity()) return;
      if (f.requestSubmit) f.requestSubmit(); else f.dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));
    });
    if (!any) {
      var msg = forms.length ? forms[0].querySelector('[role=status]') : null;
      if (msg) msg.textContent = '';
    }
  });

  // Removal asks for the manager PIN in a panel right under the button
  // (tills_pin_panel.html): no native confirm, no dialog, no showModal. The PIN
  // is checked first (POST /api/sync/tills/verify-pin, the existing step-up
  // mechanism); only then is each removal posted in turn, so every local
  // removal is exactly the existing revoke route.
  function postForm(url, pin) {
    var body = 'override_pin=' + encodeURIComponent(pin);
    return fetch(url, {
      method: 'POST', credentials: 'same-origin',
      headers: { 'HX-Request': 'true', 'Content-Type': 'application/x-www-form-urlencoded' },
      body: body
    });
  }
  function panelOf(opener) { return document.getElementById(opener.getAttribute('aria-controls')); }
  function closePanel(panel, focusOpener) {
    panel.hidden = true;
    var input = panel.querySelector('[data-pin-input]');
    var err = panel.querySelector('[data-pin-error]');
    if (input) input.value = '';
    if (err) { err.textContent = ''; err.hidden = true; }
    var opener = document.querySelector('[data-pin-open][aria-controls="' + panel.id + '"]');
    if (opener) {
      opener.setAttribute('aria-expanded', 'false');
      if (focusOpener) opener.focus();
    }
  }
  function showError(panel, text) {
    var err = panel.querySelector('[data-pin-error]');
    if (!err) return;
    err.textContent = text || panel.getAttribute('data-failed-text') || '';
    err.hidden = false;
  }
  function openPanel(opener) {
    var panel = panelOf(opener);
    if (!panel) return;
    // One panel open at a time.
    Array.prototype.forEach.call(document.querySelectorAll('[data-pin-panel]'), function (p) {
      if (p !== panel && !p.hidden) closePanel(p, false);
    });
    panel.hidden = false;
    opener.setAttribute('aria-expanded', 'true');
    var input = panel.querySelector('[data-pin-input]');
    if (input) input.focus();
  }
  function confirmPanel(panel) {
    var input = panel.querySelector('[data-pin-input]');
    var confirmBtn = panel.querySelector('[data-pin-confirm]');
    var pin = input ? input.value.trim() : '';
    var urls = (panel.getAttribute('data-remove-routes') || '').split(/\s+/).filter(Boolean);
    if (!urls.length || confirmBtn.disabled) return;
    var err = panel.querySelector('[data-pin-error]');
    if (err) { err.textContent = ''; err.hidden = true; }
    if (!pin) { showError(panel, null); if (input) input.focus(); return; }
    confirmBtn.disabled = true;
    postForm('/api/sync/tills/verify-pin', pin).then(function (r) {
      if (!r.ok) {
        return r.text().then(function (t) {
          showError(panel, (t || '').trim());
          if (input) { input.value = ''; input.focus(); }
          confirmBtn.disabled = false;
        });
      }
      var failed = false;
      return urls.reduce(function (p, u) {
        return p.then(function () {
          return postForm(u, pin).then(function (rr) { if (!rr.ok) failed = true; }).catch(function () { failed = true; });
        });
      }, Promise.resolve()).then(function () {
        confirmBtn.disabled = false;
        var text = failed ? panel.getAttribute('data-failed-text') : panel.getAttribute('data-done-text');
        say(panel, text || '');
        closePanel(panel, false);
        document.body.dispatchEvent(new CustomEvent('tills-changed', { bubbles: true }));
      });
    }).catch(function () {
      showError(panel, null);
      confirmBtn.disabled = false;
    });
  }
  document.addEventListener('click', function (ev) {
    var t = ev.target.closest ? ev.target : null;
    if (!t) return;
    var opener = t.closest('[data-pin-open]');
    if (opener) { ev.preventDefault(); var p = panelOf(opener); if (p && !p.hidden) closePanel(p, true); else openPanel(opener); return; }
    var cancel = t.closest('[data-pin-cancel]');
    if (cancel) { ev.preventDefault(); closePanel(cancel.closest('[data-pin-panel]'), true); return; }
    var ok = t.closest('[data-pin-confirm]');
    if (ok) { ev.preventDefault(); confirmPanel(ok.closest('[data-pin-panel]')); }
  });
  document.addEventListener('keydown', function (ev) {
    var panel = ev.target.closest ? ev.target.closest('[data-pin-panel]') : null;
    if (!panel) return;
    if (ev.key === 'Escape') { ev.preventDefault(); closePanel(panel, true); }
    else if (ev.key === 'Enter' && ev.target.hasAttribute('data-pin-input')) { ev.preventDefault(); confirmPanel(panel); }
  });
})();

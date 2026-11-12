/* ut-docs#5524: the till's own in-page confirm for hx-confirm.
 *
 * htmx fires `htmx:confirm` before every request that carries an hx-confirm
 * question and, unless a listener preventDefault()s it, asks with the
 * browser's window.confirm -- a native dialog that makes the status bar
 * inert, cannot be styled, and is unreachable in kiosk mode. This file takes
 * the event over and asks in #ut-confirm (base.html), a non-modal <dialog>
 * opened with .show() over the shared #ut-scrim (ut-docs#2097: the modal API would make the status bar inert).
 *
 * Fallback contract: if this script is blocked, or #ut-confirm is missing,
 * no listener runs and htmx's native confirm() still gates the request --
 * there is never a path that sends unconfirmed.
 *
 * Listener order: the one global listener is on `document` (bubble). The
 * jiggle-mode and order-type listeners in app.js are on document.body, so
 * they always run first and, when they take the event over, set
 * defaultPrevented -- which makes this listener skip: exactly one prompt.
 * Do not move it to body or to the capture phase.
 *
 * No user-facing strings here: the question comes from the opener (already
 * translated in its template) and the button labels from the shell.
 */
(function () {
  'use strict';

  var ARM_MS = 350; // Confirm ignores taps this long after opening (double-tap guard)
  var pending = null; // { resolve, opener }
  var armTimer = 0;

  function dialog() { return document.getElementById('ut-confirm'); }
  function part(dlg, which) { return dlg.querySelector('[data-ut-confirm="' + which + '"]'); }
  function focusIt(el) {
    try { if (el && el.focus) el.focus({ preventScroll: true }); } catch (e) { /* ignore */ }
  }

  function settle(answer) {
    var p = pending;
    if (!p) return; // a second tap / the close event after settle: no-op
    pending = null;
    clearTimeout(armTimer);
    var dlg = dialog();
    if (dlg) {
      dlg.removeAttribute('data-armed');
      try { if (dlg.open) dlg.close(); } catch (e) { /* ignore */ }
    }
    // Focus goes back to the opener BEFORE the caller issues the confirmed
    // request: inline-actions.js captures document.activeElement at
    // htmx:beforeRequest so an elevation prompt returns focus to the real
    // opener, and the item form keeps focus across the swap (ut-docs#2727).
    var op = p.opener;
    if (op && op.isConnected && !op.disabled) focusIt(op);
    p.resolve(answer);
  }

  // UT.confirm(question, opts) -> Promise<boolean>
  //   opts.tone    'danger' (default) | 'neutral'
  //   opts.ok      already-translated Confirm label (else the shell's)
  //   opts.opener  element to give focus back to
  window.UT = window.UT || {};
  window.UT.confirm = function (question, opts) {
    opts = opts || {};
    var dlg = dialog();
    if (!dlg) return Promise.resolve(window.confirm(String(question))); // native-confirm:allow no #ut-confirm shell: native fallback keeps "never sends unconfirmed" (ut-docs#5524)
    wire();
    if (pending) return Promise.resolve(false); // one question at a time
    return new Promise(function (resolve) {
      var neutral = opts.tone === 'neutral';
      var okBtn = part(dlg, 'ok');
      var cancelBtn = part(dlg, 'cancel');
      pending = { resolve: resolve, opener: opts.opener || null };
      dlg.querySelector('#ut-confirm-msg').textContent = String(question);
      okBtn.classList.toggle('danger', !neutral);
      okBtn.classList.toggle('primary', neutral);
      okBtn.textContent = opts.ok || okBtn.getAttribute('data-default-label') || okBtn.textContent;
      dlg.removeAttribute('data-armed');
      dlg.show();
      focusIt(neutral ? okBtn : cancelBtn);
      clearTimeout(armTimer);
      armTimer = setTimeout(function () {
        if (pending) dlg.setAttribute('data-armed', '');
      }, ARM_MS);
    });
  };
  window.UT.confirmOpen = function () { return !!pending; };

  function wire() {
    var dlg = dialog();
    if (!dlg || dlg.getAttribute('data-ut-confirm-wired')) return;
    dlg.setAttribute('data-ut-confirm-wired', '1');
    dlg.addEventListener('click', function (ev) {
      var b = ev.target && ev.target.closest ? ev.target.closest('[data-ut-confirm]') : null;
      if (!b) return;
      if (b.getAttribute('data-ut-confirm') === 'cancel') { settle(false); return; }
      if (!dlg.hasAttribute('data-armed')) return; // too early: a double-tap, not an answer
      settle(true);
    });
    dlg.addEventListener('keydown', function (ev) {
      if (ev.key === 'Escape' || ev.key === 'Esc') {
        ev.preventDefault(); ev.stopPropagation(); settle(false);
        return;
      }
      // A held key must not answer: only a fresh keydown activates a button.
      if (ev.repeat && (ev.key === 'Enter' || ev.key === ' ' || ev.key === 'Spacebar')) ev.preventDefault();
    });
    // Anything else that closed it (a swap, UT.closeAll, the lock screen):
    // treat as Cancel so the promise always resolves. The close event is
    // queued, so it can arrive after settle() already reopened the dialog for
    // a question chained in .then(): a dialog that is open again is that new
    // question, never cancelled by the old close.
    dlg.addEventListener('close', function () { if (!dlg.open) settle(false); });
  }
  wire(); // deferred script: the shell's dialog is already parsed

  document.addEventListener('htmx:confirm', function (e) {
    var d = e.detail || {};
    if (e.defaultPrevented || !d.question) return; // not ours / already taken over
    if (!dialog()) return; // native fallback
    e.preventDefault(); // htmx stops here; we issue the request on Confirm
    if (pending) return; // double-tap on the opener: swallow, one dialog
    var elt = d.elt;
    var toneEl = elt && elt.closest ? elt.closest('[data-confirm-tone]') : null;
    var okEl = elt && elt.closest ? elt.closest('[data-confirm-ok]') : null;
    // The focused element is the opener only when it belongs to this request
    // (a button inside the hx-confirm carrier); focus left in a search box
    // must not get focus back on Cancel or steer the poll guard.
    var ae = document.activeElement;
    var opener = (ae && ae !== document.body && elt && elt.contains && elt.contains(ae)) ? ae
      : ((d.triggeringEvent && d.triggeringEvent.submitter) || elt);
    window.UT.confirm(d.question, {
      tone: toneEl && toneEl.getAttribute('data-confirm-tone') === 'neutral' ? 'neutral' : 'danger',
      ok: okEl ? okEl.getAttribute('data-confirm-ok') : '',
      opener: opener
    }).then(function (ok) {
      if (!ok) return;
      // The opener was swapped away while the operator read the question
      // (htmx would silently drop the request anyway): the row changed under
      // them, so the destructive action is not sent.
      if (!elt || !elt.isConnected) return;
      d.issueRequest(true); // true = already confirmed; never re-asks natively
    });
  });

  // While a question is open, a region refresh whose swap target contains the
  // opener (the Tills roster polls every 10 s) would detach the very button
  // being confirmed. Hold such a request; a poll retries on its next tick.
  document.addEventListener('htmx:beforeRequest', function (e) {
    var p = pending;
    if (!p || !p.opener) return;
    var t = e.detail && e.detail.target;
    if (t && t !== document.body && t.contains && t.contains(p.opener)) e.preventDefault();
  });
})();

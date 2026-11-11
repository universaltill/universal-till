/* F11 toggles the desktop shell's live window mode, fullscreen <-> normal
 * (ut-docs#3735, ADR-0064 channel).
 *
 * Inert unless <body data-window-toggle> is present — the server renders it
 * only for a manager, on the till itself, while a live desktop shell is
 * attached. Everywhere else (plain browser, cashier, self-order, Pi kiosk)
 * the key is left alone, so the browser keeps its own F11.
 *
 * The server is the authority: it reads the live mode, never enters or
 * leaves kiosk (that stays behind the Exit-to-OS PIN) and answers
 * applied:false when there is nothing to do. No dialogs: a kiosk-locked
 * answer shows a status message in the page's #pos-alert strip.
 */
(function () {
  'use strict';
  var URL = '/api/settings/window-mode/toggle';
  var DEBOUNCE_MS = 400;
  var last = 0;

  function showChip(text) {
    var box = document.getElementById('pos-alert');
    if (!box || !text) return;
    delete box.dataset.sticky;
    box.hidden = false; // unhide before the text changes so aria-live announces it
    var span = box.querySelector('.notice-text');
    if (span) span.textContent = text;
    // #pos-alert sits at the top of the document: on a scrolled page the chip
    // would be off-screen (ut-docs#5298, same call as app.js). No smooth scroll.
    try { box.scrollIntoView({ block: 'nearest' }); } catch (e) { /* best effort */ }
  }

  document.addEventListener('keydown', function (e) {
    if (e.key !== 'F11') return;
    var body = document.body;
    if (!body || body.getAttribute('data-window-toggle') !== '1') return;
    if (e.ctrlKey || e.altKey || e.shiftKey || e.metaKey || e.isComposing) return;
    e.preventDefault();
    if (e.repeat) return;
    var now = Date.now();
    if (now - last < DEBOUNCE_MS) return;
    last = now;
    fetch(URL, {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'HX-Request': 'true' }
    }).then(function (res) {
      return res.ok ? res.json() : null;
    }).then(function (j) {
      if (j && j.data && j.data.reason === 'kiosk_locked') {
        showChip(body.getAttribute('data-f11-kiosk-locked'));
      }
    }).catch(function () { /* offline/server hiccup: leave the window as is */ });
  }, true);
})();

// Hide the mouse cursor on touch-first tills (ut-docs#155): a kiosk till
// should not show a pointer arrow sitting over the sale screen. Deliberately
// NOT tied to osk.js (whose mode 'off' bails out entirely) — cursor hiding
// applies whenever the device looks touch-capable, same heuristic as the
// OSK's. A REAL mouse movement brings the cursor back (a kiosk Pi with a
// plugged-in mouse stays usable); the next touch hides it again. The
// "mouse in use" decision persists across navigations (sessionStorage) so
// the cursor doesn't vanish on every page load of a mouse-driven session.
(function () {
  'use strict';

  var touchy = window.matchMedia('(pointer: coarse)').matches ||
    window.matchMedia('(any-pointer: coarse)').matches ||
    (navigator.maxTouchPoints || 0) > 0 || ('ontouchstart' in window);
  if (!touchy) return;

  var KEY = 'ut-cursor-mouse';
  function flag(v) {
    try {
      if (v === undefined) return sessionStorage.getItem(KEY) === '1';
      if (v) sessionStorage.setItem(KEY, '1');
      else sessionStorage.removeItem(KEY);
    } catch (e) { return false; } // storage blocked: fall back to per-page
  }

  var root = document.documentElement;
  if (!flag()) root.classList.add('cursor-hidden');
  window.addEventListener('pointermove', function (ev) {
    if (ev.pointerType === 'mouse') {
      root.classList.remove('cursor-hidden');
      flag(true);
    }
  }, { passive: true });
  window.addEventListener('touchstart', function () {
    root.classList.add('cursor-hidden');
    flag(false);
  }, { passive: true });
})();

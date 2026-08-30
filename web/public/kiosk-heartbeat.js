// Kiosk input-liveness heartbeat (ut-docs#1329, split from #1228's Pi5-1
// input-freeze incident) — a throttled signal that real user input is still
// reaching this page, relayed to unitill-desktop's control channel so an
// on-demand snapshot (cmd/unitill-desktop's GET /snapshot) has something to
// report the next time a till strands to all input. No self-recovery here
// — that's the sibling watchdog card — this is diagnosability only.
//
// Loaded unconditionally on the till's own POS pages (base.html) and its
// two standalone boot screens (login.html, setup.html) — NOT gated on
// {{ kiosk }}, see base.html's own comment for why that template func is
// the wrong signal here. Costs nothing where there's no live shell control
// channel to relay to: the server-side handler (registerKioskHeartbeat)
// already no-ops safely via Deps.WindowCtl.
(function () {
  'use strict';

  // Long enough that a burst of taps (a whole checkout) sends one request,
  // not dozens; short enough that a freeze starting seconds after the last
  // real tap still shows up as a small last_input_age_ms on the snapshot
  // rather than minutes of silence looking identical to "already frozen".
  var THROTTLE_MS = 15000;
  var lastSent = 0;

  function sendHeartbeat() {
    var now = Date.now();
    if (now - lastSent < THROTTLE_MS) return;
    lastSent = now;
    // Offline-first (ADR-0003): this must never throw, retry, or queue —
    // a missed heartbeat is just a slightly longer last_input_age_ms next
    // time, never a blocked or slowed-down tap. keepalive lets the request
    // outlive a page navigation the same tap triggered.
    try {
      fetch('/api/kiosk/input-heartbeat', { method: 'POST', keepalive: true }).catch(function () {});
    } catch (e) {
      /* fetch itself throwing (e.g. disabled in a locked-down webview) is
         exactly as harmless as a network failure — nothing to recover. */
    }
  }

  ['pointerdown', 'touchstart', 'keydown'].forEach(function (type) {
    document.addEventListener(type, sendHeartbeat, { passive: true, capture: true });
  });
})();

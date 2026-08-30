// Input-liveness heartbeat (ut-docs#1329, split from #1228): a future kiosk
// input-freeze (till stops responding to all touch input while backend/app
// internals stay healthy — see the incident report) should leave a
// diagnosable trail, not another anecdote. This listens for genuine user
// input anywhere on a base.html-rendered page and tells the server "input
// is still reaching this page right now"; the server forwards it on to
// unitill-desktop's own control channel (see
// internal/pages/window_state_api.go and
// internal/pages/common/http_window_controller.go), which records it for
// GET /diagnostics and logs a resume line after a gap.
//
// Deliberately diagnosability-only — no self-recovery/auto-restart logic
// lives here or anywhere else in this card. Just a heartbeat.
//
// Scope (ut-docs#1329): wired from base.html only, the shared layout most
// pages render through (including Settings, where the #1228 freeze was
// observed). Five standalone documents do NOT load this script — login.html,
// setup.html, self_order.html, self_order_shop.html, and order_tracking.html
// (the last is deliberate beyond just "standalone": it's an anonymous
// customer's own phone, not till hardware this card cares about). That is a
// deliberate, documented gap for this card, not an oversight (see the
// card's PR body). A future card can extend coverage there if ever needed.
(function () {
  'use strict';

  var THROTTLE_MS = 5000;
  // FETCH_TIMEOUT_MS bounds one heartbeat POST (review of ut-docs#1329,
  // should-fix 6): without it, a wedged/unreachable server — precisely
  // the condition this diagnostic exists to catch — leaves the request
  // pending forever with no way to distinguish "still sending" from
  // "silently dead", permanently going dark instead of just missing one
  // beat. AbortSignal.timeout is a same-origin fetch abort, never
  // surfaced anywhere the operator could see it.
  var FETCH_TIMEOUT_MS = 4000;
  var lastSent = 0;

  function sendHeartbeat() {
    var now = Date.now();
    // Leading-edge throttle: fire immediately on the first event, then
    // ignore further events until the window elapses — never delay the
    // first signal (a debounce would mean the very first touch after a
    // freeze never gets reported until the user stops touching). Timing
    // alone already caps concurrency to ~1 in-flight request per
    // THROTTLE_MS, so no separate in-flight latch is needed (and one
    // would risk going permanently dark on a request that never settles).
    if (now - lastSent < THROTTLE_MS) return;
    lastSent = now;
    // Best-effort telemetry: a failure here must never surface to the
    // operator or affect the page in any way, so every error path is
    // silently swallowed. No credentials/body needed — the session cookie
    // already rides along on a same-origin fetch.
    fetch('/api/window/input-heartbeat', { method: 'POST', signal: AbortSignal.timeout(FETCH_TIMEOUT_MS) })
      .catch(function () { /* ignore — best-effort telemetry */ });
  }

  // Capture phase on document: sees every genuine pointer/touch/key event
  // on the page regardless of what element handles (or stops propagation
  // on) it. passive: true — this never calls preventDefault and must never
  // slow down or block the input it's observing.
  document.addEventListener('pointerdown', sendHeartbeat, { capture: true, passive: true });
  document.addEventListener('touchstart', sendHeartbeat, { capture: true, passive: true });
  document.addEventListener('keydown', sendHeartbeat, { capture: true, passive: true });
})();

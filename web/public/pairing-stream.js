/* Pairing push (ut-docs#2743, ADR-0114 §2 -> ADR-0079 §1). One EventSource per
   page, opened only when the pairing-notice fragment rendered the
   data-pairing-stream marker (authorised manager on a primary; self-order
   customer pages never load this file). A nudge carries no data: it only
   fires the existing `tills-changed` body event, so the fragments re-fetch
   through their one render path. The 30s polls stay as the fallback; errors
   are silent (no banner, no toast, no modal). */
(function () {
  /* A refused stream (403/204/503/401) is retried at most once per notice
     poll interval — sync() also runs on every htmx swap, which alone would
     re-request a refused stream several times a poll (review F1). */
  var RETRY_MS = 30000;
  var es = null;
  var opened = false;
  var catchUp = false; /* nudge once the next open succeeds (closed while hidden) */
  var retryAt = 0;

  /* True while a manager is mid-approval in #pending-pairings: focus inside,
     a typed PIN, or a role changed from its rendered default. An outerHTML
     swap would wipe it, so the 30s poll and the push both skip while busy
     and catch up on the next tick (UX condition, ut-docs#2743). */
  window.utPairingBusy = function () {
    var el = document.getElementById('pending-pairings');
    if (!el) return false;
    if (el.matches(':focus-within')) return true;
    var pins = el.querySelectorAll('input[name=manager_pin]');
    for (var i = 0; i < pins.length; i++) if (pins[i].value) return true;
    var sels = el.querySelectorAll('select[name=role]');
    for (var j = 0; j < sels.length; j++) {
      for (var k = 0; k < sels[j].options.length; k++) {
        if (sels[j].options[k].selected !== sels[j].options[k].defaultSelected) return true;
      }
    }
    return false;
  };

  function nudge() {
    if (window.htmx) window.htmx.trigger(document.body, 'tills-changed');
  }

  /* A display board (/orders, /kitchen-display/<id>) stays up on an idle
     manager session that auth narrows to board-only requests. Any other
     request, this stream included, would revoke that session, so the board
     never opens the stream. Mirrors auth.displayBoardPage. */
  function displayBoardPage() {
    var p = window.location.pathname;
    if (p === '/orders') return true;
    var rest = p.indexOf('/kitchen-display/') === 0 ? p.slice('/kitchen-display/'.length) : '';
    return rest !== '' && rest.indexOf('/') === -1;
  }

  function sync() {
    var marker = document.querySelector('[data-pairing-stream]');
    var want = !!marker && !!window.EventSource && !displayBoardPage() &&
      document.visibilityState === 'visible';
    if (want && !es && Date.now() >= retryAt) {
      es = new EventSource(marker.getAttribute('data-pairing-stream'));
      es.addEventListener('pairing', nudge);
      es.onopen = function () {
        /* catch up on anything missed while disconnected or hidden */
        if (opened || catchUp) nudge();
        opened = true;
        catchUp = false;
      };
      es.onerror = function () {
        /* CLOSED = the browser gave up: drop it and wait RETRY_MS before the
           next attempt. CONNECTING = a transient drop the browser retries. */
        if (es && es.readyState === 2) {
          es = null;
          opened = false;
          retryAt = Date.now() + RETRY_MS;
        }
      };
    } else if (!want && es) {
      es.close();
      es = null;
      catchUp = catchUp || opened;
      opened = false;
    }
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', sync);
  else sync();
  document.addEventListener('htmx:afterSettle', sync);
  document.addEventListener('visibilitychange', sync);
})();

/* Manage-tills roster refresh (ut-docs#7097, ut-docs#7057): the roster polls
   every 10 s, but only the live status cells (link, version, last seen,
   marked data-live-cell) are updated in place. The rows, their buttons and
   anything the manager has open are never re-rendered. When a till was added
   or removed (the id list changed) the whole table is swapped, unless the
   manager is busy in it; then the swap waits for the next tick. */
(function () {
  function ids(root) {
    return Array.prototype.map.call(root.querySelectorAll('tr[data-till-id]'), function (r) {
      return r.getAttribute('data-till-id');
    }).join('|');
  }
  document.addEventListener('htmx:beforeSwap', function (e) {
    var d = e.detail;
    var target = d && d.target;
    if (!target || target.id !== 'tills-roster' || typeof d.serverResponse !== 'string') return;
    var next = document.createElement('div');
    next.innerHTML = d.serverResponse;
    if (ids(next) === ids(target)) {
      d.shouldSwap = false;
      Array.prototype.forEach.call(target.querySelectorAll('tr[data-till-id]'), function (row) {
        var id = row.getAttribute('data-till-id');
        var fresh = null;
        Array.prototype.forEach.call(next.querySelectorAll('tr[data-till-id]'), function (r) {
          if (r.getAttribute('data-till-id') === id) fresh = r;
        });
        if (!fresh) return;
        ['data-link'].forEach(function (a) {
          if (fresh.hasAttribute(a)) row.setAttribute(a, fresh.getAttribute(a));
        });
        var cur = row.querySelectorAll('[data-live-cell]');
        var upd = fresh.querySelectorAll('[data-live-cell]');
        for (var i = 0; i < cur.length && i < upd.length; i++) {
          if (cur[i].innerHTML !== upd[i].innerHTML) cur[i].innerHTML = upd[i].innerHTML;
        }
      });
      return;
    }
    var busy = target.matches(':focus-within') || (window.utPairingBusy && window.utPairingBusy());
    if (busy) d.shouldSwap = false;
  });
})();

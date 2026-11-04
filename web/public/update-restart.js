// ut-docs#5438: the ONE "wait for the till to restart into the new version"
// poll, shared by the status-bar update chip (base.html #sb-update-btn) and
// the Settings "Install from a file" form (the ut:update-installing event).
// Before this it lived inline in the chip's IIFE, which only renders when an
// online update is available - a till with no internet never has one.
//
//   window.utUpdateRestart.watch(targetVersion, { onGiveUp: function (pending) {} })
//
// Polls GET /api/update/status every 2s and reloads (UT.reload) once
// running_version === target. A 4xx or a redirect (signed out across the
// restart, or a server without this endpoint) reloads too; a 5xx or network
// error means "still restarting". After 90 tries it stops and calls
// onGiveUp(pending), pending being true when the old process answered but
// the version never changed. No user-facing text lives here: callers own it.
//
// ut-docs#2759: poll /api/update/status, not /healthz - the OLD process
// answers /healthz too, so a restart that never happened used to "succeed"
// with a reload onto the old build.
(function () {
  'use strict';
  function watch(target, opts) {
    var onGiveUp = (opts && opts.onGiveUp) || function () {};
    var tries = 0, pending = false;
    var iv = setInterval(function () {
      tries++;
      fetch('/api/update/status', { cache: 'no-store' }).then(function (r) {
        if (r.redirected || (r.status >= 400 && r.status < 500)) {
          clearInterval(iv); UT.reload('update-restarted');
          return null;
        }
        return r.ok ? r.json() : null;
      }).then(function (res) {
        var st = res && res.data;
        if (!st) return;
        if (st.running_version === target) { clearInterval(iv); UT.reload('update-restarted'); return; }
        pending = true;
      }).catch(function () {});
      if (tries > 90) {
        clearInterval(iv);
        onGiveUp(pending);
      }
    }, 2000);
  }
  window.utUpdateRestart = { watch: watch };
})();

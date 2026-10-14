// ut-docs#4312: the ONE Android in-app update action, shared by every surface
// that starts it — the status-bar chip (base.html #sb-android-update-btn), the
// Settings form (settings.html #android-update-form) and the "Check for
// updates" result's button (update_api.go updateUnavailableHTML,
// [data-android-update-install]). Before this, each surface had its own copy
// of the flow, none knew about the others' download, and the check result was
// a link to the page the operator was already on.
//
// Two halves:
//
// 1. utAndroidUpdater.install(pin) — POST /api/update/android-install (the
//    server's gate: a manager session, or the manager PIN; refuses when the
//    till is already current), then window.AndroidKiosk.installUpdate(). The
//    bridge takes no URL — the native shell resolves the release APK itself —
//    so nothing here chooses what gets installed. Resolves to an outcome
//    string: started | needs_pin | wrong_pin | locked | failed | uptodate |
//    nobridge. Each surface maps it to its own translated data-* text.
//
// 2. window.utAndroidUpdate(state) — called BY the native shell
//    (MainActivity.reportUpdate, evaluateJavascript) on every transition and
//    once a second while downloading: {state: downloading|ready|installing|
//    failed, percent, error}. Re-broadcast as a `ut:android-update` document
//    event; utAndroidUpdater.text(el, state) turns it into the element's own
//    data-progress / data-downloading / data-installing / data-retry text.
//
// Deferred shell-head script (httpx.HeadAssets): loaded once per document,
// survives boosted navigations; a no-op on any till without the bridge.
(function () {
  'use strict';
  var last = null;
  var lastAt = 0;

  window.utAndroidUpdate = function (state) {
    if (!state || typeof state !== 'object') return;
    last = state;
    lastAt = Date.now();
    try {
      document.dispatchEvent(new CustomEvent('ut:android-update', { detail: state }));
    } catch (e) { /* very old WebView: progress is cosmetic */ }
  };

  // A download is running when the shell reported "downloading" within the
  // last few seconds (it reports every second while it does).
  function busy() {
    return !!last && last.state === 'downloading' && Date.now() - lastAt < 5000;
  }

  function install(pin) {
    if (busy()) {
      // A second tap during a download must not restart it (the shell would
      // not either — this just skips a pointless network round trip).
      return Promise.resolve('started');
    }
    return fetch('/api/update/android-install', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams(pin ? { manager_pin: pin } : {}).toString()
    }).then(function (res) {
      // 403: this caller needs the PIN (a cashier, or a self-order kiosk where
      // the session is not trusted), or the PIN given was wrong.
      if (res.status === 403) return pin ? 'wrong_pin' : 'needs_pin';
      if (res.status === 429) return 'locked';
      if (!res.ok) return 'failed';
      // ut-docs#1545: a 200 is NOT "go ahead and download". The endpoint's
      // freshness re-check answers { data: { already_current: true } } with
      // status 200 (the { data, error } envelope), because the template's
      // `updateavailable` comes from a check up to 24h stale. Treating any 200
      // as authorisation downloaded the running build again.
      return res.json().catch(function () { return {}; }).then(function (payload) {
        var data = payload && payload.data;
        if (data && data.already_current) return 'uptodate';
        if (window.AndroidKiosk && window.AndroidKiosk.installUpdate) {
          window.AndroidKiosk.installUpdate();
          return 'started';
        }
        return 'nobridge';
      });
    }).catch(function () { return 'failed'; });
  }

  // The element's own translated text for a native state. Progress strings are
  // Go-style ("Downloading update… %d%%") so guard-i18n checks their verbs.
  function text(el, s) {
    if (!el || !s) return '';
    var attr = function (k) { return el.getAttribute('data-' + k) || ''; };
    if (s.state === 'failed') return attr('retry');
    if (s.state === 'ready' || s.state === 'installing') return attr('installing');
    if (typeof s.percent === 'number' && attr('progress')) {
      return attr('progress').replace('%d', String(s.percent)).replace('%%', '%');
    }
    return attr('downloading');
  }

  window.utAndroidUpdater = {
    install: install,
    text: text,
    busy: busy,
    state: function () { return last; }
  };

  // The "Check for updates" result's button. On /settings the install form is
  // usually on the page already: drive IT, so the PIN prompt and messages are
  // the form's own. The form is absent when the page rendered before the check
  // found this update (its render-time `updateavailable` was false), so then
  // run the same flow here, on the button itself.
  document.addEventListener('click', function (e) {
    var b = e.target && e.target.closest && e.target.closest('[data-android-update-install]');
    if (!b) return;
    e.preventDefault();
    var form = document.getElementById('android-update-form');
    if (form) {
      form.scrollIntoView({ block: 'center' });
      if (form.requestSubmit) { form.requestSubmit(); } else { form.dispatchEvent(new Event('submit', { cancelable: true })); }
      return;
    }
    b.disabled = true;
    b.textContent = b.getAttribute('data-downloading') || '';
    install('').then(function (outcome) {
      if (outcome === 'started') return; // progress arrives via the event below
      if (outcome === 'needs_pin' || outcome === 'wrong_pin') {
        // Reload Settings so the PIN form renders (the check has updated the
        // server's cache by now). Same path: assigning the same URL would be a
        // fragment-only no-op, so reload explicitly.
        if (window.location.pathname === '/settings') {
          window.location.hash = 'android-update';
          window.location.reload();
        } else {
          window.location.href = '/settings#android-update';
        }
        return;
      }
      b.disabled = false;
      var key = { locked: 'locked', uptodate: 'uptodate', nobridge: 'nobridge' }[outcome] || 'failed';
      b.textContent = b.getAttribute('data-' + key) || '';
    });
  });

  document.addEventListener('ut:android-update', function (e) {
    var s = e.detail;
    var buttons = document.querySelectorAll('[data-android-update-install]');
    for (var i = 0; i < buttons.length; i++) {
      buttons[i].textContent = text(buttons[i], s);
      // Failed: tap to retry. Otherwise it stays disabled until Android's
      // installer takes over the screen.
      buttons[i].disabled = s.state !== 'failed';
    }
  });

  // Back from Android's installer (or from its "allow this app to install
  // apps" settings, on the first install of a build): the operator may have
  // cancelled, so the button must be tappable again.
  document.addEventListener('visibilitychange', function () {
    if (document.visibilityState !== 'visible' || !last) return;
    if (last.state !== 'installing' && last.state !== 'ready') return;
    var buttons = document.querySelectorAll('[data-android-update-install]');
    for (var i = 0; i < buttons.length; i++) { buttons[i].disabled = false; }
  });
})();

// ut-docs#5620: Settings -> Display "Keep the screen on" (Android shell only).
// The native shell keeps the display on while the till is in front and lets the
// OS sleep it after the chosen idle minutes (0 = never). No bridge (browser,
// Pi, desktop) -> the form stays hidden. The value is per device, held by the
// app; the bridge only accepts the offered choices.
(function () {
  'use strict';
  var form = document.getElementById('screen-awake-form');
  var sel = document.getElementById('screen-awake-select');
  var bridge = window.AndroidKiosk;
  if (!form || !sel || !bridge || typeof bridge.getScreenAwakeMinutes !== 'function') return;
  try { sel.value = String(bridge.getScreenAwakeMinutes()); } catch (e) { return; }
  form.hidden = false;
  sel.addEventListener('change', function () {
    var msg = document.getElementById('screen-awake-msg');
    try {
      var applied = bridge.setScreenAwakeMinutes(parseInt(sel.value, 10));
      sel.value = String(applied);
      if (msg) msg.textContent = '\u2713';
    } catch (e) {
      if (msg) msg.textContent = '\u2717';
    }
  });
})();

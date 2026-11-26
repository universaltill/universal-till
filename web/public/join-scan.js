// "Scan QR code" on the join forms (ut-docs#3428): the setup wizard's
// "Enter a code" tab and the Tills page. Loaded on both (the wizard is a
// standalone document, so it loads qr-scan.js + this file itself).
//
// Fill-then-confirm (UX verdict): a valid scan closes the overlay, fills the
// existing code field and shows the main till's host. It NEVER submits: the
// person adds a till name and presses Join, so the till-name check and the
// replace-data checkbox run unchanged and a mis-scan does not burn the
// one-time token. No route, no new field; the server stays the authority.
//
// SECURITY: the scanned code is a one-time pairing token. It goes into the
// code field's value and nowhere else: not logged, not in the URL, not in a
// data-* attribute, not in visible text (the chip shows the host only), not
// in web storage. No console output in this file.
//
// Markup contract (ids share one prefix per page, see setup.html/tills.html):
//   <button data-join-scan id=P-join-scan> ... overlay id=P-join-scan-overlay
//   with data-msg-*; elements P-join-scan-{video,status,close,type,clear,next},
//   P-join-scanned (chip, with [data-join-scanned-text]).
(function () {
  'use strict';
  var active = null; // { overlay, session, btn }

  function byId(id) { return document.getElementById(id); }

  function parts(btn) {
    var p = btn.id;
    var form = btn.closest('form');
    return {
      form: form,
      overlay: byId(p + '-overlay'),
      video: byId(p + '-video'),
      status: byId(p + '-status'),
      closeBtn: byId(p + '-close'),
      chip: byId(p.replace(/-scan$/, '-scanned')),
      next: byId(p + '-next'),
      code: form && form.querySelector('input[name=code]'),
      address: form && form.querySelector('input[name=address]')
    };
  }

  function say(el, text) { if (el) el.textContent = text || ''; }

  function stop(restoreFocus) {
    if (!active) return;
    var a = active;
    active = null;
    a.session.close();
    a.overlay.hidden = true;
    if (restoreFocus) a.btn.focus();
  }

  // The name field is the first id in the form's hx-include (the shared
  // till-name input); fall back to any input[name=name].
  function nameField(form) {
    var inc = (form.getAttribute('hx-include') || '').split(',')[0].trim();
    var el = inc ? document.querySelector(inc) : null;
    return el || document.querySelector('input[name=name]');
  }

  function showChip(p, msgs, host) {
    var chip = p.chip;
    if (!chip) return;
    var holder = chip.querySelector('[data-join-scanned-text]');
    holder.textContent = '';
    var tpl = msgs.msgScannedFrom || '%s';
    var at = tpl.indexOf('%s');
    var before = at < 0 ? tpl : tpl.slice(0, at);
    var after = at < 0 ? '' : tpl.slice(at + 2);
    holder.appendChild(document.createTextNode(before));
    var bdi = document.createElement('bdi'); // the host stays left-to-right inside an RTL sentence
    bdi.setAttribute('dir', 'ltr');
    bdi.textContent = host;
    holder.appendChild(bdi);
    holder.appendChild(document.createTextNode(after));
    chip.hidden = false;
  }

  function filled(btn, p, msgs, r) {
    p.code.value = r.code;
    p.code.readOnly = true;
    p.code.setAttribute('data-scanned', '');
    if (p.address) p.address.value = ''; // the code carries the address
    showChip(p, msgs, r.host);
    say(p.next, msgs.msgScannedNext);
    var name = nameField(p.form);
    if (name && !name.value.trim()) { name.focus(); return; }
    var join = p.form.querySelector('button[type=submit]');
    if (join) join.focus();
  }

  function clear(btn) {
    var p = parts(btn);
    if (p.code) {
      p.code.value = '';
      p.code.readOnly = false;
      p.code.removeAttribute('data-scanned');
    }
    if (p.chip) p.chip.hidden = true;
    say(p.next, '');
  }

  function open(btn) {
    var p = parts(btn);
    if (!p.overlay || !p.code || active) return;
    var msgs = p.overlay.dataset;
    p.overlay.hidden = false;
    say(p.status, msgs.msgScanning);
    if (p.closeBtn) p.closeBtn.focus();
    var text = {
      scanning: msgs.msgScanning,
      permission_denied: msgs.msgPermissionDenied,
      no_camera: msgs.msgNoCamera,
      busy: msgs.msgBusy,
      not_ut_code: msgs.msgNotUt
    };
    var entry = { overlay: p.overlay, btn: btn, session: null };
    active = entry;
    if (!window.utQrScan) { say(p.status, text.no_camera); entry.session = { close: function () {} }; return; }
    entry.session = window.utQrScan.open({
      video: p.video,
      formats: ['qr_code'],
      onStatus: function (kind) { say(p.status, text[kind]); },
      onCode: function (r) {
        // The session closed itself (camera released) before this call.
        active = null;
        p.overlay.hidden = true;
        filled(btn, p, msgs, r);
      }
    });
  }

  document.addEventListener('click', function (ev) {
    var t = ev.target && ev.target.closest ? ev.target : null;
    if (!t) return;
    var scan = t.closest('[data-join-scan]');
    if (scan) { open(scan); return; }
    var clr = t.closest('[data-join-scan-clear]');
    if (clr) {
      var form = clr.closest('form');
      var b = form && form.querySelector('[data-join-scan]');
      if (b) { clear(b); b.focus(); }
      return;
    }
    if (active) {
      var base = active.btn.id;
      if (t.closest('#' + base + '-close')) { stop(true); return; }
      if (t.closest('#' + base + '-type')) {
        var codeEl = parts(active.btn).code;
        var b2 = active.btn;
        stop(false);
        clear(b2); // typing replaces whatever was scanned
        if (codeEl) codeEl.focus();
      }
    }
  });

  document.addEventListener('keydown', function (ev) {
    if (ev.key === 'Escape' && active) { ev.preventDefault(); stop(true); }
  });

  // qr-scan.js releases the camera on pagehide; make the overlay follow so it
  // is not left open over a restored page.
  window.addEventListener('pagehide', function () { stop(false); });
})();

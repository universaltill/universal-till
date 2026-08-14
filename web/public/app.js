// Currency metadata comes from <body data-currency-*> (set by base.html from
// the configured currency). Decimals drive the major<->minor conversion:
// GBP=2 (pence), IRR/IRT=0 (no subunit). Suffix currencies (rial/toman)
// render the word after the number.
window.utCurrency = (function(){
  var d = document.body ? document.body.dataset : {};
  var decimals = parseInt(d.currencyDecimals || '2', 10);
  if (isNaN(decimals) || decimals < 0) decimals = 2;
  var factor = Math.pow(10, decimals);
  var display = d.currencyDisplay || '£';
  var suffix = d.currencySuffix === '1';
  function formatMinor(units){
    var neg = units < 0; if (neg) units = -units;
    var major = Math.floor(units / factor);
    var num = major.toString().replace(/\B(?=(\d{3})+(?!\d))/g, ',');
    if (decimals > 0) num += '.' + String(units % factor).padStart(decimals, '0');
    if (neg) num = '-' + num;
    return suffix ? num + ' ' + display : display + num;
  }
  return {
    decimals: decimals, factor: factor, display: display, suffix: suffix,
    toMinor: function(v){
      var num = Number(String(v == null ? '' : v).trim());
      return isNaN(num) ? 0 : Math.round(num * factor);
    },
    toMajor: function(units){ return (units / factor).toFixed(decimals); },
    format: formatMinor
  };
})();

(function(){
  var buf = "";
  var last = 0;
  var timeout = null;
  function scanCodeInput(){
    var form = document.querySelector('form[action="/api/pos/scan"], form[hx-post="/api/pos/scan"]');
    return form ? form.querySelector('input[name="code"]') : null;
  }
  function submit(code, codeInput){
    var form = codeInput && codeInput.form;
    if(!form || !codeInput) return;
    codeInput.value = code;
    if (window.htmx) { window.htmx.trigger(form, 'submit'); } else { form.submit(); }
    setTimeout(function(){ codeInput.value = ""; }, 0);
  }
  // A wedge scanner "types" its scan as fast keystrokes into whatever has
  // focus (ut-docs#423) — e.g. the sale screen's products-search box, which
  // has no scan handling of its own. The buffer above already recognizes
  // and submits the scan regardless of focus; this only cleans up the
  // stray characters the scanner left behind in a non-scan field, so they
  // don't survive as a corrupted filter query (search box) or a corrupted
  // quantity (the scan row's own qty input — "1" plus a 13-digit barcode
  // rang up as twelve trillion units).
  //
  // It strips the scanned code as a SUFFIX rather than blanking the field,
  // so whatever the cashier had already typed there survives. If the code
  // isn't the field's trailing text (caret was mid-string, a number input
  // rejected the value, focus is on a checkbox, …) nothing is touched at
  // all. Never touches the real scan input.
  //
  // MIN_SCAN_CLEANUP guards the cleanup — NOT the submit — against the
  // buffer's false positives: one fast keystroke followed by Enter within
  // 100ms is enough for `buf` to look like a scan, which is exactly what a
  // human typing a query into the search box and hitting Enter produces.
  // Submitting that as a bogus barcode is pre-existing behaviour and harmless
  // (the item is simply not found); silently eating the last character of
  // what they typed would not be. Every real scan — EAN-8/13, UPC, Code-128
  // SKUs, 4-5 digit PLUs — clears this floor comfortably. Deliberately does
  // not gate the submit itself: that would risk breaking a till already
  // scanning short internal codes today.
  var MIN_SCAN_CLEANUP = 4;
  function clearStrayScanTarget(target, code, codeInput){
    if (!target || target === codeInput || !code) return;
    if (code.length < MIN_SCAN_CLEANUP) return;
    if (target.tagName !== 'INPUT' && target.tagName !== 'TEXTAREA') return;
    var v = target.value;
    if (typeof v !== 'string' || v.slice(-code.length) !== code) return;
    target.value = v.slice(0, v.length - code.length);
    target.dispatchEvent(new Event('input', { bubbles: true }));
  }
  window.addEventListener('keydown', function(e){
    if (e.isComposing || e.metaKey || e.ctrlKey || e.altKey) return;
    var now = Date.now();
    if (now - last > 100) { buf = ""; }
    last = now;
    if (e.key === 'Enter') {
      var codeInput = scanCodeInput();
      var code = buf;
      if (!code && codeInput && document.activeElement === codeInput) {
        code = (codeInput.value || '').trim();
      }
      if (code && codeInput) {
        e.preventDefault();
        clearStrayScanTarget(document.activeElement, code, codeInput);
        submit(code, codeInput);
      }
      buf = "";
      return;
    }
    if (e.key.length === 1) buf += e.key;
    clearTimeout(timeout);
    timeout = setTimeout(function(){ buf = ""; }, 300);
  });

  // Exposed so other on-page scan sources (ut-docs#548's camera scan) submit
  // through the exact same path a wedge scan does, rather than duplicating
  // scanCodeInput()/submit()'s form-lookup and focus-safe-clear logic.
  window.utScan = { input: scanCodeInput, submit: submit };
})();

(function(){
  function toMinor(value){
    return window.utCurrency.toMinor(value);
  }

  function escapeHtml(str){
    return String(str || '').replace(/[&<>"']/g, function(ch){
      switch (ch) {
        case '&': return '&amp;';
        case '<': return '&lt;';
        case '>': return '&gt;';
        case '"': return '&quot;';
        case "'": return '&#39;';
        default: return ch;
      }
    });
  }

  function ready(fn){
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', fn, { once: true });
    } else {
      fn();
    }
  }

  function initSplitTender(){
    var card = document.getElementById('split-tender-card');
    if (!card || card.dataset.bound === '1') {
      return;
    }
    card.dataset.bound = '1';

    var form = card.querySelector('#split-tender-form');
    var addBtn = card.querySelector('#split-tender-add');
    var submitBtn = card.querySelector('#split-tender-submit');
    var clearBtn = card.querySelector('#split-tender-clear');
    var fillBtn = card.querySelector('#split-tender-fill');
    var paymentsList = card.querySelector('#split-tender-payments');
    var statusEl = card.querySelector('#split-tender-status');

    if (!form || !addBtn || !submitBtn || !clearBtn || !paymentsList) {
      return;
    }

    var payments = [];
    function formatMoney(units){
      return window.utCurrency.format(units);
    }

    function setStatus(message, level){
      if (!statusEl) return;
      statusEl.textContent = message || '';
      statusEl.classList.remove('error', 'success', 'info');
      if (message) {
        statusEl.classList.add(level || 'info');
      }
    }

    function clearForm(){
      form.reset();
      var methodInput = form.querySelector('[name="method"]');
      if (methodInput) {
        if (methodInput.dataset && methodInput.dataset.default) {
          methodInput.value = methodInput.dataset.default;
        } else if (methodInput.tagName === 'SELECT' && methodInput.options.length) {
          methodInput.selectedIndex = 0;
        }
      }
      var changeInput = form.querySelector('input[name="change"]');
      if (changeInput) {
        changeInput.value = window.utCurrency.toMajor(0);
      }
    }

    function renderPayments(){
      if (!payments.length) {
        paymentsList.classList.add('empty');
        paymentsList.innerHTML = '<p>No pending payments yet.</p>';
        return;
      }
      paymentsList.classList.remove('empty');
      var html = payments.map(function(payment, idx){
        var net = payment.amount - (payment.change || 0);
        var details = formatMoney(net);
        if (payment.change) {
          details += ' (change ' + formatMoney(payment.change) + ')';
        }
        if (payment.reference) {
          details += '<br><small>' + escapeHtml(payment.reference) + '</small>';
        }
        return '<div class="payment-pill"><div><strong>' + escapeHtml(payment.method) + '</strong><div class="pill-meta">' + details + '</div></div><button type="button" class="pill-remove" data-remove-payment="' + idx + '">&times;</button></div>';
      }).join('');
      paymentsList.innerHTML = html;
    }

    function netPayments(){
      return payments.reduce(function(sum, payment){
        return sum + (payment.amount - (payment.change || 0));
      }, 0);
    }

    function basketTotal(){
      var basket = document.getElementById('basket');
      if (!basket) return 0;
      var totalEl = basket.querySelector('.total');
      if (!totalEl) return 0;
      // Rendered text may use localized digits (fa/ar) — the raw minor units
      // ride on a data attribute instead.
      var minor = parseInt(totalEl.getAttribute('data-minor') || '', 10);
      if (!isNaN(minor)) return minor;
      var text = (totalEl.textContent || '').replace(/[^0-9.,-]/g, '').replace(/,/g, '');
      var num = Number(text);
      if (!text || Number.isNaN(num)) return 0;
      return window.utCurrency.toMinor(num);
    }

    function addPayment(){
      var data = new FormData(form);
      var method = (data.get('method') || '').trim();
      if (!method) {
        setStatus('Select a payment method.', 'error');
        return false;
      }
      var amountMinor = toMinor(data.get('amount'));
      if (amountMinor <= 0) {
        setStatus('Amount must be greater than zero.', 'error');
        return false;
      }
      var changeMinor = toMinor(data.get('change'));
      if (changeMinor < 0) changeMinor = 0;
      if (changeMinor > amountMinor) {
        setStatus('Change cannot exceed amount.', 'error');
        return false;
      }
      var payment = { method: method, amount: amountMinor };
      if (changeMinor > 0) {
        payment.change = changeMinor;
      }
      var reference = (data.get('reference') || '').trim();
      if (reference) {
        payment.reference = reference;
      }
      payments.push(payment);
      renderPayments();
      clearForm();
      setStatus('Added ' + method + ' payment for ' + formatMoney(payment.amount - (payment.change || 0)) + '.', 'success');
      return true;
    }

    function fillRemaining(){
      var total = basketTotal();
      if (!total) {
        setStatus('Basket total unavailable. Scan an item first.', 'error');
        return;
      }
      var remaining = total - netPayments();
      if (remaining <= 0) {
        setStatus('Pending payments already cover the basket total.', 'info');
        return;
      }
      var amountInput = form.querySelector('input[name="amount"]');
      if (amountInput) {
        amountInput.value = window.utCurrency.toMajor(remaining);
        amountInput.focus();
      }
      var changeInput = form.querySelector('input[name="change"]');
      if (changeInput) {
        changeInput.value = window.utCurrency.toMajor(0);
      }
      setStatus('Filled with remaining balance (' + formatMoney(remaining) + ').', 'info');
    }

    async function submitPayments(){
      if (!payments.length) {
        var autoAdded = false;
        var amountInput = form.querySelector('input[name="amount"]');
        if (amountInput && amountInput.value) {
          autoAdded = addPayment();
        }
        if (!autoAdded) {
          setStatus('Add at least one payment before completing the sale.', 'error');
          return;
        }
      }

      submitBtn.disabled = true;
      setStatus('Submitting payments...', 'info');
      try {
        var response = await fetch('/api/pos/tender', {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
            'Accept': 'text/html'
          },
          body: JSON.stringify({ payments: payments, offline: offlineOverrideEnabled() || !navigator.onLine })
        });
        var text = await response.text();
        if (!response.ok) {
          setStatus(text || 'Payment failed. Please retry.', 'error');
          return;
        }
        var basket = document.getElementById('basket');
        if (basket) {
          basket.outerHTML = text;
        }
        payments = [];
        renderPayments();
        clearForm();
        setStatus('Sale completed.', 'success');
      } catch (err) {
        var msg = err && err.message ? err.message : 'Network error';
        setStatus(msg, 'error');
      } finally {
        submitBtn.disabled = false;
      }
    }

    addBtn.addEventListener('click', addPayment);
    clearBtn.addEventListener('click', function(){
      payments = [];
      renderPayments();
      clearForm();
      setStatus('Cleared pending payments.', 'info');
    });
    if (fillBtn) {
      fillBtn.addEventListener('click', fillRemaining);
    }
    submitBtn.addEventListener('click', submitPayments);
    paymentsList.addEventListener('click', function(e){
      var target = e.target.closest('[data-remove-payment]');
      if (!target) return;
      var idx = Number(target.getAttribute('data-remove-payment'));
      if (Number.isNaN(idx)) return;
      payments.splice(idx, 1);
      renderPayments();
      setStatus('Removed payment.', 'info');
    });

    renderPayments();
  }

  ready(initSplitTender);
  document.addEventListener('htmx:afterSwap', initSplitTender);
  document.addEventListener('htmx:load', initSplitTender);
})();

// Sale-screen notification surface (ut-docs#213,
// docs/sale-screen-notifications.md): info/success notices
// auto-expire; error notices persist until the operator dismisses them.
function scheduleToastDismiss(){
  var toast = document.getElementById('toast-message');
  if (!toast || toast.dataset.dismissed === '1') return;
  if (toast.classList.contains('error')) return; // errors persist until dismissed
  toast.dataset.dismissed = '1';
  setTimeout(function(){
    toast.classList.add('hide');
    setTimeout(function(){
      if (toast && toast.parentNode) {
        toast.parentNode.removeChild(toast);
      }
    }, 250);
  }, 2500);
}

document.addEventListener('DOMContentLoaded', scheduleToastDismiss);
document.addEventListener('htmx:afterSwap', scheduleToastDismiss);

// Dismiss control — delegated so it survives every #basket outerHTML swap.
document.addEventListener('click', function(e){
  var btn = e.target.closest ? e.target.closest('.notice-dismiss') : null;
  if (!btn) return;
  var notice = btn.closest('.pos-notice');
  if (!notice) return;
  if (notice.id === 'pos-alert') { notice.hidden = true; return; } // reusable slot
  notice.classList.add('hide');
  setTimeout(function(){ if (notice.parentNode) notice.parentNode.removeChild(notice); }, 250);
});

// Request failures surface in the client-side slot (#pos-alert) — a server
// error response or an unreachable server would otherwise fail silently
// (there was no htmx error handler at all before ut-docs#213). Strings come
// from data-* attributes so this file stays locale-free.
(function(){
  function showAlert(kind){
    var alertBox = document.getElementById('pos-alert');
    if (!alertBox) return;
    var msg = kind === 'network' ? alertBox.dataset.msgNetwork : alertBox.dataset.msgServer;
    // Unhide BEFORE writing the text: a role=alert region that changes
    // while display:none doesn't reliably announce to screen readers.
    alertBox.hidden = false;
    alertBox.querySelector('.notice-text').textContent = msg || '';
  }
  // A 400 from a sale-screen endpoint whose body is a rendered basket
  // fragment carries its own .pos-notice (e.g. modifier validation): swap
  // it in and clear the error flag so the specific message shows instead
  // of the generic alert. Path-scoped like self-order's equivalent
  // handler. (htmx never swaps 4xx by default — before ut-docs#213 these
  // rendered error toasts were silently dropped on the sale screen.)
  document.body.addEventListener('htmx:beforeSwap', function(ev){
    var d = ev.detail;
    if (!d || !d.xhr || d.xhr.status !== 400) return;
    var path = (d.pathInfo && (d.pathInfo.finalRequestPath || d.pathInfo.requestPath)) || '';
    if (path.indexOf('/api/pos/') !== 0) return;
    if (typeof d.serverResponse === 'string' && d.serverResponse.indexOf('id="basket"') !== -1) {
      d.shouldSwap = true;
      d.isError = false;
    }
  });
  document.body.addEventListener('htmx:responseError', function(){ showAlert('server'); });
  document.body.addEventListener('htmx:sendError', function(){ showAlert('network'); });
  // Self-heal: the first successful request clears a stale alert, so an
  // intermittent-connectivity till doesn't wear a permanent red banner
  // (offline-first: transient failure must not leave persistent chrome).
  document.body.addEventListener('htmx:afterRequest', function(ev){
    if (!ev.detail || !ev.detail.successful) return;
    var alertBox = document.getElementById('pos-alert');
    if (alertBox && !alertBox.hidden) alertBox.hidden = true;
  });
})();

function offlineOverrideEnabled(){
  var toggle = document.getElementById('offline-override');
  if (!toggle) return false;
  return !!toggle.checked;
}

function initOfflineOverride(updateFn){
  var toggle = document.getElementById('offline-override');
  if (!toggle) return;
  try {
    var stored = localStorage.getItem('ut_offline_override');
    if (stored === '1') {
      toggle.checked = true;
    }
  } catch (e) {
    // localStorage may be unavailable; continue without persistence
  }
  toggle.addEventListener('change', function(){
    try {
      localStorage.setItem('ut_offline_override', toggle.checked ? '1' : '0');
    } catch (e) {
      // ignore storage failures
    }
    if (typeof updateFn === 'function') {
      updateFn();
    }
  });
}

(function(){
  function updateOfflineFlag(){
    var input = document.getElementById('offline-flag');
    if (!input) return;
    var forcedOffline = offlineOverrideEnabled();
    input.value = (forcedOffline || !navigator.onLine) ? '1' : '0';
  }
  initOfflineOverride(updateOfflineFlag);
  updateOfflineFlag();
  window.addEventListener('online', updateOfflineFlag);
  window.addEventListener('offline', updateOfflineFlag);
  document.addEventListener('htmx:configRequest', updateOfflineFlag);
})();

// Camera identify (AI-assisted; strictly optional). The button only shows
// when the server rendered it (UT_AI_API_KEY set) AND the till is online —
// barcode scan stays the primary path and never waits on this.
(function(){
  var openBtn = document.getElementById('ai-identify-open');
  var overlay = document.getElementById('ai-identify-overlay');
  if (!openBtn || !overlay) return;

  var video = document.getElementById('ai-identify-video');
  var results = document.getElementById('ai-identify-results');
  var status = document.getElementById('ai-identify-status');
  var captureBtn = document.getElementById('ai-identify-capture');
  var retakeBtn = document.getElementById('ai-identify-retake');
  var closeBtn = document.getElementById('ai-identify-close');
  var msgs = overlay.dataset;
  var stream = null;
  var lastPhoto = null;

  function updateVisibility(){
    openBtn.hidden = !navigator.onLine;
    if (!navigator.onLine && !overlay.hidden) close();
  }
  window.addEventListener('online', updateVisibility);
  window.addEventListener('offline', updateVisibility);
  updateVisibility();

  function setStatus(text){ status.textContent = text || ''; }

  function open(){
    overlay.hidden = false;
    results.innerHTML = '';
    setStatus('');
    lastPhoto = null;
    captureBtn.hidden = false;
    retakeBtn.hidden = true;
    navigator.mediaDevices.getUserMedia({ video: { facingMode: 'environment' } })
      .then(function(s){ stream = s; video.srcObject = s; })
      .catch(function(){ setStatus(msgs.msgCameraError); });
  }

  function close(){
    overlay.hidden = true;
    if (stream) { stream.getTracks().forEach(function(t){ t.stop(); }); stream = null; }
    video.srcObject = null;
  }

  // Bound the upload client-side: max 1024px long edge, JPEG.
  function capture(cb){
    var w = video.videoWidth, h = video.videoHeight;
    if (!w || !h) { setStatus(msgs.msgCameraError); return; }
    var scale = Math.min(1, 1024 / Math.max(w, h));
    var canvas = document.createElement('canvas');
    canvas.width = Math.round(w * scale);
    canvas.height = Math.round(h * scale);
    canvas.getContext('2d').drawImage(video, 0, 0, canvas.width, canvas.height);
    canvas.toBlob(cb, 'image/jpeg', 0.85);
  }

  function renderMatches(data){
    results.innerHTML = '';
    var matches = (data && data.matches) || [];
    if (!matches.length) {
      var text = msgs.msgNoMatch;
      if (data && data.suggested_name) text += ' — ' + msgs.msgSuggested + ' ' + data.suggested_name;
      setStatus(text);
      return;
    }
    setStatus('');
    matches.forEach(function(m){
      var btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'btn ai-match';
      if (m.thumb_url) {
        var img = document.createElement('img');
        img.src = m.thumb_url;
        img.alt = '';
        btn.appendChild(img);
      }
      var label = document.createElement('span');
      label.textContent = m.name + ' · ' + m.price_display;
      btn.appendChild(label);
      btn.addEventListener('click', function(){ pick(m); });
      results.appendChild(btn);
    });
  }

  function pick(m){
    // Add the line through the normal scan path (SKU exact match).
    if (window.htmx) {
      window.htmx.ajax('POST', '/api/pos/scan', {
        target: '#basket', swap: 'outerHTML', values: { code: m.sku, qty: 1 }
      });
    }
    // Save the confirmed photo as an ai_ref reference image (fire-and-forget).
    if (lastPhoto) {
      var fd = new FormData();
      fd.append('item_id', m.item_id);
      fd.append('photo', lastPhoto, 'capture.jpg');
      fetch('/api/pos/identify/confirm', { method: 'POST', body: fd });
    }
    close();
  }

  function identify(){
    capture(function(blob){
      if (!blob) { setStatus(msgs.msgError); return; }
      lastPhoto = blob;
      setStatus(msgs.msgSearching);
      captureBtn.hidden = true;
      retakeBtn.hidden = false;
      var fd = new FormData();
      fd.append('photo', blob, 'capture.jpg');
      fetch('/api/pos/identify', { method: 'POST', body: fd })
        .then(function(r){ return r.json(); })
        .then(function(body){
          if (!body || body.error) { setStatus(msgs.msgError); return; }
          renderMatches(body.data);
        })
        .catch(function(){ setStatus(msgs.msgError); });
    });
  }

  function retake(){
    results.innerHTML = '';
    setStatus('');
    lastPhoto = null;
    captureBtn.hidden = false;
    retakeBtn.hidden = true;
  }

  openBtn.addEventListener('click', open);
  captureBtn.addEventListener('click', identify);
  retakeBtn.addEventListener('click', retake);
  closeBtn.addEventListener('click', close);
})();

// Camera barcode/QR scan (ut-docs#548): an alternative input mode alongside
// the wedge/HID scanner path above (never disables or steals focus from it —
// this is purely an on-demand overlay, opened and closed by the cashier).
// Decoding is 100% client-side via the browser's native BarcodeDetector — no
// frame or image is ever sent anywhere, unlike the AI-identify feature above
// which uploads a still photo by design. Browser support varies, so the
// button only appears when `BarcodeDetector` actually exists; there is no JS
// fallback decoder in this pass (ut-docs#548 non-goal — a bundled decode
// library is separate scope under ADR-0003's vendored-assets rule).
(function(){
  var openBtn = document.getElementById('barcode-scan-open');
  var overlay = document.getElementById('barcode-scan-overlay');
  if (!openBtn || !overlay || !window.utScan) return;
  // typeof-check, not `'BarcodeDetector' in window`: a test (or a future
  // polyfill probe) stubbing the property to undefined must still read as
  // unsupported, not merely "present".
  if (typeof window.BarcodeDetector !== 'function') return;

  var video = document.getElementById('barcode-scan-video');
  var status = document.getElementById('barcode-scan-status');
  var closeBtn = document.getElementById('barcode-scan-close');
  var msgs = overlay.dataset;
  var stream = null;
  var rafID = null;
  var detector;
  try {
    // Formats this product's wedge scanners actually read (ut-docs#423's
    // review: "EAN-8/13, UPC, Code-128 SKUs"), plus qr_code for #210-style
    // self-order use later. An explicit list keeps decode behaviour the
    // same across browsers rather than however each one's default differs.
    detector = new BarcodeDetector({ formats: [
      'ean_13', 'ean_8', 'upc_a', 'upc_e', 'code_128', 'qr_code'
    ] });
  } catch (e) {
    return; // constructor throws if the browser can't support any listed format
  }

  openBtn.hidden = false;

  function setStatus(text){ status.textContent = text || ''; }

  function open(){
    if (!overlay.hidden) return; // already open (button keeps focus; Space/Enter re-fires it)
    overlay.hidden = false;
    setStatus(msgs.msgScanning);
    navigator.mediaDevices.getUserMedia({ video: { facingMode: 'environment' } })
      .then(function(s){
        // The cashier can close while the permission prompt / camera start is
        // still pending — never leave an orphaned live camera behind a hidden
        // overlay (nor let it scan and ring up a line the cashier can't see).
        if (overlay.hidden) { s.getTracks().forEach(function(t){ t.stop(); }); return; }
        stream = s;
        video.srcObject = s;
        rafID = requestAnimationFrame(scanFrame);
      })
      .catch(function(){ setStatus(msgs.msgCameraError); });
  }

  function close(){
    overlay.hidden = true;
    if (rafID) { cancelAnimationFrame(rafID); rafID = null; }
    if (stream) { stream.getTracks().forEach(function(t){ t.stop(); }); stream = null; }
    video.srcObject = null;
  }

  function scanFrame(){
    if (!stream) return;
    detector.detect(video)
      .then(function(codes){
        // close() nulls `stream`; a detect() already in flight when the
        // cashier closed must not ring up a line after the overlay is gone.
        if (!stream) return;
        if (codes && codes.length) {
          var code = codes[0].rawValue;
          close();
          if (code) window.utScan.submit(code, window.utScan.input());
          return;
        }
        rafID = requestAnimationFrame(scanFrame);
      })
      .catch(function(){
        // A transient per-frame decode error shouldn't kill the session —
        // keep scanning until the cashier closes the overlay themselves.
        if (!stream) return;
        rafID = requestAnimationFrame(scanFrame);
      });
  }

  openBtn.addEventListener('click', open);
  closeBtn.addEventListener('click', close);
})();

// Idle auto-lock, cosmetic half (docs: pos-auth.md). The server revokes the
// session authoritatively; this timer just sends an abandoned till to the
// keypad without waiting for the next request. Absent when the feature is off.
(function () {
  var secs = parseInt(document.body.dataset.idleLock || '0', 10);
  if (!secs || window.location.pathname === '/login') return;
  var last = Date.now();
  function bump() { last = Date.now(); }
  ['pointerdown', 'keydown', 'touchstart', 'wheel'].forEach(function (ev) {
    document.addEventListener(ev, bump, { passive: true });
  });
  setInterval(function () {
    if ((Date.now() - last) / 1000 > secs) window.location.replace('/login');
  }, 5000);
})();

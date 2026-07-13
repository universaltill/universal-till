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
  function submit(code){
    var form = document.querySelector('form[action="/api/pos/scan"], form[hx-post="/api/pos/scan"]');
    if(!form) return;
    var codeInput = form.querySelector('input[name="code"]');
    if(!codeInput) return;
    codeInput.value = code;
    if (window.htmx) { window.htmx.trigger(form, 'submit'); } else { form.submit(); }
    setTimeout(function(){ codeInput.value = ""; }, 0);
  }
  window.addEventListener('keydown', function(e){
    if (e.isComposing || e.metaKey || e.ctrlKey || e.altKey) return;
    var now = Date.now();
    if (now - last > 100) { buf = ""; }
    last = now;
    if (e.key === 'Enter') {
      var code = buf;
      if (!code) {
        var form = document.querySelector('form[action="/api/pos/scan"], form[hx-post="/api/pos/scan"]');
        if (form) {
          var codeInput = form.querySelector('input[name="code"]');
          if (codeInput && document.activeElement === codeInput) {
            code = (codeInput.value || '').trim();
          }
        }
      }
      if (code) {
        e.preventDefault();
        submit(code);
      }
      buf = "";
      return;
    }
    if (e.key.length === 1) buf += e.key;
    clearTimeout(timeout);
    timeout = setTimeout(function(){ buf = ""; }, 300);
  });
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

function scheduleToastDismiss(){
  var toast = document.getElementById('toast-message');
  if (!toast || toast.dataset.dismissed === '1') return;
  toast.dataset.dismissed = '1';
  setTimeout(function(){
    toast.classList.add('hide');
    setTimeout(function(){
      if (toast && toast.parentNode) {
        toast.parentNode.removeChild(toast);
      }
    }, 250);
  }, 1500);
}

document.addEventListener('DOMContentLoaded', scheduleToastDismiss);
document.addEventListener('htmx:afterSwap', scheduleToastDismiss);

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

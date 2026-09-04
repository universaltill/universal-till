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

  // Clear the scan code field after ANY submission of the scan form, not
  // only the hardware/wedge path above (ut-docs#1177). osk.js's own '↵' key
  // (press('↵') -> form.requestSubmit()) and a direct tap on the visible
  // "Add" submit button both bypass submit() entirely, so neither cleared
  // the field — the next scan concatenated onto the stale value instead of
  // replacing it (reproduced live: two OSK-driven scans in a row produced
  // a garbled 27-digit code and "item not found", exactly the reported
  // symptom — SSH-injecting a real Enter keydown always took the hardware
  // path above and cleared the field, which is why that reproduction
  // step alone never showed the bug). Delegated at the document level on
  // the native 'submit' event so it fires regardless of trigger (a plain
  // click, htmx.trigger(), form.requestSubmit()) with no per-trigger-path
  // wiring needed — matches submit()'s own setTimeout(...,0): after the
  // value has already been read and sent, never before. A harmless no-op
  // duplicate of submit()'s own clear on the hardware/wedge/camera paths,
  // all of which already go through submit() above.
  document.addEventListener('submit', function (e) {
    var input = scanCodeInput();
    if (input && e.target === input.form) {
      setTimeout(function(){ input.value = ""; }, 0);
    }
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

    // ut-docs#925: this panel's own status/validation copy is user-facing and
    // must localize, but app.js is a shipped static file with no template
    // rendering, so it can't use CLAUDE.md's inline-<script> `var T = {...}`
    // pattern. It rides on data-msg-* attributes instead — the same bridge
    // #barcode-scan-overlay already uses in this template, keeping app.js
    // locale-free. Placeholders are `%s`, matching the Go-side locale strings
    // -- but ONLY the plain `%s` verb: this is not a Sprintf, so `%d` and
    // indexed `%[1]s` (both legal in Go-rendered keys) would pass through
    // literally. TestSplitTenderKeysUseOnlyPlainStringVerb pins these keys to
    // that subset so a translator reordering a sentence can't quietly ship a
    // literal `%[2]s` to an operator.
    var msg = card.dataset;
    function fmt(template){
      var args = Array.prototype.slice.call(arguments, 1);
      var i = 0;
      return String(template || '').replace(/%s/g, function(){
        return i < args.length ? args[i++] : '';
      });
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
        paymentsList.innerHTML = '<p>' + escapeHtml(msg.msgNoPending) + '</p>';
        return;
      }
      paymentsList.classList.remove('empty');
      var html = payments.map(function(payment, idx){
        var net = payment.amount - (payment.change || 0);
        var details = formatMoney(net);
        if (payment.change) {
          details += ' ' + escapeHtml(fmt(msg.msgChangeNote, formatMoney(payment.change)));
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
        setStatus(msg.msgSelectMethod, 'error');
        return false;
      }
      var amountMinor = toMinor(data.get('amount'));
      if (amountMinor <= 0) {
        setStatus(msg.msgAmountPositive, 'error');
        return false;
      }
      var changeMinor = toMinor(data.get('change'));
      if (changeMinor < 0) changeMinor = 0;
      if (changeMinor > amountMinor) {
        setStatus(msg.msgChangeExceeds, 'error');
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
      setStatus(fmt(msg.msgAdded, method, formatMoney(payment.amount - (payment.change || 0))), 'success');
      return true;
    }

    function fillRemaining(){
      var total = basketTotal();
      if (!total) {
        setStatus(msg.msgBasketUnavailable, 'error');
        return;
      }
      var remaining = total - netPayments();
      if (remaining <= 0) {
        setStatus(msg.msgAlreadyCovered, 'info');
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
      setStatus(fmt(msg.msgFilled, formatMoney(remaining)), 'info');
    }

    async function submitPayments(){
      if (!payments.length) {
        var autoAdded = false;
        var amountInput = form.querySelector('input[name="amount"]');
        if (amountInput && amountInput.value) {
          autoAdded = addPayment();
        }
        if (!autoAdded) {
          setStatus(msg.msgNeedPayment, 'error');
          return;
        }
      }

      submitBtn.disabled = true;
      setStatus(msg.msgSubmitting, 'info');
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
        var genericFailure = msg.msgPaymentFailed;
        if (!response.ok) {
          setStatus(text || genericFailure, 'error');
          return;
        }
        var basket = document.getElementById('basket');
        if (basket) {
          basket.outerHTML = text;
        }
        // ut-docs#921 review finding: a rejected tender (underpayment,
        // insufficient stock, the fiscal hard gate, ...) renders as a 200
        // with an in-basket error toast, not a 4xx -- same htmx-partial
        // contract every other tender control on this page already uses
        // (the pos_api.go handler never blockingly errors on a rejection,
        // it re-renders the basket in place so the operator's items
        // survive). Checking response.ok alone read that as success here,
        // wiping the operator's pending split payments and declaring "Sale
        // completed." on a sale that did not happen. #toast-message only
        // exists in the swapped markup at all when the server actually set
        // a ToastMessage, and only carries the "error" class when
        // ToastLevel is "error" (web/ui/partials/basket.html) -- re-query
        // the DOM the swap just wrote, don't trust the HTTP status alone.
        var rejection = document.getElementById('toast-message');
        if (rejection && rejection.classList.contains('error')) {
          var rejectionText = (rejection.querySelector('.notice-text') || rejection).textContent.trim();
          setStatus(rejectionText || genericFailure, 'error');
          return;
        }
        payments = [];
        renderPayments();
        clearForm();
        setStatus(msg.msgSaleCompleted, 'success');
        // ut-docs#1252: the split-tender card now lives inside the
        // #payment-overlay dialog (opened by the .payment-trigger button)
        // instead of always being on screen -- close it on an actually-
        // completed sale, same "checked the rejection branch above already
        // returned" guarantee the pay-grid buttons' own hx-on::after-request
        // close handler relies on (basket.html's #toast-message.error).
        var overlay = document.getElementById('payment-overlay');
        if (overlay && overlay.open) { overlay.close(); }
      } catch (err) {
        // ut-docs#925: err.message is raw, always-English browser text
        // ("Failed to fetch", ...) — the same leak class this card is about,
        // so it stays out of the operator-facing status and goes to the
        // console for diagnostics instead.
        console.error('split tender submit failed:', err);
        setStatus(msg.msgNetworkError, 'error');
      } finally {
        submitBtn.disabled = false;
      }
    }

    addBtn.addEventListener('click', addPayment);
    clearBtn.addEventListener('click', function(){
      payments = [];
      renderPayments();
      clearForm();
      setStatus(msg.msgCleared, 'info');
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
      setStatus(msg.msgRemoved, 'info');
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
//
// ut-docs#238 generalizes this from the single hardcoded #toast-message id
// to every .pos-notice currently in the document — the pattern now also
// covers non-sale-screen spots (catalog's export-save/labels-print
// handlers, and its own client-JS notices). Each element tracks its own
// dismissed state via its own dataset (was a single module-level flag,
// which could only ever track one notice at a time) — #toast-message's own
// timing/behaviour is unchanged by this, it's simply one of however many
// .pos-notice elements this now iterates.

// Shared client-JS .pos-notice builder (ut-docs#918) — extracted from the
// page-local copy ut-docs#238 first wrote inline in catalog.html, which
// only had 5 call sites; settings.html's ~9 ad-hoc-textContent spots made a
// 6th copy-paste not worth it. catalog.html keeps its own local copy for
// now (out of scope for #918 — no functional difference, just unmerged
// duplication tracked for a later cleanup pass) rather than risk touching
// its already-shipped behaviour in this change.
//
// level: "error" | "success" | "info" — error gets role="alert" and
// persists until dismissed; anything else gets role="status" and
// auto-expires (scheduleToastDismiss below).
//
// Built with DOM calls, never an innerHTML string: `text` is routinely NOT
// ours to trust (a server error message, user input echoed back), so it
// goes in via textContent, which cannot produce markup at all — see
// catalog.html's identical comment for the innerHTML/String.replace pitfall
// this avoids.
//
// The dismiss button's aria-label comes from <body data-notice-dismiss>
// (set by base.html via {{ T "notice.dismiss" }}) rather than a parameter —
// app.js is a static, non-templated asset, so it can't call {{ T }} itself;
// this is the same data-attribute convention utCurrency (top of this file)
// and base.html's data-conn-online/offline already use for the same reason.
function renderNotice(el, level, text){
  if (!el) return;
  el.textContent = '';
  if (!text) return;
  var notice = document.createElement('div');
  notice.className = 'pos-notice ' + level;
  notice.setAttribute('role', level === 'error' ? 'alert' : 'status');
  var body = document.createElement('span');
  body.className = 'notice-text';
  body.textContent = text;
  var dismiss = document.createElement('button');
  dismiss.type = 'button';
  dismiss.className = 'notice-dismiss';
  dismiss.setAttribute('aria-label', (document.body && document.body.dataset.noticeDismiss) || '✕');
  dismiss.textContent = '✕';
  notice.appendChild(body);
  notice.appendChild(dismiss);
  el.appendChild(notice);
  if (typeof scheduleToastDismiss === 'function') scheduleToastDismiss();
}

function scheduleToastDismiss(){
  document.querySelectorAll('.pos-notice').forEach(function(toast){
    if (toast.dataset.dismissed === '1') return;
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
  });
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
  // htmx never swaps a non-2xx response into its target by default — it
  // fires htmx:responseError instead and discards the body. That's fine on
  // the sale screen, which has the generic #pos-alert fallback below, but
  // several admin-page handlers that fail (print/labels, print/test, invoice
  // issue, backup now/restore, sync join/promote, …) still render a real,
  // translated `.muted` fragment straight into their own hx-target — and on
  // those pages there is no #pos-alert equivalent, so the rendered error
  // was silently discarded and the operator saw nothing at all (ut-docs#916).
  //
  // NOT every non-2xx response is safe to force-swap, though (review finding
  // on the first version of this fix): a plain `http.Error(...)` body is
  // `text/plain` and htmx's fragment parser yields zero nodes for it against
  // an outerHTML/innerHTML target — swapping it in wipes the target instead
  // of showing anything (`#catalog-variants`, the 15s-polled `#orders-list`,
  // …). And forcing `isError = false` on one of those unconditionally also
  // defeats every page's own already-working `htmx:responseError` handler
  // (refund.html, catalog.html's item form) and flips `detail.successful` to
  // true in `hx-on::after-request` branches that key off it (settings.html's
  // save handlers reload the page as if a rejected save had succeeded).
  //
  // The discriminator: every handler ut-docs#916 actually targets sets
  // `Content-Type: text/html` before writing its fragment — `http.Error`
  // always answers `text/plain`. So only force the swap for a real,
  // non-empty HTML body; everything else (plaintext, JSON, empty) falls
  // through to htmx:responseError/showAlert exactly as before this fix.
  document.body.addEventListener('htmx:beforeSwap', function(ev){
    var d = ev.detail;
    if (!d || !d.xhr || d.xhr.status < 400) return;
    var path = (d.pathInfo && (d.pathInfo.finalRequestPath || d.pathInfo.requestPath)) || '';
    if (path.indexOf('/api/pos/') === 0) {
      // Sale-screen carve-out (ut-docs#213): only a 400 whose body is a
      // rendered basket fragment (e.g. modifier validation) swaps in place
      // of the generic alert — anything else on this path falls through to
      // the #pos-alert banner via htmx:responseError below.
      if (d.xhr.status === 400 && typeof d.serverResponse === 'string' && d.serverResponse.indexOf('id="basket"') !== -1) {
        d.shouldSwap = true;
        d.isError = false;
      }
      return;
    }
    var contentType = (d.xhr.getResponseHeader && d.xhr.getResponseHeader('Content-Type')) || '';
    if (contentType.indexOf('text/html') === -1) return;
    if (typeof d.serverResponse !== 'string' || d.serverResponse.trim() === '') return;
    d.shouldSwap = true;
    d.isError = false;
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
    // ut-docs#1251: on a non-secure-context origin (plain http:// to a LAN
    // IP rather than localhost — reachable here since this button has no
    // BarcodeDetector-style feature gate, only the server-side AI-key/online
    // check above), `navigator.mediaDevices` is undefined entirely, and
    // calling `.getUserMedia` on it throws a SYNCHRONOUS TypeError before
    // the promise chain (and its .catch() below) even exists — the overlay
    // opens but the camera never starts and no error is ever shown, not
    // even "Camera unavailable". Guard it explicitly so that case reports
    // the same honest, already-existing error as any other failure instead
    // of hanging silently.
    if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
      setStatus(msgs.msgCameraError);
      return;
    }
    navigator.mediaDevices.getUserMedia({ video: { facingMode: 'environment' } })
      .then(function(s){ stream = s; video.srcObject = s; })
      .catch(function(err){
        if (err && err.name) {
          switch (err.name) {
            case 'NotFoundError':
            case 'OverconstrainedError':
              setStatus(msgs.msgCameraNotFound);
              break;
            case 'NotAllowedError':
            case 'SecurityError':
              setStatus(msgs.msgCameraPermissionDenied);
              break;
            case 'NotReadableError':
              setStatus(msgs.msgCameraBusy);
              break;
            default:
              setStatus(msgs.msgCameraError);
          }
        } else {
          setStatus(msgs.msgCameraError);
        }
      });
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
    // ut-docs#1251: same guard as the AI-identify IIFE above — a
    // non-secure-context origin leaves `navigator.mediaDevices` undefined,
    // and calling `.getUserMedia` on it throws synchronously, before the
    // .catch() below exists to report anything. In practice BarcodeDetector
    // itself is also secure-context-gated in the browsers that ship it, so
    // this button is usually already hidden in that case (the typeof check
    // above) — this guard is defence-in-depth for whatever browser doesn't
    // tie the two together the same way.
    if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
      setStatus(msgs.msgCameraError);
      return;
    }
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
      .catch(function(err){
        if (err && err.name) {
          switch (err.name) {
            case 'NotFoundError':
            case 'OverconstrainedError':
              setStatus(msgs.msgCameraNotFound);
              break;
            case 'NotAllowedError':
            case 'SecurityError':
              setStatus(msgs.msgCameraPermissionDenied);
              break;
            case 'NotReadableError':
              setStatus(msgs.msgCameraBusy);
              break;
            default:
              setStatus(msgs.msgCameraError);
          }
        } else {
          setStatus(msgs.msgCameraError);
        }
      });
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

// utPostWithElevation (ut-docs#794): a raw-fetch equivalent of the
// checkOrElevate/elevation_prompt.html dialog (elevation.go, ut-docs#557)
// for the handful of endpoints that can't be driven by htmx at all —
// specifically POST /api/reports/eod/range and POST
// /api/reports/archive/export, both of which trigger a browser file
// download via a Content-Disposition response header, something htmx has
// no way to do (it swaps response bodies as HTML). Every OTHER
// checkOrElevate site is a normal hx-post button/form, so the dialog's own
// OOB swap (hx-swap-oob="true" into the shared #elevation-modal
// placeholder, web/ui/layouts/base.html) is handled by htmx automatically
// — this helper exists ONLY for the two sites that bypass htmx.
//
// url/params: the endpoint and its (non-PIN) form fields, exactly what
// would otherwise go straight into a fetch() body.
// onDone(response): called once with the FINAL response — a real success
// or a real error, never another elevation prompt (that case is handled
// internally, recursively). Any dialog this call opened is already closed
// by the time onDone runs, so onDone never needs to close it itself.
// onCancel(): optional — called if the user dismisses the dialog (Cancel,
// or any other way it closes) before a terminal response was ever reached,
// so the caller can undo whatever "request in flight" UI state (disabled
// button, spinner text) it set before calling this. Never called once
// onDone has fired.
//
// Detecting "this response IS the elevation prompt" (vs. a real
// success/error): renderElevationPrompt explicitly sets Content-Type:
// text/html for exactly this reason (elevation.go) — a real success here
// always carries Content-Disposition: attachment, and a real error is
// http.Error's text/plain, so text/html is unambiguous.
window.utPostWithElevation = function (url, params, onDone, onCancel) {
  var openDialog = null;
  var finished = false; // true once onDone has fired or close() was called for it
  function close() {
    finished = true;
    if (!openDialog) return;
    try { openDialog.close(); } catch (e) { /* already closed */ }
    openDialog = null;
  }
  function showDialog(html) {
    var wrap = document.createElement('div');
    wrap.innerHTML = html;
    var dialog = wrap.querySelector('#elevation-modal');
    if (!dialog) return false; // shouldn't happen — server always renders it on needsElevation
    // Defensive: keeps htmx from processing this manually-inserted
    // instance if anything ever calls htmx.process() on an ancestor later
    // — every htmx-DRIVEN checkOrElevate site still needs its own hx-post
    // form processed normally, so this only touches the one instance we
    // insert by hand here, never the shared template itself.
    dialog.setAttribute('hx-disable', '');
    var old = document.getElementById('elevation-modal');
    if (old) old.replaceWith(dialog); else document.body.appendChild(dialog);
    openDialog = dialog;
    if (typeof dialog.show === 'function') dialog.show();
    // Fires on Cancel (elevation_prompt.html's button calls
    // this.closest('dialog').close()) and on our own close() above — the
    // `finished` guard means it's a no-op in the latter case, so this is
    // purely the "user backed out" signal.
    dialog.addEventListener('close', function () {
      if (!finished && onCancel) onCancel();
    }, { once: true });
    var form = dialog.querySelector('form');
    if (form) {
      form.addEventListener('submit', function (ev) {
        ev.preventDefault();
        // The retry's own promise chain is otherwise disconnected from
        // whatever the caller attached to the original send(params) call
        // (this listener fires later, asynchronously) — without this
        // catch, a network failure on the RETRY specifically would be an
        // unhandled rejection that never reaches the caller's .catch(),
        // leaving its UI state (disabled button, spinner text) stuck.
        send(new FormData(form)).catch(function (e) {
          if (window.console) console.error('utPostWithElevation: retry failed', e);
          close();
          if (onCancel) onCancel();
        });
      });
    }
    return true;
  }
  function send(bodyParams) {
    var body = new URLSearchParams();
    if (bodyParams instanceof FormData) {
      bodyParams.forEach(function (v, k) { body.append(k, v); });
    } else {
      Object.keys(bodyParams || {}).forEach(function (k) { body.append(k, bodyParams[k]); });
    }
    return fetch(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: body.toString()
    }).then(function (r) {
      var cd = r.headers.get('Content-Disposition') || '';
      var ct = r.headers.get('Content-Type') || '';
      if (cd.indexOf('attachment') === -1 && ct.indexOf('text/html') !== -1) {
        // Read the body ONLY here, and only decide what to do with it
        // afterward — never also hand `r` to onDone from this branch (its
        // body is already consumed by the time showDialog resolves).
        return r.text().then(function (html) {
          if (!showDialog(html)) {
            close();
            // Nothing left to show the caller — this genuinely shouldn't
            // happen (the server always renders the dialog on
            // needsElevation), so surface it AND release the caller's UI
            // state the same way a cancelled dialog would, rather than
            // leaving a disabled button stuck forever.
            if (window.console) console.error('utPostWithElevation: elevation response had no #elevation-modal', html);
            if (onCancel) onCancel();
          }
        });
      }
      close();
      return onDone(r);
    });
  }
  return send(params);
};

// Currency metadata comes from <body data-currency-*> (set by base.html from
// the configured currency). Decimals drive the major<->minor conversion:
// GBP=2 (pence), IRR/IRT=0 (no subunit). Suffix currencies (rial/toman)
// render the word after the number. data-number-thousands/-decimal (same
// convention as server-side httpx.FormatMoney, ut-docs#1130) keep this
// client-rendered money agreeing with server-rendered money on the same
// screen (e.g. the payment overlay's pills next to the server-rendered
// basket total) — a de-DE till shows "1.234,56" everywhere, not "1.234,56"
// server-side and "1,234.56" here. Digit SHAPE (fa/ar numerals) is a
// separate, still-unaddressed gap — out of scope here, same as it already
// was before this file's separators became locale-aware.
window.utCurrency = (function(){
  var d = document.body ? document.body.dataset : {};
  var decimals = parseInt(d.currencyDecimals || '2', 10);
  if (isNaN(decimals) || decimals < 0) decimals = 2;
  var factor = Math.pow(10, decimals);
  var display = d.currencyDisplay || '£';
  var suffix = d.currencySuffix === '1';
  var thousandsSep = d.numberThousands || ',';
  var decimalSep = d.numberDecimal || '.';
  function formatMinor(units){
    var neg = units < 0; if (neg) units = -units;
    var major = Math.floor(units / factor);
    var num = major.toString().replace(/\B(?=(\d{3})+(?!\d))/g, thousandsSep);
    if (decimals > 0) num += decimalSep + String(units % factor).padStart(decimals, '0');
    if (neg) num = '-' + num;
    return suffix ? num + ' ' + display : display + num;
  }
  var commaDecimal = decimals > 0 ? new RegExp('^-?[0-9]+,[0-9]{1,' + decimals + '}$') : null;
  function parseMinor(v){
    var text = String(v == null ? '' : v).trim();
    if (text === '') return NaN;
    if (commaDecimal && commaDecimal.test(text)) text = text.replace(',', '.');
    var num = Number(text);
    return isFinite(num) ? Math.round(num * factor) : NaN;
  }
  return {
    decimals: decimals, factor: factor, display: display, suffix: suffix,
    // ut-docs#2815: a decimal COMMA ("3,50" -- German/Turkish keyboards,
    // the de/tr OSK) is a decimal separator, not garbage: it used to parse
    // as NaN and silently become 0. Only a single comma followed by at most
    // `decimals` digits is read that way, so an en-style thousands "1,234"
    // is still refused rather than misread as 1.234. parseMinor returns NaN
    // for anything unreadable, so a caller can refuse it instead of saving 0.
    parseMinor: parseMinor,
    toMinor: function(v){
      var n = parseMinor(v);
      return isNaN(n) ? 0 : n;
    },
    toMajor: function(units){ return (units / factor).toFixed(decimals); },
    format: formatMinor
  };
})();

// ut-docs#2364: below 480px .nav reverts from the fixed-height rail to a
// horizontal top bar that WRAPS (app.css), so its real height varies with
// content/locale -- currently 3 rows at 360px -- and nothing CSS-only can
// track that. Same pattern as public/osk.js's updateReservedHeight(): measure
// the real element and publish it as a CSS custom property so anything that
// needs to sit below the bar (currently just .bugreport-panel) reads the
// true value instead of a guessed rem. Guarded to the <=480px matchMedia
// only -- above that breakpoint .nav is a full fixed-height rail and
// measuring it would publish a meaningless value nothing then uses (the var
// is only referenced inside app.css's own <=480px block).
(function(){
  var nav = document.querySelector('.nav');
  if (!nav) return;
  var mq = window.matchMedia('(max-width: 480px)');
  var last = 0;
  function updateTopbarHeight(){
    if (!mq.matches) return;
    var h = nav.getBoundingClientRect().height;
    if (h > 0 && h !== last) {
      document.documentElement.style.setProperty('--topbar-h', h + 'px');
      last = h;
    }
  }
  updateTopbarHeight();
  window.addEventListener('resize', updateTopbarHeight);
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
    // ut-docs#1832: voucher redemption (a voucher_id on a "voucher"-method
    // payment) and voucher issue ("Sell a voucher": issue_vouchers on the
    // same tender POST). All optional — the panel keeps working without them.
    var methodSelect = form ? form.querySelector('[name="method"]') : null;
    var voucherField = card.querySelector('#split-tender-voucher-field');
    var voucherCheckBtn = card.querySelector('#split-tender-voucher-check');
    var changeField = card.querySelector('#split-tender-change-field');
    var issueDetails = card.querySelector('#split-tender-issue');
    var issueForm = card.querySelector('#split-tender-issue-form');
    var issueAddBtn = card.querySelector('#split-tender-issue-add');
    var vouchersList = card.querySelector('#split-tender-vouchers');
    // ut-docs#1037: single-purpose vouchers — the purpose radios + VAT-rate
    // field on the issue form, and the Redeem row under the voucher-id
    // field. All optional, same as the ut-docs#1832 hooks above.
    var issueVatField = card.querySelector('#split-tender-issue-vat-field');
    var redeemRow = card.querySelector('#split-tender-voucher-redeem');
    var redeemInfo = card.querySelector('#split-tender-voucher-redeem-info');
    var redeemBtn = card.querySelector('#split-tender-voucher-redeem-btn');
    // ut-docs#1037 (reviewer): the sale's pricing mode, for issueGross
    // below. Absent (an older cached template) reads as inclusive — the
    // pre-#1037 behaviour, and the mode in which face value already is the
    // gross, so nothing changes for a multi-purpose issue either way.
    var taxInclusive = card.getAttribute('data-tax-inclusive') !== '0';

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
    var pendingVoucherIssues = [];
    function formatMoney(units){
      return window.utCurrency.format(units);
    }

    // Only the built-in "voucher" method carries a tracked redemption —
    // pos.CompleteSale honours voucher_id on that MethodID alone; the legacy
    // 'gift' method stays a generic, untracked tender and gets no id field.
    function isVoucherMethod(){
      return !!methodSelect && String(methodSelect.value || '').trim().toLowerCase() === 'voucher';
    }

    // Reveal the voucher-id field for the voucher method and hide Change
    // (a voucher redemption can't give change — sales.go rejects it — so
    // it's forced to 0 rather than left for the operator to trip over).
    function syncVoucherField(){
      var voucher = isVoucherMethod();
      if (voucherField) voucherField.hidden = !voucher;
      if (changeField) changeField.hidden = voucher;
      if (voucher) {
        var changeInput = form.querySelector('input[name="change"]');
        if (changeInput) changeInput.value = window.utCurrency.toMajor(0);
      }
      hideRedeem();
    }

    // ut-docs#1037: the Redeem row is only ever shown by a fresh Check
    // balance result — any change of method or code hides it again, so a
    // stale "Redeem" can never apply to a different voucher than the one
    // the cashier last checked.
    function hideRedeem(){
      if (redeemRow) redeemRow.hidden = true;
      if (redeemInfo) redeemInfo.textContent = '';
      if (redeemRow) redeemRow.removeAttribute('data-voucher-id');
    }

    // ut-docs#1037: the purpose radios on the issue form. The VAT-rate field
    // is hidden unless "specific item" is selected — same hidden-attribute
    // toggle as syncVoucherField above.
    function issuePurpose(){
      if (!issueForm) return 'multi_purpose';
      var checked = issueForm.querySelector('input[name="purpose"]:checked');
      return checked && checked.value === 'single_purpose' ? 'single_purpose' : 'multi_purpose';
    }
    function syncIssuePurpose(){
      if (issueVatField) issueVatField.hidden = issuePurpose() !== 'single_purpose';
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
      syncVoucherField();
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
        if (payment.voucher_id) {
          details += '<br><small class="pill-code">' + escapeHtml(payment.voucher_id) + '</small>';
        }
        if (payment.reference) {
          details += '<br><small>' + escapeHtml(payment.reference) + '</small>';
        }
        return '<div class="payment-pill"><div><strong>' + escapeHtml(payment.method) + '</strong><div class="pill-meta">' + details + '</div></div><button type="button" class="pill-remove" data-remove-payment="' + idx + '">&times;</button></div>';
      }).join('');
      paymentsList.innerHTML = html;
    }

    // Pending voucher issues render like the payment pills: code (or the
    // "generated at checkout" stand-in for a blank one), face value, holder.
    function renderVoucherIssues(){
      if (!vouchersList) return;
      if (!pendingVoucherIssues.length) {
        vouchersList.classList.add('empty');
        vouchersList.innerHTML = '<p>' + escapeHtml(msg.msgNoPendingVouchers) + '</p>';
        return;
      }
      vouchersList.classList.remove('empty');
      vouchersList.innerHTML = pendingVoucherIssues.map(function(issue, idx){
        var code = issue.code ? '<span class="pill-code">' + escapeHtml(issue.code) + '</span>' : escapeHtml(msg.msgVoucherAutoCode);
        var details = formatMoney(issue.amount);
        if (issue.purpose === 'single_purpose') {
          // ut-docs#1037: name the kind and the rate it will be taxed at,
          // so a pending specific-item voucher is told apart from an
          // any-use one before Complete Sale.
          details += ' · ' + escapeHtml(msg.msgVoucherSinglePurpose) + ' ' + escapeHtml(formatVatRate(issue.vat_rate_bp)) + '%';
        }
        if (issue.holder_label) {
          details += '<br><small>' + escapeHtml(issue.holder_label) + '</small>';
        }
        return '<div class="payment-pill"><div><strong>' + code + '</strong><div class="pill-meta">' + details + '</div></div><button type="button" class="pill-remove" data-remove-voucher="' + idx + '">&times;</button></div>';
      }).join('');
    }

    function netPayments(){
      return payments.reduce(function(sum, payment){
        return sum + (payment.amount - (payment.change || 0));
      }, 0);
    }

    // ut-docs#1037 (reviewer): what a pending issue adds to what the
    // customer owes. A multi-purpose voucher is a 0% liability — its face
    // value, flat, as before. A SINGLE-purpose one is taxed at issue, so
    // under EXCLUSIVE pricing its VAT rides on top of the face value (under
    // inclusive the face value already contains it). Quoting the flat face
    // value there left this panel short by exactly that VAT, and the server
    // — which taxes the issue in pos.computeSaleTotals — then refused the
    // cashier's own quoted amount with "does not cover the sale total".
    // Mirrors pos.ComputeTaxBasisPoints's exclusive branch, half-up, so the
    // two round identically.
    function issueGross(issue){
      if (issue.purpose !== 'single_purpose' || taxInclusive) return issue.amount;
      var bp = Number(issue.vat_rate_bp || 0);
      if (!Number.isFinite(bp) || bp <= 0) return issue.amount;
      return issue.amount + Math.floor((issue.amount * bp + 5000) / 10000);
    }

    function voucherIssueTotal(){
      return pendingVoucherIssues.reduce(function(sum, issue){
        return sum + issueGross(issue);
      }, 0);
    }

    // What the customer owes right now: the server-rendered basket total
    // plus every voucher pending issue — a voucher-only sale (no basket
    // lines) is legitimately all voucher, and the server charges the same
    // sum (pos_api.go adds voucherIssueTotal to the basket total).
    function amountDue(){
      return basketTotal() + voucherIssueTotal();
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
      var voucherID = '';
      if (isVoucherMethod()) {
        voucherID = String(data.get('voucher_id') || '').trim();
        if (!voucherID) {
          setStatus(msg.msgVoucherIdRequired, 'error');
          return false;
        }
      }
      var changeMinor = voucherID ? 0 : toMinor(data.get('change'));
      if (changeMinor < 0) changeMinor = 0;
      if (changeMinor > amountMinor) {
        setStatus(msg.msgChangeExceeds, 'error');
        return false;
      }
      var payment = { method: method, amount: amountMinor };
      if (changeMinor > 0) {
        payment.change = changeMinor;
      }
      if (voucherID) {
        payment.voucher_id = voucherID;
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

    // Best-effort balance preview (ut-docs#1832) via the existing
    // GET /api/vouchers/{id}. Never gates Add Payment: offline, a 404 or a
    // malformed body just reports and leaves the real verdict to the tender
    // POST. When the amount box is still empty, it's pre-filled with the
    // lesser of the balance and what's still due, the natural redemption.
    async function checkVoucherBalance(){
      var input = form.querySelector('input[name="voucher_id"]');
      var id = input ? String(input.value || '').trim() : '';
      if (!id) {
        setStatus(msg.msgVoucherIdRequired, 'error');
        return;
      }
      try {
        var response = await fetch('/api/vouchers/' + encodeURIComponent(id), { headers: { 'Accept': 'application/json' } });
        if (response.status === 404) {
          setStatus(msg.msgVoucherInvalid, 'error');
          return;
        }
        if (!response.ok) {
          setStatus(msg.msgVoucherCheckUnavailable, 'error');
          return;
        }
        var payload = await response.json();
        var voucher = payload && payload.data;
        if (!voucher || voucher.status !== 'active' || typeof voucher.balance !== 'number') {
          setStatus(msg.msgVoucherInvalid, 'error');
          return;
        }
        if (voucher.purpose === 'single_purpose') {
          // ut-docs#1037: a specific-item voucher was taxed when sold and
          // is never tender — instead of pre-filling the Amount box, offer
          // the deliberate second tap: Redeem (hands the item over, drains
          // the voucher at once). The row names the value and holder so
          // the cashier sees exactly what they are committing to.
          if (redeemRow && redeemInfo) {
            var info = formatMoney(voucher.original_amount);
            if (voucher.holder_label) info += ' · ' + voucher.holder_label;
            redeemInfo.textContent = info;
            redeemRow.setAttribute('data-voucher-id', voucher.id);
            redeemRow.hidden = false;
          }
          setStatus(msg.msgVoucherSinglePurpose, 'info');
          return;
        }
        hideRedeem();
        var amountInput = form.querySelector('input[name="amount"]');
        if (amountInput && !amountInput.value) {
          var remaining = amountDue() - netPayments();
          var suggested = Math.min(voucher.balance, remaining > 0 ? remaining : voucher.balance);
          if (suggested > 0) amountInput.value = window.utCurrency.toMajor(suggested);
        }
        setStatus(fmt(msg.msgVoucherBalance, formatMoney(voucher.balance)), 'info');
      } catch (err) {
        console.error('voucher balance check failed:', err);
        setStatus(msg.msgVoucherCheckUnavailable, 'error');
      }
    }

    // ut-docs#1037: the VAT rate is typed as a percentage ("19", "7",
    // "5.5") and travels as basis points; formatVatRate is its inverse for
    // the pending pill. A comma decimal separator is accepted (de/tr
    // keyboards) — same tolerance window.utCurrency.toMinor has for amounts.
    function parseVatRateBP(raw){
      var text = String(raw || '').trim().replace(',', '.');
      if (!text) return NaN;
      var pct = Number(text);
      if (!Number.isFinite(pct) || pct < 0 || pct > 100) return NaN;
      return Math.round(pct * 100);
    }
    function formatVatRate(bp){
      var pct = Number(bp || 0) / 100;
      return Number.isInteger(pct) ? String(pct) : pct.toFixed(2).replace(/0+$/, '');
    }

    function addVoucherIssue(){
      if (!issueForm) return false;
      var data = new FormData(issueForm);
      var amountMinor = toMinor(data.get('amount'));
      if (amountMinor <= 0) {
        setStatus(msg.msgAmountPositive, 'error');
        return false;
      }
      var issue = { amount: amountMinor };
      var code = String(data.get('voucher_code') || '').trim();
      if (code) issue.code = code;
      var holder = String(data.get('holder_label') || '').trim();
      if (holder) issue.holder_label = holder;
      if (issuePurpose() === 'single_purpose') {
        var rateBP = parseVatRateBP(data.get('vat_rate'));
        if (Number.isNaN(rateBP)) {
          setStatus(msg.msgVoucherVatRateInvalid, 'error');
          return false;
        }
        issue.purpose = 'single_purpose';
        issue.vat_rate_bp = rateBP;
      }
      pendingVoucherIssues.push(issue);
      renderVoucherIssues();
      issueForm.reset();
      syncIssuePurpose();
      setStatus(fmt(msg.msgVoucherAdded, formatMoney(amountMinor)), 'success');
      return true;
    }

    // ut-docs#1037: the single-purpose hand-over. Irreversible on success
    // (the server drains the voucher in one step), so the button is
    // disabled while the request is in flight to swallow a double tap. A
    // 404/409 (unknown, already redeemed, or not a specific-item voucher
    // after all) gets the same invalid-voucher wording the balance check
    // uses; anything else is a "could not reach the till" retry prompt.
    async function redeemSinglePurposeVoucher(){
      if (!redeemRow || !redeemBtn) return;
      var id = redeemRow.getAttribute('data-voucher-id') || '';
      if (!id) {
        setStatus(msg.msgVoucherIdRequired, 'error');
        return;
      }
      redeemBtn.disabled = true;
      try {
        var response = await fetch('/api/vouchers/' + encodeURIComponent(id) + '/redeem', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', 'Accept': 'application/json' },
          body: '{}'
        });
        if (response.status === 401) {
          window.location.href = '/login';
          return;
        }
        if (response.status === 404 || response.status === 409) {
          setStatus(msg.msgVoucherInvalid, 'error');
          hideRedeem();
          return;
        }
        if (!response.ok) {
          setStatus(msg.msgNetworkError, 'error');
          return;
        }
        var payload = await response.json();
        var voucher = payload && payload.data;
        var amount = voucher && typeof voucher.original_amount === 'number' ? formatMoney(voucher.original_amount) : '';
        hideRedeem();
        var input = form.querySelector('input[name="voucher_id"]');
        if (input) input.value = '';
        setStatus(fmt(msg.msgVoucherRedeemedSingle, amount), 'success');
      } catch (err) {
        console.error('single-purpose voucher redeem failed:', err);
        setStatus(msg.msgNetworkError, 'error');
      } finally {
        redeemBtn.disabled = false;
      }
    }

    function fillRemaining(){
      var total = amountDue();
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
          body: JSON.stringify({ payments: payments, issue_vouchers: pendingVoucherIssues, offline: offlineOverrideEnabled() || !navigator.onLine })
        });
        // ut-docs#2144: unlike hold/resume/scan (hx-post forms, fixed at the
        // auth-middleware level), this is a raw fetch() with no HX-Request
        // header, so an expired session's 401 reaches here directly. Without
        // this check, the branch below would render the raw, hardcoded-
        // English JSON error body ({"data":null,"error":{...}}) as the
        // payment status line — worse than the generic banner it was meant
        // to replace. Redirect the same way every other expired-session path
        // already does; nothing to roll back, the middleware short-circuits
        // BEFORE completeTender ever runs, so no payment was taken.
        if (response.status === 401) {
          window.location.href = '/login';
          return;
        }
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
        pendingVoucherIssues = [];
        renderVoucherIssues();
        if (issueForm) issueForm.reset();
        if (issueDetails) issueDetails.open = false;
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
      pendingVoucherIssues = [];
      renderVoucherIssues();
      if (issueForm) issueForm.reset();
      syncIssuePurpose();
      clearForm();
      setStatus(msg.msgCleared, 'info');
    });
    if (methodSelect) {
      methodSelect.addEventListener('change', syncVoucherField);
    }
    if (voucherCheckBtn) {
      voucherCheckBtn.addEventListener('click', checkVoucherBalance);
    }
    var voucherIdInput = form.querySelector('input[name="voucher_id"]');
    if (voucherIdInput) {
      voucherIdInput.addEventListener('input', hideRedeem);
    }
    if (redeemBtn) {
      redeemBtn.addEventListener('click', redeemSinglePurposeVoucher);
    }
    if (issueForm) {
      issueForm.addEventListener('change', function(e){
        if (e.target && e.target.name === 'purpose') syncIssuePurpose();
      });
    }
    if (issueAddBtn) {
      issueAddBtn.addEventListener('click', addVoucherIssue);
    }
    if (vouchersList) {
      vouchersList.addEventListener('click', function(e){
        var target = e.target.closest('[data-remove-voucher]');
        if (!target) return;
        var idx = Number(target.getAttribute('data-remove-voucher'));
        if (Number.isNaN(idx)) return;
        pendingVoucherIssues.splice(idx, 1);
        renderVoucherIssues();
        setStatus(msg.msgVoucherRemoved, 'info');
      });
    }
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
    renderVoucherIssues();
    syncVoucherField();
    syncIssuePurpose();
  }

  ready(initSplitTender);
  document.addEventListener('htmx:afterSwap', initSplitTender);
  document.addEventListener('htmx:load', initSplitTender);
})();

// ut-docs#1629 (found reviewing #1625): #1625 gave the ORIGINAL Hold Sale /
// New Sale buttons in .tender-default-footer their own unambiguous
// accessible NAME while #payment-overlay is open, but did not (and, per
// its own review, correctly did not) address focus VISIBILITY — a
// different defect class. At desktop viewports where the overlay's fixed,
// right-anchored 26rem panel geometrically covers that footer (measured
// live up to ~1440px, see payment-overlay-footer-reachable-1542.spec.ts),
// the originals stay in the keyboard tab order with no visible focus
// indicator anywhere on screen: WCAG 2.2 SC 2.4.11 (Focus Not Obscured).
//
// A blanket `inert` on .tender-default-footer while the overlay is open
// was already considered and rejected by #1625's own review: the overlay
// opens non-modally (`.show()`, ut-docs#1385 — the on-screen keyboard must
// stay usable while it's open), so nothing outside it becomes inert today,
// and at WIDE viewports the originals are never covered at all and are
// driven directly by an existing spec
// (new-sale-closes-payment-overlay-1386.spec.ts, 1920x1080) — `inert`-ing
// the whole footer would silently break that already-legitimate path too,
// not just the genuinely-covered narrow one.
//
// Fix, narrower than that: `tabindex="-1"` on just these two originals,
// applied only while the overlay is open AND only while they are actually
// covered. Rather than re-encode the "~1440px" figure as a second,
// driftable magic breakpoint, this reuses the exact same center-point
// hit-test payment-overlay-footer-reachable-1542.spec.ts's own e2e spec
// already uses to prove coverage — so it tracks the real, current geometry
// (this file's responsive grid, the overlay's own CSS, RTL) instead of a
// number someone has to remember to update if any of those ever change.
// ut-docs#1674 (found by #1629's own review, same coverage sweep): the
// third footer button (Payment, the overlay's own trigger),
// `.tender-quickpay`'s one-tap charge button, and the phone-width New Sale
// duplicate (kiosk-checkout-start-phone) all measurably stayed focusable
// while 100%-covered too — #1629 deliberately left them out as beyond
// #1542/#1625's original two-button scope, not because they don't apply.
// quick-pay is the most urgent of the three: unlike Payment (activating it
// while already open is a no-op) it POSTs /api/pos/tender directly, so a
// keyboard operator could complete a charge on a control they can't see.
// All three share the exact same isCoveredByOverlay() hit-test and
// tabindex save/restore below — no new mechanism, just three more targets.
// quick-pay and the phone duplicate live outside .tender-default-footer
// (`.tender-quickpay` and `.kiosk-header.phone-fallback-only` respectively)
// so they're queried from `document`, not `footer`. (ut-docs#2702 removed
// the quick-pay row; candidates() below never named it specifically.)
//
// ut-docs#1702 (found by #1674's own review, same coverage sweep, with
// #1674's fix already applied): the hand-maintained targets array above
// doesn't generalize — the reviewer measured 11 (1024x600) / 8 (1280x800)
// / 4 (1920x1080) OTHER focusable controls on the sale screen (the scan
// input, products-add-link, the active category tab, product tiles, the
// scan-row Add button, .osk-toggle, …) still geometrically covered by the
// open overlay and still reachable, none of them in the array. Rather than
// a 6th/7th/... hardcoded entry, `candidates()` below queries every
// normally-focusable element outside `#payment-overlay` FRESH on each
// run (not a load-time snapshot like the old `targets` — the product grid
// is dynamic) and feeds it through the identical isCoveredByOverlay() hit-
// test. The old `targets` array and its `.filter(Boolean)` guard are gone;
// candidates() subsumes it (the 5 elements it named all match the broad
// selector below and are outside the overlay, so behavior for them is
// unchanged) and its own tests keep passing unmodified.
(function () {
  var overlay = document.getElementById('payment-overlay');
  if (!overlay) return;

  var SAVED_ATTR = 'data-a11y-tabindex-saved';
  // Deliberately broad — anything that can normally receive focus.
  // Elements this misses (a bare div with a click handler and no role/
  // tabindex, say) were never keyboard-reachable to begin with, so
  // leaving them out is correct, not a gap.
  var FOCUSABLE_SELECTOR = 'a[href], area[href], button, input, select, textarea, [tabindex]';

  function isCoveredByOverlay(el) {
    if (!overlay.open) return false;
    var r = el.getBoundingClientRect();
    if (r.width === 0 && r.height === 0) return false; // not rendered
    var at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
    return !!at && (at === overlay || overlay.contains(at));
  }

  // Queried fresh on every updateFocusability() call — the product grid,
  // held-sales list, etc. can change between one overlay-open and the
  // next, so a cached NodeList would silently stop covering new controls.
  function candidates() {
    return Array.prototype.filter.call(document.querySelectorAll(FOCUSABLE_SELECTOR), function (el) {
      if (overlay.contains(el)) return false; // never touch the overlay's own controls
      if (el.disabled) return false;
      var tabindex = el.getAttribute('tabindex');
      // tabindex="-1" with no SAVED_ATTR means something else (e.g. the
      // roving-tabindex ARIA-tabs pattern on inactive category tabs)
      // deliberately made this unfocusable on purpose — leave it alone,
      // don't fold it into our own save/restore bookkeeping.
      if (tabindex === '-1' && !el.hasAttribute(SAVED_ATTR)) return false;
      return true;
    });
  }

  function updateFocusability() {
    candidates().forEach(function (el) {
      if (isCoveredByOverlay(el)) {
        if (!el.hasAttribute(SAVED_ATTR)) {
          el.setAttribute(SAVED_ATTR, el.getAttribute('tabindex') || '');
        }
        el.setAttribute('tabindex', '-1');
      } else if (el.hasAttribute(SAVED_ATTR)) {
        var saved = el.getAttribute(SAVED_ATTR);
        if (saved) {
          el.setAttribute('tabindex', saved);
        } else {
          el.removeAttribute('tabindex');
        }
        el.removeAttribute(SAVED_ATTR);
      }
    });
  }

  // Every close path (.close() from the header ✕ button, every tender
  // hx-on::after-request success handler) removes the `open` attribute the
  // same native way — observing it, rather than hooking each call site
  // individually, catches all of them uniformly including any future one.
  // (Escape does NOT close this dialog — it's .show()n, not
  // .showModal()'d, ut-docs#1385, so Escape-to-dismiss only applies to the
  // top-layer/modal case and isn't a path here at all.) `.show()` sets the
  // same attribute to open, so this also covers the single .payment-trigger
  // open path with no separate hook.
  new MutationObserver(updateFocusability).observe(overlay, { attributes: true, attributeFilter: ['open'] });
  // The overlay never moves once open, but the covered/not-covered
  // boundary is a real, live viewport width, not a one-time computation —
  // a kiosk browser window can resize (or an operator can rotate/resize a
  // desktop window) while it's open. ut-docs#1702: a full-DOM sweep is
  // more work per call than the old 5-element array, and a drag-resize can
  // fire many `resize` events per second — coalesce to at most one sweep
  // per animation frame rather than one per event.
  var resizeRafId = null;
  window.addEventListener('resize', function () {
    if (!overlay.open) return;
    if (resizeRafId) return;
    resizeRafId = requestAnimationFrame(function () {
      resizeRafId = null;
      updateFocusability();
    });
  });
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

// ADR-0098: app.js loads once per document (defer) -- the readyState guard
// covers the deferred-script case; boosted arrivals come through afterSwap.
if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', scheduleToastDismiss);
else scheduleToastDismiss();
document.addEventListener('htmx:afterSwap', scheduleToastDismiss);

// ut-docs#2162: web/ui/layouts/base.html's own <title> only ever renders
// on a full-page response — an in-panel swap (/items, /admin,
// /help/{topic}) never touches document.title at all, so the browser tab
// kept showing whichever shell's own bare-GET title it started on.
// internal/httpx.RenderContentFragment/RenderPartial (Go side) set the
// X-UT-Page-Title response header (percent-encoded — see that header's own
// comment for why: getResponseHeader() reads bytes as Latin-1, not UTF-8,
// and this product ships ar/fa/tr locales) whenever the swapped fragment's
// template data carries a "title". This is the one place that reads it
// back. Scoped to an explicit swap-target allowlist, NOT "any response
// carrying the header": /import's modal (#import-modal) also renders
// through RenderContentFragment and would otherwise wrongly retitle the
// tab to "Import" while just a dialog is open over the real panel.
document.addEventListener('htmx:afterSwap', function (evt) {
  var target = evt.detail && evt.detail.target;
  if (!target || ['items-panel', 'admin-panel', 'manual-panel'].indexOf(target.id) === -1) return;
  var xhr = evt.detail.xhr;
  var encoded = xhr && xhr.getResponseHeader('X-UT-Page-Title');
  if (!encoded) return;
  try { document.title = decodeURIComponent(encoded); } catch (_) {}
});

// ut-docs#2319: the All grid's "load more" button (buttons.html's
// all-more-button, hx-swap="outerHTML" on itself) drops keyboard focus to
// <body> once it retires itself, the same class of bug utTileJiggle's
// exit() already guards against for a different control (this file's own
// comment there: "keep keyboard focus on the screen rather than letting it
// fall to <body>"). Per the verified htmx 1.9.12 outerHTML mechanics
// documented on the fade-in-on-swap listener below, `evt.detail.target` is
// the OLD, detached button by the time this fires, but its `id` attribute
// survives detachment — so a same-id lookup finds the live replacement
// whenever more items remain (AllMore's own fragment re-renders the same
// #buttons-all-more id).
//
// Scope, corrected in independent review (ut-docs#2319 review, verified
// against the vendored htmx.min.js, not assumed):
//
//  1. htmx ALREADY re-focuses a same-id replacement by itself — its swap
//     closure saves document.activeElement before the swap and afterwards,
//     if that element left the document (`se()`/bodyContains) and carries
//     an id, does getElementById(id).focus(). So the "more remain" branch
//     below is defence-in-depth, not the load-bearing fix; the case htmx
//     genuinely cannot handle is EXHAUSTED — no replacement button exists
//     at all — where focus goes to the last tile the response just
//     appended, mirroring exit()'s own "first real tile" fallback rather
//     than leaving focus on a detached node.
//  2. htmx fires htmx:afterSwap once per INSERTED ELEMENT (`oe(n.elts, …)`
//     in the minified source; the outerHTML handler `Ie()` pushes every
//     inserted element node into that list), so one "load more" click
//     dispatches it up to AllTabPageSize + 1 = 201 times, all carrying the
//     same detail.target. Re-running the exhausted branch's
//     querySelectorAll over a fully-loaded All grid 201 times is a real,
//     avoidable hitch on the Raspberry Pi kiosk this card exists to speed
//     up, so the request's own xhr is used as a once-per-swap token. Every
//     dispatch happens after ALL nodes are inserted and after `Ie()` has
//     removed the old button, so acting on the first one is correct.
(function () {
  var lastSwap = null; // the xhr of the swap already handled
  document.addEventListener('htmx:afterSwap', function (evt) {
    var d = evt.detail;
    var oldTarget = d && d.target;
    if (!oldTarget || oldTarget.id !== 'buttons-all-more') return;
    if (d.xhr && d.xhr === lastSwap) return;
    lastSwap = d.xhr || null;
    var next = document.getElementById('buttons-all-more');
    if (next) { next.focus(); return; }
    var grid = document.getElementById('buttons-grid-all');
    var tiles = grid ? grid.querySelectorAll('.btn-tile[data-code]') : [];
    var last = tiles[tiles.length - 1];
    if (last) last.focus();
  });
})();

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

// Copy-to-clipboard buttons (ut-docs#2720: Settings → Diagnostic mode's
// "Copy folder path" / "Copy diagnostics"). Delegated so it survives the
// card's htmx outerHTML swaps. navigator.clipboard needs a secure context
// (127.0.0.1 is one; a till opened over the LAN by IP is not), so fall back
// to a hidden textarea + execCommand('copy'). The button briefly shows its
// data-copied-label (already localized by the template) as confirmation.
document.addEventListener('click', function(e){
  var btn = e.target.closest ? e.target.closest('[data-copy-target]') : null;
  if (!btn) return;
  var src = document.querySelector(btn.getAttribute('data-copy-target'));
  if (!src) return;
  var text = src.textContent || '';
  function done(){
    var label = btn.getAttribute('data-copied-label');
    if (!label) return;
    var orig = btn.textContent;
    btn.textContent = label;
    setTimeout(function(){ btn.textContent = orig; }, 1500);
  }
  function fallback(){
    var ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    try { if (document.execCommand('copy')) done(); } catch (_) {}
    document.body.removeChild(ta);
  }
  if (navigator.clipboard && window.isSecureContext) {
    navigator.clipboard.writeText(text).then(done, fallback);
  } else {
    fallback();
  }
});

// Shrinkage reason sheet (void/comp/waste, ut-docs#1465, G41) — delegated
// so it survives every #basket outerHTML swap, same reasoning as the
// notice-dismiss handler just above. The sheet is a real <dialog>
// (web/ui/partials/basket.html's own comment on why: a top-layer dialog
// can't be clipped by .basket-scroll's overflow:auto the way an
// absolutely-positioned popover anchored to the row could). .show(), not
// .showModal() — same reasoning as elevation_prompt.html's own dialog
// (showModal() would make the rest of the page, including the on-screen
// keyboard, inert). The toggle opens/closes the dialog client-side only;
// the three reason buttons inside it are plain htmx POSTs.
document.addEventListener('click', function(e){
  var toggle = e.target.closest ? e.target.closest('.shrinkage-remove-toggle') : null;
  if (toggle) {
    var sheet = toggle.nextElementSibling;
    if (!sheet || !sheet.classList.contains('shrinkage-sheet')) return;
    var opening = !sheet.open;
    // Only one sheet open at a time — closing every other one first means
    // a second tap never leaves two dialogs open over two different lines.
    document.querySelectorAll('.shrinkage-sheet').forEach(function(s){ if (s.open) s.close(); });
    document.querySelectorAll('.shrinkage-remove-toggle').forEach(function(b){ b.setAttribute('aria-expanded', 'false'); });
    if (opening) { sheet.show(); toggle.setAttribute('aria-expanded', 'true'); }
    return;
  }
  var cancel = e.target.closest ? e.target.closest('.shrinkage-sheet-cancel') : null;
  if (cancel) {
    var openSheet = cancel.closest('.shrinkage-sheet');
    if (openSheet) openSheet.close();
    return;
  }
  // Tap-elsewhere dismisses without removing (card's own UX requirement) —
  // any click that lands outside every open sheet/toggle closes them all.
  // A non-modal .show() dialog has no backdrop of its own to catch this,
  // so it's handled the same way as everything else here: delegation.
  if (!e.target.closest || !e.target.closest('.shrinkage-remove')) {
    document.querySelectorAll('.shrinkage-sheet').forEach(function(s){ if (s.open) s.close(); });
    document.querySelectorAll('.shrinkage-remove-toggle[aria-expanded="true"]').forEach(function(b){ b.setAttribute('aria-expanded', 'false'); });
  }
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
  // fires htmx:responseError instead and discards the body. That's fine
  // when the generic #pos-alert fallback below is the best available
  // answer, but several admin-page handlers that fail (print/labels,
  // print/test, invoice issue, backup now/restore, sync join/promote, …)
  // still render a real, translated `.muted` fragment straight into their
  // own hx-target — a SPECIFIC answer (which barcode conflicted, why the
  // sync join failed) the operator needs, not a generic "something failed"
  // banner. Even now that #pos-alert exists on every page (ut-docs#2183),
  // discarding that fragment and falling back to the generic banner would
  // still lose the actionable detail, so this still needs a force-swap
  // into the real hx-target rather than the fallback (ut-docs#916).
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
    // ut-docs#2179: httpx.RenderError (page routes' last-resort error
    // renderer) has no htmx-fragment awareness — it always answers with a
    // full base-templated HTML document (own <head>, own nav), non-2xx,
    // Content-Type text/html, non-empty. That satisfies every check above
    // just like a real targeted `.muted` fragment does, but force-swapping
    // a whole document as innerHTML into a small panel target (#admin-panel/
    // #items-panel) doesn't render anything sane — exactly the "silent
    // no-op" this card reports, one layer deeper than a missing #pos-alert
    // element. A real fragment meant for a swap target is never a full
    // document, so this is a safe, cheap discriminator: fall through to
    // htmx:responseError/showAlert instead, same as a plain-text/empty body.
    if (/^\s*(<!doctype html|<html)/i.test(d.serverResponse)) return;
    d.shouldSwap = true;
    d.isError = false;
  });
  document.body.addEventListener('htmx:responseError', function(){ showAlert('server'); });
  document.body.addEventListener('htmx:sendError', function(){ showAlert('network'); });
  // ut-docs#2227: /api/pos/scan retargets a parent-code scan onto
  // #modifier-modal (HX-Retarget/HX-Reswap) when the item has sellable
  // variants, and fires this event via HX-Trigger-After-Swap once the
  // picker markup has actually swapped in -- HX-Trigger alone would fire
  // before the swap, calling showModal() on a still-empty <dialog>.
  document.body.addEventListener('open-modifier-modal', function(){
    var m = document.getElementById('modifier-modal');
    // review finding, non-blocker 4: a wedge/HID scanner submits the scan
    // row programmatically regardless of focus, so a second parent-code
    // scan can fire while the picker from a first one is still open --
    // showModal() on an already-open <dialog> throws InvalidStateError.
    // The swapped-in markup (the new item) still replaces the old one first.
    if (m && !m.open) m.showModal();
  });
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

// utTileJiggle (ut-docs#2339): iOS-springboard-style edit mode for the
// sell-screen quick-button grid. A long-press (~500ms hold, cancelled by
// >10px movement) or a right-click/contextmenu on a tile puts the WHOLE
// #buttons-grid into .jiggle-mode: every tile wobbles in place (app.css's
// ut-jiggle), grows two corner badges (edit = a plain link into the
// catalog, remove = an hx-post/hx-confirm button -- both server-rendered in
// buttons.html's product-tile, so entering the mode is a pure class toggle
// with ZERO network calls), and can be dragged to reorder. Done / Escape /
// a tap outside the grid exits, and THAT is the one moment the new order
// is POSTed to /api/buttons/reorder -- never per drag step.
//
// This replaced the ut-docs#2285 per-tile long-press sheet (#tile-sheet,
// GET /ui/pos/tile-sheet, POST /api/buttons/move -- all gone). The hold
// detection itself is that card's, unchanged: HOLD_MS/MOVE_CANCEL_PX and
// the delegated document-level listeners; only what a successful hold DOES
// changed. Its trailing-click problem is the same too -- a real long-press
// still ends in the SAME native 'click' htmx's own hx-trigger="click" scan
// handler is listening for on the tile (pointerup -> click is the browser's
// standard sequence, hold or not) -- and is solved more simply now: while
// the mode is on, EVERY click on a tile is eaten in the capture phase,
// which covers the hold's own trailing click, a tap on a jiggling tile,
// and a keyboard Enter/Space on a focused one alike. Badges are siblings
// of the tile, not children (see product-tile), so their clicks are never
// matched by that check and go through untouched.
//
// Drag is Pointer Events, never HTML5 drag-and-drop: WebKitGTK (and
// browsers generally) never synthesize dragstart/dragover/drop from a
// touch pointer on the real till hardware -- the reason buttons_admin.html
// abandoned HTML5 DnD (ut-docs#1221). Same shape as tables.html's
// floor-plan editor (pointerdown captures the pointer, pointermove tracks
// it, pointerup/pointercancel commit -- NOT lostpointercapture, see
// capture() below for why that differs from tables.html), adapted from a
// freeform XY canvas to a grid: on every move the pointer is hit-tested
// against the sibling cells of the dragged tile's own .grid, and when it
// crosses a sibling's midpoint (along the reading direction, so RTL flips
// for free) the dragged cell is moved before/after it in the DOM -- the
// siblings reflow live (FLIP-animated on the .tile-cell wrapper, app.css
// .shuffling) so it reads as tiles sliding out of the way, iOS-style.
// Reordering is confined to the tile's own .grid: dragging a tile into
// ANOTHER category's grid would mean re-categorising the item, which is
// catalog data, not button order -- the Designer/catalog own that.
//
// Persisting: /api/buttons/reorder rewrites sort_order = index for EVERY
// code it's given, so it must get the full global list, and this grid's
// DOM order is grouped by category, not global. Each tile carries its
// global index (data-pos, ui.ButtonVM.Pos); orderedCodes() re-deals each
// .grid's tiles, in their new DOM order, into the set of global slots that
// same grid already occupied -- so a drag within one category never moves
// another category's buttons in the Designer's flat list (the outcome the
// retired sheet's server-side "nearest same-category neighbour" Move had).
// Only the SET of slots per grid matters, so a stale data-pos after a
// persisted reorder is harmless; they're refreshed client-side anyway.
//
// Scoped to the sale screen by the #buttons-grid ancestor in every
// selector: the Designer's admin tiles (#buttons-grid-admin) and the
// category-tiles mode's popup tiles (#category-items-modal, outside
// #buttons-grid, ut-docs#2499) never match, and the plugin-contributed
// #plugin-buttons strip is a sibling of the grid, not inside it. The
// category tiles themselves (#browsing-category-tiles, ut-docs#2499) are
// not .btn-tile[data-code] either, so that mode never arms this at all.
(function () {
  var HOLD_MS = 500;
  var MOVE_CANCEL_PX = 10;
  var DRAG_START_PX = 3;      // jitter under a resting finger isn't a drag
  var EDGE_SCROLL_PX = 40;    // auto-scroll band at the products panel's top/bottom
  var EDGE_SCROLL_STEP = 10;

  var active = false;   // edit mode on
  var dirty = false;    // a reorder happened since the last persist
  var hold = null;      // { timer, pointerId, x, y, tile } -- an armed long-press
  var drag = null;      // { tile, cell, pointerId, offX, offY, moved, onLost }

  function grid() { return document.getElementById('buttons-grid'); }
  function bar() { return document.querySelector('.products-finder .jiggle-bar'); }
  // ut-docs#2294 fallout: #buttons-grid-all (the All grid, every active
  // catalog item -- not just quick buttons) renders INSIDE #buttons-grid,
  // so a plain '#buttons-grid .btn-tile[data-code]' match also catches
  // every All-grid tile. Since ut-docs#2613 the strip has no All tab, so
  // the All grid exists only as the whole grid of the all_filter_chips
  // browsing mode (ut-docs#2499), where it is the very first screen an
  // operator sees: without this exclusion a long-press/right-click there
  // would wobble/badge the WHOLE catalogue and let a drag "reorder" items
  // that were never quick buttons -- sort_order has no meaning for the
  // All grid's fixed alphabetical listing. inAllGrid() gates every entry
  // point (tileFor/badgeFor) so the mode simply never arms from there, and
  // the code-gathering helpers below (orderedCodes/refreshPositions) skip
  // any tile under #buttons-grid-all as defence in depth.
  function inAllGrid(el) { return !!(el && el.closest && el.closest('#buttons-grid-all')); }
  function tileFor(el) {
    var t = el && el.closest ? el.closest('#buttons-grid .btn-tile[data-code]') : null;
    return (t && !inAllGrid(t)) ? t : null;
  }
  function badgeFor(el) {
    var b = el && el.closest ? el.closest('#buttons-grid .tile-badge') : null;
    return (b && !inAllGrid(b)) ? b : null;
  }
  function isRTL(el) { return getComputedStyle(el).direction === 'rtl'; }
  // ut-docs#2698: controls outside #buttons-grid that are part of editing:
  // the category tabs and their '...' sheet, and the sale-screen search
  // (its open/back buttons, input and results). Deliberately NOT the whole
  // strip: its pencil link navigates to /designer, and leaving through the
  // normal exit is what persists an unsaved drag first.
  function stayInEdit(el) {
    return !!el.closest('.products-finder .tab-bar, #category-overflow-dialog, .products-strip-search, .products-strip-back, #products-search, #search-results');
  }
  function visibleCells(gridEl) {
    return Array.prototype.filter.call(gridEl.children, function (c) {
      return c.classList.contains('tile-cell') && c.getClientRects().length > 0;
    });
  }

  // ut-docs#2698: .jiggle-active on the .products-finder section mirrors
  // #buttons-grid's .jiggle-mode for what sits OUTSIDE the grid -- a
  // category tab whose tiles are all hidden (shown only while editing, so
  // its greyed tiles can be unhidden) and the search results' "Add to quick
  // buttons" action. The window event lets buttons.html's strip re-measure
  // its tab overflow now that such a tab appeared or went away.
  function markFinder(on) {
    var g = grid();
    var f = g && g.closest ? g.closest('.products-finder') : null;
    if (f) f.classList.toggle('jiggle-active', on);
    // ut-docs#2698 review F2: buttons.html's restoreGridState reads .jiggle
    // to keep a hidden-only tab selected across an edit-mode re-render
    // (the fresh section has no .jiggle-active yet at x-init), and the
    // event's detail tells it an exit happened, so it can move the
    // selection off a tab that just vanished.
    window.utSaleGridState = Object.assign({}, window.utSaleGridState, { jiggle: on });
    window.dispatchEvent(new CustomEvent('ut-jiggle-change', { detail: { active: on } }));
  }
  function enter() {
    var g = grid(), b = bar();
    if (!g) return;
    active = true;
    g.classList.add('jiggle-mode');
    if (b) b.hidden = false;
    markFinder(true);
  }
  function exit() {
    if (!active) return;
    endDrag(null);
    clearHold();
    active = false;
    var g = grid(), b = bar();
    if (g) g.classList.remove('jiggle-mode');
    markFinder(false);
    if (b) {
      // Done itself is about to be display:none'd; keep keyboard focus on
      // the screen rather than letting it fall to <body>.
      if (b.contains(document.activeElement) && g) {
        // Exclude any #buttons-grid-all tile (all_filter_chips' All grid,
        // ut-docs#2294/#2499) the same way tileFor/badgeFor already do. That
        // alone isn't enough, though: every OTHER category's panel is also
        // present-but-hidden (x-show, only the active tab's panel actually
        // renders), so its own tiles come first in DOM order whenever they
        // precede the active panel -- require real visibility too, the same
        // getClientRects() check visibleCells() already uses above.
        var candidates = Array.prototype.slice.call(g.querySelectorAll('.btn-tile[data-code]'))
          .filter(function (t) { return !inAllGrid(t) && t.getClientRects().length > 0; });
        var first = candidates[0];
        if (first) first.focus();
      }
      b.hidden = true;
    }
    if (dirty) { dirty = false; persistOrder(); }
  }

  // ---- order + persistence ----
  function orderedCodes() {
    var tiles = Array.prototype.slice.call(document.querySelectorAll('#buttons-grid .btn-tile[data-code]'))
      .filter(function (t) { return !inAllGrid(t); });
    var n = tiles.length;
    var result = new Array(n), ok = true;
    var byGrid = [];
    tiles.forEach(function (t) {
      var g = t.closest('.grid');
      var entry = null;
      for (var i = 0; i < byGrid.length; i++) { if (byGrid[i].grid === g) { entry = byGrid[i]; break; } }
      if (!entry) { entry = { grid: g, tiles: [] }; byGrid.push(entry); }
      entry.tiles.push(t);
    });
    byGrid.forEach(function (entry) {
      var slots = entry.tiles.map(function (t) { return parseInt(t.dataset.pos, 10); });
      if (slots.some(isNaN)) { ok = false; return; }
      slots.sort(function (a, b) { return a - b; });
      entry.tiles.forEach(function (t, i) {
        var slot = slots[i];
        if (slot < 0 || slot >= n || result[slot] !== undefined) { ok = false; return; }
        result[slot] = t.dataset.code;
      });
    });
    for (var i = 0; ok && i < n; i++) { if (result[i] === undefined) ok = false; }
    if (!ok) {
      // A tile without a usable data-pos (shouldn't happen -- every render
      // sets it): fall back to plain DOM order. Still a complete list of
      // every code, just grouped by category in the Designer afterwards.
      return tiles.map(function (t) { return t.dataset.code; });
    }
    return result;
  }
  // After a persisted reorder each .grid's tiles own the same slot SET
  // but in a new order -- re-deal so data-pos stays exact (see the header
  // for why even a stale one would still have been harmless).
  function refreshPositions() {
    var seen = [];
    Array.prototype.forEach.call(document.querySelectorAll('#buttons-grid .btn-tile[data-code]'), function (t) {
      if (inAllGrid(t)) return;
      var g = t.closest('.grid');
      if (seen.indexOf(g) !== -1) return;
      seen.push(g);
      var tiles = Array.prototype.slice.call(g.querySelectorAll(':scope > .tile-cell > .btn-tile[data-code]'));
      var slots = tiles.map(function (x) { return parseInt(x.dataset.pos, 10); });
      if (slots.some(isNaN)) return;
      slots.sort(function (a, b) { return a - b; });
      tiles.forEach(function (x, i) { x.dataset.pos = String(slots[i]); });
    });
  }
  function showAlert(text, kind) {
    var box = document.getElementById('pos-alert');
    if (!box) return;
    var msg = text || (kind === 'network' ? box.dataset.msgNetwork : box.dataset.msgServer);
    box.hidden = false;
    var span = box.querySelector('.notice-text');
    if (span) span.textContent = msg || '';
  }
  function refetchGrid() {
    // buttons.html's root listens for this on body and re-renders itself
    // from the server's (now authoritative) order.
    if (window.htmx) window.htmx.trigger(document.body, 'buttons-changed');
  }
  function persistOrder() {
    var codes = orderedCodes();
    if (!codes.length) return Promise.resolve();
    // ut-docs#2312 (found during that card's own merge with this one):
    // /api/buttons/reorder gates on catalog_management (checkOrElevate) --
    // a plain fetch can't tell a real 200 success apart from a 200 carrying
    // the elevation-prompt HTML (needsElevation), so it would have treated
    // "manager approval needed" as success, called refreshPositions(), and
    // left the reorder silently unpersisted with no way for a cashier to
    // ever see the PIN prompt. window.utPostWithElevation (below) is the
    // established raw-fetch counterpart of the htmx OOB-swap dialog flow
    // every other checkOrElevate site gets for free -- buttons_admin.html's
    // own persistOrder uses the identical pattern for the Designer's
    // reorder, which hits this same route. A real success or a real
    // failure both resolve below; a needs-PIN response opens the dialog
    // itself and is handled internally, never reaching onDone until the
    // dialog's own retry resolves it.
    var fd = new FormData();
    codes.forEach(function (c) { fd.append('codes', c); });
    return new Promise(function (resolve) {
      window.utPostWithElevation('/api/buttons/reorder', fd, function (res) {
        if (res.ok) { refreshPositions(); resolve(); return; }
        res.text().then(function (text) {
          showAlert((text || '').trim(), 'server');
          refetchGrid(); // the DOM shows an order that never took -- reload it
        }).then(resolve, resolve);
      }, function () {
        // Dialog cancelled: nothing was persisted -- reload so the DOM
        // matches the real, unsaved order rather than the dragged-to
        // one it's still showing.
        refetchGrid();
        resolve();
      });
    }).catch(function () {
      showAlert('', 'network');
      refetchGrid();
    });
  }

  // ---- long-press ----
  function clearHold() {
    if (hold) { clearTimeout(hold.timer); hold = null; }
  }
  document.addEventListener('pointerdown', function (e) {
    if (!grid()) return;
    if (e.button !== undefined && e.button !== 0) return; // right-click: see contextmenu below
    if (badgeFor(e.target)) return; // a badge tap is that badge's own action, never a hold or a drag
    var tile = tileFor(e.target);
    if (!tile) {
      // Outside the grid (basket, nav rail, ...) while editing: leave the
      // mode -- except the Done bar, whose own button does that on click. A
      // pointerdown INSIDE the grid but between tiles (a header, a gap) is
      // neither an exit nor a hold. ut-docs#2698: nor is anything in
      // stayInEdit() -- switching category (so another category's greyed
      // tiles can be reached) and the sale-screen search (so a removed item
      // can be added back) are part of editing now.
      if (active && !(e.target.closest && (e.target.closest('#buttons-grid') || e.target.closest('.jiggle-bar') || stayInEdit(e.target)))) exit();
      return;
    }
    if (active) { startDrag(e, tile); return; }
    clearHold();
    var h = { pointerId: e.pointerId, x: e.clientX, y: e.clientY, tile: tile };
    h.timer = setTimeout(function () {
      hold = null;
      if (navigator.vibrate) navigator.vibrate(15);
      enter();
      // The finger is still down on this tile: let the very same gesture
      // continue straight into a drag (iOS does exactly this), so a hold-
      // and-slide reorders in one motion instead of hold, lift, press again.
      startDrag({ pointerId: h.pointerId, clientX: h.x, clientY: h.y, preventDefault: function () {} }, tile);
    }, HOLD_MS);
    hold = h;
  });
  document.addEventListener('pointermove', function (e) {
    if (hold && e.pointerId === hold.pointerId &&
        (Math.abs(e.clientX - hold.x) > MOVE_CANCEL_PX || Math.abs(e.clientY - hold.y) > MOVE_CANCEL_PX)) {
      clearHold();
    }
    onDragMove(e);
  });
  document.addEventListener('pointerup', function (e) {
    if (hold && e.pointerId === hold.pointerId) clearHold();
    endDrag(e);
  });
  document.addEventListener('pointercancel', function (e) {
    if (hold && e.pointerId === hold.pointerId) clearHold();
    endDrag(e);
  });
  document.addEventListener('contextmenu', function (e) {
    var tile = tileFor(e.target);
    if (!tile) return;
    e.preventDefault(); // desktop right-click, and Android's own long-press-to-contextmenu
    clearHold();
    if (!active) enter();
  });

  // Capture phase, deliberately: must run BEFORE the tile's own bubbling
  // hx-trigger="click" handler sees the same click. Eats the hold's
  // trailing click, taps on jiggling tiles, and keyboard activation alike.
  // ut-docs#2698: a search RESULT tile too -- while editing, a tap on a
  // result must not ring it up; its own "Add to quick buttons" action (a
  // sibling, not a .btn-tile) is how a result is used in edit mode.
  document.addEventListener('click', function (e) {
    if (!active) return;
    var result = e.target.closest ? e.target.closest('#search-results .btn-tile') : null;
    if (!tileFor(e.target) && !result) return;
    e.preventDefault(); e.stopPropagation(); e.stopImmediatePropagation();
  }, true);

  // ---- drag ----
  function startDrag(e, tile) {
    if (drag) return;
    e.preventDefault(); // no text selection / native image drag under the finger
    // Offsets are measured against the CELL, never the tile: the tile
    // carries the jiggle rotation and, once .dragging, a scale, both of
    // which skew its own rect; the cell is never transformed and the tile
    // fills it exactly in edit mode (app.css).
    var rect = tile.parentElement.getBoundingClientRect();
    drag = {
      tile: tile, cell: tile.parentElement, pointerId: e.pointerId,
      offX: e.clientX - rect.left, offY: e.clientY - rect.top, moved: false
    };
    capture();
  }
  // Pointer capture is a nicety here (keeps moves coming when the finger
  // wanders off the grid or out of the window), NOT what ends the drag:
  // unlike tables.html's node, the dragged cell is moved in the DOM on
  // every reorder, and that momentary detach makes the browser drop the
  // capture (lostpointercapture) -- ending the drag on it, as tables.html
  // does, killed every drag after its first crossing (found by the e2e
  // spec: [A,B,C] dragged past C ended as [B,A,C]). So the drag ends only
  // on pointerup/pointercancel, both delegated on document and delivered
  // whether or not a capture is in effect, and capture is simply
  // re-acquired after each DOM move (the pointer is still active, so the
  // browser allows it).
  function capture() {
    if (!drag || !drag.tile.setPointerCapture) return;
    try { drag.tile.setPointerCapture(drag.pointerId); } catch (err) { /* keep dragging uncaptured */ }
  }
  function positionDragged(x, y) {
    var rect = drag.cell.getBoundingClientRect();
    var dx = x - drag.offX - rect.left, dy = y - drag.offY - rect.top;
    drag.tile.style.transform = 'translate(' + dx + 'px,' + dy + 'px) scale(1.06)';
  }
  function onDragMove(e) {
    if (!drag || e.pointerId !== drag.pointerId) return;
    var x = e.clientX, y = e.clientY;
    if (!drag.moved) {
      var r = drag.cell.getBoundingClientRect();
      if (Math.abs(x - drag.offX - r.left) < DRAG_START_PX && Math.abs(y - drag.offY - r.top) < DRAG_START_PX) return;
      drag.moved = true;
      drag.tile.classList.add('dragging');
    }
    reorderAt(x, y);
    positionDragged(x, y);
    edgeScroll(y);
  }
  // Move the dragged cell before/after the sibling cell under the pointer
  // once the pointer has crossed that sibling's midpoint along the reading
  // direction (a later sibling: past its centre toward the inline-end; an
  // earlier one: past its centre toward the inline-start).
  function reorderAt(x, y) {
    var cell = drag.cell, gridEl = cell.parentElement;
    if (!gridEl) return;
    var cells = visibleCells(gridEl);
    var from = cells.indexOf(cell);
    if (from === -1) return;
    var rtl = isRTL(gridEl);
    for (var i = 0; i < cells.length; i++) {
      var s = cells[i];
      if (s === cell) continue;
      var r = s.getBoundingClientRect();
      if (x < r.left || x > r.right || y < r.top || y > r.bottom) continue;
      var midX = r.left + r.width / 2;
      var towardEnd = rtl ? x < midX : x > midX;
      var later = i > from;
      if (later && towardEnd) { moveCell(cells, cell, function () { s.after(cell); }); }
      else if (!later && !towardEnd) { moveCell(cells, cell, function () { s.before(cell); }); }
      return;
    }
  }
  // FLIP on the sibling CELLS (the tile buttons carry the jiggle animation
  // on transform, which would override a FLIP transform set on them).
  function moveCell(cells, cell, domMove) {
    var before = cells.filter(function (c) { return c !== cell; }).map(function (c) { return { c: c, r: c.getBoundingClientRect() }; });
    domMove();
    dirty = true;
    if (drag && drag.cell === cell) capture();
    before.forEach(function (b) {
      var r2 = b.c.getBoundingClientRect();
      var dx = b.r.left - r2.left, dy = b.r.top - r2.top;
      if (!dx && !dy) return;
      var c = b.c;
      c.classList.remove('shuffling');
      c.style.transform = 'translate(' + dx + 'px,' + dy + 'px)';
      void c.offsetWidth; // commit the start frame before transitioning
      c.classList.add('shuffling');
      c.style.transform = '';
      c.addEventListener('transitionend', function done() { c.classList.remove('shuffling'); c.removeEventListener('transitionend', done); });
    });
  }
  function edgeScroll(y) {
    var sc = drag.tile.closest('.products');
    if (!sc) return;
    var r = sc.getBoundingClientRect();
    if (y < r.top + EDGE_SCROLL_PX) sc.scrollTop -= EDGE_SCROLL_STEP;
    else if (y > r.bottom - EDGE_SCROLL_PX) sc.scrollTop += EDGE_SCROLL_STEP;
  }
  function endDrag(e) {
    if (!drag || (e && e.pointerId !== undefined && e.pointerId !== drag.pointerId)) return;
    var d = drag; drag = null;
    d.tile.classList.remove('dragging');
    d.tile.style.transform = '';
    if (d.tile.hasPointerCapture && d.tile.hasPointerCapture(d.pointerId)) {
      try { d.tile.releasePointerCapture(d.pointerId); } catch (err) { /* already released */ }
    }
  }

  // ---- keyboard: Escape exits; ArrowLeft/ArrowRight on a focused tile
  // moves it one place among its visible siblings (DOM step flipped under
  // RTL, same convention as buttons.html's focusTab), so the reorder the
  // retired sheet offered by keyboard (Move earlier/later) is still there.
  document.addEventListener('keydown', function (e) {
    if (!active) return;
    // ut-docs#2698: Escape in the search box closes the search (its own
    // handler), not edit mode as well.
    if (e.key === 'Escape' && e.target.closest && e.target.closest('#products-search')) return;
    if (e.key === 'Escape') { e.preventDefault(); exit(); return; }
    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
    var tile = tileFor(e.target);
    if (!tile) return;
    e.preventDefault();
    var cell = tile.parentElement, gridEl = cell.parentElement;
    var cells = visibleCells(gridEl);
    var i = cells.indexOf(cell);
    var step = (e.key === 'ArrowRight') ? 1 : -1;
    if (isRTL(gridEl)) step = -step;
    var target = cells[i + step];
    if (i === -1 || !target) return;
    moveCell(cells, cell, function () { if (step > 0) target.after(cell); else target.before(cell); });
    tile.focus();
  });

  document.addEventListener('click', function (e) {
    var btn = e.target.closest ? e.target.closest('.jiggle-bar .jiggle-done') : null;
    if (btn) exit();
  });

  // The edit badge is a plain <a href> into the catalog: following it tears
  // this document down, which would silently discard an unsaved reorder --
  // the same hazard the two htmx hooks below already close for the remove
  // badge and for a grid refetch, and the one member of that family a
  // navigation (not an htmx request) takes. Found by independent review,
  // 2026-09-17: drag a tile, then tap the pencil instead of Done, and the
  // drag was lost with ZERO reorder POSTs. Persist first, then navigate.
  // Not in the capture-phase swallow above -- that one is scoped to tiles
  // and must never eat a badge's own click.
  document.addEventListener('click', function (e) {
    var edit = e.target.closest ? e.target.closest('#buttons-grid .tile-badge-edit') : null;
    if (!edit || inAllGrid(edit) || !active || !dirty) return;
    var href = edit.getAttribute('href');
    if (!href) return;
    e.preventDefault();
    dirty = false;
    // Navigate even if the POST failed: persistOrder() surfaces its own
    // error and never rejects, so this resolves either way.
    persistOrder().then(function () { window.location.href = href; });
  });

  // ---- htmx interplay ----
  // The remove badge's own hx-confirm/hx-post: if a drag is still
  // unsaved, confirm first (htmx's own question, so the operator sees the
  // same dialog either way), then persist the order, THEN let htmx issue
  // the remove -- its buttons-changed refresh re-renders the grid from the
  // server, which would otherwise silently drop the unsaved reorder.
  document.body.addEventListener('htmx:confirm', function (e) {
    var elt = e.detail && e.detail.elt;
    if (!elt || !elt.classList || !elt.classList.contains('tile-badge-remove')) return;
    if (!dirty) return; // nothing pending: htmx's own confirm + request as usual
    e.preventDefault();
    if (e.detail.question && !window.confirm(e.detail.question)) return;
    dirty = false;
    persistOrder().then(function () { e.detail.issueRequest(true); });
  });
  // Any OTHER refetch of the grid root while a reorder is unsaved (a
  // modifiers-changed from elsewhere): persist first, then re-trigger it.
  document.body.addEventListener('htmx:beforeRequest', function (e) {
    var elt = e.detail && e.detail.elt;
    if (!active || !dirty || !elt || !elt.matches || !elt.matches('.products[hx-get="/ui/buttons"]')) return;
    e.preventDefault();
    dirty = false;
    persistOrder().then(refetchGrid);
  });
  // The grid root outerHTML-swaps itself on buttons-changed (a Remove from
  // inside the mode does exactly that): the fresh render has no
  // .jiggle-mode class and a hidden bar, so put the mode back -- iOS keeps
  // jiggling after a delete too. Idempotent, so any settle is fine.
  document.body.addEventListener('htmx:afterSettle', function () {
    if (active && grid() && !grid().classList.contains('jiggle-mode')) enter();
  });
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
      // ut-docs#2157: the auth middleware's non-htmx 401 (session expired)
      // answers with Content-Type: application/json, never text/html, so it
      // falls straight past the elevation-dialog branch below into
      // onDone(r) — and every caller here (settings.html's customer-erase/
      // cleanup-catalog, reports_tab_eod.html) then parses that body and
      // renders its {code,message} error OBJECT directly, showing the
      // literal string "[object Object]" instead of a way back to sign in.
      // One fix here covers every utPostWithElevation caller.
      if (r.status === 401) { close(); window.location.href = '/login'; return; }
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

// ut-docs#2024 (moved here by ut-docs#2173): toggles the two fade classes
// app.css's ::before/::after pseudo-elements key off, for any horizontally-
// scrollable .tab-bar. Math.abs() on scrollLeft (rather than comparing
// against 0/scrollWidth directly) is what makes this correct for BOTH
// scroll-origin conventions at once — Chromium/Firefox/Safari all use "0 at
// the reading start, growing toward the reading end" for LTR and the
// mirrored "0 at the reading start, going NEGATIVE toward the reading end"
// for RTL (verified live, 360px, `dir="fa"`: `el.scrollLeft` ranges from 0
// to -(scrollWidth-clientWidth), never positive) — so a plain
// `scrollLeft > 0` check silently never fires for RTL. No overflow at all
// (fits within its box) clears both classes rather than leaving a stale
// fade from a wider viewport.
//
// Originally a local `function tabBarFade(el)` inside catalog.html's own
// inline script (the catalog item-form tab strip, ≤700px only). ut-docs#2173
// added a second call site — the sale screen's always-single-row category
// strip (index.html) — and the scroll-origin handling above is subtle
// enough that it must not be duplicated, so it moved here once. catalog.html
// keeps a same-named local wrapper that delegates to this, so its own
// comments/call sites didn't need to change. ut-docs#2307 removed index.html's
// own call site again: that strip no longer scrolls at all (a category tab
// that doesn't fit is hidden behind a trailing "..." button instead of
// fading off-screen), so this is back down to catalog.html's one caller —
// left here, not deleted, since nothing about ITS own tab strip changed.
window.utTabBarFade = function (el) {
  if (!el) return;
  var max = el.scrollWidth - el.clientWidth;
  if (max <= 1) {
    el.classList.remove('tab-bar--fade-start', 'tab-bar--fade-end');
    return;
  }
  var pos = Math.abs(el.scrollLeft);
  el.classList.toggle('tab-bar--fade-start', pos > 1);
  el.classList.toggle('tab-bar--fade-end', pos < max - 1);
};

// ut-docs#2223: after every in-page htmx swap, replay a short opacity ease
// on the swapped-in region (app.css's .ut-swap-fx/@keyframes ut-swap-in) —
// zero latency: no swap delay, no settle-timing dependency, ut-docs#239's
// defaultSettleDelay:0 stays untouched. Compositor-only (opacity); restarts
// on the next swap (interrupting, never queuing); skipped entirely under
// prefers-reduced-motion.
//
// Which element to animate — verified against the actual vendored
// web/public/vendor/htmx.min.js (1.9.12), not assumed:
// `evt.detail.target` is the element htmx resolved as the swap target
// BEFORE the swap ran. For an "innerHTML"-style swap (the default) that
// element is never removed, so it's still the right, live node afterward.
// But for `hx-swap="outerHTML"` (e.g. #basket) htmx's internal outerHTML
// handler (`Ie()` in the minified source) inserts the new content, drops
// the OLD node from its own settle-info list, and only THEN removes the
// old node from the document — `detail.target` is never repointed at the
// replacement, so by the time "htmx:afterSwap" fires it's a DETACHED
// element (`!isConnected`). Confirmed by tracing `Ie`/`ce`/`Mr` in
// htmx.min.js: `ce()` (the event dispatcher) sets `detail.elt` to whatever
// node the event is actually dispatched ON, which for outerHTML IS the
// live replacement — so that's the fallback once `target.isConnected` is
// false. A same-id lookup is tried first since it's the simplest correct
// answer for the common case (the replacement partial keeps the same root
// id, e.g. basket.html's `id="basket"`).
(function () {
  var mq = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)');
  // afterSETTLE, not afterSwap (independent review + tester trace,
  // 2026-09-16): for an id-matched target (every outerHTML swap here, e.g.
  // #basket) htmx's settle step clones the OLD element's attributes onto
  // the new one and then restores the new ones -- overwriting `class`. It
  // runs synchronously right after htmx:afterSwap (settleDelay is 0), so a
  // class added in afterSwap was wiped before the first frame: the
  // animation never ran once, and a MutationObserver-based test still saw
  // the transient add and passed. afterSettle fires after that restore,
  // in the same tick -- still zero added latency.
  document.addEventListener('htmx:afterSettle', function (evt) {
    if (mq && mq.matches) return;
    var d = evt.detail || {};
    // Only a swap the OPERATOR caused gets the ease (independent review,
    // 2026-09-16): htmx fires afterSwap identically for `hx-trigger="load"`
    // and `every Ns` polls — the orders list, the customer-facing counter
    // display, the floor plan, and the rail's sync/fiscal/diagnostics chips
    // all re-swap on a timer with unchanged content, and would otherwise
    // dim to 55% and fade back every 15–30 s (inside the rail the card says
    // must read as fixed). htmx 1.9.12 sets requestConfig.triggeringEvent
    // to the DOM event that issued the request (click/submit/keyup/custom
    // events like `buttons-changed from:body`) and leaves it undefined for
    // load/every/htmx.ajax-without-event — exactly the split we want.
    var rc = d.requestConfig;
    if (!rc || !rc.triggeringEvent) return;
    // ADR-0098: a boosted page navigation swaps #ut-page under its own
    // same-document View Transition (the ADR-0097/ADR-0118 root push) -- one motion,
    // not the slide plus this ease on top.
    if (rc.boosted && d.target && d.target.id === 'ut-page') return;
    // `hx-swap="none"` swaps nothing, but htmx still fires afterSwap on the
    // target (its issuing element for every one of the ~45 such sites,
    // e.g. whole settings forms and the catalog delete button) — no
    // content changed, so no ease.
    var issuer = d.elt;
    var swapOwner = issuer && issuer.closest ? issuer.closest('[hx-swap]') : null;
    if (swapOwner && swapOwner.getAttribute('hx-swap') === 'none') return;
    var t = d.target;
    if (t && !t.isConnected) {
      t = (t.id && document.getElementById(t.id)) || d.elt;
    }
    if (!t || !t.classList || t === document.body || t === document.documentElement) return;
    // ut-docs#2338: the rail-driven in-panel swaps (#items-panel,
    // #admin-panel, #manual-panel -- same allowlist as the X-UT-Page-Title
    // listener above) are master-detail navigation, not a live update --
    // app.css's .ut-panel-fx (a slightly longer, still zero-latency,
    // opacity-only ease) replaces the generic .ut-swap-fx for these
    // targets specifically.
    var isPanelNav = ['items-panel', 'admin-panel', 'manual-panel'].indexOf(t.id) !== -1;
    var cls = isPanelNav ? 'ut-panel-fx' : 'ut-swap-fx';
    // Restart only when an ease is still running (a second swap inside
    // 150 ms) — the forced reflow is not free on the sale screen's basket.
    if (t.classList.contains(cls)) { t.classList.remove(cls); void t.offsetWidth; }
    t.classList.add(cls);
  });
  document.addEventListener('animationend', function (e) {
    if ((e.animationName === 'ut-swap-in' || e.animationName === 'ut-panel-in') && e.target && e.target.classList) {
      e.target.classList.remove('ut-swap-fx', 'ut-panel-fx');
    }
  });
})();

// ut-docs#2282: WHEN/WHERE the sale screen asks the cashier dine-in or
// takeaway is a shop setting (Settings → Dine-in/takeaway prompt) with
// three placements -- "top" (today's always-visible toggle, unchanged, the
// default) needs nothing from this file at all. "before_item" and
// "at_pay" both gate on the SAME two pieces of live state, read fresh on
// every check rather than cached, since either can change under this
// script between checks (a #basket swap, a settings change on another
// tab):
//   - <body data-order-type-prompt-mode> (base.html/httpx.InitOrderTypePromptMode)
//     -- WHICH placement is configured. Lives on <body>, not #basket,
//     because it never changes basket-to-basket and #basket gets replaced
//     wholesale (outerHTML) on nearly every action; <body> is the one
//     element that survives every swap.
//   - #basket's data-lines-count/data-order-type-chosen (basket.html) --
//     is the basket empty, and has the cashier already answered THIS sale.
//
// Both gated actions (an item add, the Pay button) are things the cashier
// is already IN THE MIDDLE of doing when the gate fires, so the modal must
// not lose that action -- it must ANSWER, then CONTINUE it, not just answer
// and leave the cashier to repeat the tap.
//
// The item-add case is an htmx request that hasn't been SENT
// yet when the gate fires -- htmx's own htmx:confirm event (fired before
// every request, cancelable) hands back evt.detail.issueRequest, which
// looks tailor-made for "defer, then resume": a closure over the
// already-built request, invokable at any later time. It is NOT used here
// on purpose (tried first, and it real-bug-found itself out): htmx
// resolves hx-target to a concrete DOM node BEFORE htmx:confirm fires, not
// lazily at delivery time. The choice buttons below POST to
// /api/pos/order-type against that SAME #basket first and swap it
// (outerHTML) -- so by the time issueRequest() finally runs, its captured
// target is the ORIGINAL, now-detached #basket node. The deferred
// request's response really does land, but swapping into a detached node
// has NO VISIBLE EFFECT -- exactly ut-docs#1337's failure mode, self-
// inflicted here by deferring across a #basket swap instead of racing two
// live requests. htmx.ajax(verb, path, {target: '#basket', values: ...})
// below is the fix: target is a SELECTOR, re-resolved fresh against the
// CURRENT #basket at the moment it actually runs. Values are read via
// htmx.values(elt, 'post') rather than a {source: elt} option -- tried
// first, and NOT equivalent: source only affects whose hx-headers/
// hx-swap-oob context applies, it does not itself walk elt's own form
// fields/hx-vals/hx-include the way a real trigger's value-gathering does,
// which silently posted an empty body (confirmed live: the item never
// landed, no error either). htmx.values(elt, verb) is the same resolved
// value bag a real trigger on elt would have sent.
//
// at_pay's OTHER entry point, the main Pay button, is a plain onclick
// (posOpenPayment below), not an htmx request at all -- there is no
// request object to defer or replay, so its own resume is simply "open
// #payment-overlay once the modal is answered".
(function () {
  function promptMode() {
    return (document.body && document.body.dataset.orderTypePromptMode) || 'top';
  }
  function basketEmpty() {
    var b = document.getElementById('basket');
    return !b || b.dataset.linesCount === '0' || !b.dataset.linesCount;
  }
  function orderTypeChosen() {
    var b = document.getElementById('basket');
    return !!b && b.dataset.orderTypeChosen === 'true';
  }

  // ut-docs#2371: `dismissedThisSale` tracks whether the CURRENT sale's
  // load-time/sale-start prompt (maybePromptAtSaleStart below) was closed
  // WITHOUT a choice (Cancel, Escape) -- while true, that particular nag
  // is suppressed for the rest of this sale, but it never disables the
  // per-item/per-Pay GATES above and below, which still fire on their own
  // triggers regardless of this flag. Reset to false whenever a genuine
  // sale boundary is crossed (a choice is made, /api/pos/reset or
  // /api/pos/tender goes out, or the basket is observed non-empty/
  // already-answered -- see maybePromptAtSaleStart and the
  // htmx:beforeRequest listener below).
  var dismissedThisSale = false;
  // Sits alongside window.posOrderTypePromptResolve: true once the
  // CURRENTLY open prompt's Dine-in/Takeaway button has actually run its
  // resolve callback, checked by the close-event listener below to tell a
  // real choice apart from Cancel/Escape/any other close with no choice.
  var choiceMade = false;

  // Opens #order-type-prompt-modal and calls onChosen() once a real choice
  // is made (the modal's own Dine-in/Takeaway buttons set
  // window.posOrderTypePromptResolve before closing themselves, per
  // index.html's hx-on::after-request). Tapping Cancel just closes the
  // dialog -- onChosen is never called, so the gated action (item add /
  // Pay) simply does not happen, same as the cashier never having tapped
  // it. No modal in the DOM (a page that doesn't carry the sale screen's
  // markup) is a same-tick passthrough, never a stuck gate.
  //
  // ut-docs#2371: showModal(), not show() -- this dialog needs no typing
  // (see app.css's own comment on the same point), so the on-screen-
  // keyboard reason #hold-modal/#pfand-modal use .show() for does not
  // apply here, and a real top-layer modal is what the bug report asked
  // for (centred, backdropped, everything else inert). Guarded: calling
  // showModal() on an already-open dialog throws -- this can legitimately
  // happen now that the prompt can also open unprompted at sale start
  // (maybePromptAtSaleStart), so a caller racing that with its own gate
  // re-arms the resolve (its action is the one to continue) and skips only
  // the showModal() call.
  function showOrderTypePromptModal(onChosen) {
    var modal = document.getElementById('order-type-prompt-modal');
    if (!modal) { onChosen(); return; }
    choiceMade = false;
    // Arm the resolve BEFORE the already-open guard below (independent
    // review, ut-docs#2371): the prompt now opens unprompted at sale start,
    // and a wedge/camera scan arriving while it is open still reaches the
    // item gate (app.js's window-level keydown buffer submits the scan form
    // regardless of focus). That gate has already cancelled the scan's own
    // request, so if this call bailed without re-arming, the scan would be
    // silently dropped and the cashier's eventual choice would resume the
    // sale-start no-op instead -- "scan first, look at the screen second"
    // is the common sequence, so the LATEST intercepted action is the one a
    // choice must continue. Only the showModal() call itself is skipped.
    window.posOrderTypePromptResolve = function () {
      window.posOrderTypePromptResolve = null;
      choiceMade = true;
      dismissedThisSale = false;
      onChosen();
    };
    if (!modal.open) modal.showModal();
  }

  // ut-docs#2371: a close WITHOUT a choice (Cancel's onclick, or the
  // browser's own Escape handling now that this is a real showModal()
  // dialog) behaves like Cancel for the rest of THIS sale -- suppress the
  // sale-start nag, without touching the per-item/per-Pay gates. `close`
  // does not bubble, so this has to be a capturing document-level
  // listener rather than the usual delegation this file uses elsewhere.
  // Piggybacks the SAME listener to also re-run maybePromptAtSaleStart on
  // every dialog close in the app (trigger (3) below) -- ordering matters:
  // the dismissed-flag update above must happen before that re-check, or
  // a Cancel could see its own stale flag and immediately reopen itself;
  // one listener, run top-to-bottom, is the simplest way to guarantee it.
  document.addEventListener('close', function (evt) {
    if (evt.target && evt.target.id === 'order-type-prompt-modal' && !choiceMade) {
      dismissedThisSale = true;
    }
    maybePromptAtSaleStart();
  }, true);

  // ut-docs#2371: the "before_item" placement's OTHER failure mode -- the
  // prompt never fired at the START of a sale, only lazily on the first
  // add, so a cashier assembling a dine-in order behind the counter (no
  // item added yet) never saw it at all until they scanned something.
  // Fires on every plausible "a new, empty, unanswered basket is now
  // showing" moment; each one is listed on its own registration below so
  // it's obvious which real user action it corresponds to. Cheap by
  // design -- every one of these can fire many times a shift for reasons
  // that have nothing to do with this prompt (a settings save, an
  // unrelated dialog closing), so the function itself does the real
  // filtering and bails in one line the moment any condition doesn't
  // hold.
  function maybePromptAtSaleStart() {
    // The sell screen ships a PLACEHOLDER basket (index.html: <div
    // class="basket" hx-get="/ui/basket" hx-trigger="load">) that only
    // becomes #basket, with its data-lines-count/data-order-type-chosen
    // bridge, once that load swap lands. Until then there is nothing to
    // read -- basketEmpty() would answer "empty" for a basket that hasn't
    // arrived, and a prompt opened on that guess stays open even when the
    // real basket then says the choice was already made (found by the
    // Tester's driven run: answer, reload, prompt back). The load swap's
    // own htmx:afterSettle re-runs this once the real basket is in.
    var basket = document.getElementById('basket');
    if (!basket || !('linesCount' in basket.dataset)) return;
    // A non-empty or already-answered basket means a sale is genuinely in
    // progress -- the NEXT time this observes an empty, unanswered basket
    // it's a new sale, so the previous sale's dismissal no longer applies.
    if (!basketEmpty() || orderTypeChosen()) { dismissedThisSale = false; return; }
    if (promptMode() !== 'before_item' || dismissedThisSale) return;
    var modal = document.getElementById('order-type-prompt-modal');
    if (!modal || modal.open) return;
    // Never stack this over another already-open dialog -- the payment
    // overlay showing change due/a receipt, the hold dialog, the modifier
    // or category picker, an admin PIN prompt... none of those should
    // grow this prompt on top of them. The close-event trigger (above)
    // is what catches the sale-start moment once whichever dialog this
    // deferred behind actually closes.
    if (document.querySelector('dialog[open]')) return;
    showOrderTypePromptModal(function () {});
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', maybePromptAtSaleStart);
  } else {
    maybePromptAtSaleStart();
  }
  // Covers every #basket outerHTML swap (New Sale from any of its three
  // buttons, a post-tender reset, New Customer) and a boosted #ut-page
  // navigation onto the sell screen -- maybePromptAtSaleStart itself
  // decides whether any of that actually left an empty, unanswered
  // basket worth asking about.
  document.addEventListener('htmx:afterSettle', function () { maybePromptAtSaleStart(); });
  // /api/pos/reset and /api/pos/tender are sale boundaries the owner
  // explicitly wants New Sale (and the next sale after a tender) to re-ask on, even if
  // this same sale's prompt was dismissed earlier -- reset the flag here,
  // on beforeRequest, not on afterRequest: with this app's global
  // defaultSettleDelay:0 (base.html), the swap+settle for these two
  // requests' own #basket outerHTML response -- and therefore the
  // htmx:afterSettle-driven maybePromptAtSaleStart re-check above, and
  // (for the New Sale buttons that live inside #payment-overlay) that
  // button's own hx-on::after-request closing the overlay and firing the
  // close-event trigger above -- all run to completion BEFORE
  // htmx:afterRequest fires (confirmed against web/public/vendor/
  // htmx.min.js's b.onload: it calls the response handler, which settles
  // synchronously at settleDelay 0, before ever triggering
  // "htmx:afterRequest"). Resetting on afterRequest would run AFTER all
  // of that had already re-checked the still-stale flag and skipped
  // re-prompting.
  document.body.addEventListener('htmx:beforeRequest', function (evt) {
    var d = evt.detail || {};
    var p = (d.pathInfo && d.pathInfo.requestPath) || (d.requestConfig && d.requestConfig.path) || '';
    if (p === '/api/pos/reset' || p === '/api/pos/tender') dismissedThisSale = false;
  });

  // "before_item": intercept the very first item landing in an empty
  // basket. Matched by REQUEST PATH, not by the triggering element, so
  // every item-add surface is covered with one listener -- the manual
  // scan-row form, every catalog tile button, the modifier picker's
  // "Add to basket" submit, and the suggestions strip all POST to one of
  // these same two endpoints (web/ui/pages/index.html, buttons.html,
  // modifier_picker.html, suggestions.html) -- PLUS (ut-docs#2371) a
  // modifier/variant tile's OWN hx-get that opens the picker in the first
  // place (buttons.html's "product-tile" define, ~line 658; the same
  // tile inside the category-tiles popup's #category-items-modal body,
  // and any other surface, are covered for free since this listener is
  // delegated on document.body). Before ut-docs#2371 the prompt only ever
  // fired on the picker's own "Add to basket" submit (scan-with-modifiers
  // below), by which point the picker was already open -- and since it
  // opened with .show(), the prompt opened UNDERNEATH it, unreachable.
  // Gating the picker's OWN open means a modifier/variant item now asks
  // BEFORE the picker ever appears, same as a plain tile. scan-with-
  // modifiers stays wired below purely as a fallback for any path that
  // still reaches it with the gate not yet resolved -- now that the
  // prompt is itself a real top-layer modal, it stacks ABOVE #modifier-
  // modal even if that happens, so it can no longer lock the picker shut
  // the way ut-docs#2371's bug report described.
  document.body.addEventListener('htmx:confirm', function (evt) {
    var path = evt.detail.path || '';
    var isItemAdd = path === '/api/pos/scan' || path === '/api/pos/scan-with-modifiers';
    var isModifierOpen = path.indexOf('/ui/pos/modifiers') === 0;
    if ((!isItemAdd && !isModifierOpen) || promptMode() !== 'before_item' || !basketEmpty() || orderTypeChosen()) return;
    evt.preventDefault();
    var elt = evt.detail.elt;
    if (isModifierOpen) {
      // The path already carries ?item=&code= (buttons.html's hx-get) --
      // htmx.values(elt, 'get') is deliberately NOT passed here, so
      // nothing gets double-appended onto the query string. Re-run the
      // exact GET the tile itself would have issued, then open the
      // picker exactly the way its own hx-on::after-request does.
      showOrderTypePromptModal(function () {
        // ut-docs#2525: a stale tile's GET is retargeted onto #basket, which
        // leaves the (closed) picker holding the PREVIOUS item's markup --
        // empty it first so the innerHTML check below can't open that.
        var prev = document.getElementById('modifier-modal');
        if (prev && !prev.open) prev.innerHTML = '';
        htmx.ajax('get', path, { target: '#modifier-modal', swap: 'innerHTML' }).then(function () {
          // htmx 1.9 resolves this promise even when a 4xx swapped nothing
          // -- don't open an empty picker on it (the tile's own after-request
          // has the same edge; this path just closes it for free).
          var m = document.getElementById('modifier-modal');
          if (m && !m.open && m.innerHTML.trim()) m.showModal();
        });
      });
      return;
    }
    // Snapshot NOW, not inside the callback below: this same native
    // 'submit' event ALSO reaches this file's own document-level
    // 'submit' listener (ut-docs#1177, registered earlier, further up
    // this file) that unconditionally clears the scan-row's code field a
    // tick later regardless of whether htmx's own request actually goes
    // ahead -- our evt.preventDefault() above only cancels HTMX's
    // pipeline, not that unrelated sibling listener on the same native
    // event. Confirmed live: reading htmx.values(elt, 'post') from inside
    // the onChosen callback below (i.e. after the modal round-trip) came
    // back with code:"" every time, even though the cashier really did
    // type/tap a real value -- the field was already wiped before the
    // modal was even answered.
    var values = htmx.values(elt, 'post');
    showOrderTypePromptModal(function () {
      htmx.ajax('post', path, { target: '#basket', swap: 'outerHTML', values: values });
    });
  });

  // ut-docs#2702: the other "at_pay" entry point, the sale screen's one-tap
  // quick-pay button (a direct hx-post tender intercepted via htmx:confirm
  // here), is gone -- its job moved inside the payment panel, which is only
  // reachable through posOpenPayment() below, so that is now the single
  // "at_pay" gate.
  // Called by index.html's Pay button (data-testid="payment-open") in
  // place of a bare document.getElementById('payment-overlay').show().
  window.posOpenPayment = function () {
    var overlay = document.getElementById('payment-overlay');
    if (!overlay) return;
    if (promptMode() !== 'at_pay' || orderTypeChosen()) { overlay.show(); return; }
    showOrderTypePromptModal(function () { overlay.show(); });
  };
})();

// ut-docs#2762: refresh only the region a successful write changed, instead
// of window.location.reload() (a full-page flash of the whole shell). The
// region is named declaratively: the nearest ancestor of `el` carrying
// data-ut-refresh="<selector>". The current URL is re-fetched as a plain
// page GET (never cached, no HX-Request, so it is the same document a
// reload would get), the matching element is lifted out of it and swapped
// in place, then handed to htmx.process so its forms work again. The
// triggering action's own message (el, or el's hx-target) is carried over,
// so a "✓ Saved" confirmation survives the swap. Anything unexpected (no
// host, a non-2xx such as an expired session, the region missing from the
// response, a network error) falls back to the old full reload — never
// worse than before.
(function () {
  var UT = window.UT = window.UT || {};
  UT.refreshRegion = function (el) {
    var host = null;
    try {
      if (typeof el === 'string') el = document.querySelector(el);
      host = el && el.closest ? el.closest('[data-ut-refresh]') : null;
    } catch (e) { host = null; }
    var sel = host && host.getAttribute('data-ut-refresh');
    var cur = sel && document.querySelector(sel);
    if (!cur || !window.fetch || !window.DOMParser) { window.location.reload(); return Promise.resolve(false); }
    // Only the triggering action's own message is carried over (el itself,
    // or el's hx-target): a stale hint or refusal in ANOTHER row is dropped,
    // exactly as a reload dropped it.
    var keep = {};
    var msg = null;
    try {
      msg = el.id && el.matches('[aria-live]') ? el
        : (el.getAttribute && el.getAttribute('hx-target') ? document.querySelector(el.getAttribute('hx-target')) : null);
    } catch (e) { msg = null; }
    if (msg && msg.id && cur.contains(msg) && msg.innerHTML.trim()) keep[msg.id] = msg.innerHTML;
    return fetch(window.location.pathname + window.location.search, { credentials: 'same-origin', cache: 'no-store' })
      .then(function (r) {
        if (!r.ok) throw new Error('status ' + r.status);
        return r.text();
      })
      .then(function (html) {
        var fresh = new DOMParser().parseFromString(html, 'text/html').querySelector(sel);
        var live = document.querySelector(sel);
        if (!fresh || !live) throw new Error('region missing');
        fresh = document.importNode(fresh, true);
        live.replaceWith(fresh);
        Object.keys(keep).forEach(function (id) {
          var m = document.getElementById(id);
          if (m) m.innerHTML = keep[id];
        });
        if (window.htmx) window.htmx.process(fresh);
        // A page whose own script post-processes the region (catalog.html
        // re-applies its search filter) listens for this; htmx:afterSwap
        // never fires for a swap htmx did not make.
        fresh.dispatchEvent(new CustomEvent('ut:region-refreshed', { bubbles: true }));
        return true;
      })
      .catch(function () { window.location.reload(); return false; });
  };
})();

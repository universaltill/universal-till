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
  }
  window.addEventListener('keypress', function(e){
    var now = Date.now();
    if (now - last > 100) { buf = ""; }
    last = now;
    if (e.key === 'Enter') {
      e.preventDefault();
      if (buf.length > 0) submit(buf);
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
    if (value === null || value === undefined) return 0;
    var normalized = value.toString().trim();
    if (!normalized) return 0;
    var num = Number(normalized);
    if (Number.isNaN(num)) return 0;
    return Math.round(num * 100);
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
    var container = document.querySelector('.pos-container');
    var currency = (container && container.getAttribute('data-currency')) || 'GBP';
    var formatter;
    try {
      formatter = new Intl.NumberFormat(undefined, { style: 'currency', currency: currency });
    } catch (err) {
      formatter = new Intl.NumberFormat(undefined, { style: 'currency', currency: 'GBP' });
    }

    function formatMoney(units){
      return formatter.format(units / 100);
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
        changeInput.value = '0.00';
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
      var text = (totalEl.textContent || '').replace(/[^0-9.,-]/g, '').replace(/,/g, '');
      if (!text) return 0;
      var num = Number(text);
      if (Number.isNaN(num)) return 0;
      return Math.round(num * 100);
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
        amountInput.value = (remaining / 100).toFixed(2);
        amountInput.focus();
      }
      var changeInput = form.querySelector('input[name="change"]');
      if (changeInput) {
        changeInput.value = '0.00';
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
          body: JSON.stringify({ payments: payments })
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


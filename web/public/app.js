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


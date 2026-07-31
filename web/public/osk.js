// On-screen keyboard for touch tills (kiosk Pis have no OS keyboard at all).
// Self-contained, offline (ADR-0003). Modes via <body data-osk>:
//   auto (default) — only when the device reports a coarse pointer (touch)
//   on / off       — force / disable
// Layouts follow the page locale (<html lang>): en, tr, fa, ar; a numeric
// pad serves number/tel/numeric-inputmode fields. Keys write into the
// focused input and fire an `input` event so HTMX/Alpine listeners run;
// Enter submits the input's form (requestSubmit → normal submit handlers).
(function () {
  'use strict';

  var mode = (document.body.dataset.osk || 'auto');
  if (mode === 'off') return;
  // auto: show on anything that looks like a touch device. `pointer: coarse`
  // alone is unreliable on kiosk setups (a plugged-in mouse makes the PRIMARY
  // pointer fine even with a touchscreen), so also accept touch capability —
  // and if a real touch ever happens, enable from that moment on.
  var touchy = window.matchMedia('(pointer: coarse)').matches ||
    window.matchMedia('(any-pointer: coarse)').matches ||
    (navigator.maxTouchPoints || 0) > 0 || ('ontouchstart' in window);
  var enabled = (mode === 'on') || touchy;
  if (!enabled) {
    window.addEventListener('touchstart', function once() {
      enabled = true;
      window.removeEventListener('touchstart', once);
    }, { passive: true });
  }

  var LAYOUTS = {
    en: [
      ['1','2','3','4','5','6','7','8','9','0'],
      ['q','w','e','r','t','y','u','i','o','p'],
      ['a','s','d','f','g','h','j','k','l'],
      ['⇧','z','x','c','v','b','n','m','⌫'],
      ['?123',',','SPACE','.','↵']
    ],
    tr: [
      ['1','2','3','4','5','6','7','8','9','0'],
      ['q','w','e','r','t','y','u','ı','o','p','ğ','ü'],
      ['a','s','d','f','g','h','j','k','l','ş','i'],
      ['⇧','z','x','c','v','b','n','m','ö','ç','⌫'],
      ['?123',',','SPACE','.','↵']
    ],
    fa: [
      ['۱','۲','۳','۴','۵','۶','۷','۸','۹','۰'],
      ['ض','ص','ث','ق','ف','غ','ع','ه','خ','ح','ج','چ'],
      ['ش','س','ی','ب','ل','ا','ت','ن','م','ک','گ'],
      ['ظ','ط','ز','ر','ذ','د','پ','و','⌫'],
      ['?123','،','SPACE','.','↵']
    ],
    ar: [
      ['١','٢','٣','٤','٥','٦','٧','٨','٩','٠'],
      ['ض','ص','ث','ق','ف','غ','ع','ه','خ','ح','ج','د'],
      ['ش','س','ي','ب','ل','ا','ت','ن','م','ك','ط'],
      ['ئ','ء','ؤ','ر','لا','ى','ة','و','ز','ظ','⌫'],
      ['?123','،','SPACE','.','↵']
    ],
    sym: [
      ['1','2','3','4','5','6','7','8','9','0'],
      ['@','#','£','$','€','%','&','*','(',')'],
      ['-','_','+','=','/',':',';','"','\''],
      ['!','?','~','<','>','[',']','⌫'],
      ['ABC',',','SPACE','.','↵']
    ],
    num: [
      ['1','2','3'],
      ['4','5','6'],
      ['7','8','9'],
      ['.','0','⌫'],
      ['↵']
    ]
  };

  var osk = null, current = null, shift = false, layer = '';

  function baseLayout() {
    var lang = (document.documentElement.lang || 'en').slice(0, 2);
    return LAYOUTS[lang] ? lang : 'en';
  }

  function isNumeric(el) {
    return el.type === 'number' || el.type === 'tel' ||
      (el.getAttribute('inputmode') || '').match(/^(numeric|decimal|tel)$/);
  }

  function wantsOSK(el) {
    if (!el || el.readOnly || el.disabled) return false;
    // A field's own data-osk="off" (e.g. the sale screen's scan input)
    // opts out of AUTO-mode auto-show only. An admin's explicit "Always
    // on" (mode === 'on', for a kiosk with no physical keyboard at all)
    // is a stronger, deliberate signal and still wins on every field.
    if (el.dataset.osk === 'off' && mode !== 'on') return false;
    if (el.tagName === 'TEXTAREA') return true;
    if (el.tagName !== 'INPUT') return false;
    return ['text', 'search', 'password', 'email', 'url', 'number', 'tel'].indexOf(el.type) !== -1;
  }

  function build() {
    osk = document.createElement('div');
    osk.id = 'osk';
    osk.setAttribute('dir', 'ltr');
    // Keys must never steal focus from the input.
    osk.addEventListener('pointerdown', function (ev) { ev.preventDefault(); });
    osk.addEventListener('click', function (ev) {
      var btn = ev.target.closest('button[data-k]');
      if (btn) press(btn.dataset.k);
    });
    document.body.appendChild(osk);
  }

  function render() {
    var rows = LAYOUTS[layer];
    osk.innerHTML = '';
    rows.forEach(function (row) {
      var r = document.createElement('div');
      r.className = 'osk-row';
      row.forEach(function (k) {
        var b = document.createElement('button');
        b.type = 'button';
        b.dataset.k = k;
        b.className = 'osk-key' +
          (k === 'SPACE' ? ' osk-space' : '') +
          (k.length > 1 && k !== 'SPACE' && k !== 'لا' ? ' osk-fn' : '') +
          (k === '⇧' && shift ? ' osk-on' : '');
        b.textContent = k === 'SPACE' ? '' : (shift && k.length === 1 ? k.toLocaleUpperCase(baseLayout()) : k);
        r.appendChild(b);
      });
      osk.appendChild(r);
    });
  }

  function insert(text) {
    if (!current) return;
    if (typeof current.setRangeText === 'function' && current.type !== 'number' && current.type !== 'email') {
      current.setRangeText(text, current.selectionStart, current.selectionEnd, 'end');
    } else {
      current.value += text; // number/email inputs don't expose a selection
    }
    current.dispatchEvent(new Event('input', { bubbles: true }));
  }

  function backspace() {
    if (!current) return;
    if (typeof current.setRangeText === 'function' && current.type !== 'number' && current.type !== 'email') {
      var s = current.selectionStart, e = current.selectionEnd;
      if (s === e && s > 0) s -= 1;
      current.setRangeText('', s, e, 'end');
    } else {
      current.value = current.value.slice(0, -1);
    }
    current.dispatchEvent(new Event('input', { bubbles: true }));
  }

  function press(k) {
    switch (k) {
      case '⌫': backspace(); return;
      case '⇧': shift = !shift; render(); return;
      case '?123': layer = 'sym'; render(); return;
      case 'ABC': layer = baseLayout(); render(); return;
      case 'SPACE': insert(' '); return;
      case '↵':
        if (current) {
          var form = current.form;
          var el = current;
          hide();
          el.blur();
          if (form) {
            if (typeof form.requestSubmit === 'function') form.requestSubmit();
            else form.submit();
          }
        }
        return;
      default:
        insert(shift && k.length === 1 ? k.toLocaleUpperCase(baseLayout()) : k);
        if (shift) { shift = false; render(); }
    }
  }

  function show(el) {
    current = el;
    if (!osk) build();
    shift = false;
    // isNumeric reads the REAL inputmode (numeric/decimal/tel) for layout
    // selection — must happen before the inputmode="none" override below
    // overwrites it.
    layer = isNumeric(el) ? 'num' : baseLayout();
    render();
    osk.classList.add('osk-open');
    document.body.classList.add('osk-padded');
    // Suppress the native/OS on-screen keyboard while ours is up. Without
    // this, a real Android WebView shows BOTH: the page has no way to tell
    // Android "don't open your own IME," so it opens anyway on focus —
    // which resizes the WebView's viewport and breaks position:sticky/
    // fixed layout (confirmed live, real device, 2026-07-28: the status
    // bar ended up floating mid-screen, overlapping content) — while this
    // custom keyboard also tries to render in whatever space is left.
    // inputmode="none" is the standard, widely-supported way to do this;
    // insert()/backspace() below already write via setRangeText + a
    // synthetic `input` event, never needed a real IME to begin with.
    // Kiosk/desktop are unaffected (no OS virtual keyboard to suppress
    // there in the first place). Save+restore, not just remove: the
    // isNumeric() call above already needed the ORIGINAL inputmode.
    if (el.dataset.oskPrevInputmode === undefined) {
      el.dataset.oskPrevInputmode = el.getAttribute('inputmode') || '';
    }
    el.setAttribute('inputmode', 'none');
    // Keep the focused field visible above the keyboard.
    setTimeout(function () { el.scrollIntoView({ block: 'center', behavior: 'smooth' }); }, 60);
  }

  function hide() {
    if (current) {
      var prev = current.dataset.oskPrevInputmode;
      if (prev) current.setAttribute('inputmode', prev);
      else current.removeAttribute('inputmode');
      delete current.dataset.oskPrevInputmode;
    }
    current = null;
    if (!osk) return;
    osk.classList.remove('osk-open');
    document.body.classList.remove('osk-padded');
  }

  document.addEventListener('focusin', function (ev) {
    if (!enabled) return;
    if (wantsOSK(ev.target)) show(ev.target);
    else if (!osk || !osk.contains(ev.target)) hide();
  });
  document.addEventListener('focusout', function (ev) {
    // If focus lands on another OSK-able field, focusin re-shows immediately.
    setTimeout(function () {
      var a = document.activeElement;
      if (!wantsOSK(a) && (!osk || !osk.contains(a))) hide();
    }, 50);
  });

  // An autofocused field (the sale screen's scan input) can gain focus
  // BEFORE this deferred script attaches its focusin listener — on slow
  // devices (the real Pi) the parser focuses it long before osk.js
  // evaluates, so no focusin ever fires for it and the keyboard never
  // appears until focus leaves and comes back. Catch up with the current
  // focus state at init, mirroring exactly what the focusin handler would
  // have done.
  if (enabled && wantsOSK(document.activeElement)) {
    show(document.activeElement);
  }
})();

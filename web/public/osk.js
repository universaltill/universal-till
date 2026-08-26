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

  // Both declared up here, not lower down where they'd read more
  // naturally (next to guardField()/LAYOUTS's own users), because
  // guardSweep() already runs a few lines down (from the enabled/
  // touchstart setup) and, via guardField() → localeSupported(), needs
  // both. A `var` initializer only runs when its own line executes,
  // unlike a function declaration — leaving either next to its natural
  // neighbor would leave it still `undefined` the first time guardSweep()
  // actually runs.
  var oskGuarded = new WeakSet();
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
      guardSweep(document);
      updateToggles();
      window.removeEventListener('touchstart', once);
    }, { passive: true });
  } else {
    // enabled is already true at parse time (mode="on", or a coarse
    // pointer was already reported) — the touchstart branch above won't
    // fire to trigger the first sweep, so do it here instead.
    // (guardSweep/wantsOSK aren't defined yet lexically, but function
    // declarations are hoisted within this IIFE, so this call is safe.)
    guardSweep(document);
  }
  // ut-docs#1022: keep sweeping as htmx swaps in new content, and as
  // anything else (Alpine, plugin-served fragments) mutates the DOM —
  // same belt-and-braces pattern as autofill.js's suppress()/sweep(),
  // for the same reason: a per-template opt-in silently regresses the
  // moment someone adds a 29th page that forgets it.
  document.addEventListener('htmx:afterSwap', function () { guardSweep(document); });
  if (window.MutationObserver) {
    new MutationObserver(function (mutations) {
      for (var i = 0; i < mutations.length; i++) {
        var m = mutations[i];
        if (m.type === 'attributes') {
          // ut-docs#1050: a field that failed wantsOSK() when first swept
          // (readOnly/disabled/type="hidden" at that moment) can be
          // flipped into an OSK-able state later — via either the
          // `.disabled`/`.readOnly`/`.type` IDL property or the reflected
          // attribute — with no further childList mutation for the
          // observer's own addedNodes branch, below, to ever catch.
          // guardField() is idempotent (no-ops once a field is guarded,
          // and no-ops again if it still isn't OSK-able), so re-running it
          // on every such flip is safe and cheap. Scoped via
          // attributeFilter, not a bare `attributes: true` — that would
          // re-run the sweep on every htmx-driven `hx-swap-oob` attribute
          // tweak anywhere in the app, the cost this same fix originally
          // opted out of paying; `disabled`/`readonly`/`type` firing only
          // on a genuine eligibility flip (independent review, ut-docs#1050)
          // keeps that cost off the table for every other attribute write.
          guardField(m.target);
          continue;
        }
        var added = m.addedNodes;
        for (var j = 0; j < added.length; j++) {
          var node = added[j];
          if (node.nodeType !== 1) continue; // element nodes only
          guardField(node);
          if (node.querySelectorAll) guardSweep(node);
        }
      }
    }).observe(document.documentElement, {
      childList: true,
      subtree: true,
      attributes: true,
      attributeFilter: ['disabled', 'readonly', 'type']
    });
  }

  var osk = null, current = null, shift = false, layer = '';

  function baseLayout() {
    var lang = (document.documentElement.lang || 'en').slice(0, 2);
    return LAYOUTS[lang] ? lang : 'en';
  }

  function isNumeric(el) {
    // guardSweep()/show() may already have overwritten the live inputmode
    // attribute with "none" (ut-docs#1022) — read the saved ORIGINAL value
    // once one exists, never the possibly-overridden live one, or every
    // numeric field would silently fall back to the letter layout the
    // moment it's been swept.
    var im = el.dataset.oskPrevInputmode !== undefined
      ? el.dataset.oskPrevInputmode
      : (el.getAttribute('inputmode') || '');
    return el.type === 'number' || el.type === 'tel' || im.match(/^(numeric|decimal|tel)$/);
  }

  // Whether LAYOUTS has a real (non-'en'-fallback) entry for the page's own
  // locale — ut-docs#1022 review: LAYOUTS only covers en/tr/fa/ar, but the
  // shop locale can be anything a language plugin ships (de, es, ...).
  // guardField() uses this to decide whether pre-suppressing the native
  // keyboard is actually safe for a given field — see there for why.
  function localeSupported() {
    return !!LAYOUTS[(document.documentElement.lang || 'en').slice(0, 2)];
  }

  function wantsOSK(el) {
    if (!el || el.dataset.osk === 'off' || el.readOnly || el.disabled) return false;
    if (el.tagName === 'TEXTAREA') return true;
    if (el.tagName !== 'INPUT') return false;
    return ['text', 'search', 'password', 'email', 'url', 'number', 'tel'].indexOf(el.type) !== -1;
  }

  // Suppress the native OS keyboard on every OSK-able field BEFORE it is
  // ever focused, not reactively inside show() (ut-docs#1022). The browser
  // decides whether to open its own IME at FOCUS time — which, for a
  // click, happens before our click handler (and therefore show()) ever
  // runs — so a guard applied only in show() is always one interaction too
  // late for a field's first tap. This sweeps every OSK-able field up
  // front instead, central rather than per-template, so the fix doesn't
  // need a 29th one-off `{{ if ne (oskmode) "off" }}inputmode="none"{{ end }}`
  // guard in every page that ships one. index.html's scan input keeps its
  // own static template guard on top of this regardless — osk.js is a
  // separate, `defer`-loaded network fetch that can land arbitrarily late
  // (settings-osk.spec.ts deliberately delays it 700ms in one spec, and
  // still expects that field focused and IME-free by then), so a
  // server-rendered guard is the only thing that's actually always there
  // in time for an `autofocus` field, whichever of the two finishes first.
  // (oskGuarded/LAYOUTS are both declared at the top of the file — see the
  // comment there for why.)
  //
  // Gated on `enabled`, same as guardSweep() below — not just for symmetry:
  // the MutationObserver calls guardField() directly on every added node,
  // bypassing guardSweep()'s own gate, so without this an OSK-able field
  // arriving as a top-level swapped-in node (an htmx outerHTML swap whose
  // response root IS the field, an Alpine x-for clone, an oob swap) would
  // get suppressed even while OSK is fully disabled (ut-docs#1022 review).
  //
  // Non-numeric fields are additionally skipped when localeSupported() is
  // false: LAYOUTS has no layout for every locale a language plugin can
  // ship (e.g. de, es — ut-docs#1022 review), and suppressing the native
  // keyboard there with no usable replacement would trade "both keyboards
  // pop, awkwardly" for "no keyboard at all, ever" — strictly worse for
  // exactly the shops those locales serve. Numeric entry is exempt because
  // the 'num' layer is just digits, which every locale can already use.
  function guardField(el) {
    if (!enabled || !wantsOSK(el) || oskGuarded.has(el)) return;
    if (!isNumeric(el) && !localeSupported()) return;
    oskGuarded.add(el);
    if (el.dataset.oskPrevInputmode === undefined) {
      el.dataset.oskPrevInputmode = el.getAttribute('inputmode') || '';
    }
    el.setAttribute('inputmode', 'none');
  }

  function guardSweep(root) {
    if (!enabled) return;
    var els = (root || document).querySelectorAll('input, textarea');
    for (var i = 0; i < els.length; i++) guardField(els[i]);
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
    // Re-tapping the field the OSK is already open for (caret placement)
    // must not reset the layer/shift or re-trigger the smooth scroll.
    if (current === el && osk && osk.classList.contains('osk-open')) return;
    current = el;
    if (!osk) build();
    shift = false;
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
    // guardField() (above) already does this for every OSK-able field up
    // front, before it is ever focused (ut-docs#1022) — calling it again
    // here (idempotent: it no-ops once a field is in oskGuarded) is a
    // fallback for a field guardSweep() never reached (inserted between
    // sweeps by something other than htmx or a DOM mutation), and — via
    // guardField()'s own localeSupported() check — correctly stays a no-op
    // for a non-numeric field on a locale LAYOUTS can't serve, same as the
    // up-front sweep would have. Not duplicated inline here on purpose:
    // an earlier version of this fix did, and that copy silently skipped
    // the locale check, suppressing the native keyboard even where our OSK
    // has nothing to offer instead.
    guardField(el);
    // Keep the focused field visible above the keyboard. Instant, not
    // smooth: a smooth scroll animates layout for ~300-500ms after the
    // keyboard opens, and any tap landing mid-animation (a fast operator
    // going straight for a button, or an automated test) hits stale
    // coordinates — with the ut-docs#213 layout that can mean the fixed
    // OSK sheet itself, which swallows the tap silently (caught as a
    // ~40% e2e flake in settings-osk.spec.ts once sale-screen-213.spec.ts
    // ran before it).
    setTimeout(function () { el.scrollIntoView({ block: 'center' }); }, 60);
  }

  function hide() {
    // Deliberately does NOT restore the field's original inputmode
    // (ut-docs#1022): guardField() sets inputmode="none" once, up front,
    // for the life of the page while OSK is enabled. Restoring it here on
    // every blur would reopen the exact race this fix closes — the native
    // keyboard racing ours — for any field a second tap ever lands on.
    // `enabled` can flip false→true mid-page (the touchstart listener,
    // above) — that direction only ever sweeps, never restores, so it
    // doesn't need handling here either. The one way it could go true→
    // false is the settings form's full page reload (web/ui/pages/
    // settings.html), which re-renders from scratch and never applies
    // this override in the first place when the new mode is "off" —
    // nothing left to restore there.
    current = null;
    if (!osk) return;
    osk.classList.remove('osk-open');
    document.body.classList.remove('osk-padded');
  }

  // ut-docs#155: the OSK opens ONLY from a deliberate user action — a click/
  // tap on an OSK-able field, or a data-osk-toggle button. Programmatic focus
  // (the sale screen's autofocus, the checkout-start button's delayed
  // .focus() for the scanner) must never pop it; this REVERSES the earlier
  // "catch up with the autofocused field at init" behavior, per the field
  // report. Click (not focusin) is the show trigger so that tapping the
  // already-focused scan input — which fires no focusin — still opens it.
  // The toggle must never steal focus (same rule as the OSK's own keys):
  // a button takes focus on pointerdown, which fires focusin -> hide()
  // BEFORE the click handler runs — the handler then sees "closed" and
  // re-opens, so the toggle races itself and "close" only wins on lucky
  // timing (caught as an e2e flake in settings-osk.spec.ts once
  // ut-docs#213 moved the tender panel and shifted that timing).
  document.addEventListener('pointerdown', function (ev) {
    var t = ev.target.closest ? ev.target.closest('[data-osk-toggle]') : null;
    if (t) ev.preventDefault();
  });
  document.addEventListener('click', function (ev) {
    if (!enabled) return;
    var t = ev.target.closest ? ev.target.closest('[data-osk-toggle]') : null;
    if (t) {
      // Note: a toggle outside a <form> has no target and no-ops by design.
      var target = null, els = t.form ? t.form.elements : [];
      for (var i = 0; i < els.length; i++) {
        if (wantsOSK(els[i])) { target = els[i]; break; }
      }
      var open = osk && osk.classList.contains('osk-open');
      // Open for a DIFFERENT field (e.g. the qty pad): retarget to this
      // form's field instead of making the operator tap twice.
      if (open && (!target || current === target)) { hide(); return; }
      if (target) { target.focus(); show(target); }
      return;
    }
    if (wantsOSK(ev.target)) show(ev.target);
  });
  document.addEventListener('focusin', function (ev) {
    if (!enabled) return;
    // Focus moving somewhere non-OSK-able closes the keyboard; opening is
    // click-only (above).
    if (!wantsOSK(ev.target) && (!osk || !osk.contains(ev.target))) hide();
  });
  document.addEventListener('focusout', function (ev) {
    // If focus lands on another OSK-able field, its click re-shows it.
    setTimeout(function () {
      var a = document.activeElement;
      if (!wantsOSK(a) && (!osk || !osk.contains(a))) hide();
    }, 50);
  });

  // On-demand affordance: reveal any data-osk-toggle buttons (they ship
  // hidden) whenever the OSK is available at all. Mode 'off' never reaches
  // here, so the buttons stay hidden there.
  function updateToggles() {
    var btns = document.querySelectorAll('[data-osk-toggle]');
    for (var i = 0; i < btns.length; i++) btns[i].hidden = !enabled;
  }
  updateToggles();
})();

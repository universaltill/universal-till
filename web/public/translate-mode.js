// Translate mode (ADR-0160 §7, ut-docs#4370). Loaded by base.html only
// when the server rendered the page in translate mode (a manager turned it
// on; never on a self-order kiosk).
//
// 1. Markers -> data-i18n-key. In this mode the T helper appends each
//    string's key as an invisible marker (Unicode tag characters, see
//    internal/httpx/translate_mode.go). This turns every marker — in text,
//    in an attribute, in <title>, or in a string a script inserts later —
//    into a data-i18n-key attribute on the element that shows it (space-
//    separated when it shows several), and removes the marker.
// 2. Alt+click, or a long press on touch, opens #translate-panel (.show(),
//    never showModal) with that element's key(s). Alt+Enter does the same
//    for the focused element.
//
// No request ever leaves the till: the panel is filled from
// /ui/translate-mode/panel, and the Open in Weblate link is a plain link.
(function () {
  'use strict';
  if (window.UTTranslateMode) return;
  var panel = document.getElementById('translate-panel');
  var body = document.getElementById('translate-panel-body');
  if (!panel || !body) return;
  window.UTTranslateMode = true;
  document.documentElement.classList.add('ut-translate-mode');

  var HI = 0xDB40;      // every tag character's high surrogate (U+E0000..U+E03FF)
  var MARK = '\uDB40';
  var START = 0x01, END = 0x7F;

  // decode returns { text, keys } for a string carrying markers, else null
  // (also for text whose only tag characters are an emoji's).
  function decode(s) {
    if (!s || s.indexOf(MARK) < 0) return null;
    var out = '', keys = [], key = null;
    for (var i = 0; i < s.length; i++) {
      var c = s.charCodeAt(i);
      if (c === HI && i + 1 < s.length) {
        var off = s.charCodeAt(i + 1) - 0xDC00;
        if (off >= 0 && off <= 0x7F) {
          i++;
          // Only a marker's own characters are dropped: a tag character
          // outside one belongs to the text (the England, Scotland and
          // Wales flag emoji are tag sequences ending in U+E007F).
          if (off === START) { key = ''; }
          else if (key === null) { out += s.charAt(i - 1) + s.charAt(i); }
          else if (off === END) { if (key) keys.push(key); key = null; }
          else if (off >= 0x20) { key += String.fromCharCode(off); }
          continue;
        }
      }
      if (key !== null) key = null; // a broken marker: drop it
      out += s.charAt(i);
    }
    // Nothing removed (only an emoji's own tag characters): not ours. The
    // callers write back only on a change, so the observer never loops.
    if (out === s) return null;
    return { text: out, keys: keys };
  }

  function addKeys(el, keys) {
    if (!el || !keys.length) return;
    var have = (el.getAttribute('data-i18n-key') || '').split(' ').filter(Boolean);
    keys.forEach(function (k) { if (have.indexOf(k) < 0) have.push(k); });
    el.setAttribute('data-i18n-key', have.join(' '));
  }

  function fixAttrs(el) {
    if (!el.attributes) return;
    for (var i = 0; i < el.attributes.length; i++) {
      var a = el.attributes[i];
      var d = decode(a.value);
      if (!d) continue;
      el.setAttribute(a.name, d.text);
      addKeys(el, d.keys);
    }
    // A form control's live value (set from a marked value attribute
    // before this ran) follows the attribute.
    if ((el.tagName === 'INPUT' || el.tagName === 'TEXTAREA') && el.value && el.value.indexOf(MARK) >= 0) {
      var v = decode(el.value);
      if (v) { el.value = v.text; addKeys(el, v.keys); }
    }
  }

  function fixText(node) {
    var d = decode(node.nodeValue);
    if (!d) return;
    node.nodeValue = d.text;
    var p = node.parentElement;
    if (p && p.tagName !== 'SCRIPT' && p.tagName !== 'STYLE') addKeys(p, d.keys);
  }

  function sweep(root) {
    if (!root) return;
    if (root.nodeType === 3) { fixText(root); return; }
    if (root.nodeType !== 1 && root.nodeType !== 9) return;
    if (root.nodeType === 1) {
      if (root.tagName === 'SCRIPT' || root.tagName === 'STYLE') return;
      fixAttrs(root);
    }
    var els = root.querySelectorAll ? root.querySelectorAll('*') : [];
    for (var i = 0; i < els.length; i++) {
      if (els[i].tagName !== 'SCRIPT' && els[i].tagName !== 'STYLE') fixAttrs(els[i]);
    }
    var w = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, null);
    var n, texts = [];
    while ((n = w.nextNode())) { if (n.nodeValue.indexOf(MARK) >= 0) texts.push(n); }
    texts.forEach(fixText);
  }

  sweep(document);
  // Everything that arrives later: htmx swaps, script-built rows, a
  // document.title change.
  new MutationObserver(function (records) {
    records.forEach(function (r) {
      if (r.type === 'childList') r.addedNodes.forEach(sweep);
      else if (r.type === 'characterData') fixText(r.target);
      else if (r.type === 'attributes' && r.attributeName !== 'data-i18n-key') fixAttrs(r.target);
    });
  }).observe(document.documentElement, { childList: true, subtree: true, characterData: true, attributes: true });

  // --- the panel -----------------------------------------------------
  var selected = null;
  var opener = null;

  function pageNamespaces() {
    var seen = {}, out = [];
    var els = document.querySelectorAll('[data-i18n-key]');
    for (var i = 0; i < els.length; i++) {
      // The panel's and the status-bar chip's own strings are translate
      // mode itself, not the page.
      if (panel.contains(els[i]) || els[i].closest('.sb-translate')) continue;
      els[i].getAttribute('data-i18n-key').split(' ').forEach(function (k) {
        var ns = k.split('.')[0];
        if (ns && !seen[ns]) { seen[ns] = true; out.push(ns); }
      });
    }
    return out;
  }

  function open(el) {
    var keys = (el.getAttribute('data-i18n-key') || '').split(' ').filter(Boolean);
    if (!keys.length) return;
    if (selected) selected.classList.remove('ut-tm-selected');
    selected = el;
    el.classList.add('ut-tm-selected');
    opener = document.activeElement;
    var tpl = document.getElementById('translate-panel-loading');
    body.replaceChildren(tpl ? tpl.content.cloneNode(true) : '');
    if (!panel.open) panel.show();
    var url = '/ui/translate-mode/panel?key=' + encodeURIComponent(keys[0]) +
      '&keys=' + encodeURIComponent(keys.join(',')) +
      '&ns=' + encodeURIComponent(pageNamespaces().join(','));
    if (window.htmx) window.htmx.ajax('GET', url, { target: '#translate-panel-body', swap: 'innerHTML' });
    var close = panel.querySelector('[data-testid="translate-panel-close"]');
    if (close) close.focus();
  }

  panel.addEventListener('close', function () {
    if (selected) selected.classList.remove('ut-tm-selected');
    selected = null;
    if (opener && opener.focus && document.contains(opener)) { try { opener.focus(); } catch (e) { /* gone */ } }
    opener = null;
  });

  function target(e) {
    var t = e.target;
    if (!t || !t.closest || panel.contains(t)) return null;
    return t.closest('[data-i18n-key]');
  }

  // Alt+click (mouse, trackpad). Capture phase, so the page's own click
  // handler (a sale button, a link) never runs for it.
  var suppressClick = false;
  document.addEventListener('click', function (e) {
    if (suppressClick) { suppressClick = false; e.preventDefault(); e.stopImmediatePropagation(); return; }
    if (!e.altKey) return;
    var el = target(e);
    if (!el) return;
    e.preventDefault();
    e.stopImmediatePropagation();
    open(el);
  }, true);

  // Long press on touch or pen: 600 ms without moving more than 10 px. A
  // move (a scroll or a drag) or lifting the finger first cancels it, so
  // scrolling and ordinary taps behave as before. The click that follows a
  // long press is swallowed.
  //
  // Chrome on Android fires its own contextmenu at ~500 ms of a touch hold,
  // before LONG_MS, and follows it with pointercancel — which would cancel
  // the press. So a contextmenu during a press IS the long press: its menu
  // is prevented and the panel opens then; the pointercancel after it
  // finds no press left to cancel.
  var LONG_MS = 600, SLOP = 10;
  var press = null;
  function cancelPress() { if (press) { clearTimeout(press.timer); press = null; } }
  function longPress(el) {
    cancelPress();
    suppressClick = true;
    setTimeout(function () { suppressClick = false; }, 800);
    open(el);
  }
  document.addEventListener('pointerdown', function (e) {
    if (e.pointerType === 'mouse' || !e.isPrimary) return;
    var el = target(e);
    cancelPress();
    if (!el) return;
    press = { el: el, x: e.clientX, y: e.clientY, timer: setTimeout(function () { longPress(el); }, LONG_MS) };
  }, true);
  document.addEventListener('pointermove', function (e) {
    if (press && (Math.abs(e.clientX - press.x) > SLOP || Math.abs(e.clientY - press.y) > SLOP)) cancelPress();
  }, true);
  ['pointerup', 'pointercancel'].forEach(function (t) { document.addEventListener(t, cancelPress, true); });
  // The browser's own long-press menu (text selection, "copy") would cover
  // the panel on Android. A right-click with a mouse (no press) keeps its
  // menu.
  document.addEventListener('contextmenu', function (e) {
    if (press) { e.preventDefault(); longPress(press.el); return; }
    if (suppressClick && target(e)) e.preventDefault();
  }, true);

  // Keyboard: Alt+Enter on the focused element.
  document.addEventListener('keydown', function (e) {
    if (e.key !== 'Enter' || !e.altKey) return;
    var el = document.activeElement && document.activeElement.closest && document.activeElement.closest('[data-i18n-key]');
    if (!el || panel.contains(el)) return;
    e.preventDefault();
    open(el);
  }, true);
})();

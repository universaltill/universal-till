/* ut-docs#1103: roving-tabindex arrow-key navigation for WAI-ARIA radio
 * groups (APG "radio group" pattern). Same shape as setup.html's join-till
 * `focusTab`: select by .click() on the target, then focus it, so the tile's
 * own Alpine @click runs exactly as for a tap. Delegated listener in a 'self'
 * script (ut-docs#3325) - no inline eval'd code. Opt in with
 * data-roving-radiogroup on the role="radiogroup" element; tiles are
 * role="radio" with their own :tabindex/:aria-checked bindings.
 * Also drives the colour pickers, the photo-cutout background and the basket
 * order type (ut-docs#5553).
 *
 * ut-docs#5601: a second, FOCUS-ONLY branch for the built-in icon grids
 * (APG single-select listbox with grouped options). Opt in with
 * role="listbox" + data-roving-listbox on the container; items are
 * role="option" (a <button>, so Enter/Space fire its native click). Arrow
 * keys / Home / End only move focus - they never .click() - because choosing
 * an icon in the catalog is an immediate server write that replaces an
 * uploaded photo. Exactly one option holds tabindex=0 (the aria-selected one,
 * else the first visible) and window.utRovingListbox.sync(listbox) re-derives
 * that after a page repaints selection or a filter hides options.
 *
 * ut-docs#5663: in that listbox Up/Down move by VISUAL row (same column,
 * crossing sections, wrapping; rows come from rendered geometry, falling
 * back to +/-1 when the listbox has no layout), PageUp/PageDown jump to the
 * first option of the previous/next section, and typing the start of an
 * option's name focuses it (APG type-ahead; 750 ms idle window, a repeated
 * letter cycles). Left/Right/Home/End are unchanged. Still focus-only.
 * No user-facing strings here (guard-i18n scans web/public/**\/*.js). */
(function () {
  'use strict';
  // ut-docs#5553: loaded globally from base.html now; bind the delegated
  // listener once even if the script is ever evaluated twice.
  if (window.__utRadiogroupBound) return;
  window.__utRadiogroupBound = true;

  // Target of an arrow/Home/End key among `items` (DOM order, wrapping), or
  // null when the key is not a navigation key. RTL mirrors Left/Right only.
  function pick(items, cur, key, rtl) {
    var step;
    switch (key) {
      case 'ArrowDown': step = 1; break;
      case 'ArrowUp': step = -1; break;
      case 'ArrowRight': step = rtl ? -1 : 1; break;
      case 'ArrowLeft': step = rtl ? 1 : -1; break;
      case 'Home': return items[0];
      case 'End': return items[items.length - 1];
      default: return null; // Tab, Space, Enter, ... untouched
    }
    var i = items.indexOf(cur);
    if (i < 0) i = 0;
    return items[(i + step + items.length) % items.length];
  }

  // ut-docs#5663 ------------------------------------------------------
  // Sections in DOM order: "No image" (outside any group) is its own, then one
  // per .builtin-icon-group. Built from visible options only.
  function sectionsOf(listbox, opts) {
    var secs = [], keys = [];
    opts.forEach(function (o) {
      var g = o.closest('.builtin-icon-group');
      var i = g ? keys.indexOf(g) : -1;
      if (i < 0) { keys.push(g || o); secs.push({ opts: [] }); i = keys.length - 1; }
      secs[i].opts.push(o);
    });
    return secs;
  }

  // Visual rows of one section: [[opt...]], or null when geometry is not
  // available (hidden tab panel, not yet laid out). DOM order is reading order
  // (CSS grid auto-flow, also in RTL); a new row starts when top moves by more
  // than half a tile height.
  function rowsOf(section) {
    var rows = [], rowTop = 0;
    for (var i = 0; i < section.opts.length; i++) {
      var o = section.opts[i], r = o.getBoundingClientRect();
      if (r.width === 0 && r.height === 0) return null;
      if (!rows.length || Math.abs(r.top - rowTop) > r.height / 2) {
        rows.push([]); rowTop = r.top;
      }
      rows[rows.length - 1].push(o);
    }
    return rows;
  }
  function centre(o) { var r = o.getBoundingClientRect(); return (r.left + r.right) / 2; }
  // Tile of `row` whose centre is nearest cx; a tie keeps the DOM-earlier one.
  function nearest(row, cx) {
    var best = row[0], bd = Math.abs(centre(best) - cx);
    for (var i = 1; i < row.length; i++) {
      var d = Math.abs(centre(row[i]) - cx);
      if (d < bd) { best = row[i]; bd = d; }
    }
    return best;
  }

  function pickListbox(listbox, opts, cur, key, rtl) {
    if (key !== 'ArrowDown' && key !== 'ArrowUp' && key !== 'PageDown' && key !== 'PageUp') {
      return pick(opts, cur, key, rtl); // Left/Right/Home/End: #5601 behaviour
    }
    var secs = sectionsOf(listbox, opts);
    var s = -1;
    secs.forEach(function (sec, i) { if (sec.opts.indexOf(cur) !== -1) s = i; });
    if (s < 0) return pick(opts, cur, key, rtl);
    if (key === 'PageDown') return s < secs.length - 1 ? secs[s + 1].opts[0] : cur;
    if (key === 'PageUp') {
      if (cur !== secs[s].opts[0]) return secs[s].opts[0];
      return s > 0 ? secs[s - 1].opts[0] : cur;
    }
    var all = secs.map(rowsOf);
    if (all.indexOf(null) !== -1) return pick(opts, cur, key, rtl); // no geometry: +/-1
    var r = -1;
    all[s].forEach(function (row, i) { if (row.indexOf(cur) !== -1) r = i; });
    var cx = centre(cur), target;
    if (key === 'ArrowDown') {
      if (r + 1 < all[s].length) target = all[s][r + 1];
      else target = all[(s + 1) % all.length][0];
    } else if (r > 0) {
      target = all[s][r - 1];
    } else {
      var ps = all[(s - 1 + all.length) % all.length];
      target = ps[ps.length - 1];
    }
    return nearest(target, cx);
  }

  // Type-ahead: one buffer for the page, timestamp-based (no timers).
  var TA_MS = 750;
  var ta = { buf: '', at: 0, box: null };
  var swallowSpaceUp = false;
  var NON_NAV_KEYS = /^(Shift|CapsLock|NumLock|ScrollLock|Dead|Process|Unidentified|Compose|AltGraph)$/;
  function norm(s) {
    return s.toLocaleLowerCase().normalize('NFD').replace(/[\u0300-\u036f]/g, '');
  }
  function nameOf(o) {
    return norm((o.getAttribute('aria-label') || o.textContent || '').replace(/\s+/g, ' ').trim());
  }
  function typeahead(opts, cur, ch) {
    ta.buf += norm(ch);
    var cycle = /^(.)\1*$/u.test(ta.buf);
    var needle = cycle ? ta.buf.charAt(0) : ta.buf;
    var from = opts.indexOf(cur);
    if (from < 0) from = 0;
    for (var k = cycle ? 1 : 0; k <= opts.length; k++) {
      var o = opts[(from + k) % opts.length];
      if (nameOf(o).indexOf(needle) === 0) return o;
    }
    return null;
  }

  // Options a user can land on: enabled and not hidden by the option or any
  // ancestor up to the listbox ([hidden], set by icon-picker-filter.js).
  // Attribute-based (not getClientRects) so sync() also works while the
  // listbox sits in an x-show-hidden tab panel.
  function visibleOptions(listbox) {
    return Array.prototype.filter.call(
      listbox.querySelectorAll('[role="option"]'),
      function (o) {
        if (o.disabled) return false;
        for (var n = o; n && n !== listbox; n = n.parentElement) {
          if (n.hidden) return false;
        }
        return true;
      });
  }

  // leaving: called from focusout, so the option losing focus is not kept.
  function sync(listbox, leaving) {
    if (!listbox) return;
    var opts = visibleOptions(listbox);
    var stop = null;
    // Review finding (5601): a page repaint (catalog's async icon-state
    // read) can land while focus is still on an option; that option keeps
    // the stop, else Shift+Tab/Tab from it could land on the selected option
    // (a second stop in the same listbox) instead of leaving it.
    // focusout re-derives from the selection once focus really leaves.
    var active = document.activeElement;
    if (!leaving && active && opts.indexOf(active) !== -1) stop = active;
    for (var i = 0; !stop && i < opts.length; i++) {
      if (opts[i].getAttribute('aria-selected') === 'true') stop = opts[i];
    }
    if (!stop) stop = opts[0] || null;
    Array.prototype.forEach.call(listbox.querySelectorAll('[role="option"]'), function (o) {
      o.setAttribute('tabindex', o === stop ? '0' : '-1');
    });
  }
  window.utRovingListbox = { sync: function (listbox) { sync(listbox, false); } };

  document.addEventListener('keydown', function (e) {
    // A lost Space keyup must not cancel a later, unrelated Space: any other
    // key (or a fresh Space, re-set below) clears the swallow flag.
    if (e.key !== ' ' && !NON_NAV_KEYS.test(e.key)) swallowSpaceUp = false;
    if (e.altKey || e.ctrlKey || e.metaKey || e.defaultPrevented) return;
    var opt = e.target.closest && e.target.closest('[role="option"]');
    var listbox = opt && opt.closest('[role="listbox"][data-roving-listbox]');
    if (listbox) {
      if (e.isComposing) return;
      // A lone Shift/CapsLock/dead-key keydown (typing "ice Cream", "Café")
      // is part of typing, not navigation: it must not reset the buffer.
      if (NON_NAV_KEYS.test(e.key)) return;
      var opts = visibleOptions(listbox);
      var now = Date.now();
      var fresh = now - ta.at <= TA_MS && ta.box === listbox && ta.buf !== '';
      if (!fresh) ta.buf = '';
      if (Array.from(e.key).length === 1 && (e.key !== ' ' || fresh)) {
        e.preventDefault();
        if (e.key === ' ') swallowSpaceUp = true;
        var hit = typeahead(opts, opt, e.key);
        ta.at = now; ta.box = listbox;
        if (hit && hit !== opt) hit.focus();
        return;
      }
      ta.buf = ''; // any navigation key, Enter or an empty-buffer Space
      var to = pickListbox(listbox, opts, opt, e.key, getComputedStyle(listbox).direction === 'rtl');
      if (!to) return;
      e.preventDefault(); // no page/box scroll, even on a no-op
      if (to !== opt) to.focus(); // focus only: never .click()
      return;
    }
    var cur = e.target.closest && e.target.closest('[role="radio"]');
    if (!cur) return;
    var group = cur.closest('[role="radiogroup"][data-roving-radiogroup]');
    if (!group) return;
    // Visible = rendered (x-show -> display:none has no client rects); DOM order.
    var radios = Array.prototype.filter.call(
      group.querySelectorAll('[role="radio"]'),
      function (r) { return !r.disabled && r.getClientRects().length > 0; });
    var next = pick(radios, cur, e.key, getComputedStyle(group).direction === 'rtl');
    if (!next) return;
    e.preventDefault(); // no page scroll, even on a no-op
    if (next === cur) return; // R5: collapsed grid / single item -> no-op
    next.click();
    next.focus();
  });

  // Belt and braces: a Space consumed by type-ahead must not activate the
  // button on keyup.
  document.addEventListener('keyup', function (e) {
    if (swallowSpaceUp && e.key === ' ') { e.preventDefault(); swallowSpaceUp = false; }
  }, true);

  // The focused option holds the tab stop, so Shift+Tab/Tab from it behave.
  document.addEventListener('focusin', function (e) {
    var opt = e.target.closest && e.target.closest('[role="option"]');
    var listbox = opt && opt.closest('[role="listbox"][data-roving-listbox]');
    if (!listbox) return;
    Array.prototype.forEach.call(listbox.querySelectorAll('[role="option"]'), function (o) {
      o.setAttribute('tabindex', o === opt ? '0' : '-1');
    });
  });
  // Focus left the listbox: the tab stop goes back to the selected option.
  document.addEventListener('focusout', function (e) {
    var opt = e.target.closest && e.target.closest('[role="option"]');
    var listbox = opt && opt.closest('[role="listbox"][data-roving-listbox]');
    if (!listbox) return;
    var to = e.relatedTarget;
    if (to && listbox.contains(to)) return;
    sync(listbox, true);
  });
})();

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
    if (e.altKey || e.ctrlKey || e.metaKey || e.defaultPrevented) return;
    var opt = e.target.closest && e.target.closest('[role="option"]');
    var listbox = opt && opt.closest('[role="listbox"][data-roving-listbox]');
    if (listbox) {
      var opts = visibleOptions(listbox);
      var to = pick(opts, opt, e.key, getComputedStyle(listbox).direction === 'rtl');
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

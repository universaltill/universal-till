/* ut-docs#1103: roving-tabindex arrow-key navigation for WAI-ARIA radio
 * groups (APG "radio group" pattern). Same shape as setup.html's join-till
 * `focusTab`: select by .click() on the target, then focus it, so the tile's
 * own Alpine @click runs exactly as for a tap. Delegated listener in a 'self'
 * script (ut-docs#3325) - no inline eval'd code. Opt in with
 * data-roving-radiogroup on the role="radiogroup" element; tiles are
 * role="radio" with their own :tabindex/:aria-checked bindings.
 * Also drives the colour pickers, the photo-cutout background and the basket
 * order type (ut-docs#5553).
 * No user-facing strings here (guard-i18n scans web/public/**\/*.js). */
(function () {
  'use strict';
  // ut-docs#5553: loaded globally from base.html now; bind the delegated
  // listener once even if the script is ever evaluated twice.
  if (window.__utRadiogroupBound) return;
  window.__utRadiogroupBound = true;
  document.addEventListener('keydown', function (e) {
    if (e.altKey || e.ctrlKey || e.metaKey || e.defaultPrevented) return;
    var cur = e.target.closest && e.target.closest('[role="radio"]');
    if (!cur) return;
    var group = cur.closest('[role="radiogroup"][data-roving-radiogroup]');
    if (!group) return;
    var rtl = getComputedStyle(group).direction === 'rtl';
    var step;
    switch (e.key) {
      case 'ArrowDown': step = 1; break;
      case 'ArrowUp': step = -1; break;
      case 'ArrowRight': step = rtl ? -1 : 1; break;
      case 'ArrowLeft': step = rtl ? 1 : -1; break;
      case 'Home': step = 'first'; break;
      case 'End': step = 'last'; break;
      default: return; // Tab, Space, Enter, ... untouched
    }
    e.preventDefault(); // no page scroll, even on a no-op
    // Visible = rendered (x-show -> display:none has no client rects); DOM order.
    var radios = Array.prototype.filter.call(
      group.querySelectorAll('[role="radio"]'),
      function (r) { return !r.disabled && r.getClientRects().length > 0; });
    if (!radios.length) return;
    var i = radios.indexOf(cur);
    if (i < 0) i = 0;
    var next = step === 'first' ? radios[0]
      : step === 'last' ? radios[radios.length - 1]
      : radios[(i + step + radios.length) % radios.length];
    if (next === cur) return; // R5: collapsed grid / single item -> no-op
    next.click();
    next.focus();
  });
})();

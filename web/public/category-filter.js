// ut-docs#2119 — shared category-filter logic for /catalog and /inventory.
// Loaded from base.html next to record-dialog.js. Two pure, testable
// functions (expand/matches) plus a small generic chip-row wiring helper
// (bind) so neither page's own filterRows()/applyStockFilter() has to
// reimplement chip click handling or the parent-includes-children tree
// walk — see web/ui/partials/category_filter.html for the markup this
// binds to. Full BA/Architect/UX design recorded as comments on
// ut-docs#2119.
(function (global) {
  'use strict';

  // expand(selectedIds, categoryNodes) -> Set
  //
  // Every id in selectedIds, plus every transitive descendant, walking
  // .parentId links over the FULL flat categoryNodes list — not just the
  // top-level chips category_filter.html actually renders, since a
  // grandchild category (import-only today, but real) still has to match
  // when its top-level ancestor's chip is pressed. An empty selection
  // expands to an empty set (no filter active), never "everything".
  function expand(selectedIds, categoryNodes) {
    var result = new Set(selectedIds || []);
    if (result.size === 0) return result;
    var childrenOf = {};
    (categoryNodes || []).forEach(function (n) {
      if (!n || !n.parentId) return;
      if (!childrenOf[n.parentId]) childrenOf[n.parentId] = [];
      childrenOf[n.parentId].push(n.id);
    });
    var queue = [];
    result.forEach(function (id) { queue.push(id); });
    while (queue.length) {
      var id = queue.pop();
      (childrenOf[id] || []).forEach(function (childID) {
        if (!result.has(childID)) {
          result.add(childID);
          queue.push(childID);
        }
      });
    }
    return result;
  }

  // matches(itemCategoryId, expandedSet) -> bool
  //
  // Empty set = no category filter active = every item matches — the
  // BA's "empty selection means all categories shown" default, the same
  // convention plugin_filters.html's own "all" option already uses.
  function matches(itemCategoryId, expandedSet) {
    return !expandedSet || expandedSet.size === 0 || expandedSet.has(itemCategoryId);
  }

  // bind(rootEl, { onChange }) -> { getSelected }
  //
  // Generic chip-row wiring, reused by both /catalog and /inventory:
  // toggles aria-pressed on tap, keeps the "All categories" chip
  // (data-cat-all) in lockstep — pressed exactly when nothing else is
  // selected, unpressed the moment anything else is (UX gate finding 3)
  // — and calls onChange(selectedIds) with the resulting array of
  // selected category ids every time the selection changes, including
  // once synchronously on bind so a caller can compute its initial
  // (empty) expanded set without a separate first call.
  function bind(rootEl, opts) {
    opts = opts || {};
    var onChange = typeof opts.onChange === 'function' ? opts.onChange : function () {};
    if (!rootEl) return { getSelected: function () { return []; } };

    var allChip = rootEl.querySelector('[data-cat-all]');
    var chips = Array.prototype.slice.call(rootEl.querySelectorAll('[data-cat-id]'));

    function selected() {
      return chips
        .filter(function (c) { return c.getAttribute('aria-pressed') === 'true'; })
        .map(function (c) { return c.dataset.catId; });
    }
    function syncAllChip() {
      if (!allChip) return;
      var anySelected = chips.some(function (c) { return c.getAttribute('aria-pressed') === 'true'; });
      allChip.setAttribute('aria-pressed', anySelected ? 'false' : 'true');
    }
    function fire() { onChange(selected()); }

    if (allChip) {
      allChip.addEventListener('click', function () {
        chips.forEach(function (c) { c.setAttribute('aria-pressed', 'false'); });
        syncAllChip();
        fire();
      });
    }
    chips.forEach(function (chip) {
      chip.addEventListener('click', function () {
        var pressed = chip.getAttribute('aria-pressed') === 'true';
        chip.setAttribute('aria-pressed', pressed ? 'false' : 'true');
        syncAllChip();
        fire();
      });
    });

    fire();
    return { getSelected: selected };
  }

  // bindPopover(controlEl) -> { open, close } | undefined
  //
  // ut-docs#2165 — wires the collapsed filter-icon trigger button
  // (category_filter_popover.html) to its own <dialog> popover: open on
  // trigger click (toggles closed again on a second click), close via the
  // popover's own close button or Escape, and returns focus to the
  // trigger every time it closes so a keyboard/screen-reader user never
  // loses their place. controlEl is the popover's own wrapping element
  // (category_filter_popover.html's outer [data-category-filter-control]),
  // carrying both the trigger and the dialog. Deliberately does NOT
  // close-on-outside-click: .modifier-modal is opened via .show(), not
  // .showModal(), so it never paints a real ::backdrop element to hit-test
  // against (record_dialog.html's own comment on the same tradeoff) — an
  // "outside click" hack here would need a synthetic full-page overlay
  // this card's AC never asked for. Escape + the explicit close button
  // are the two dismiss paths the AC actually specifies.
  function bindPopover(controlEl) {
    if (!controlEl) return undefined;
    var trigger = controlEl.querySelector('[data-category-filter-trigger]');
    var dialog = controlEl.querySelector('[data-category-filter-dialog]');
    var closeBtn = controlEl.querySelector('[data-category-filter-close]');
    if (!trigger || !dialog) return undefined;

    function open() {
      if (dialog.open) return;
      dialog.show();
      trigger.setAttribute('aria-expanded', 'true');
      // The close button is always present and immediately reachable,
      // regardless of how many (or how few) category chips this shop has
      // — a more robust first-focus target than "the first chip", which
      // would be a different element's job to guarantee exists.
      if (closeBtn) closeBtn.focus();
    }
    function close() {
      if (!dialog.open) return;
      dialog.close();
      trigger.setAttribute('aria-expanded', 'false');
      trigger.focus();
    }
    trigger.addEventListener('click', function () {
      if (dialog.open) { close(); } else { open(); }
    });
    if (closeBtn) closeBtn.addEventListener('click', close);
    dialog.addEventListener('keydown', function (ev) {
      if (ev.key !== 'Escape' && ev.key !== 'Esc') return;
      ev.preventDefault();
      close();
    });

    return { open: open, close: close };
  }

  // paintTriggerState(controlEl, active) -> void
  //
  // Mirrors the popover's current selection onto its own collapsed
  // trigger: active = at least one category chip selected. aria-expanded
  // (set by bindPopover above) already carries open/closed; this instead
  // carries FILTERED/not, the state ut-docs#2165's AC calls "the
  // acceptance criterion that matters most" — a merchant must never be
  // looking at a filtered list without knowing it. The badge is a
  // presence/absence toggle (a real hidden attribute, not a colour swap)
  // for the same WCAG 1.4.11 reason category_filter.html's own chip-check
  // is shape-based, not colour-based; the accessible name gets the same
  // signal in words via data-label-active/data-label-idle (filled from
  // locale keys by the call site, so this file stays locale-free).
  function paintTriggerState(controlEl, active) {
    if (!controlEl) return;
    var trigger = controlEl.querySelector('[data-category-filter-trigger]');
    if (!trigger) return;
    var badge = trigger.querySelector('.category-filter-badge');
    if (badge) badge.hidden = !active;
    var activeLabel = trigger.getAttribute('data-label-active');
    var idleLabel = trigger.getAttribute('data-label-idle');
    var label = (active && activeLabel) ? activeLabel : idleLabel;
    if (label) {
      trigger.setAttribute('aria-label', label);
      trigger.setAttribute('title', label);
    }
  }

  global.CategoryFilter = {
    expand: expand,
    matches: matches,
    bind: bind,
    bindPopover: bindPopover,
    paintTriggerState: paintTriggerState
  };
})(window);

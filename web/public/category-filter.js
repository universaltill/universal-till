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

  global.CategoryFilter = { expand: expand, matches: matches, bind: bind };
})(window);

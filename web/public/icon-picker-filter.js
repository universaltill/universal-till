// Built-in icon picker search (ut-docs#2506). The library grew from 5 to
// ~70 tiles, so both pickers (item image in catalog.html, category editor
// in categories.html) carry a search box:
//
//   <input type="search" data-icon-filter="<grid id>">
//   <div id="<grid id>"> .builtin-icon-group-label … .builtin-icon-tile … </div>
//   <div data-icon-filter-empty="<grid id>" hidden>{{ T "…no_match" }}</div>
//
// A tile matches when every typed word appears in its visible (translated)
// label or its English data-keywords. The "No image" tile (data-icon=
// "none") always stays visible; a group heading hides when none of its
// tiles match. Delegated on document because the category editor arrives
// in an htmx-swapped dialog. No strings live here — the no-match message
// is the server-rendered element above (guard-i18n.sh check 5).
(function () {
  'use strict';

  function norm(s) {
    return (s || '').toLocaleLowerCase().normalize('NFD').replace(/[\u0300-\u036f]/g, '');
  }

  function applyFilter(input) {
    var grid = document.getElementById(input.getAttribute('data-icon-filter'));
    if (!grid) return;
    var words = norm(input.value).split(/\s+/).filter(Boolean);
    var shown = 0;
    var label = null;
    var labelHasMatch = false;
    function closeGroup() {
      if (label) label.hidden = !labelHasMatch;
    }
    Array.prototype.forEach.call(grid.children, function (el) {
      if (el.classList.contains('builtin-icon-group-label')) {
        closeGroup();
        label = el;
        labelHasMatch = false;
        return;
      }
      if (!el.classList.contains('builtin-icon-tile')) return;
      if (el.getAttribute('data-icon') === 'none') { el.hidden = false; return; }
      var hay = norm(el.textContent + ' ' + (el.getAttribute('data-keywords') || ''));
      var match = words.every(function (w) { return hay.indexOf(w) !== -1; });
      el.hidden = !match;
      if (match) { shown++; labelHasMatch = true; }
    });
    closeGroup();
    // Roving tabindex (categories.html): if the one tabbable tile is now
    // hidden, hand the tab stop to the first visible tile so Tab from the
    // search box still reaches the grid.
    var tabbable = grid.querySelector('.builtin-icon-tile[tabindex="0"]');
    if (tabbable && tabbable.hidden) {
      tabbable.setAttribute('tabindex', '-1');
      var first = grid.querySelector('.builtin-icon-tile:not([hidden])');
      if (first) first.setAttribute('tabindex', '0');
    }
    var empty = document.querySelector('[data-icon-filter-empty="' + grid.id + '"]');
    if (empty) empty.hidden = shown > 0;
  }

  // clearIconFilter(gridId) empties a grid's search box and shows every
  // tile again — for callers that reuse the picker for another record
  // (form.reset() clears the box's value without firing "input").
  window.clearIconFilter = function (gridId) {
    var input = document.querySelector('input[data-icon-filter="' + gridId + '"]');
    if (!input) return;
    input.value = '';
    applyFilter(input);
  };

  document.addEventListener('input', function (ev) {
    var t = ev.target;
    if (t && t.matches && t.matches('input[data-icon-filter]')) applyFilter(t);
  });
  // A search box's own clear (×) button fires "search", not always "input".
  document.addEventListener('search', function (ev) {
    var t = ev.target;
    if (t && t.matches && t.matches('input[data-icon-filter]')) applyFilter(t);
  }, true);
})();

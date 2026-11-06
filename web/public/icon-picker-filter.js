// Built-in icon picker search (ut-docs#2506). The library grew from 5 to
// ~70 tiles, so both pickers (item image in catalog.html, category editor
// in categories.html) carry a search box:
//
//   <input type="search" data-icon-filter="<grid id>">
//   <div id="<grid id>" role="listbox" data-roving-listbox>
//     .builtin-icon-tile[data-icon=none]
//     .builtin-icon-group > .builtin-icon-group-label + .builtin-icon-tile …
//   </div>
//   <div data-icon-filter-empty="<grid id>" hidden>{{ T "…no_match" }}</div>
//
// A tile matches when every typed word appears in its visible (translated)
// label or its English data-keywords. The "No image" tile (data-icon=
// "none") always stays visible; a whole .builtin-icon-group (heading and
// tiles) hides when none of its tiles match (ut-docs#5601). Delegated on
// document because the category editor arrives in an htmx-swapped dialog. No strings live here — the no-match message
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
    Array.prototype.forEach.call(grid.querySelectorAll('.builtin-icon-tile'), function (el) {
      if (el.getAttribute('data-icon') === 'none') { el.hidden = false; return; }
      var hay = norm(el.textContent + ' ' + (el.getAttribute('data-keywords') || ''));
      var match = words.every(function (w) { return hay.indexOf(w) !== -1; });
      el.hidden = !match;
      if (match) shown++;
    });
    // ut-docs#5601: a section (label + its tiles) hides when none match; the
    // label is never hidden on its own, so a visible section keeps its name.
    Array.prototype.forEach.call(grid.querySelectorAll('.builtin-icon-group'), function (g) {
      g.hidden = !g.querySelector('.builtin-icon-tile:not([hidden])');
    });
    // One Tab stop on the selected visible option (else the first visible):
    // the filter may just have hidden the one that held it.
    if (window.utRovingListbox) window.utRovingListbox.sync(grid);
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

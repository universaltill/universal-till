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
// A tile matches when every typed word appears in its folded text: the
// visible (translated) label plus data-keywords, which the server emits
// already folded — English terms and the tile's name in each of the shop's
// languages (ut-docs#6042). Query, label and keywords all go through `fold`,
// a port of internal/catimport placeholder_norm.go normalizeWord
// (testdata/search_fold_parity.json pins both). The "No image" tile
// (data-icon="none") always stays visible; a whole .builtin-icon-group
// (heading and tiles) hides when none of its tiles match (ut-docs#5601).
// Delegated on document because the category editor arrives in an
// htmx-swapped dialog. No strings live here — the no-match message is the
// server-rendered element above (guard-i18n.sh check 5).
(function () {
  'use strict';

  // Same table as placeholder_norm.go normReplacer — keep in sync.
  var MAP = {
    '\u0130': 'i', // I with dot above
    'I': 'i',
    '\u0131': 'i', // dotless i
    '\u00DF': 'ss', // sharp s
    '\u0649': '\u064A', // alef maksura -> yeh
    '\u06CC': '\u064A', // Farsi yeh -> yeh
    '\u06A9': '\u0643', // keheh -> kaf
    '\u0629': '\u0647', // teh marbuta -> heh
    '\u06C0': '\u0647', // heh with yeh above -> heh
    '\u0623': '\u0627', // alef with hamza above -> alef
    '\u0625': '\u0627', // alef with hamza below -> alef
    '\u0622': '\u0627', // alef with madda -> alef
    '\u0671': '\u0627' // alef wasla -> alef
  };
  function rep(s) {
    var o = '';
    for (var c of s) o += (MAP[c] !== undefined ? MAP[c] : c);
    return o;
  }
  // Lower-casing is per code point (Go's simple ToLower: no final-sigma
  // context) and never locale-sensitive.
  function fold(s) {
    var lo = '';
    for (var c of rep(s || '')) lo += c.toLowerCase();
    lo = lo.normalize('NFD').replace(/[\p{Mn}\u0640]/gu, ''); // all Mn + tatweel
    return rep(lo.normalize('NFC'));
  }

  // A word matches as a substring of hay; an Arabic-article word of 4+ code
  // points ("ال…") also matches with the article stripped (stripArticle).
  function wordMatches(hay, w) {
    if (hay.indexOf(w) !== -1) return true;
    return Array.from(w).length >= 4 && w.slice(0, 2) === '\u0627\u0644' && hay.indexOf(w.slice(2)) !== -1;
  }

  var hayCache = typeof WeakMap === 'function' ? new WeakMap() : null;
  function hayOf(el) {
    var h = hayCache && hayCache.get(el);
    if (h === undefined || h === null) {
      h = fold(el.textContent) + ' ' + fold(el.getAttribute('data-keywords'));
      if (hayCache) hayCache.set(el, h);
    }
    return h;
  }

  if (typeof window !== 'undefined') window.utIconFold = fold;
  if (typeof module === 'object' && module.exports) module.exports = { fold: fold, wordMatches: wordMatches };
  if (typeof document === 'undefined') return;

  function applyFilter(input) {
    var grid = document.getElementById(input.getAttribute('data-icon-filter'));
    if (!grid) return;
    var words = fold(input.value).split(/\s+/).filter(Boolean);
    var shown = 0;
    Array.prototype.forEach.call(grid.querySelectorAll('.builtin-icon-tile'), function (el) {
      if (el.getAttribute('data-icon') === 'none') { el.hidden = false; return; }
      var hay = hayOf(el);
      var match = words.every(function (w) { return wordMatches(hay, w); });
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

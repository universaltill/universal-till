// ut-docs#3359 — at the phone tier (≤480px, app.css `table.t-cards`) every
// data table is a stacked card list instead of a grid that scrolls
// sideways (the product owner, on an iPhone: "grid view is not a good
// design, we shouldn't scroll to the left and right" — it replaced
// ut-docs#3297's scroll-inside-its-own-box rule). Loaded on every page
// from base.html; a no-op on any page without a table, same pattern as
// record-dialog.js / list-reorder.js.
//
// This file only ANNOTATES the markup; the layout is pure CSS inside the
// phone media query, so desktop/kiosk widths keep the real table and a
// rotate/resize needs no re-run. For every `main table` that has a <thead>
// (except data-cards="off", the sale basket, and tables nested inside
// another table's cell):
//   table.t-cards                 marks it for the phone card rules
//   td/th[data-label]             its column's header text (colspan-aware)
//                                 — the card's "Label: value" pair
//   .t-cards-title                the first labelled cell: the card title
//   .t-cards-actions              a cell under an empty header (row
//                                 actions): no label, sits at the card end
//   tr.t-cards-row                a real record row (rows whose cells all
//                                 span several columns — empty-state and
//                                 group-heading rows — stay a plain line)
//   .t-cards-extra + a More/Less  a row with more than MAX_SHOWN labelled
//     button (.t-cards-toggle)    cells keeps the rest behind a toggle;
//                                 the button is display:none above 480px
//                                 (not on a data-cards-collapse="off" table)
//
// Captions come from <body data-show-more / data-show-less> (base.html,
// common.show_more / common.show_less) so this file stays locale-free.
// The caption is rendered by CSS from data-caption (not as text content)
// so record-dialog.js's list filter, which matches a row's textContent,
// never finds "More" in every row.
//
// Re-run on htmx:load, which htmx fires for the initial body, every boosted
// #ut-page swap and every swapped fragment (a /reports tab, a row swapped
// back by hx-target="closest tr", rows appended by infinite scroll) —
// annotating is idempotent.
(function () {
  'use strict';

  // Title + the next three labelled cells stay on the card; the rest wait
  // behind More.
  var MAX_SHOWN = 4;

  function headerText(th) {
    return (th.textContent || '').replace(/\s+/g, ' ').trim();
  }

  // One entry per column (a colspan=n header fills n columns), from the
  // LAST header row — the one sitting directly over the body cells.
  function columnLabels(table) {
    var head = table.tHead;
    if (!head || !head.rows.length) return null;
    var row = head.rows[head.rows.length - 1];
    var cols = [];
    Array.prototype.forEach.call(row.cells, function (th) {
      var text = headerText(th);
      for (var i = 0; i < (th.colSpan || 1); i++) cols.push(text);
    });
    return cols;
  }

  function skip(table) {
    if (table.getAttribute('data-cards') === 'off') return true;
    if (table.closest('.basket')) return true;
    var outer = table.parentElement && table.parentElement.closest('table');
    return !!outer;
  }

  // data-cards-collapse="off": every cell IS the content (the permissions
  // matrix — one checkbox per role), so none waits behind More.
  function collapsible(table) {
    return table.getAttribute('data-cards-collapse') !== 'off';
  }

  function caption(expanded) {
    var b = document.body;
    return (b && b.getAttribute(expanded ? 'data-show-less' : 'data-show-more')) || '';
  }

  function setToggle(btn, expanded) {
    var text = caption(expanded);
    btn.setAttribute('aria-expanded', expanded ? 'true' : 'false');
    btn.setAttribute('data-caption', text);
    btn.setAttribute('aria-label', text);
  }

  function onToggle(ev) {
    // The row may be a tap-to-edit record row (record-dialog.js's
    // [data-record-open]); this tap is the toggle's, never the row's.
    ev.stopPropagation();
    ev.preventDefault();
    var btn = ev.currentTarget;
    var tr = btn.closest('tr');
    if (!tr) return;
    var open = !tr.classList.contains('t-cards-open');
    tr.classList.toggle('t-cards-open', open);
    setToggle(btn, open);
  }

  function annotateRow(tr, cols, collapse) {
    var cells = tr.cells;
    if (!cells.length) return;
    var allSpan = true;
    Array.prototype.forEach.call(cells, function (c) { if ((c.colSpan || 1) < 2) allSpan = false; });
    if (allSpan) { tr.classList.remove('t-cards-row'); return; }
    tr.classList.add('t-cards-row');

    var col = 0;
    var labelled = 0;
    var hasExtra = false;
    Array.prototype.forEach.call(cells, function (c) {
      if (c.classList.contains('t-cards-more')) return;
      var label = cols[col] || '';
      col += c.colSpan || 1;
      c.classList.remove('t-cards-title', 't-cards-extra', 't-cards-actions');
      if (!label) {
        c.removeAttribute('data-label');
        c.classList.add('t-cards-actions');
        return;
      }
      c.setAttribute('data-label', label);
      labelled++;
      if (labelled === 1) c.classList.add('t-cards-title');
      else if (collapse && labelled > MAX_SHOWN) { c.classList.add('t-cards-extra'); hasExtra = true; }
    });

    var holder = tr.querySelector(':scope > .t-cards-more');
    if (!hasExtra) {
      if (holder) holder.remove();
      tr.classList.remove('t-cards-open');
      return;
    }
    if (holder) return;
    holder = document.createElement('td');
    holder.className = 't-cards-more';
    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'btn secondary t-cards-toggle';
    setToggle(btn, tr.classList.contains('t-cards-open'));
    btn.addEventListener('click', onToggle);
    holder.appendChild(btn);
    tr.appendChild(holder);
  }

  function annotate(table) {
    if (skip(table)) return;
    var cols = columnLabels(table);
    if (!cols) return;
    table.classList.add('t-cards');
    Array.prototype.forEach.call(table.rows, function (tr) {
      // table.rows never includes a nested table's rows.
      if (tr.parentElement !== table.tHead) annotateRow(tr, cols, collapsible(table));
    });
  }

  function scan(root) {
    if (!root || !root.querySelectorAll) return;
    // A swapped-in row (hx-target="closest tr", infinite-scroll appends —
    // htmx fires one htmx:load per new row) only needs itself annotated.
    var own = root.closest ? root.closest('main table') : null;
    if (own && root.tagName === 'TR' && own.classList.contains('t-cards')) {
      if (root.parentElement !== own.tHead) annotateRow(root, columnLabels(own), collapsible(own));
    } else if (own) {
      annotate(own);
    }
    Array.prototype.forEach.call(root.querySelectorAll('table'), function (t) {
      if (t.closest('main')) annotate(t);
    });
  }

  window.utTableCards = scan;

  document.addEventListener('htmx:load', function (ev) { scan(ev.target); });
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function () { scan(document.body); });
  } else {
    scan(document.body);
  }
})();

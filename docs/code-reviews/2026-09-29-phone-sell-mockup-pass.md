# Review: phone sell screen, second pass against the mock-up (ut-docs#3059)

**Why:** the product owner compared the iPhone build (v0.30.9) with the
approved "Phone sell screen" mock-up and listed four differences:

1. search and edit sat on their own row above the categories, not in the top bar;
2. the dark status row sat above the pay bar and took room from the grid;
3. Dine in / Takeaway had its own row above Pay instead of sitting next to it;
4. the category chips were tall boxes with a check mark, not a bar of pills.

## What changed (≤ 480px, sale screen only; tablet, desktop and Pi unchanged)

- **Search / Edit:** the same `.products-strip` buttons (still inside the
  finder's Alpine scope, so `openSearch()` is unchanged) are fixed over the
  end of the top bar, 48×48, in white. The nav reserves their width so its
  alert chips never sit underneath them. They drop under the drawer
  backdrop while ☰ is open. The search field and back button still open in
  the strip's own row, under the bar. With the Edit link absent (a
  cashier, or a locked till), Search moves to the bar's end.
- **Status row:** hidden on the phone sale screen at rest and shown at the
  foot of the ☰ drawer. It comes back above the pay bar when there is
  something to act on (`.sb-conn.is-offline`, `.sb-main-till`,
  `.sb-power`), and whenever `#ut-scrim` is on, because the scrim covers ☰
  and status must stay reachable (CLAUDE.md offline-first rules; the
  `popup-scrim-2873` 360px case caught the first draft missing this).
  app.js now always writes `--phone-sb-h`, 0 when the row is hidden, and a
  ResizeObserver follows the row as it shows, hides or wraps.
- **Bottom bar:** the resting basket is a 2-column grid. `.order-type-row`
  becomes `display: contents`, so the toggle sits beside `.basket-phonebar`.
  The toggle is capped at `min(13rem, 46vw)`, and a long label wraps inside
  its 56px segment. The Table trigger is hidden at rest and appears in the
  sheet with the whole row. The emoji moved into `.order-type-ico` spans
  (aria-hidden), which are hidden in the bar. The empty bar drops its
  "0 item(s)" line.
- **Categories:** 40px pills (radius 20px, 600 weight), sticky at the top
  of the grid's scroller on a white bar with a bottom border. The selected
  pill is filled with the pay-bar green. The check mark is hidden on phones
  only: the filled vs outlined pill is a lightness change, not colour
  alone.

## Tests

- `e2e/tests/phone-sell-3059.spec.ts` adds three cases:
  - search/edit inside the top bar at its end, and the field opening under it;
  - the status row hidden at rest, at the drawer's foot when ☰ is open, and back above the pay bar when offline (`context.setOffline`);
  - the toggle and Pay on one row at 390 and 360, with 40px pills.
- Run and passing (163 tests): phone-sell-3059, phone-width-layout-413,
  basket-item-name-phone-tier-1338, nav-rail-svg-icons-1423,
  payment-overlay-focus-obscured-1674, open-orders-row-geometry-2147,
  page-zoom-2942, popup-scrim-2873, bugreport-panel, tender-panel-reachable,
  portrait-tablet-sale-3050, sale-screen-search-strip-2173,
  sale-screen-category-tabs-search-418, sale-screen-scan-focus-search-423,
  and the order-type specs.
- Checked by hand in screenshots:
  - 390×844 and 360×800;
  - en and fa (RTL: the icons sit at the bar's left end, the drawer comes from the right);
  - all three browsing modes (pills; tabs in the strip, where the strip keeps its gap; category tiles).

## Docs

- `web/help/{en,de,fa,ar,tr}/sell.md`, "Selling on a phone": same bullet
  structure (no help-drift change), text updated.
- `docs/ux/portrait-and-phone.md`: added an as-shipped phone sketch.

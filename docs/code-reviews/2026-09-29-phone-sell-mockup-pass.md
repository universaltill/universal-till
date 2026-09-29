# Review: phone sell screen, second pass against the mock-up (ut-docs#3256, follow-up to ut-docs#3059)

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

## Independent review (Fable, 2026-09-29) — card ut-docs#3256

The reviewer was a different model from the one that built the change. It ran build/vet, 59 e2e tests and 10 guards, plus throwaway Playwright probes:
- 390, 320 and fa RTL;
- all three browsing modes;
- injected fiscal and diagnostics chips;
- 9 extra categories;
- offline, sheet open, and boosted navigation.

Its verdict on the first commit was **not safe to merge**, because of finding 1.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | The `ResizeObserver` on `.statusbar` watched a detached node after the first boosted navigation (`#ut-page` is swapped whole). Offline, the returning row then covered Pay in the sheet, and `--phone-sb-h` stayed 0. | **Fixed.** `watchSb()` moves the observer to the current row on every `measureBar()` (every afterSettle), and `online`/`offline` events re-measure on the next frame. New e2e test (boosted Sell → Menu → Sell, offline, Pay hit-testable) written first: it failed with `--phone-sb-h` 54px off, and passes after the fix. |
| 2 | minor | A single long word in an order-type segment ("Mitnehmen") was clipped with an ellipsis. | **Fixed:** `overflow-wrap: anywhere; hyphens: auto`. |
| 3 | minor | 40px pills were under the 44px touch floor. | **Fixed:** a `::after` hit area extends each pill to 44px; it still looks 40px. |
| 4 | minor | `#sb-link-live` (polite live region) sat inside the now-hidden footer, so link-state changes went unannounced. | **Fixed:** moved just outside the footer (`base.html`), same id and `hx-preserve`. |
| 5 | nit | The fixed search/edit buttons (z 61) paint above `.ai-identify-overlay` / `#barcode-scan-overlay` (z 60). Both open only from the tender, which on a phone is only in the sheet, where the grid (and so the buttons) is `display: none`. | **Accepted, latent.** Not reachable today. |
| 6 | nit | Help wording: "Tap outside it" read as referring to the search button. | **Fixed** in en; the other locales already had the sentence last. |
| 7 | nit | `--phone-sb-h` took the drawer-foot height while ☰ was open. | **Fixed:** frozen while the drawer is open, re-measured when it closes. |

Also checked beyond the automated tests:
- dark theme at 390 (sell and ☰ open);
- 1024×600 light and dark, where the tablet layout is unchanged.

**Touch:** emulated only (Playwright). Not yet tried on a real phone.

**Safe to merge** after these fixes: 152 e2e tests on the touched surfaces pass, as do `go test ./...`, golangci-lint (0 issues), the guards and docs-shots.

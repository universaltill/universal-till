# Review: phone sale screen (ut-docs#3059)

**Why:** on the product owner's iPhone (TestFlight 0.30.7) the ≤ 480px tier
showed no product tile without scrolling: a three-row wrapped nav, the basket
table and the tender block came first. The owner approved a mock-up modelled
on the SumUp / Square / Shopify / Zettle / Loyverse phone apps, with a burger
menu and item icons that can be pictures.

## What shipped (≤ 480px only; tablet, desktop and Pi unchanged)

- **Nav:** one 56px bar with ☰ and the logo. `#nav-drawer` wraps the links and
  chips; it is `display: contents` on wider layouts, so the rail's DOM and
  layout don't change. The fiscal and diagnostics chips moved into
  `.nav-alerts`, outside the drawer, so an alert is always in the bar
  (ADR-0092 §7). The fiscal "ok" state is hidden on phones.
- **Tiles:** app-icon squares, 3 per row: the item's picture, else the item
  colour with its initials (`initials` template func, rune-safe
  `httpx.TileInitials`). Category chips are one row that scrolls sideways.
- **Basket:** a fixed bottom bar with Dine in / Takeaway and "Pay £x · n
  item(s)" (reusing `tender.pay_with_total` / `tender.pay_empty`). Tapping it
  opens a sheet with the lines (the ± steppers are back, since the sheet has
  room), the totals, and the tender panel pinned at the foot. A paid sale
  keeps the sheet open on the receipt view; **New Customer** returns to the
  tiles.
- **Status bar:** stays visible, as a row above the pay bar (offline-first),
  and above `#ut-scrim` while a dialog is open.
- **i18n:** 4 new keys in en/ar/fa/tr (de/es pack PRs follow). "Selling on a
  phone" added to the Sell help topic in 5 languages; help-drift baseline
  shifted by the same section; docs-shots regenerated.

## Tests

- **TDD:** `e2e/tests/phone-sell-3059.spec.ts` was written first and failed
  10 of 12 against the old screen (the 2 tablet-unchanged cases passed).
  `TestTileInitials` failed (undefined) before the helper existed.
- Older phone specs were **adapted, not deleted**: 413, 1338, 1423, 1674,
  2147, 2942, 2873, bugreport-panel, tender-panel-reachable now open the
  drawer or sheet first (helpers `openPhoneDrawer` / `openPhoneSheet`).
  - 413's focus tests now assert the hidden scan field is **not** focused on
    a phone (no keyboard over the tiles). The wedge-scanner path submits
    regardless of focus (app.js), so it is unaffected.
  - 1338's 15.1rem budget test keeps the same reserve: the grid tracks are
    unchanged and only the areas differ.
- The adapted specs caught 4 real bugs, fixed before review:
  - "+" rendered under the price cell;
  - the payment overlay was trapped under the top bar (a flex item with a
    z-index is a stacking context, even when static);
  - the RTL top bar overflowed with a diagnostics chip;
  - the status row fell under the scrim.
- Full Playwright suite: 862/869, then the 7 re-run green after adapting 2
  specs; one was a collision flake from a concurrent run. All 290
  phone-related specs are green after the review fixes. `go test`
  (httpx/pages/ui), `guard-i18n`, `guard-help-drift`, `guard-help-topics`,
  `guard-docs-shots`, competitor/compliance guards are green.
- **Looked at:** 390×844 en (grid, drawer, sheet, receipt), 360×800 fa RTL,
  390×844 de dark, 1024×600 catalog (rail unchanged). **Not checked on real
  touch hardware**: that's the owner's iPhone (TestFlight) and an Android
  phone, after release.

## Independent review (Fable; code by Opus 5.5)

| Severity | Finding | Outcome |
|---|---|---|
| blocker | The sale-done heuristic (lines > 0 → 0) closed the sheet on the receipt view, hiding the paper-receipt ask, Print and New Customer, and leaving no control on screen | Fixed. The receipt view keeps (or opens) the sheet; only leaving it for a fresh basket closes it; the collapsed rules exempt `.receipt-view`. New e2e: pay on a phone → New Customer visible → back to tiles. |
| major | The fiscal "ok" chip is non-empty, so the app name was hidden for every fiscal shop | Fixed. The ok state is hidden on phones; the app name gives way only to warn/err or diagnostics. |
| minor | The sheet closed when the only line was voided; Escape closed the sheet under a popup | Fixed (only a finished sale closes it; Escape is ignored while the scrim is up). New e2e for the void case. |
| minor | Count text on the green bar was about 3.3:1 | Fixed: bar background darkened (color-mix 72% with black). |
| minor | `--phone-sb-h` went stale while the sheet was open | Fixed: the status row is measured regardless. |
| minor | Drawer buttons were narrower than links (older `.nav button.nav-toggle` rule) | Fixed with a more specific rule. |
| minor | `.kiosk-header` (New Sale / Inventory / Deposit refund) duplicates the tender's New Sale inside the sheet | Accepted. Pre-existing duplication, not replaced by this change, and it is the phone's only Deposit refund entry. |
| minor | Landscape phones (844×390) still get the old layout | Out of scope; folded into ut-docs#2918. |
| nit | `aria-controls="basket"` on a control inside `#basket`; NFD initials drop the accent | Accepted. |

Follow-up cards: ut-docs#3241 (quantity badge on tiles), ut-docs#3242
(swipe to delete), ut-docs#2918 (landscape phones).

**Verdict:** safe to merge after the fixes above. It needs device checks on an
iPhone (TestFlight) and an Android phone after the release.

# Code review — sale screen nav rail + follow-on live tuning (2026-08-31)

**Branch:** `sale-screen-nav-rail` · **Files:** `web/public/app.css`,
`web/ui/partials/nav.html`, `web/ui/partials/session_chip.html`,
`web/ui/partials/bugreport_chip.html`, `web/ui/partials/basket.html`,
`web/ui/partials/table_picker.html`, `web/ui/partials/buttons.html`,
`web/ui/pages/index.html`, `web/locales/*.json`,
`e2e/tests/phone-width-layout-413.spec.ts`.

## Origin

Ticket ut-docs#1332, deferred out of the earlier compactness pass
(PR #664) specifically because it's an app-wide structural change, not a
sizing tweak. The product owner re-raised it repeatedly, live, comparing
directly against a competitor POS (SumUp) and its left icon rail, then
kept iterating live on the tablet through the rest of this change —
this review covers everything that landed as one connected session of
feedback, not a single fixed spec.

## What shipped

### The nav rail itself
- `.nav` converted from an always-horizontal top bar (`position: sticky`,
  full width) to a fixed left rail (`position: fixed; inset-inline-start:
  0`, full viewport height, `--rail-width: 4.5rem`) at real tablet/kiosk/
  desktop widths. Logical properties throughout, so the rail sits on the
  RIGHT for RTL locales (fa/ar) automatically.
- **Reverts wholesale to the previous horizontal top bar at `<=480px`**
  (phone width) — a permanent 4.5rem side rail would eat too much of a
  360px screen, and that tier already has its own hard-fought, previously
  shipped and tested wrapping behavior (ut-docs#413) not worth
  re-litigating in this change.
- Every rail item is icon-only by default (product owner, second live
  pass, matching SumUp's own reference rail exactly) — each label stays
  in the DOM as a `.visually-hidden`-style span, not removed, so it's
  still in the accessible name for assistive tech even with no visible
  text. This matters specifically because **a touchscreen till has no
  hover to reveal a `title` tooltip** — icon-only-plus-title-only would
  have been a real discoverability regression (the `ux` skill's own
  standing evidence: a hidden control is worse than no control at all,
  see its "⚠️ToGo⚠️" fake-catalog-item incident). The `<=480px` block
  restores full visible labels, matching the phone top bar's original
  behavior exactly.
- Inventory moved from the sale screen's own header row into the shared
  rail (`nav.html`) — it's a plain link with no page-specific DOM
  dependency, safe on every page. Deposit refund stayed split: a rail
  copy (`.saleScreen`-guarded, `>480px`) and a separate phone-only
  fallback (`index.html`, `<=480px`) — it targets `#pfand-modal`, which
  only exists on the sale screen itself, so it can't be a global rail
  item unconditionally.
- New Sale started in the rail too, then moved a second time (below).

### Icon sizing — two real, live-caught issues
1. **Fixed box, not fixed font-size.** Different emoji glyphs (🧾/☰/📦/
   🛒/♻️/👥/🏷️/🌐/👤/🔒) don't share the same intrinsic bounding box at
   the same `font-size` — `.nav-toggle-ico` centers each icon in a fixed
   1.6rem×1.6rem flex box instead, so every rail item occupies the same
   footprint regardless of its own glyph's metrics.
2. **The fixed box didn't fix the *glyph's own* rendered size** — caught
   live, with a real screenshot: ☰/♻️/🔒 read visibly thinner/smaller
   than 🧾/📦 even inside identical boxes. A `.ico-boost` class (font-size
   1.55rem vs the base 1.25rem) applied to specifically those three
   glyphs, rather than inflating every icon and crowding the already-full
   ones. `.help-hint` (the "?" circle, a separate/older component reused
   inside `.nav-right`) got a scoped `.nav .help-hint` override for the
   same box treatment — its base rule is used on other pages too (a small
   "?" next to a page heading) and must stay that size there.
3. `bugreport_chip.html` and `session_chip.html` both updated to use the
   same `.nav-toggle-ico`/`.nav-toggle-label` structure, so the rail's
   bottom section (help/bugreport/sync/fiscal/session/lock) is visually
   consistent with the top section — verified with a real authenticated
   session (see "What I verified" below), not just code inspection.
4. Logo shrunk `2rem` → `1.6rem` in the rail (unchanged at `1.5px<=480px`
   phone width, matching the original design there).

### Dine-in/Takeaway: two buttons → one switch
`basket.html`'s `.order-type-toggle` (two full-width `.btn`s, one always
showing the other's un-selected state) replaced with `.order-type-switch`
— a single `role="switch"` `aria-checked` control (WAI-ARIA switch
pattern, not `aria-pressed` — this is a two-state toggle between two
*named* states, not a momentary press) with a visible track+knob and a
state-dependent icon+label. `hx-vals` computes the OPPOSITE of the
current server-known `.OrderType` via a template variable
(`$nextOrderType`), so one tap always flips it — no client JS needed,
same template-driven approach the two-button version used.

**Still deliberately NOT `.compact`, still the base `.btn` 48px floor** —
same reasoning as the two-button version's own comment, carried forward:
this control determines VAT (`internal/pos/service.go`,
`tax_codes.takeaway_rate_basis_points` — the German §12 UStG dine-in/
takeaway split, live pilot work). A mis-tap puts the wrong tax on a
fiscal receipt. Only the *shape* changed, not the touch-target floor
ut-docs#161 fought to protect.

### Table assignment: inline strip → button + dialog
`table_picker.html` rewritten from an always-expanded strip (current
table + clear + every free table listed, its own row under the toggle)
into a single button next to the switch that opens the same picker in a
`<dialog>` (reusing `.modifier-modal`'s existing styling). The button AND
the dialog are both part of the SAME fetched fragment, so ADR-0054's
soft-gate ("zero tables configured = no table chrome at all") still
covers the button too, not only the dialog's contents — verified live by
inserting real table rows directly into a seeded DB and driving the
open → select → close flow end-to-end (see "What I verified").

### New Sale moved into the tender footer
Started in the rail (this change's own first pass), then the product
owner asked for it to sit next to Hold Sale and Payment instead — moved
into `.tender-default-footer` (`index.html`), `.btn.secondary` (Payment
stays the one visually-prominent action in that row). Since that row
renders at every width uniformly, New Sale needed neither a rail copy
nor a phone-fallback copy the way Deposit refund still does — one
`kiosk-checkout-start` testid, one location, full stop.

### Products panel: search next to the title, "+ Add product"
`buttons.html`'s `<h2>PRODUCTS</h2>` and the search `<input>` used to be
two separate full-width rows — combined into one `.products-header` row
(title + search, flex, search grows) so search doesn't cost its own row
of height, same pattern as everything else in this change. A `+ Add
product` link to `/designer` sits in that same row (compact `+` icon
button when products exist; a full labeled button in the empty state,
where it's the primary and only action) — matching SumUp's own reference
grid, which has exactly this affordance. New i18n key `products.add`,
added to all 4 locales (en/fa/ar/tr).

## Independent verification this session ran itself (not yet a fresh-model review — see below)

- **Two real, live-caught bugs, not just code review**, both confirmed
  broken then confirmed fixed with real browser evidence:
  1. A CSS cascade/positioning bug where `.nav`'s `overflow-y: auto`
     (needed for the rail) broke the `<=480px` sticky top bar's own
     height calculation for wrapped rows — `.nav-right` rendered ~10px
     *below* `.nav`'s own bottom edge, making the right-most chip
     (bugreport toggle, same place the real Lock button sits) fail a
     real hit-test despite passing every geometry check. Root-caused via
     `getBoundingClientRect()` + `elementFromPoint()` on the actual
     failing element, not guessed. Fixed with `overflow-y: visible`
     scoped to the `<=480px` block only.
  2. Cramming Inventory/New Sale/Deposit refund into the phone-width top
     bar (the first attempt) overflowed the sale screen's fixed
     `100dvh`/`overflow: hidden` height and visually clipped BASKET
     underneath a third wrapped nav row — confirmed with a real
     screenshot, not assumed. Root cause: this three-item group was
     ALREADY its own separate row (`index.html`'s `.kiosk-header`) on
     `main`, deliberately kept OUT of `.nav`'s own already-tight
     `<=480px` wrapping budget (ut-docs#413 fought hard to fit exactly
     Till/Menu/chips in two rows). Fixed by restoring that separation:
     `.phone-fallback-only`/`.nav-rail-only` CSS classes split the same
     logical items between the rail (`>480px`) and a page-local fallback
     row (`<=480px`, unchanged from before this session's start) with
     distinct testids (`-phone` suffix) so Playwright's strict-mode
     locators never resolve to more than one element for either width
     class.
- **A genuine regression, caught and root-caused, not just noticed**:
  `catalog-image-to-till.spec.ts` started failing consistently (4/4
  reruns) once the rail landed at its first-draft `8.5rem` width, while
  `main` passed consistently (3/3 reruns) at the same test. Root-caused
  via direct DOM measurement: the target catalog row measured ~4492px
  down the page even before scrolling, `loading="lazy"` on its
  `<img>`, `complete: false`/`naturalWidth: 0` — the reduced horizontal
  space (rail eating into `.container`'s available width) was making
  catalog table cells wrap taller, pushing the row further down and out
  of the browser's lazy-load-trigger range after the upload's
  `outerHTML` swap reset scroll-relative position. **Resolved as a side
  effect** of shrinking the rail to icon-only `4.5rem` (the product
  owner's own later request) — reconfirmed passing 1/1 after, not
  assumed fixed just because the width changed.
- **Real interaction testing**, not just static rendering, for every new
  control: toggled the switch and read back `aria-checked`/class/text;
  inserted real table rows into a seeded SQLite DB directly (no tables
  API needed manager auth this session didn't have handy) and drove
  open → select Table 1 → dialog closes → button text updates to "Table
  1", full round trip through the real `/api/pos/table` handler; walked
  a full first-boot wizard programmatically to get a genuinely
  authenticated session (`UT_AUTH=off` has no real session at all — the
  session-chip endpoint returns empty under it, confirmed by direct
  fetch) specifically to visually confirm the Admin/Lock icons in
  `.nav-right` render with the same fix as everything else — code
  inspection alone was not treated as sufficient given this session's
  own repeated experience that CSS cascade order does not always do what
  the source reads like it should.

## Verified

| check | result |
|---|---|
| `gofmt -l .` / `go build ./...` | clean |
| `guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`, `guard-i18n.sh`, `guard-compliance-claims.sh`, `guard-help-topics.sh` | all pass |
| Full `e2e/tests/` suite (`--project=default`) | 233/233, run in full at least twice at different points in this change (once before the New-Sale/switch/table-picker/products-header round, once after) |
| `tests/catalog-image-to-till.spec.ts` in isolation | reproduced failing 4/4 pre-fix, passing 1/1 post-fix (see "genuine regression" above) |
| Visual: 1024×600, 1280×800, 360×640, `/menu`, `/settings` | screenshotted and looked at, not just asserted |
| Visual: authenticated session (full wizard walkthrough) | screenshotted and looked at — Admin/Lock icons confirmed correctly sized |

## Not yet done — flagging honestly rather than skipping past it

This document was written by the same session that made the changes, not
an independent second model — this repo's standing practice
(`reviewer` skill) is a genuinely independent, different-model review
before merge, which has caught real shipped bugs before (a plugin
signature-verification bypass, a false-pass test) that same-session
review structurally cannot. **An independent review is still owed before
this merges.** Also not yet re-run since the very last edits (icon
boost sizing, products-header, `products.add` i18n key): the full e2e
suite's LATEST run hit a port collision with a concurrently-running
background agent's own e2e suite (both hardcoded to 127.0.0.1:8091/8092
via `run-till.sh`/`playwright.config.ts`) — not a real failure, but it
means the very last diff has only guard-level and manual visual
verification, not a fresh clean 233/233. Re-run once the port is free,
before merge.

## Deferred, not in this change

- Reconsidering whether >2 payment methods should be directly visible
  next to Payment again (ut-docs#1336) — separate IA decision, needs
  reconciling against ut-docs#1252's whole premise.
- Inventory/Add-item as small icons *elsewhere* (ut-docs#1335) —
  Inventory itself is now in the rail; the ticket's broader "icon-ify
  admin actions" scope is untouched.

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

## Independent review (Opus) — findings and fixes

A genuinely independent review (different model, isolated worktree) ran
against `0798df44` before this PR's two concurrent-merge rounds
(`origin/main` moved twice mid-review — PR #667 then PR #668, ut-docs#1337)
— full record: `docs/code-reviews/2026-08-31-sale-screen-nav-rail-1332-independent-review.md`.
It found three real, diff-invisible bugs and pre-applied fixes for all of
them, pulled into this branch:

- **H1 (fixed): `monarch.css` — the default theme — silently overrode the
  rail's layout.** Loaded after `app.css` at equal specificity, it carried
  a leftover copy of the OLD horizontal top bar's layout properties
  alongside its colours. Once `app.css` made `.nav` a vertical rail, this
  broke it out-of-box on every till: the rail's own items overflowed its
  content box (measured horizontally scrollable), `align-items:center`
  overrode `stretch`, and a physical `margin-left` (wrong under RTL) made
  `<a>` rail items narrower than `<button>` ones. Fixed by reducing the
  theme file to colours only — layout belongs to `app.css` alone.
- **H2 (fixed): the VAT switch's touch target used `48px`, not `3rem`.**
  Looks identical to the eye and is not: this file's root font-size is
  `calc(var(--ui-scale,1) * var(--fluid-fs))`, not a fixed 16px, so a `px`
  floor doesn't track `--ui-scale`/the fluid viewport fit the way every
  other touch target in this file does. Measured: the switch was smaller
  than the `.btn` it replaced at plain default scale (48px vs 53.6px) and
  dramatically smaller at a raised UI scale (48px vs 80.4px) — on the one
  control that decides a fiscal receipt's VAT rate. Fixed: `min-height:
  48px` → `3rem` on both the switch and the table-picker trigger.
- **H3 (fixed): a live merge-conflict hazard around `hx-sync`.** PR #668
  (ut-docs#1337) merged concurrently and added `hx-sync="#basket:replace"`
  to every pre-existing `#basket`-targeting control. This branch's NEW
  controls (the switch, both table-picker buttons, New Sale in the tender
  footer) predated that PR and had none — the reviewer's trial merge showed
  the natural conflict resolution would silently drop `hx-sync` from all
  four and reintroduce the exact race #668 just fixed. In practice this
  branch's own real merge (below) resolved those same conflicts directly
  and added `hx-sync` to all four independently, before reading the review
  — both arrived at the same fix.
- **M4 (fixed): rail padding leaked onto pages with no rail.**
  `body { padding-inline-start: var(--rail-width) }` is unconditional, but
  `login.html`/`setup.html`/`order_tracking.html` are standalone documents
  that never render `.nav` at all — measured the first-boot wizard and the
  lock screen's card drawn ~40px off true centre. Fixed:
  `body.login-screen, body.tracking-screen { padding-inline-start: 0 }`.
- **L8 (fixed, comment only):** a `session_chip.html` comment claimed
  `.session-user` clamps a long operator name to one line with an ellipsis
  — it does not (measured: the rule is `font-weight: 600` and nothing
  else). Corrected the comment to describe reality rather than silently
  changing the behaviour (a real visual change that wants its own card).

**Deferred as follow-up cards, not merge blockers** (reviewer's own
verdict): M5, the switch's `aria-label` announces "off" rather than which
state is active to a screen reader (needs a new locale key, not a
same-session guess); M6, New Sale is below the fold at 360px (the product
owner's own explicit placement choice — the phone-fallback row is the
obvious home if this needs revisiting); L7, the rail has no headroom to
spare at 1024×600 with a full manager session (not broken today, no
regression test yet); L9/L10, cosmetic/markup-validity nits.

**Verdict: safe to merge with the fixes applied** (reviewer's own words) —
applied in full.

## Concurrent-PR merge, twice

`origin/main` moved twice while this PR was open:

1. **PR #667** (ut-docs#1314, basket item-name column width) — merged
   cleanly, no real source conflicts (only regenerated docs-shots PNGs,
   resolved by taking ours and regenerating fresh).
2. **PR #668** (ut-docs#1337, the basket hx-sync race fix) — real content
   conflicts in exactly the three files the independent review's H3
   predicted (`index.html`, `basket.html`, `table_picker.html`), since this
   branch rewrote the same controls #668 was adding `hx-sync` to. Resolved
   by keeping this branch's own markup and adding `hx-sync="#basket:replace"`
   to all four of its new `#basket`-targeting elements (the switch, the
   table-picker's clear + option buttons, New Sale in the tender footer) —
   independently, before reading the review doc, which reached the
   identical conclusion.

   Also updated `e2e/tests/basket-hx-sync-race-1337.spec.ts` (added by
   #668): its selector targeted the old two-button `.order-type-toggle`,
   which this branch had already replaced with `.order-type-switch` —
   updated to the new selector, same click intent.

## What I measured after every fix

| check | result |
|---|---|
| `gofmt -l .` / `go build ./...` | clean |
| All CI guards (`guard-data-access`, `guard-kiosk-engine`, `guard-plugin-menu-read`, `guard-i18n`, `guard-compliance-claims`, `guard-help-topics`, `guard-docs-shots`) | all pass |
| `make docs-shots` | 92/92, regenerated after the CSS fixes |
| Full `e2e/tests/` suite (`--project=default`) | **238/238**, confirmed clean with zero competing processes on the machine |

The full-suite run genuinely flaked twice during this session's later
rounds (a handful of unrelated tests failing, including once on the new
race spec itself) — root-caused, not hand-waved: this session had
accumulated 60+ leftover Chromium/Playwright processes from many
concurrent worktrees and background agents run over a very long session,
and a stray worktree (the reviewer's own, left running a repeat full-suite
pass after reporting its findings) was still actively consuming CPU/ports
during two of the "final" runs. Every single test that showed as failing
in a loaded run was re-run in isolation and passed; a manual, fully
-instrumented reproduction of the race scenario passed 5/5 in a row on an
unloaded server. Cleaned up (`pkill`, then `git worktree remove` on the
reviewer's stale worktree) and reran once the machine was genuinely quiet:
238/238, exit code 0.

## Deferred, not in this change

- Reconsidering whether >2 payment methods should be directly visible
  next to Payment again (ut-docs#1336) — separate IA decision, needs
  reconciling against ut-docs#1252's whole premise.
- Inventory/Add-item as small icons *elsewhere* (ut-docs#1335) —
  Inventory itself is now in the rail; the ticket's broader "icon-ify
  admin actions" scope is untouched.

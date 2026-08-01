# Review: sale screen — full-height basket, count badge, legible logo, one notification surface (ut-docs#213)

**Date:** 2026-08-01 · **Branch:** `feat/sale-screen-213` · **Card:** universaltill/ut-docs#213 (p2, product-owner field report with screenshot: basket showed ~1 line item, no item count, illegible logo, undefined message surface)

## What shipped

1. **Layout** (`app.css`): the basket is a full-height left column
   (`grid-template-areas: "basket products" / "basket tender"`, rows
   `minmax(8rem, 1fr) minmax(0, auto)`); the tender panel moved under the
   products grid, content-sized but compressible. Qty + line-discount
   inputs are side-by-side (`.line-inputs`) instead of stacked — the
   stacked pair nearly doubled row height and was the main reason only
   ~1 line fit. Result at 1280×800: 5 scanned items → ≥4 fully visible
   without scrolling (e2e-asserted; was 3 before the input change).
2. **Item count**: `pos.Basket.ItemCount()` (unit lines sum quantity,
   weighed lines count 1) + an always-visible badge in the basket header,
   inside `#basket` so every HTMX swap refreshes it.
3. **Logo**: new generated `ut-logo-name-light.svg` (navy `#254464` →
   `#f1f5f9`; the navy half of the two-tone wordmark was invisible on the
   dark `--brand` nav — the owner's "bad colour"), `height: 2.4rem`
   (replaces the nav's last fixed-px sizing so it follows `--ui-scale`),
   monarch's `30px` override updated. Dark asset kept for the light
   login/setup cards.
4. **One notification surface** (`docs/sale-screen-notifications.md`):
   `.pos-notice` — server messages via new `Basket.ToastLevel` rendered in
   `basket.html` (error = `role=alert`, persists + dismiss button;
   info/success = `role=status`, auto-expire); client slot `#pos-alert`
   filled on `htmx:responseError`/`sendError` (there was NO htmx error
   handler at all before — failed requests surfaced nothing), self-healing
   on the next successful request; path-scoped `beforeSwap` swaps in
   `/api/pos/*` 400s that carry a rendered basket (fixes the pre-existing
   silently-dropped modifier-validation toasts); five hardcoded-English
   toasts in `pos_api.go` localized (`httpx.T`, 10 new keys × 4 locales);
   the hand-built `#toast-error`+inline-script path and the dead
   `/ui/clear-toast` route removed; dead Tailwind `toast.html` partial
   deleted. Self-order keeps its own legacy `.toast` surface (ut-docs#238).

## Independent review (Opus subagent) — findings and resolutions

Verdict on first pass: **hold** — 3 SHOULD-FIX, 5 NIT, one process note. All addressed:

- **Products row could collapse to ~0 under vertical pressure** (the
  `auto` tender row maximizes before the `1fr` products row gets leftover
  space; the OSK's 15.5rem body padding would have drained products
  entirely — the same invisible-not-clipped class as the two existing
  6rem floors). **Fixed**: 8rem floor on the products row; regression
  e2e added and verified red-first against the un-floored grid.
- **Insufficient-stock notice rendered the raw engine error** (English +
  internal item/location IDs) on a now-persistent notice. **Fixed**:
  localized generic `pos.toast.insufficient_stock` (×4 locales), detail
  goes to the server log.
- **`#pos-alert` never cleared after recovery** — one transient network
  blip would wear a permanent red banner. **Fixed**: hidden on the next
  successful `htmx:afterRequest`.
- NITs fixed: 400-swap handler path-scoped to `/api/pos/*` (mirrors
  self-order's scoping); badge `aria-label`-on-span replaced with a
  `.visually-hidden` text node; `#pos-alert` unhidden before its text is
  set (alert regions don't announce from `display:none`); input widths
  restored to 3.6rem (3-digit qty clipping); pattern doc corrected to
  name the two surviving scoped status lines (`#split-tender-status`,
  `#ai-identify-status`, → #238).
- NIT accepted, no change: `ItemCount` half-up rounding on a fractional
  non-weighed qty is only reachable by a hand-crafted POST; behavior is
  sane. Reviewer confirmed everything else it actively tried to break
  (grid dependents incl. all four themes + kiosk + ≤900px + receipt-view,
  RTL logical-properties, htmx `isError` semantics against the vendored
  runtime, orphaned-route search, locale `%s` counts, false-pass checks
  on every new test).

## Found & fixed along the way (beyond the card)

- **htmx settle race** (pre-exists on main): freshly swapped `#basket`
  content has a window where buttons are visible but unbound — a click
  there is silently dropped. Reproduced 25/25 with a tight loop, shown
  present on stock main, NOT widened by this diff (0/25 with equalized
  waits). e2e works around it explicitly; product-side fix tracked as
  **ut-docs#239**.
- **OSK toggle raced itself** (pre-exists; surfaced as a ~30% full-suite
  flake in `settings-osk.spec.ts` once this card's spec ran before it):
  the toggle button took focus on pointerdown → `focusin` → `hide()`
  BEFORE the click handler, which then saw "closed" and re-opened.
  Diagnosed by wrapping document click listeners (log showed
  `oskOpen=false` entering the second toggle click). **Fixed** in
  `osk.js`: the toggle now `preventDefault()`s pointerdown — the same
  never-steal-focus rule the OSK's own keys always had. Also made the
  keep-visible scroll instant (a smooth scroll animates layout for
  ~300–500 ms after the OSK opens; taps mid-animation land on stale
  coordinates). 9 consecutive full-suite runs green after (was ~1-in-3
  failing).

## Verification beyond automated tests

- TDD: every Go test red-first (`ItemCount` undefined; notice tests
  failed on old markup; fa test failed on the English literal). The
  products-floor e2e verified red against the un-floored grid, green with
  it. Legacy tests asserting the replaced behavior updated deliberately
  (`TestClearToastHandler` deleted with its route).
- Full gate: `go build`, `go vet`, full `go test ./...`, guard-i18n,
  guard-data-access — green. Playwright: **35/35, nine consecutive full
  runs** (two before the flake hunt + six with the focus fix + one after
  de-instrumenting).
- Screenshots reviewed at 1280×800 (default + during failures): basket
  full-height with 4–5 lines, badge live, light logo legible, tender
  reachable, OSK overlay geometry sane.

## Safe to merge: yes (post-fixes)

## Deferred (cards filed)

- ut-docs#237 — guard-i18n blind spot for Go-side `ToastMessage` literals.
- ut-docs#238 — migrate remaining surfaces (admin spans, self-order,
  engine error strings) to the pos-notice pattern.
- ut-docs#239 — htmx settle window drops clicks on fresh basket content.

# 2026-09-01 — Payment overlay's on-screen keyboard was inert (ut-docs#1385)

## What shipped

`#payment-overlay` (the sale screen's Tender/Split-payment dialog,
`web/ui/pages/index.html`) opened via `dialog.showModal()`. Per the HTML
living standard, `showModal()` promotes a dialog to the browser's native
"top layer" and makes everything **outside** it `inert` — unfocusable and
excluded from hit-testing — for as long as it's open. This app's custom
on-screen keyboard (`#osk`, a singleton appended once to `<body>`, defined
in `web/public/osk.js`, never re-parented into whichever dialog is
currently open) was therefore untappable while the overlay was open: it
rendered visually (confirmed live: `getBoundingClientRect()`/
`getComputedStyle` both showed it correctly laid out) but a real click on
it landed on nothing. On kiosk hardware (no physical keyboard — this
product's whole design point) that meant the Split tab's `amount`/
`change`/`reference` fields could not be typed into at all while paying.

This exact bug class had already been found and fixed four times before in
this same codebase — `#hold-modal`, `#pfand-modal`, `#elevation-modal`,
`#table-add-modal` — all four opened non-modally (`.show()`) instead, with
an explicit `position: fixed; z-index: 500;` replacing the native
top-layer stacking `.show()` no longer grants. `#payment-overlay` was the
one dialog with OSK-able fields never given the same treatment — not an
oversight, per `index.html`'s own prior comment: it was deliberately kept
modal (ut-docs#1252) for the free focus-trap + Escape-to-close, with its
`::backdrop` made a light, basket-preserving tint instead of the near-opaque
default. That trade-off is reversed here, mirroring the identical call
already made four times for the same reason.

### The fix
- `index.html`: the payment-trigger button's `onclick` — `.showModal()` →
  `.show()`. Both design comments above it rewritten to state the new
  rationale (and why it's now the opposite of what ut-docs#1252 chose).
- `app.css`: `.payment-overlay` gets `z-index: 500` (matching the four
  precedent dialogs — needed now that `.show()` no longer promotes it to
  the native top layer, kept below `#osk`'s `z-index: 1000` so the
  keyboard renders above whichever field it's typing into). Its
  `::backdrop` rule removed (dead code once non-modal — a `.show()` dialog
  never paints one; none of the four precedents replace it with anything
  either).
- `e2e/tests/tender-panel-reachable.spec.ts` and `split-tender-i18n-925.spec.ts`:
  one comment each corrected — both asserted "`#payment-overlay` is a
  MODAL dialog that blocks pointer events on the rest of the page" as the
  *reason* for a specific test-ordering choice; no longer true. Test logic/
  ordering deliberately left unchanged (still matches the real operator
  flow).
- New `e2e/tests/payment-overlay-osk-1385.spec.ts`: drives a real,
  non-forced click on a real `#osk` key while the overlay is open and
  asserts the target field's value actually changed — proves the click
  lands, not just that the keyboard is visible (a geometry/visibility
  check alone would pass against the broken build too).

## Independent review (Opus, different model, isolated worktree)

**Verdict: yes, with one required follow-up** — not a rejection; the
core fix (`.show()` + z-index/backdrop) was confirmed correct and clean
(build/vet/gofmt clean — no Go touched; all 4 requested guards green; all
41 requested Playwright specs, then 68 more across every spec touching
`#payment-overlay`, all green; independently re-did the revert→run→restore
TDD verification and confirmed the real click-intercepted failure
pre-fix).

**Findings and disposition:**

1. **BLOCKING — fixed in this same PR.** Making `#osk` tappable also made
   it possible for `#osk` (z-index 1000) to sit ON TOP of the overlay's own
   Complete Sale/Clear buttons: `body.osk-padded`'s 15.5rem bottom
   reservation is a `<body>` padding, which does nothing for
   `.payment-overlay` — `position: fixed`, sized against the viewport, not
   body's padding box. Measured live, pre-follow-up-fix, at 1024×600:
   `#split-tender-submit` at y 436–487, `#osk` spanning y 312–600 —
   genuinely, unreachably covered (reproduced at 1280×720 and 1366×768
   too, not just one viewport). **Fix:** `body.osk-padded .payment-overlay`
   now clamps `height`/`max-height` to `calc(100dvh - 15.5rem)` (same two
   physical properties as the base rule, deliberately not `block-size`, to
   avoid any logical/physical cascade ambiguity) — this lets
   `#split-tender-card`'s **existing** `flex: 1; overflow-y: auto` (the
   same floor ut-docs#161's own review already established for this exact
   collapse class) take over: the panel now scrolls internally to reach
   the buttons instead of rendering them under the keyboard. Verified with
   real `getBoundingClientRect()`/`elementFromPoint` measurements before
   and after, at 1024×600, 1280×720, 1366×768 and 1280×800 (the last one
   already worked by coincidence). TDD-verified: removing the clamp rule
   reproduces the poll-timeout failure on the new regression test at both
   1024×600 and 1280×720; restoring it is green again, 5/5 repeated runs.
   New test: `payment-overlay-osk-1385.spec.ts`'s second describe block —
   real click, no force, completes an actual sale with the keyboard open,
   at both viewports.
2. **Note — self-contradictory comment, fixed in this same PR.**
   `app.css`'s `::backdrop`-removal comment named `#hold-modal`/
   `#pfand-modal` as still using `showModal()`, two lines after correctly
   calling them "the other four non-modal dialogs." Corrected to name only
   `#modifier-modal` (the one dialog in this file still genuinely modal).
3. **Note — accepted, not fixed here (already tracked separately).** This
   fix makes ut-docs#1284's decimal-corruption bug newly *reachable*
   inside the Split tab's `amount` field (previously untappable there at
   all under `showModal()`). ut-docs#1284 is already `p1` (top tier) and
   already being worked by a different lane/card on the board — no label
   change needed; cross-referenced by comment on both issues so whoever's
   working #1284 has this context.
4. **Note — deferred as a new Backlog card, not fixed here.** With the
   overlay open, `New Sale` (`.tender-default-footer`, outside the dialog)
   is a real hit target at some viewports and resets the live basket while
   `payment-overlay.open === true`, leaving the panel stale (no data
   corruption — a subsequent Complete Sale is rejected server-side,
   `tender.status.basket_unavailable` — but a confusing stale panel).
   Out of scope for an OSK-tappability fix; filed as ut-docs#1386 (new
   Backlog card) for a cheap follow-up (same `hx-on::after-request` close
   handler the pay-grid buttons already carry).
5. **Note — the reviewer's own first-pass regression test needed
   hardening, applied in this same PR.** The manual `elementFromPoint`
   hit-check immediately after `scrollIntoViewIfNeeded()` raced the
   payment-pill's DOM insertion/reflow and was flaky (~1 in 3 runs) in
   isolation. Replaced with `expect.poll(...)` re-scrolling and
   re-measuring each attempt — 5/5 stable afterward, still genuinely fails
   (poll timeout, not a false negative) with the clamp fix reverted.

**The five adversarial questions the review asked, and the answers**
(paraphrased from the review — full detail in the review transcript):
dropping `::backdrop` is a strict continuation of the existing
basket-stays-visible intent, not a regression, and matches the four
precedents exactly (none replace it either); the new `z-index: 500` is
verified sound against every other `z-index` in `app.css` (toast 2000,
`#osk` 1000, nav/statusbar under 500) with no ancestor stacking-context
trap on the chain (`.tender`/`.pos-container`/`main.container`/
`body.sale-screen`/`html` all checked — none), including under RTL; losing
native Escape-to-close/focus-trap is the same accepted trade the four
precedents already made, with no spec or manual topic asserting
otherwise; the manual (`web/help/en/sell.md`, `payments.md`) already
describes the panel purely by behaviour and needed no update; no i18n/
RTL/money angle, no secrets, no real shop names, no missing
`os.MkdirAll`/`paths.Data` (zero Go files touched).

## Verified beyond automated tests

- TDD claim re-verified twice, independently, by reverting only the
  production line/rule and confirming the exact expected failure mode
  before restoring: (1) `.show()` → `.showModal()` reproduces the
  original "dialog intercepts pointer events" click-timeout; (2) removing
  the `body.osk-padded .payment-overlay` clamp reproduces the new
  hit-test poll-timeout, at both viewports.
- Live measurement via `page.evaluate()` (`getBoundingClientRect`,
  `getComputedStyle`, `document.elementFromPoint`) at 1024×600, 1280×720,
  1366×768, 1280×800 — not just Playwright's pass/fail, the actual pixel
  geometry before and after.
- Full regression sweep: every spec touching `#payment-overlay` or `#osk`
  (128 tests across 20+ files) green, run 3× to rule out flakiness in the
  new tests specifically.
- `go build`/`go vet`/`gofmt -l .` clean (no Go files in this diff);
  `guard-i18n.sh`, `guard-e2e-fixtures-import.sh`,
  `guard-compliance-claims.sh`, `guard-htmx-loaded.sh`,
  `guard-help-topics.sh` all green.
- **`guard-docs-shots.sh` caught locally missed and CI-failed once**: this
  diff touches `web/ui/**`/`web/public/**`, which the guard treats as
  "the app surface changed" regardless of whether a given screenshot's
  actual pixels moved (the default sell-screen screenshot doesn't happen
  to show the payment overlay open at all, so `en/sell.png` came back
  byte-identical after regeneration) — `make docs-shots` re-ran and
  refreshed the surface-hash manifest; confirmed green after.

## Safe to merge

Yes. One blocking finding from independent review, fixed and re-verified
in this same PR; two informational notes fixed alongside it; two more
genuinely out of scope, tracked as cross-references / a new Backlog card
rather than silently dropped.

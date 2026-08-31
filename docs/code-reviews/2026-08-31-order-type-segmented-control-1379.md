# Code review — replace the ambiguous order-type switch with a two-segment control

- **Date:** 2026-08-31
- **Branch:** `fix/1379-order-type-segmented-control`
- **Reviewer:** self-reviewed inline (small, contained UI change; visually
  verified live against a real running till — see below)
- **Verdict:** Safe to merge.

## Context

ut-docs#1379: the product owner found the single sliding `.order-type-switch`
(introduced 2026-08-30, replacing an even-older two-button layout) genuinely
ambiguous in live use — the label text changes on tap, so it reads as "tap
to switch TO this" rather than "this is active now." This control decides
the applied VAT rate (§12 UStG dine-in/takeaway split); a misread puts the
wrong tax on a fiscal receipt.

Requested fix, product owner's own words: "a better switch small but show
which one is on, maybe 2 toggle switch when I on one the other one get off."

## What changed

- `web/ui/partials/basket.html`: the single `<button role="switch">` is now
  a `<div role="radiogroup">` containing two always-visible `<button
  role="radio" aria-checked>` segments (🍽️ Dine in / 🥡 Takeaway), each
  posting its own explicit `order_type` value (`""` / `"takeaway"`) instead
  of the old "compute the opposite of current state" `$nextOrderType` logic.
- `web/public/app.css`: `.order-type-switch`/`-track`/`-knob`/`-label`
  replaced with `.order-type-toggle-group` (the pill container) and
  `.order-type-option`/`.is-active` (each segment). Deliberately named
  differently from the self-order kiosk's own existing `.order-type-toggle`
  (`self_order_cart.html`) — that control already uses this exact
  two-button pattern successfully and is untouched by this change; reusing
  its class name would have silently reskinned it too.
- `e2e/tests/basket-hx-sync-race-1337.spec.ts`: updated to click
  `[data-testid="order-type-takeaway"]` explicitly instead of the old
  "click the switch, it flips to the opposite" assumption.
- `make docs-shots`: regenerated (92 screenshots) — the sell screen's
  appearance changed.

## Review notes

- **Same tap-target floor preserved**: `min-height: 3rem` on
  `.order-type-option` (the actual tappable element), matching the exact
  lesson the switch's own predecessor CSS comment recorded (ut-docs#161/
  #213 — a `px` floor previously made this control smaller than the one it
  replaced once the operator's `--ui-scale` exceeded 1). Kept as a rem
  value here too.
- **Same footprint**: `.order-type-toggle-group` is `flex: 1`, occupying
  exactly the row slot the switch used, still sharing the row evenly with
  the table-assignment button (ADR-0054) next to it — confirmed visually
  (screenshot below), not just by reading the CSS.
- **No kiosk collision**: verified `.order-type-toggle`/`.selforder-order-type`
  (the self-order cart's existing class names) have zero CSS rules
  targeting them today (`grep` confirmed) and this change doesn't add any —
  the two controls now happen to share a *design pattern* but not a single
  line of CSS or a class name.
- **ARIA**: `role="radiogroup"` + `role="radio"` + `aria-checked` (a
  single mutually-exclusive choice between two named states) rather than
  the switch's `role="switch"` + `aria-checked` or two independent
  `aria-pressed` buttons — this is the semantically correct WAI-ARIA
  pattern for "exactly one of two named options is selected."
- **htmx**: both segments keep `hx-sync="#basket:replace"` (ut-docs#1337)
  — every other `#basket`-targeting trigger already has this; dropping it
  on either segment would reopen the exact race that fix closed.
- **i18n**: no new keys — `basket.order_type.{label,dine_in,takeaway}` all
  already existed (used by the switch they're replacing), confirmed
  present in `en.json` and unchanged.
- **Live-verified, not just unit-tested**: booted a real till (`e2e/run-till.sh`),
  drove it with a real headless Chromium, and screenshotted both states —
  Dine in filled / Takeaway plain, then after clicking Takeaway, the
  reverse. Both segments visible and unambiguous in both states.

## Before committing checklist

- `gofmt -l .` — clean (no Go source touched by the diff itself).
- `go build ./...` — clean.
- `go test ./internal/pages/... ./internal/pos/...` — clean.
- `e2e/tests/basket-hx-sync-race-1337.spec.ts` (the one spec referencing
  the old switch) — passes against the new markup.
- `scripts/ci/guard-docs-shots.sh` — clean after `make docs-shots`.
- `scripts/ci/guard-i18n.sh` — clean (1318 keys, no drift).

## What this does NOT do

This doesn't touch the self-order kiosk's own order-type control
(`self_order_cart.html`) — it already uses a two-segment pattern and
wasn't reported as confusing. It also doesn't fix the separate,
still-open takeaway-VAT computation bug (ut-docs#1370) — this is purely
about making it possible to *trust what the control is telling you*
before that bug is even in play.

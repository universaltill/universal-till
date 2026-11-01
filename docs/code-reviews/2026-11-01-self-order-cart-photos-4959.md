# Review — self-order cart shows item photos (ut-docs#4959)

Date: 2026-11-01 · Branch `feat/4959-self-order-cart-photos` · Lane `lane:cloud-41`
Author: Opus 5.5 build cycle · Independent reviewer: Sonnet subagent (different model)

## What shipped

- **Cart photo.** `web/ui/partials/self_order_cart.html`: each self-order cart
  line shows the item's picture (`ImageURL`, the same resolver the tile and the
  cashier basket's `.line-thumb` use, #4838). A line with no picture gets an
  empty placeholder so rows stay aligned. It never renders an empty `<img>`.
- **Line layout** (`web/ui/pages/self_order_shop.html`): two grid rows beside
  the photo. The name and modifiers take the full width, then qty, total and
  remove. A plain photo column left the name about 22px on a 360px phone and
  about 70px in the 22rem kiosk cart (measured). Logical properties only.
- **Mid-order refresh.** `catalog.RefreshBasketPhoto` now also pushes a changed
  photo to `d.KioskEngine` and every table session, through the new
  `pos.SessionBasketManager.SetItemImage`. Before, it reached only the cashier
  `Engine`. Only an image string goes into each basket, and no basket is read
  across engines (ADR-0020 isolation holds).
- Help: `web/help/en/self-order.md`, one sentence (cart photo, refresh on the
  next update).
- `web/help/img/manifest.json`: surface hash only. No docs screenshot shows a
  self-order page and the topic has no manifest entry, so no screenshotted
  pixel changes.

## UX decision (the card's open question)

Customer-facing carts show photos: self-order kiosks commonly show item
pictures in the order summary, and it helps a customer check the order before
paying. Mid-order refresh: yes. It costs one method and keeps the three
baskets consistent.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | nit | `.selforder-cart-line-qty` is declared twice (grid-area rule plus the older flex rule) | Accepted: the two rules set different properties |
| 2 | nit | `.btn-x` gets its grid area through a child combinator | Accepted: no current restyle path |

No blocker, major or minor findings. The reviewer checked:

- **Lock order:** snapshot under `m.mu`, then Service calls outside it, the same shape as `SetConfig`. `-race` is clean.
- **Nil safety:** every combination of nil engines.
- **Kiosk guard:** `guard-kiosk-engine` passes.
- **Grid areas:** they hold with modifiers, counter mode and age-restricted variants.
- **E2E specs:** the three cart specs use only selectors this change keeps.

## Verified beyond unit tests

- **TDD:** both page tests failed before the fix. The kiosk and session lines kept `ImageURL=""`, and the cart had no thumbnail markup. They pass after.
- **Driven run:** I ran the e2e demo till with real Chromium on `/self-order/shop`.

| Viewport | Name width | Page horizontal scroll |
|---|---|---|
| 1280×800 | 227px | 0 |
| 1080×1920 portrait | 219px | 0 |
| 360×800 phone | 216px | 0 |

- **RTL:** fa at 360px mirrors correctly, with no horizontal scroll. I looked at the screenshots myself.
- **Gate:** gofmt clean, `go build ./...`, golangci-lint 0 issues, `go test ./...` (every package; `internal/plugins` hit the default 10-min timeout compiling wasm in this container, untouched here, re-run with `-timeout 30m`, and CI runs it too), and the ci.yml build-job guards all passed. Four checks failed only because of the cloud container:
  - `guard-shellcheck-version`: no shellcheck binary here.
  - `prune-nightly-assets`: needs a release `dist/`.
  - `retry-with-backoff`: a helper, not a guard.
  - The docs-shots guard went green after the surface-hash refresh.

## Verdict

Safe to merge.

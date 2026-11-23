# Review: kiosk basket edits are refused while that basket's checkout runs (ut-docs#6926)

Date: 2026-11-23 · Branch `fix/6926-kiosk-basket-edit-guard` · Lane `lane:cloud-41`
Author: Sonnet (Dev). Independent review: Opus 5.5, fresh context. Tester: separate agent.

## What shipped

- `internal/pages/self_order_shop.go`: the new helper `kioskBasketEdit` admits each kiosk basket mutation with the per-basket engine's `BeginBasketEdit()` (ut-docs#6919). The caller defers `EndBasketEdit()`. On refusal it answers 409 and renders the kiosk cart with the toast `selforder.basket_locked_checkout`, and the basket stays unchanged. Five routes use it: `/api/self-order/scan`, `/scan-with-modifiers`, `/line`, `/order-type` and `/remove`. Admission comes before any read-modify-write, including `/line`'s read of the current quantity. `d.Engine` is never used (ADR-0020, `guard-kiosk-engine.sh`).
- Before this change, an item a customer added while `POST /api/self-order/checkout` held `TryBeginTender()` was in neither the paid sale nor the basket after the post-sale Reset. It was silently lost.
- `web/locales/en.json` and `web/i18n-meta/en.meta.json` add the new key, English only (ADR-0160). `web/help/en/self-order.md` gains a "What can go wrong" bullet.
- The page already force-swaps any 4xx response on `/api/self-order/*` (`self_order_shop.html`), so the 409 cart reaches the customer. No ADR and no migration were needed: this applies the #6919 admission mechanism to the kiosk.

## Tests

The tests are in `internal/pages/self_order_basket_edit_6926_test.go`.

- `TestSelfOrder_BasketEditsRefusedWhileCheckoutRuns_6926`: all five routes return 409 with the message. The lines, quantity, line order type and basket order type are unchanged. After `EndTender` a scan succeeds.
- `TestSelfOrder_BasketEditRefusalIsPerBasket_6926`: while one table guest's checkout holds, that guest's edits are refused and a second table's guest can still add.
- Both tests fail on `main`. The Dev ran them red before green. The Tester stashed only the production file and saw every route return 200 and change the basket. The Reviewer re-ran them in a clean worktree at HEAD and got the same failure.

## Independent review (Opus 5.5)

No blocking findings. The review checked:

- every admitted edit is released on every return path;
- no route calls `TryBeginTender` while it holds an edit, so no deadlock;
- the counter-order checkout is covered, because `TryBeginTender` is taken before its branch;
- 409 is consistent with checkout's own refusals.

Fixed in this branch:

- **N3, wording.** "Your order is being paid…" was wrong for counter mode and table-QR guests, where checkout takes no payment. It now reads "Your order is being placed. Wait for it to finish, then add more." The meta description says the wording must fit both modes.
- **N4, help line.** It now lists every refused action and says "finishes or fails", since a customer can't cancel a running kiosk payment. The Tester found that the line said "Eat in" while the kiosk button says **Dine in**. That is fixed too.
- **N5, test gap.** The test now also asserts that the line and basket order types are unchanged, so a guard that answered 409 but still switched to takeaway would fail.

Accepted as is:

- **N6.** While checkout runs, `/line` and `/remove` answer 409 rather than 400 for a missing key. The cashier `basketEdit` wrapper does the same, and it does no harm.

Pre-existing, filed as ut-docs#6943 (findings 1 and 2):

- `GET /self-order` resets the walk-up basket with no guard.
- `bindSelfOrderTableSession` can move a table session's table mid-checkout.

## Tester

All of the following ran on the final wording:

- **Full gate:** `go test ./internal/pages/ ./internal/pos/ -count=1 -timeout 60m` passes. internal/pages takes about 650s here.
- **Static checks:** `go build ./...`, `gofmt -l .` and `go vet ./internal/pages` are clean.
- **Guards:** kiosk-engine, i18n, i18n-meta, help-topics, fixed-dates, no-native-confirm, rtl-logical-css and compliance-claims all exit 0.
- **Driven run on the real app** (temp data dir, demo catalogue): real `/api/self-order/scan` requests sent in parallel with a real walk-up card checkout got 409 with the toast body (19 to 47 per run).
- **Browser:** in Playwright chromium at 1024x600, a real tile tap added to the cart. The server's real 409 body was then swapped into `#selforder-cart`, and the toast showed. It is centred, wraps onto two lines and is not clipped.
- **Not checked:** dark theme, phone size, RTL and German. A slow payment was not driven, because that needs a signed blocking payment plugin. `-race` on internal/pages was not run: it takes more than 90 minutes on this container. The engine admission it relies on is covered by `internal/pos` race tests from #6919.

Pre-flight: review record written; `web/help/` updated (self-order "What can go wrong"); `reference/` n/a (no reference page lists kiosk route behaviour); `en.json` + `en.meta.json` updated (English only); README n/a; follow-up → ut-docs#6943.

# Review — self-order kiosk language picker (ut-docs#4200)

Date: 2026-10-20 · Lane: `lane:cloud-41` · Author: Opus 5.5 · Reviewer: Fable 5.1 (independent subagent, own worktree)

## What shipped

- **Start screen picker.** `GET /self-order` lists every installed language (`httpx.AvailableLocales`) by its native name. Each button carries its own `lang`, and the shop's language is marked `aria-current`. The picker is hidden with fewer than two languages. There are no flags, because a flag names a country, not a language.
- **`GET /self-order/language?code=<loc>`.** The code is checked against the installed languages; an unknown code redirects to `/self-order` and is never stored. A valid code sets a browser-session cookie `ut_self_order_lang` (HttpOnly, SameSite=Lax, Path `/`) and redirects 303 to `/self-order/shop`, so one tap starts the order. Picking the shop's own language clears the pick, so an `en-GB` shop keeps its regional formatting tag.
- **`httpx.RequestLocale`** honours the pick only on `/self-order`, `/self-order/*` and `/api/self-order/*`. The order is `?lang=`, then the pick, then `ut_lang`, then the default. `T`, `dir` (RTL), `<html lang>` and money/number formatting follow it. The 1-year `ut_lang` cookie is never written, so the pick can't greet the next customer.
- **Reset.** The walk-up start screen (`!bound`: idle reset, the confirmation's Done and auto-return, Back, or a `?table=` that binds nothing) clears the pick and renders in the shop's language. A bound table-QR landing (the guest's own phone) keeps it.
- **Unchanged.** Order lines, the kitchen ticket (`DefaultLocale`), the counter-order label, the journal and reports stay in the shop's language. Checkout uses the request locale only for on-screen messages and the tracking link.
- **No new i18n key.** The nav's accessible name reuses `setup.language.title` ("Choose your language"), which means the same thing, so no language-pack follow-ups are needed.
- **Help.** One paragraph in `web/help/{en,de,tr,ar,fa}/self-order.md`.
- **Demo mode.** The route is classified as denied, like the rest of `/self-order`.
- **Split out.** Per-item catalogue translations (ut-docs#4703), the menu in the customer's language (ut-docs#4704) and the customer receipt/order status in their language (ut-docs#4705).

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | medium | The clear keyed on `?table=` being present, not on a table actually binding. An unknown or disabled `?table=` reset the basket but kept the last customer's language. | **Fixed:** keyed on `!bound`. New test `UnboundTableLandingClearsPick` failed before the fix and passes after. |
| 2 | low | On an `en-GB` shop, tapping "English" stored the bare `en`, which shifts money/number formatting for that order. | **Fixed:** picking the shop's language clears the pick. Test `ShopLanguageClearsPick`. |
| 3 | low | The GET has a side effect: a cross-site `<img>` could flip a visitor's kiosk language. | **Accepted.** It affects only the visitor's own browser on the self-order routes, and the value is validated. |
| 4 | low | A table-QR guest returns to the shop's language after Done, because the confirmation goes to bare `/self-order`. | **Accepted.** This matches the help text ("when the order ends"). The table-binding loss on Done was already there. |
| 5 | low | The e2e spec assumes the e2e till ships ≥4 languages, including fa. | **Accepted.** The assumption is stated in the spec. |

Found by the Tester's driven run before review: the shop's regional locale (`en-GB`) marked no button as current. It is fixed with a base-language fallback, and `TestSelfOrderLanguages_MarksRegionalShopLocale` failed before the fix.

## Verification

- **TDD re-verified by the reviewer in its own worktree.** With the `RequestLocale` hook removed, `TestRequestLocale_SelfOrderCookie` fails. With the base-language fallback removed, `TestSelfOrderLanguages_MarksRegionalShopLocale` fails. Both were restored and pass.
- **Full gate after the last edit.** `gofmt` is clean, `go build ./...` passes, `go test ./...` passes (`internal/plugins` hit the 10-minute default timeout under a parallel run and passed alone in 833s; this change doesn't touch it), and `golangci-lint` reports 0 issues.
- **Guards.** The `ci.yml` build-job guards pass. Two exceptions are local only:
  - `guard-deadcode-baseline` flags `internal/logging` on `main` too, because the GTK headers are missing so `cmd/unitill-desktop` is skipped here.
  - `guard-shellcheck-version` fails because there is no shellcheck binary here. No shell files changed.
- **Driven run.** Playwright `e2e/tests/self-order-language-picker-4200.spec.ts` ran against a real till at 1024×600 and 360×740.
  - Geometry asserted: buttons ≥48px tall, inside the viewport, no overlap, no horizontal scroll.
  - Flow: tap فارسی → `/self-order/shop` with `lang=fa dir=rtl` → walk-up start screen back in ltr/shop language.
- **Screenshots looked at.** The kiosk start screen at both sizes: four buttons, English outlined as current, wrapping 2×2 at 360px. The shop page in fa: RTL, Persian digits in prices, item names in the shop's own text as designed.
- **Not checked.** Dark theme, a physical kiosk Pi, and a screen reader's voice switching per `lang`.

## Verdict

Safe to merge.

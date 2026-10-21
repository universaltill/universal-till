# Review — kiosk customer's receipt in their language (ut-docs#4705)

Date: 2026-10-21 · Lane: `lane:cloud-41` · Author: Opus 5.5 · Reviewer: Fable 5.1 (independent subagent, read-only)

## What shipped

- **Already in place, left alone.** The confirmation screen renders on a self-order route, so it follows the pick (ut-docs#4200). The order-tracking link bakes `?lang=` from that request. The gap was the printed receipt, which always used `httpx.DefaultLocale()`.
- **Carrier, no migration.** The pick rides the basket the way the order-tracking token does (ut-docs#4206): `pos.Service.customerLocale`, `BasketSnapshot.CustomerLocale` (`customer_locale`, omitted when empty), carried by `Snapshot`/`RestoreHeld` and cleared in the same four places as the token. The design note on the card suggested a `customer_locale` sales column; it isn't needed, because only the auto-printed customer receipt reads the pick. Reprints from the journal are the shop's copy and stay in the shop's language.
- **Kiosk card sale.** `POST /api/self-order/checkout` writes `eng.SetCustomerLocale(pick)` just before `completeTender`, `""` when there's no pick. That way a failed checkout on the shared walk-up basket can't hand its pick to the next customer. `completeTender` reads it before `Reset` and passes it to `printReceiptAsync`.
- **Pay at counter.** The parked snapshot's `CustomerLocale` is set from the request (never from the basket). When the cashier resumes and tenders the order, the receipt prints in the customer's language.
- **Print path.** `customerReceiptDoc` uses the pick only when it's an installed locale. The payload is device input on a replica. If the printer's restricted charset can't encode the customer-language receipt while the shop's one does, it prints the shop's language instead (`print.Encodable`, same approach as `kitchenTicketText`).
- **Help.** One sentence added to the language paragraph in `web/help/{en,de,tr,ar,fa}/self-order.md`.
- **No new i18n key.** So no language-pack follow-ups.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | medium | Receipt policy "ask" or manual printing: the cashier's Print button (`POST /api/print/receipt/{no}`) prints in the shop's language, so a pay-at-counter customer at such a shop never gets their language. | **Narrowed and filed.** The help now says the receipt prints *automatically* in the customer's language, and a receipt printed by hand is in the shop's language. Carrying the pick into the tender screen's Print button is a follow-up Backlog card. |
| 2 | low/medium | The fallback also tested language-independent text: an unencodable store name dropped a fully encodable customer language. | **Fixed:** fall back only when the shop's receipt does encode. The new assertion in `TestCustomerReceiptDoc_Language` failed before the fix. |
| 3 | low | The counter test stops at `RestoreHeld` and doesn't tender on the cashier engine. | **Accepted.** `completeTender` reads `engine.CustomerLocale()` from whichever engine it gets. The kiosk card test covers that read, and the `pos` lifecycle test covers resume/re-park. |
| 4 | low | Cloud `guest_order` directive orders carry no locale. | **Filed** as a Backlog card. The cloud GuestOrder has no locale field yet. |
| 5 | low | Reference doc silent on receipt language; no review record yet. | **Fixed:** ut-docs `architecture/receipt-printing.md` gains a "Receipt language" section; this record. |

Also found during the build: `buildReceiptDoc` had lost its last production caller, which would trip `guard-deadcode-baseline`. The shop-language path now goes through it.

## Verification

- **TDD, verified locally:** each new test failed with the behaviour removed and passed restored.
  - Removing `SetCustomerLocale(pick)`: `ReceiptPrintsInCustomersPick` and `PrintedReceiptBytesInPick` fail.
  - Removing `snap.CustomerLocale`: `HeldOrderCarriesPickToTender` fails.
  - Removing the encodability fallback: `CustomerReceiptDoc_Language` fails (Persian printed on a win1254 printer).
  - Ignoring the shop doc's encodability: the store-name assertion fails.
- **Driven print, no stubs:** a real kiosk checkout with a Turkish pick wrote ESC/POS bytes to a win1254 device file. They carry Windows-1254-encoded "Fiş " and no "Receipt ".
- **Gate:** gofmt clean, `go build ./...`, golangci-lint 0 issues, `internal/pages`/`internal/pos`/`internal/httpx` tests pass. The `ci.yml` guards pass except two that are local only: `guard-deadcode-baseline` (the pinned `deadcode` tool was built with go1.26 and refuses this repo's go1.27 files) and `guard-shellcheck-version` (no shellcheck binary here; no shell files changed). CI runs both.
- **Not checked:** a physical thermal printer, and RTL rendering of an ar/fa receipt on a UTF-8 printer (same honest-v1 status as a fa shop's own receipts).

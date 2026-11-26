# Review: external card-terminal tender (ut-docs#6755)

Date: 2026-11-26. Lane `lane:cloud-41`. Branch `feat/6755-external-card-tender`.
Design: ADR-0205 §1/§7/§8 (Accepted); no new ADR needed.

## What shipped

- Migration `117_payment_method_card_external.sql`: built-in payment method
  `card_external` (type card, no plugin owner); checksum pinned.
- Settings > Card readers: manager-only "Add your own terminal" (name only).
  The row is `kind=external`, `transport=none`, `method_key=card_external`,
  `plugin_id=''`, server-generated `ext-…` ref. Same 403 for cashiers and the
  existing `card_reader_added` audit entry.
- Tender: the "Which reader?" panel shows an own-terminal row as Ready (never
  probed, works offline). The cashier types the amount into their own
  terminal and presses **Paid** (optional auth code, up to 12 letters/digits;
  optional last 4, exactly 4 digits) or **Declined** (closes the panel,
  sends nothing). The panel never submits an external row on its own.
- Server: auth code / last 4 are read only for `card_external` on the form
  tender branch; bad input → 422; no resolved external reader → 409
  `reader_required`; `terminal_id` = the reader ref; last 4 stored as
  `•••• NNNN` (no other card data, ADR-0127). One `external_card_tender`
  audit row per leg, without the typed values.
- X/Z: own line through the existing group-by-method; `PaymentMethodKey`
  returns `tender.card_external` ("Card (own terminal)").
- Never offered on the self-order kiosk, in Split, or in the settings
  default-method and fee lists; a JSON split leg naming `card_external` is
  refused with 400 before anything is written.
- `readerEligible` fails closed: kind `external` if and only if the method is
  `card_external`.
- Help: `card-readers.md` (no longer "every reader needs internet"; own
  terminal section), `payments.md` (Paid/Declined steps). 12 English keys
  with `en.meta.json` metadata.
- Legacy "Card" button unchanged (BA decision D1, default kept).

## Review

Independent review by Opus 5.5 (code by a Sonnet Dev). UX §7 gate and
Tester run by separate agents.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| R-F1 | high | Every self-order kiosk showed a "Card (own terminal)" button (`self_order_shop.go` lists) | Fixed: `withoutExternalCardMethod`; `TestSelfOrderShop_NeverOffersCardExternal` (seen failing first) |
| R-F2 / UX-F1 | medium | Declined notice wrote into the hidden `#ut-print-unavailable` (no `#pos-live`), wiping its link/dismiss | Fixed: own visible `#pay-live` region in `#panel-pay`; fallback removed |
| R-F3 | medium | Split offered `card_external`; with one external row a split leg auto-picked it with no Paid step | Fixed: removed from Split; server refuses the leg (400); `TestExternalTender_SplitJSONRefusesExternalLeg` |
| R-F4 | low | Shown in settings default-method and fee lists | Fixed (helper test; page wiring untested) |
| R-F5 | low | `readerEligible` didn't tie external kind to the method | Fixed; table test extended both ways |
| R-F6 | low | No X/Z or refund tests | Fixed by Tester: `card_external_reports_test.go` (X line, Z label — fails without the `PaymentMethodKey` case — and refund) |
| UX-F2 | partial | Browser `pattern` bubble hid the translated error; duplicate announce; no `aria-invalid` | Fixed: `novalidate`, `aria-invalid` + `aria-describedby`, focus to bad field |
| UX-F3 | minor | Amount not in `<bdi>` | Fixed (`printfBDI`) |
| UX-F4 | nit | Inputs 44px vs the picker's 48px floor | Fixed |
| Fix-verify | — | `TestReaderEligible_Table` external fixture had no method after R-F5 | Fixed (fixture; rows added for both mismatches) |

Deferred (low, epic #6753 checklist): T-D2 on a 422 the overlay's generic
"Something went wrong" banner stays beside the specific error; T-D3 the
inline error line is unstyled; T-D4 refund method dropdown shows the raw id
`card_external` (existing pattern for method ids); sticky action bar height
at 1024×600 (UX-F5, not measured); per-reader totals (#6683); a plugin
subscribing to `payment.card_external.authorize`; DSFinV-K/TSE mapping check
in the fiscal plugin repos.

## Verification beyond unit tests

Tester drove the real binary (Playwright, 1280×800): added an own terminal in
Settings, rang a sale, Paid → sale completed and receipt printed; bad last 4
forced through → 422 shown in the panel; Declined → nothing sent, basket
kept. Not checked: dark theme, kiosk size, RTL, German, phone width.

Gates: `gofmt -l .` empty, `go build ./...`, `go vet ./internal/...`,
`golangci-lint run ./...` 0 issues, `shellcheck scripts/ci/*.sh` 0 issues,
targeted pages/data/db/httpx tests pass, guards (i18n, i18n-meta,
help-topics, card-data-schema, core-neutral, rtl-logical-css, no-showmodal,
no-native-confirm, kiosk-engine, data-access, fixed-dates) exit 0; full
`go test ./...` run locally after the last edit (result in the PR).

Pre-flight: help topic updated (card-readers, payments); docs-shots deferred
→ #6753 checklist (screenshots not regenerated); `reference/` n/a (ADR-0205
already describes it); README n/a (no claim changed); en.json keys →
en.meta.json updated; review record this file; follow-ups listed above.

Verdict: ship.

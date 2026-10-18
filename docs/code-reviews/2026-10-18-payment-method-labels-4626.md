# Review: payment-method names instead of raw ids (ut-docs#4626)

## What shipped

Payment-method ids (`cash`, `card`, `gift`, `voucher`) showed raw, in
English, in every locale in these places:
- the payment overlay's Split tab: method `<select>`, pending-payment pills
  and the "added" status;
- the on-screen receipt's payment line;
- the journal detail's payments and the journal tender column;
- the reports Payments tab.

The printed receipt and Z-report already translated `cash`/`card`/`voucher`
(ut-docs#4199). The legacy `gift` row still printed "Gift".

- `httpx.PaymentMethodKey(id)` maps the built-in ids to the keys they
  already have in every locale: `tender.cash`, `tender.card`,
  `tender.voucher`, `tender.gift_card`. It is case-insensitive on the
  trimmed id. No new locale keys, so no language-pack follow-ups.
- The `tenderLabel` template func translates those ids too. A plugin's own
  method id still renders as-is, and the `unknown`/`split` sentinels
  translate as before. `receipt.html`, `journal_detail.html` and
  `reports_tab_payments.html` use `{{ tenderLabel .Method }}`.
- `receiptMethodLabel` (printed receipt + Z-report) uses the same helper, so
  `gift` prints "Gift Card" / its translation. Plugin ids keep the
  title-cased fallback.
- The Split `<select>` gets the full `[]data.PaymentMethod`. It renders
  `{{ T .Name }}`, the same as the Pay grid. Migrations 022/024 give the
  built-ins `tender.*` names, and a plugin method's label key resolves the
  same way it does on the grid. Option values stay the ids.
- `app.js` `methodLabel(id)` reads the visible option text for the pills
  and the "added" status. The POSTed payment object still carries only the
  id.

## Review

Independent review by a different model from the author (author Sonnet,
reviewer Opus, fresh context, per MODEL-ROUTING for `complexity:easy`).
No blocking findings.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Low | The legacy `gift` row's printed and Z-report label changes in en from "Gift" to "Gift Card". Other locales may be longer on a 42-column print. | Accepted. This is intended, and the print layout already wraps long labels. |
| 2 | Low | A shop that renamed a built-in method now sees its own name in the Split select, not the id. | Accepted. This matches the Pay grid. |
| 3 | Info | Review record and e2e run were still to do. | Done (this file; e2e below). |

Checks the reviewer made:
- Every parse path of the three templates builds its funcs from `FuncsFor`,
  so `tenderLabel` is always present. There is no separate kiosk, email,
  PDF or plugin-view renderer for them. 18 test-only stub FuncMaps gained
  an identity `tenderLabel`.
- `paymentMethods` had no other consumer.
- No XSS: the pill escapes the label, and the status uses `textContent`.
- No new hardcoded strings. No left/right CSS.

TDD re-verified by the reviewer in a separate worktree. Each production
change was reverted, the test failed for the right reason, then passed once
restored:
- `TestPaymentMethodKey`: build failure, as expected for a new function.
- `TestTenderLabelTranslatesBuiltInMethodIDs`: `"cash"` returned in fa.
- `TestReceiptMethodLabel_GiftTranslated`: `"Gift"`.
- `TestIndexSplitSelect_OptionsAreTranslatedLabels`: raw `gift` as option
  text.
- `TestOnScreenReceipt_PaymentMethodTranslated`,
  `TestJournalDetail_PaymentMethodTranslated` and
  `TestReportsPaymentsTab_MethodTranslated`: raw id leaked, fa label
  missing.

## Verified beyond unit tests

- e2e `e2e/tests/payment-method-labels-4626.spec.ts` (`?lang=fa`) and the
  existing `split-tender-i18n-925.spec.ts` ran green against a real till.
- Screenshot of the fa sale screen with the Split tab open at 1024×600,
  looked at: the select, the pending pill and the "added" status read
  نقدی; nothing clipped; RTL layout intact.
- Not checked visually: 360 px phone width. The scratch flow needs the
  desktop scan box. The change only swaps text in existing elements whose
  layout is unchanged.
- Not checked: a real printer. The printed label for `gift` is covered by
  the unit test only.

## Gates

`gofmt -l .` empty; `go build ./...`; `go vet ./internal/...`;
`golangci-lint run ./...` 0 issues; `go test ./...` green except
`internal/plugins`, which hit the local 600 s default timeout inside a wazero
compile (package untouched; re-run separately, CI is the arbiter); guards
i18n, i18n-meta, rtl-logical-css, no-showmodal, data-access, kiosk-engine,
compliance-claims, help-topics, help-drift, core-neutral, e2e-no-browser,
mail-addresses all exit 0.

No help-topic change: `web/help/` prose names methods by their labels, not
ids.

**Verdict:** safe to merge.

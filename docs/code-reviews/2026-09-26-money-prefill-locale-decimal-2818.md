# Review — money inputs prefill in the locale's decimal separator (ut-docs#2818)

**Change:** on a comma-decimal till (de/es/it/nl/tr/fr) every editable money
input now shows its prefill and placeholder with a comma ("4,00"), matching
the till's keyboard and the rest of the German screen; en/fa/ar keep "4.00".
- `web/public/app.js` `utCurrency.toMajor` emits `<body data-number-decimal>`
  when it is ',' (no grouping — `parseMinor` refuses it).
- `internal/httpx`: `LocalizeMajor(plain, locale)`, `MoneyPlaceholderLocalAttr`;
  template funcs `majorlocal`, `moneyplaceholderlocal` bound to the request
  locale (same `FuncsFor` closure as `decimalsep`, so client and server agree).
- Templates: all 12 `moneyplaceholder` sites (every one a `moneypatternlocal`
  field) → `moneyplaceholderlocal`; server prefills wrapped in `majorlocal`:
  settings payments-fee `fixed` (+ its literal "0.00" placeholder), shifts
  `opening-cash`, promotions `value_amount`, variants-panel item `cost`,
  tender `change` default.
- Help: en/de/tr/fa/ar catalog topic says a saved amount shows in the till
  language's separator; de/tr payments/vouchers examples use a comma.
  `make docs-shots` regenerated.
Author: Opus 5.5. Reviewer: Fable.

Every comma-rendered field was traced to a comma-tolerant reader
(`parseMinor`/`toMinor` client-side, `httpx.ParseMoneyMajor` server-side);
dot-only fields (`percent`, `value_percent` — #2954) untouched.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Numeric OSK pad (`osk.js`) only offers '.', so on a de till a cashier who keeps the "4,00" prefill and taps '.' gets "4,00.5" (refused with the localized message — never misread). Pre-existing, more visible now | Accepted; Backlog card filed |
| 2 | nit | `index.html` tender `change` default hardcodes `"0.00"` for non-zero decimals (pre-existing shape; no 3-decimal currency shipped) | Accepted |
| 3 | nit | `catalog/handlers.go` `costMajor` uses FormatFloat, not FormatMajorPlain (pre-existing, exact for realistic values) | Accepted |
| 4 | nit | Go test asserted a full attribute sequence (brittle to reorder) | Fixed — regex on `id="opening-cash"[^>]*value=` |

**TDD re-verified:** httpx tests failed to build before the helpers; the
three page tests failed on "5.00" before the template edits; the new e2e
spec failed with `Expected "4,00" Received "4.00"` with app.js reverted.
Three existing e2e assertions in de sessions (#2815 OSK `6,005`, #2819 cost,
#2925 promotion) moved from dot to comma — they'd fail on old code too.

**Verification:** `go build`, `go vet`, `go test ./...` (all pass),
golangci-lint 0 issues, every `ci.yml` build-job guard green except two
local-only: deadcode-baseline (fails identically on `main` here — desktop
GTK headers absent) and shellcheck-version (no shellcheck binary; no shell
changed). Playwright: new spec + #2815/#2819/#2925/#1284/#1851/#1272/#1275
money specs, 39 passed. Looked at the de item editor at 1024×600 and 360px
and de shifts at 360px (4,50 / 0,00, nothing clipped). Not looked at:
dark theme, RTL (fa/ar keep the dot — unchanged), a real tablet OSK.

**Verdict:** safe to merge.

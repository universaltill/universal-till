# Code review — CreateReturn hardcoded Currency:"GBP"/TaxInclusive:false (ut-docs#1494)

- **Date:** 2026-09-04
- **Branch:** `fix/1494-return-hardcoded-gbp-currency`
- **Reviewer:** independent read via a fresh-context `Sonnet` subagent
  (complexity:easy → reviewer relaxes to a fresh-context instance of the
  same model, per the `reviewer`/`scrum-master` skills' model-routing
  table), no shared context with the implementation.
- **Verdict: SAFE TO MERGE.** No blocking findings. One coverage nit
  applied before merge; one efficiency nit left as-is (see below).

## What shipped

`internal/pages/inventory_api.go`'s `CreateReturn` (`POST
/api/inventory/return`) built its `pos.SaleInput` with `Currency: "GBP"`
hardcoded and `TaxInclusive` left at its `false` zero value, regardless of
the original sale's actual currency/pricing mode. Both values feed the
`fiscal.sign.ask` payload (ut-docs#1405) and the persisted return sale, so
a German (EUR, VAT-inclusive-priced) shop's return was signed and recorded
as `"currency":"GBP"`, `"tax_inclusive":false` — wrong on both counts for
that market, and load-bearing now that fiscal signing depends on it.

- `CreateReturn` now fetches the original sale's `data.SaleDetail` via
  the existing `repo.GetSaleDetailByID` (no new SQL — repository pattern
  intact) and derives `Currency`/`TaxInclusive` from it via the existing
  `saleIsTaxInclusive` helper — the exact same source `refund_page.go`'s
  sibling `/api/refund` flow already reads (`detail.Currency` /
  `saleIsTaxInclusive(detail)`).
- A missing original sale now returns a clean `404` before any further
  work, instead of surfacing later as a `400 "line_id not found"` once
  `ListSaleLineSnapshots` came up empty.
- Fixed a second, previously-hidden hardcode in the same function: the
  `returnTotal` (refund payment amount) computation passed a hardcoded
  `false` to `pos.ComputeTaxBasisPoints`. This used to silently agree with
  `TaxInclusive`'s own hardcoded `false` — fixing only `TaxInclusive`
  without also fixing this would have made the two disagree for any
  inclusive-priced sale, and `pos.CompleteSale`'s `netPayments` check would
  reject the return with `"payments do not cover total"` (verified live by
  the reviewer, see below). Both now share one `inclusive` value derived
  once from the original sale.
- New regression test `TestCreateReturn_UsesOriginalSaleCurrencyAndTaxMode`
  seeds an EUR, tax-inclusive original sale, drives the real HTTP handler,
  and asserts on the *persisted* return sale (read back via
  `GetSaleDetailByID`), not an intermediate value.

## Review findings

No correctness, money-handling (`internal/money.Money` discipline
maintained — no raw `int64` mixed in), repository-pattern, or
i18n/compliance-wording issues found. The reviewer independently verified
the "both hardcodes must move together" reasoning empirically: reverting
only the `Currency`/`TaxInclusive` fields back to their old hardcoded
values while leaving the `ComputeTaxBasisPoints` call fixed reproduced
exactly the predicted failure —
`"payments (120) do not cover total (144)"` — confirming that fix is not
scope creep but required in lockstep.

Two non-blocking nits:

1. **(coverage, fixed before merge)** No test exercised the new `!found`
   branch for an `original_sale_id` that parses but matches no row.
   Added one assertion to `TestCreateReturn_ValidationErrors` (expects
   `404`), reusing the existing test's pattern.
2. **(efficiency, left as-is)** `GetSaleDetailByID` and the pre-existing
   `ListSaleLineSnapshots` now both query sale-line data separately. This
   mirrors `refund_page.go`'s own existing pattern exactly, and returns
   are a low-frequency, human-driven action — not worth a combined query
   for this fix's scope.

## Verification beyond automated tests

- `go build ./...`, `gofmt -l .`, `go vet ./...` — clean.
- `go test ./...` — full suite green (all packages).
- All CI-blocking guards in `.github/workflows/ci.yml`'s `build` job run
  locally and pass: `guard-data-access`, `guard-migration-version-collision`,
  `guard-kiosk-engine`, `guard-plugin-menu-read`, `guard-page-http-error`,
  `guard-i18n`, `guard-compliance-claims`, `guard-docs-shots`,
  `guard-help-topics`, `guard-webkit-version`, `guard-kiosk-launch-flags`,
  `guard-android-status-address`, `guard-android-i18n`, `guard-emoji-font`,
  `guard-htmx-loaded`, `guard-autofill-suppression`,
  `guard-e2e-fixtures-import`, `check-brand-assets`,
  `guard-makefile-version`, `guard-osk-loaded`.
- TDD claim re-verified personally: reverted the implementation fix (kept
  the test), confirmed `TestCreateReturn_UsesOriginalSaleCurrencyAndTaxMode`
  fails with the exact predicted symptom (`currency "GBP", want EUR`), then
  restored the fix and confirmed it passes again — the test is not a
  false-pass.
- Existing `TestCreateReturn_ByOriginalSaleID`/`ByReceiptNo` fixtures are
  GBP/exclusive-priced, so they read back as `inclusive=false` — confirmed
  they still pass unchanged, i.e. no regression for the existing (English-
  market) behaviour this fix doesn't otherwise touch.

Refs: ut-docs#1494, ut-docs#1405, ut-docs#1203 (contract 1.6.0), ADR-0044.

# Code review — tax summary report (2026-07-17)

Branch `feat/tax-summary`. Final core owner-report: VAT/tax per rate for the
selected period — what the owner (or their accountant) needs per return.

- `POSRepo.TaxSummary`: sale_lines grouped by tax_rate_bp; net
  (total_before_tax) and tax (tax_amount) with RETURNS SUBTRACTED
  (sale_type='return' lines negate) — the figures match what's owed.
- Reports page "Tax summary" card: rate / net sales / tax collected,
  rates formatted from basis points. i18n ×4.
- Test extends the product-reports test (0% band: net 700, tax 0 across a
  sale + a variant sale).

Suites + guards green.

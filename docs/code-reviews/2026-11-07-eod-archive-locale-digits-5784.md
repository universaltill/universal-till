# Review — EOD archive counts follow the locale's digits (ut-docs#5784)

Date: 2026-11-07 · Branch: `fix/5784-eod-archive-locale-digits` · Lane: `lane:cloud-24`

## What shipped
- `web/ui/partials/reports_tab_eod.html`: the archived-EOD list's counts now
  render through `{{ qty }}`, so ar/fa show their own digits like the money
  beside them. This covers the sales count, the per-operator sales count,
  the per-operator no-sale opens, the no-sale total and the "by article (N)"
  count. Machine values are left raw: the `article_print_cap` input `value`
  and the `.Period` in the print `hx-post` URL.
- `internal/pages/reports_eod_archive_locale_digits_5784_test.go`: renders the
  EOD tab with a stored report in ar and fa and asserts each of the five
  cells carries locale digits and no Latin ones. It also pins en to Latin
  digits.
- `internal/httpx/currency.go`: the `FormatQty` doc comment lists the new
  call sites.

## Review (independent, different model from the author)
TDD re-verified by the reviewer: with the template hunk reverted, the test
fails in ar and fa on all five cells (e.g. want `<td>٢٣</td>`, found
`<td>23</td>`). Restored, it passes. Every wrapped field is a plain `int`,
which `qtyFloat` handles. `FormatFloat(…,'f',-1,…)` never prints "3.0".

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nice, out of scope | Fiscal-device (ÖKC, TR only) counts and the `%d` mismatch line on the same row are still Latin. A fix needs a new key (ADR-0160). | Deferred → ut-docs#5785 |
| 2 | nice, out of scope | Period labels (`.Period`/`.From`/`.To`) render as raw ISO dates | Deferred → ut-docs#5785 |
| 3 | nice | The test did not pin en to Latin digits | Fixed |
| 4 | nit | The `FormatQty` doc's call-site list is stale | Fixed |

## Tester
- Mutation check: reverting `qty (len .Articles)` or `qty .NoSaleCount` makes
  the test fail. Gates are green: build, vet, `go test ./internal/pages/
  ./internal/httpx/`, guard-i18n, guard-data-access.
- Driven run: the real till binary with a fresh data dir and one seeded
  `report_archive` row, Reports → End of day in Chromium at 1024×600. The fa
  screenshot was read and shows ۲۳ / ۴۷ / ۱۲ / ۱۵ / (۳), with no clipping in
  RTL. The ar and en page text was checked (ar Arabic-Indic, en Latin).
- Not looked at: dark theme, other viewports, de, an archive from a real
  end-of-day run, the e2e suite (no e2e covers this partial's digit shape).
- Seen, pre-existing: the Reports summary count and the "14 days" option are
  Latin in fa/ar → added to ut-docs#5785.

Pre-flight: docs-shots surface hash refreshed with the escape hatch (no screenshot renders the EOD archive tab; reports.png shows the summary cards only); help topic n/a (digit shape only, no flow or wording change); en.json/meta n/a (no keys); reference n/a; README n/a; review record updated (this file); follow-ups → ut-docs#5785.

Verdict: approve.

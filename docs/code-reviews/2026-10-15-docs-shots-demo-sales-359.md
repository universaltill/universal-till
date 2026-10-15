# Review: demo sales in the manual's reports/invoices screenshots (ut-docs#359)

**Date:** 2026-10-15 · **Lane:** lane:local-mahshid76 · **Author model:** Sonnet 5.5 · **Reviewer:** Opus 5.5 (independent subagent)

## What shipped

The `reports` and `invoices` manual screenshots were captured on an empty till ("No invoices in this range yet", zero totals). Harness-only change, no `internal/` code touched:

- `e2e/seed_docs_sales` (new): three completed sales through the real `pos.CompleteSale` and two invoices through `InvoiceRepo`, re-stamped to fixed December-2025 dates (VAT banding as `invoice_page.go`; seller equals `ensureInvoiceSeller`; sales marked collected so `/orders` stays unchanged). Three tests.
- Gated by `UT_DOCS_SHOTS_SEED_SALES=1`, set only in `DOCS_SHOTS_ENV` (`playwright.docs.config.ts`) and read by `e2e/run-till.sh`; the auth till and the other configs never seed.
- `pinnedQuery`: `reports` -> `period=month&anchor=2025-12-15`, `invoices` -> `from=2025-12-01&to=2025-12-31` (both pages default to wall-clock windows that the pinned docs clock does not reach).
- `TZ=UTC` added to `DOCS_SHOTS_ENV` (review finding 1); the determinism workflow now also triggers on `e2e/seed_docs_sales/**` (finding 3).
- 8 PNGs regenerated: `{ar,en,fa,tr}/{reports,invoices}.png`. `manifest.json` unchanged (`e2e/` is not in the surface hash).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | low-medium | "Any runner timezone" was false for UTC+12..+14: midday-UTC invoice dates print as the next day (measured under Pacific/Auckland and Kiritimati: 13/12 and 16/12), which would churn all four `invoices.png` | Fixed: `TZ: 'UTC'` in `DOCS_SHOTS_ENV`; comment states the real range |
| 2 | low | The re-stamp is partial: `payments.local_date`, 6 stock-movement rows, 9 audit rows and `inventory.updated_at` keep the seed run's clock | Accepted and documented in the seed's header comment: no screenshotted route renders them (reviewer diffed the HTML of every topic's first route seeded vs unseeded; only the pinned `/reports` KPIs differ, plus two below-the-fold `/inventory` rows) |
| 3 | low | `docs-shots-determinism.yml` did not trigger on seed edits | Fixed: path added |
| 4 | low | Comments said the real e2e suite boots `run-till.sh`; its default project uses `worker-till.ts`, only the locale-audit config also boots it | Fixed (wording). Prices/VAT banding are copied from the catalogue and could drift if the demo catalogue changes (no test catches it): accepted, noted |
| 5 | observation | ar `reports.png`: the KPI sales count renders Western "3" while money uses Arabic-Indic digits (plain int in the template). Pre-existing, not part of this change | Not fixed here; offered as a follow-up card |

Verified by the reviewer: seeded data is internally consistent (lines = totals, tax, receipt/display numbers, invoices match sales, sync_status synced); flag-unset path seeds nothing; seven mutations each make a test fail (no false passes); `guard-docs-shots.sh` passes on the patched tree; screenshots (en/ar/fa) show representative non-empty data with generic names only.

## Not verified

- The Playwright determinism guard was not re-run after the `TZ=UTC` addition. The reviewer measured identical output across UTC, Tehran, Auckland and Kiritimati for `/reports`, and the shipped PNGs show dates that are the same in UTC; CI already runs in UTC. A pre-existing `tr/designer.png` flake (status-bar chip, ~9px) was seen on unchanged `main`; not caused by this change.

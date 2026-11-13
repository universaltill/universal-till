# Review: Reports → Online channels tab (ut-docs#5427)

**Date:** 2026-11-04 · **Lane:** lane:cloud-54 · **Branch:** `feat/5427-online-channels-report`
**Built by:** Sonnet (Dev subagent) · **Reviewed by:** Opus 5.5 (independent subagent, separate checkout)
**Verdict:** approve. No high or medium findings. All four low findings are fixed in this branch.

## What shipped

- `internal/data/channel_orders_report_repo.go`: `ChannelOrdersRepo.SummaryByWindow(ctx, from, to)`
  returns one row per `plugin_key` + currency with orders, order value (`total_minor`), refunds
  (`refund_total_minor`) and after-refunds, plus the count of orders cancelled before payment.
  It also adds `HasAny`. The window is half-open on `placed_at`, compared with `datetime()` the same
  way as `RefundsByWindow`.
- A new on-demand tab `/ui/reports/tab/online-channels`
  (`internal/pages/reports_tab_online_channels.go`, `web/ui/partials/reports_tab_online_channels.html`).
  It is the last tab in the strip. It is gated on `reports` through the existing tab switch, so a cashier
  gets 403. It is hidden until this till has recorded a channel order. It uses the same period picker
  as the other tabs.
- The tab shows one line saying these orders are not till sales (not in the figures at the top, the
  drawer, the day-end (Z) report or the fiscal journal) and links to Online orders.
- Totals are shown per currency only when two or more channels share that currency. Amounts in
  different currencies are never added together.
- CSS (logical properties only):
  - The shared `.channel-orders-num` end-alignment is fixed inside `.card` tables. This also fixes the
    Online orders page and regenerates its `channel-orders.png` docs-shots.
  - The Total row is styled so it does not look like a column header.
  - The phone card table uses `data-cards-collapse="off"`, so "After refunds" is never hidden behind "More".
- 13 new keys in en/ar/fa/tr, with `en.meta.json` metadata, including a plural key.
- Help: `reports.md` in en/de/ar/fa/tr gets a new section plus edits to the tab list and the period
  bullet. `channel-orders.md` gets a cross-link. `help-drift-baseline.json` is re-recorded.

## Decisions accepted (from BA / Architect / UX)

- ADR-0169 §9 already covers this ("show in reports under 'Online channels'"), so there is no new ADR.
- **Refunds are dated by `placed_at`.** The table has no refund timestamp, so a refund shows in the
  period the order was placed. The UI and the help both say so.
- **Cancelled before payment** means `status=cancelled` with `payment_state` empty, pending, authorized
  or voided. These orders are excluded and shown as a count. An order that was paid and then
  cancelled is counted, and its refund nets it out.
- **Column labels** are "Order value" and "After refunds", not Gross/Net, because Gross/Net read as
  with/without tax in de, fa and tr (UX A5).

## Findings

| # | Sev | Finding | Status |
|---|---|---|---|
| T-D1 | low | Total label `th` kept the header's 2px bottom border (app.css) | fixed |
| T-D2 | low | 360px Total card drew a rule above every line | fixed: rule only at ≥481px |
| T-H1 | low | Help listed Online channels among tabs that "agree with the numbers at the top" | fixed |
| T-H2 | low | Help said "Z report, end of day" while the UI notice says "day-end (Z) report" | fixed |
| R-1 | low | Help claimed "no other tab changes", but channel orders lower stock (stock warnings, Forecast, dead stock) | fixed, 5 languages |
| R-2 | low | A lost "and" in the en/tr help tab list | fixed |
| R-3 | low | `HasAny` error was dropped silently in `reports_page.go` | fixed: logged with `Warnf`, tab still hidden |
| R-4 | low | fa/tr used a different word for "marketplaces" than the existing Online orders strings | fixed: now "بازارچه‌ها" / "pazaryerlerinden" |

T- = Tester (driven run), R- = Reviewer.

## Verification beyond unit tests

- **TDD re-verified by the reviewer.** Each of these three mutations made the named tests fail, and
  they passed again after restoring the code:
  - dropping `voided` from the filter
  - changing the window's `<` to `<=`
  - making `HasAny` always return true

  The Tester also ran 20 mutations, and each one was caught.
- **Tester's driven run.** The real binary on a seeded DB with these orders: two GBP channels, one EUR,
  one refunded, one cancelled+voided, and a month with only cancelled orders.
  - Screens checked: 1024×600 windowed and kiosk, 360px with touch emulated, desktop; en, de (test
    overlay), ar, fa.
  - Results:
    - Figures checked by hand.
    - No total across currencies.
    - Numbers end-aligned, in RTL too.
    - No horizontal scroll.
    - The cashier PIN session gets 403.
    - The tab stays hidden until there is an order.
  - Not checked: real touch hardware, the real German pack (it doesn't exist yet), dark theme, and
    Turkish visuals.
  - `reports.png` doesn't show the new tab button, because the shot's crop ends above the tab strip.
    This is accepted: the new section is described in the help prose.
- **"Kept out of sales" test.** The KPI header and the sales-trend, items, tax, payments, EOD,
  shrinkage and tips tabs are byte-identical before and after seeding channel orders.

## Pre-flight

Pre-flight:
- **Till user-visible behaviour:** `web/help/` updated, plus `make docs-shots` (channel-orders shots
  changed).
- `reference/`: n/a (no reference page covers till report tabs).
- `README.md`: n/a (not stale).
- **New `en.json` keys:** `en.meta.json` updated, every `web/locales/*.json` updated. Pack follow-ups for
  ut-plugin-language-de/es/pt are opened in the same cycle after the core merge (brand-new keys, so
  core merges first per reviewer step 4).
- Review record: this file.
- Follow-up: rename "Payments & channels" (UX A4), filed as a Backlog card.

## Deferred

- Friendly channel names instead of `plugin_key`, per-connection split, tax/shipping breakdown and export
  are non-goals (BA).

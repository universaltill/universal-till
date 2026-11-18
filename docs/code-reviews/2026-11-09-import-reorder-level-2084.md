# Review — catalogue import maps a reorder-level column (ut-docs#2084)

Date: 2026-11-09 · Lane: lane:cloud-41 · Author: Sonnet (Dev) · Reviewer: Opus 5.5 (independent) · Tester: separate agent

## What shipped

- **catimport 0.6.0** (ADR-0164): `ImportItem` gains `ReorderLevel`/`HasReorderLevel`
  plus `ReorderLevelIssue`/`ReorderLevelIssueRaw`; header synonyms `reorder_level`
  (Reorder level, Reorder point, Reorder at, Low stock, Low stock threshold/level,
  Stock alert count, Min/Minimum stock) and `reorder_alert` (Stock alert enabled, …).
  Loyverse `Low stock [Store]` and Square `Stock Alert Count [Loc]` match through the
  existing `"<syn> ["` rule — no vendor code. One `applyReorderLevel` helper serves
  the CSV and XLSX paths (XLSX reads the raw numeric cell). An alert column reading
  No drops the count silently; a value that is not a whole number 0..1,000,000 gives
  a non-blocking warning. `catalogrules.MaxReorderLevel` is now the one ceiling
  (the Catalog handler uses it too).
- **Till apply:** new repo method `SetItemReorderLevelTx`, called in the same
  transaction right after `CreateItemTx`; a failure rolls the row back and marks it
  failed. Existing-SKU rows stay skipped (level untouched, like stock); gift-voucher
  rows ignore the field.
- **Export:** `ExportRow.ReorderLevel` (`reorder_level`, also in the plugin
  `export.requested` items payload) and a trailing `Reorder level` CSV column, blank
  at 0, so export → wipe → re-import round-trips.
- No new `en.json` keys (the warning reuses `import.status.unknown_issue` +
  `catalog.reorder_level`, like the net-quantity warning).
- Help: `web/help/en/catalog.md`, `web/help/en/inventory.md`.

## Findings

All low; none blocking.

| # | Finding | Disposition |
|---|---|---|
| L1 | New fields are not in `formatOnlyFields`, so "Low stock"/"Min stock" now count as neutral headers; an import plugin whose only `file_headers` claim is one of them would be refused at install (`manifest.go:940`). Org-wide code search: no plugin claims any of them. | Accepted, noted here and in the PR. |
| L2 | Both "Reorder Point" and "Reorder Level" present → first column wins; some exports use "Reorder Level" as order-up-to. | Deferred → ut-docs#5959 |
| L3 | Reorder level is deliberately not mappable in my.'s mapping screen. | Accepted (BA Q1; threshold is editable after import). |
| L4 | No pages-level test for a failing `SetItemReorderLevelTx`. | Deferred → ut-docs#5959 (repo-level rollback test exists). |
| L5 | Gift-voucher row with an invalid reorder cell still shows ⚠. | Deferred → ut-docs#5959 |
| T1 | Warning text "issue — Reorder level: abc" does not state the allowed range. | Deferred → ut-docs#5959 |

## Verification

- Reviewer: catimport `go test ./...`, vet, golangci-lint clean; root `go build ./...`;
  `go test ./internal/data/...`; pages `-run Import` and `-run 'Reorder|Export|NetQuantity'`;
  guards core-neutral, i18n, help-topics, data-access, catimport-version (0.6.0 vs
  v0.5.0), fixed-dates, compliance-claims, competitor-naming. Mutations (XLSX call
  removed, alert gate disabled) make the tests fail. **ut-cloud built and tested
  against this catimport** via a throwaway `replace` copy: `go build ./...` and every
  catimport-importing package's tests pass — the pin bump needs no code change.
- Tester: seven mutations (save step off, both/one `applyReorderLevel` call removed,
  voucher guard dropped, preview warning dropped, alert gate off, tx write bypassed)
  each caught by a test. Playwright import/inventory/items-shell specs 22/22.
  Full `-race`: no races; one unrelated wall-clock test
  (`TestStockUploadPendingAndHasSendable_…`) slow under contention, pages hit the
  25-min timeout (the Makefile's `test-race-pages` uses 60 min); targeted `-race`
  on reorder/import/export tests pass.
- **Driven run** (built binary, temp data dir, headless Chromium): Preview shows
  "⚠ issue — Reorder level: abc" on the bad row; import "Imported 3 — Warnings: 1";
  item editor Variants tab shows Reorder level 7; export CSV ends in `Reorder level`
  with `,7` on that row. Loyverse two-location file, Square alert N/Y/negative all
  behave as designed. fa 360px phone and 1024×600 kiosk previews: no overflow.

Pre-flight: help topics `catalog.md`/`inventory.md` updated (docs-shots n/a — no
screen layout change); `reference/plugin-manifest.md` updated (ut-docs PR);
README n/a (catimport/README updated); en.json / en.meta / lang packs n/a (no new
keys); ADR n/a (established optional-column pattern, ADR-0164/0036); catimport
VERSION 0.6.0 updated; ut-cloud catimport pin deferred → same-day ut-cloud PR
after the tag; ut-website n/a; follow-ups ut-docs#5956, #5957, #5958, #5959.

## Verdict

Approve.

# Code review — mixed dine-in/takeaway lines in one sale (ADR-0073)

- **Date:** 2026-09-02
- **Card:** ut-docs#1181 (`complexity:hard`, `lane:local`)
- **Branch:** `feat/1181-mixed-order-types` (core) + `feat/1181-mixed-order-types-adr` (ut-docs) + language-pack follow-ups (de, es)
- **Reviewer:** independent Opus subagent, two rounds (round 1 full diff, round 2 scoped to the blocker fixes per the hard-complexity rule); TDD red runs re-verified personally
- **Verdict:** Safe to merge after the fixes below; all blockers closed with regression tests.

## What changed

Order type becomes authoritative on each basket/sale **line**; the sale header
is a derived summary (`""` dine-in, `takeaway`, or the new `mixed`). Design:
`ut-docs/adr/0073-per-line-order-type-and-derived-sale-summary.md`.

- `internal/pos`: `BasketLine.OrderType`, `OrderTypeMixed`, `NormalizeLineOrderType`,
  `SummarizeOrderType`, per-line tax rating in `computeTotals`, order type in
  merge identity, `SetLineOrderType(key, mode)`, `SetOrderType` kept as the
  one-tap bulk action + default, table policy over the lines
  (`applyTablePolicyLocked`, also on every void path), hold/resume with the
  legacy header fallback, `CompleteSale` normalization + header derivation
  (incl. legacy untyped returns inheriting the original's line modes).
- `internal/db`: migration 077 (`sale_lines.order_type` + archive twin,
  backfilled from the header, and historic returns via `sale_links`,
  batch-scoped for the archive).
- `internal/data`: `SaleLineRow`/`SaleDetailLine`/`SaleLineSnapshot` carry the
  mode; `RefundLineKey` + `ReturnedQuantities` keyed per mode; reset archive
  column list.
- `internal/pages`: `POST /api/pos/line-order-type`; tender persists the
  summary derived from the same line snapshot; refund/inventory-return/LAN
  replay/kiosk propagate the mode; held-sale table gate over lines; table
  picker over lines; kitchen ticket `Mixed` header + per-line marker (mixed
  sales only); receipt/reprint/journal/order-board markers (mixed only);
  `sale.completed` gains `order_type` on the sale and on `line_items[]`.
- UI: compact icon-only per-line pill (product owner's live call: the first
  labelled version "is too big for each item and has a bad design"), `Mixed`
  chip, bulk segments relabelled "All dine in / All takeaway" when mixed,
  phone tier stacks the pill in the qty column. Locales en/tr/fa/ar + de/es
  packs (translated on the homelab Ollama, hand-aligned to each locale's
  shipped dine-in/takeaway vocabulary). Manual + 92 regenerated screenshots.

## Independent review — round 1 (Opus)

Blockers, both proven with throwaway tests by the reviewer, both fixed:

- **B1** tender persisted `Engine.OrderType()` (the *default*) instead of the
  derived summary → "bulk Takeaway, scan, flip the line to dine in" recorded
  a dine-in-taxed line as takeaway. Fix: derive from the line snapshot
  (`pos.SummarizeOrderType(lines, …)`). Test: `TestTender_DefaultTakeawayButLineDineIn_PersistsDineIn`.
- **B2** migration 077 backfilled historic *returns'* lines to `''` while the
  original takeaway sale's became `takeaway`, so the per-mode refund pool
  never saw the returned unit → double refund. Fix: backfill return lines and
  headers through `sale_links` (live + archive). Tests:
  `TestMigration077_BackfillsLineOrderTypeFromSaleHeader` (extended),
  `TestRefund_HistoricTakeawaySaleAlreadyRefunded_StillRejected`.

High/medium, all fixed: H3 void-last-dine-in-line kept the table
(`TestLineOrderType_VoidingLastDineInLineClearsTable`); H4/H5 phone overflow +
touch-target — resolved by the compact 2rem pill, measured 34px, no basket
overflow at 360px, ADR-0073 amended to state the 2rem secondary-control
floor; H6 stale screenshots — regenerated after the final UI; M7 Spanish pack
missing the three base keys — added, baseline pruned; M8 refund marker on
every sale — gated to mixed (`TestRefund_UniformSaleHasNoLineModeMarker`).
Low: dead `heldSaleOrderType` removed, kitchen text renderer clips the Mode
line, manual wording corrected (empty basket, tax-exclusive tills),
`.gitignore` covers SQLite sidecars.

## Independent review — round 2 (Opus, scoped)

B1/B2/H3/M7/M8 confirmed fixed. One new blocker found and fixed:

- **BLOCKER-1** LAN replay of a *pre-ADR-0073 peer's return* (header `""`,
  untyped lines) re-created B2 at runtime for mixed-version fleets. Fix: at
  the `CompleteSale` choke point an untyped return inherits the original
  sale's persisted line modes (the runtime twin of 077's `sale_links`
  backfill). Test: `TestApplyJournal_LegacyReturnInheritsOriginalLineModes`.
- MEDIUM-2 archive return backfill untested → test extended with
  `sale_links_archive` rows across two batches.
- LOW-3 tender read lines and summary at two moments → same snapshot now.
- LOW-4 no negative marker assertion → added.
- LOW-5 ES pack drift gate must run after core merges; it is already red at
  ES HEAD for 16 unrelated keys — filed as its own card.

Not changed on purpose (LOW): the thermal reprint's English literals match
that renderer's existing convention; `strings.ToUpper` on localized kitchen
markers is a pre-existing quirk of that renderer.

## Verified

- TDD: every new test was run red against the pre-change code first
  (compile errors on the missing fields/constants, then real assertion
  failures after stubbing), then green.
- `gofmt -l .` clean, `go build ./...`, `go vet ./...`, every `scripts/ci`
  guard in `ci.yml`'s build job (incl. `guard-i18n`, `guard-docs-shots`,
  `guard-help-topics`, `guard-kiosk-engine`, `guard-data-access`).
- `go test ./...`: all packages green except two **pre-existing, unrelated**
  local-only failures that reproduce on clean `main` on this macOS box and
  are green in CI on `main` (`internal/alerts TestStart_RunsDigestLoopBody`
  timer race; `internal/server TestListenWithFallback_WildcardHostFallsBackToLoopback`
  binds `[::]` here).
- e2e (Playwright, real Chromium): basket/hold/sale-screen/hx-sync specs
  green after the compact control; full default project run recorded below.
- Driven UX check on a throwaway till (1024×600 en/de/fa, 360×740 en/de):
  per-line targets 34×34px, RTL correct with logical properties, no page or
  basket horizontal overflow, keyboard path (Tab + Enter) flips a line,
  `aria-checked` tracks state, Mixed chip + "All dine in/All takeaway"
  labels render in every locale.
- **Real hardware**: signed release APK (v0.8.6-1181, versionCode 8007, CI's
  keystore from Key Vault) installed on the 10-inch Android tablet over
  ADB; real touch taps on the per-line icon flipped the line and re-rated
  tax (server state verified by HTTP after each tap); also verified in the
  tablet's Chrome against a LAN-bound throwaway till. Screenshots inspected.

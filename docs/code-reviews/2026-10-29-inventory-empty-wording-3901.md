# Review — Inventory empty-state wording (ut-docs#3901)

Date: 2026-10-29 · Lane: `lane:cloud-41` · Complexity: easy

## What shipped

- `inventory.empty` reworded in en/tr/fa/ar. Since #3631 the stock table
  lists every active stock-tracked item (at 0 if never received), so the
  empty row shows only when the shop has **no** stock-tracked items. "No
  stock recorded yet" no longer matched that case. New en text: "No
  stock-tracked items yet. Add items in the Catalog, or untick an item's
  “Not stock-tracked” box there." Each locale quotes its own `nav.catalog`
  and `catalog.stock_untracked` labels.
- The key was kept: it has no placeholders and the meaning is the same
  empty row. `web/i18n-meta/en.meta.json` now describes it, and its line
  left the shrink-only `key-meta-baseline.txt`.
- `web/ui/pages/inventory.html`: the comment that quoted the old text was
  updated. This is comment-only, so the docs-shots surface hash was
  refreshed instead of the screenshots being regenerated. No screenshot or
  e2e spec shows the empty row: every docs-shots seed has tracked items.
- New test `internal/pages/inventory_empty_state_3901_test.go`, run against
  `/inventory` and `/ui/inventory/stock-table`:
  - A shop with only retired and untracked items shows the new text and
    never the old one.
  - A tracked item at 0 hides the empty row.
- Language packs: each `inventory.empty` translation changed, with a
  manifest bump, in ut-plugin-language-de (1.1.281), es (1.1.270) and pt
  (1.0.125).

## Review

Independent review by a Sonnet subagent (Opus authored). It found no
blockers and listed these:

- **nit:** "untick" is British English. Accepted: en.json already uses
  untick/tick throughout and never "uncheck".
- **nit:** the sentence is long for a `colspan=6` cell. Accepted: it wraps,
  and it is shown only in an empty table.
- **low:** if every item is flagged untracked, "Add items" slightly misses.
  Accepted: the second clause covers that case.
- **low:** the test asserts the English string, not the key. Accepted: that
  ties the test to the reworded meaning on purpose.
- The reviewer confirmed the claim against
  `ListStockLevelsIncludingUnstocked`:
  - The shop-wide "sell without tracking" setting doesn't empty the table.
  - The search filter uses the separate `stock-no-matches` row.
- Translations were checked for register (de Sie, es usted, pt-PT formal)
  and exact label quotes.

## Verified

- TDD: with the locale changes stashed, `TestInventoryEmptyState_NoTrackedItems`
  fails. With them restored, both tests pass.
- These guards pass: `gofmt`, `go build ./...`, `guard-i18n`,
  `guard-i18n-meta`, RTL CSS, compliance, competitor naming and docs-shots.
- Each pack's `validate.sh` passes.
- The packs' `check-key-drift.sh` fails only on 3 unrelated keys
  (`sync.link.rejoin*`, `tills.rejoin_needed_help`). Another lane's open
  #5162 pack PRs cover those.
- `go test ./...` passes. `internal/plugins` needed `-timeout 40m` in this
  sandbox (618s), because it is slow here, not failing.
- Not verified locally:
  - `guard-deadcode-baseline` needs go1.27 and `guard-shellcheck-version`
    needs shellcheck, and neither is in this container. CI covers both.

Verdict: safe to merge. Packs land first, because this is an existing key
(reviewer rule 4).

# Code review — a weighed item's shelf label prints its unit price (ut-docs#3343)

- **Date:** 2026-10-01
- **Ticket:** ut-docs#3343 (`complexity:easy`), narrowed by BA to the
  weighed-item half only — the pre-packed-goods half (net quantity, opt-in
  setting, unit-price division) is split to ut-docs#3391 and out of scope.
- **Branch:** `uk-label-per-kg-3343`
- **Author:** Sonnet (this cycle's build model, `complexity:easy`).
- **Reviewer:** independent pass, fresh-context Opus 5.5 subagent in an
  isolated worktree (no visibility into the implementation reasoning), per
  `MODEL-ROUTING.md`'s easy tier.
- **Verdict: SAFE TO MERGE**, after fixing three real findings the review
  turned up.

## The gap

Price Marking Order 2004 (as amended, commencing 6 April 2026;
`reference/uk-compliance.md` §7): a weighed item sold loose must show a unit
price, standardised to **per kg**. `POST /api/print/labels`
(`internal/pages/print_api.go`, `print.RenderLabel`) printed name, price and
barcode only — never a unit price — for every item, weighed or not.

## What shipped

- `internal/data/catalog_repo.go`: `ItemLabel` gained `IsWeighed bool`;
  `GetItemLabel`'s query now also selects `i.is_weighed`. `VariantLabel`
  gained the same `IsWeighed bool`, populated from the **parent item's**
  `is_weighed` (a variant has no column of its own, but
  `internal/data/pos_repo.go`'s variant-sale queries price it by weight
  whenever its parent is weighed — see finding 2 below).
- `internal/pages/print_api.go`: when `label.IsWeighed`, the price text
  passed to `print.RenderLabel` becomes `"<price> per kg"` (via a new i18n
  key, store locale) instead of the bare price. The variant branch now
  carries `IsWeighed` through from `VariantLabel`.
- `web/locales/{en,ar,fa,tr}.json`: new key `catalog.labels.price_per_unit`
  (`"%s per %s"` in English, 2 placeholders preserved in ar/fa/tr).
- `web/help/en/printing.md`: documents the new label line for shop owners,
  including the variant case.
- `scripts/ci/i18n-baseline/help-drift-baseline.json`: the "printing" topic's
  already-tracked, already-deferred ar/de/fa/tr translation drift
  (ut-docs#332/#341) re-recorded at its new absolute English
  `numbered_steps` count (21→22) — this topic's translation stays
  deliberately deferred; nothing here changes that policy.
- Tests: `internal/data/catalog_repo_crud_test.go`
  (`TestGetItemLabel_IncludesIsWeighed`,
  `TestGetVariantLabel_InheritsParentIsWeighed`) and
  `internal/pages/print_labels_notice_test.go`
  (`TestPostPrintLabels_WeighedItemPrintsPricePerUnit`, covering a plain
  weighed item, a variant of a weighed item, and a non-weighed item as a
  regression guard) — all asserting against the literal rendered ESC/POS
  device bytes, not just the HTTP response.

## Review findings (all fixed before merge)

1. **HIGH — the suffix would often read "per each", not "per kg".** The
   first draft printed the item's own free-text `items.unit` verbatim.
   Review found that field is `"each"` on every catalog-imported weighed
   item regardless of `IsWeighed` (`internal/pages/import_page.go`), and the
   hand-entry catalog form's unit field has no link to the Sold-by-weight
   checkbox either (`web/ui/pages/catalog.html`) — so "kg is the only value
   this codebase's conventions produce for a weighed item" (the original BA
   note) was false in the general case, only true for the demo seed data.
   **Fix:** the suffix is now the fixed word `"kg"`, never `items.unit` —
   correct unconditionally, because a weighed item's quantity is always
   kilograms regardless of what its `unit` field says (the same convention
   `internal/ui/buttons.go`'s embedded-weight barcode decode already relies
   on). `ItemLabel.Unit` was removed as dead weight (it existed only to
   carry a value nothing can safely trust). Test
   `TestGetItemLabel_IncludesIsWeighed` and the handler test now
   deliberately seed `unit = "each"` on a weighed item to pin this.
2. **MEDIUM-HIGH — a variant of a weighed item printed no unit price at
   all, and the help text's claim that "variants aren't weighed items" was
   wrong.** `item_variants` has no `is_weighed` column, but
   `internal/data/pos_repo.go`'s variant-sale queries (lines ~8078, 8219,
   8259) select the **parent item's** `i.is_weighed`, so a variant like
   "Apples Large" under a weighed "Apples" genuinely sells by weight.
   `GetVariantLabel` didn't select it, so the Architect's "no variant is
   ever is_weighed" was true of the column but false of the real behaviour.
   **Fix:** `VariantLabel.IsWeighed` is now selected from the joined parent
   item and threaded through the handler's variant branch; help text
   corrected to describe the actual behaviour; new regression test
   `TestGetVariantLabel_InheritsParentIsWeighed` plus a handler-level
   assertion.
3. **MEDIUM — the format string was resolved from the operator's own UI
   locale (`ut_lang` cookie) instead of the shop's configured locale.** The
   price on the same line already deliberately uses `httpx.DefaultLocale()`
   (store convention, ut-docs#1130), not the per-request `locale`, because a
   printed shelf label is a shop artifact a customer reads, not operator
   chrome. The "per kg" wording had been wired to the wrong one, which could
   print mixed-script label text (reviewer reproduced `"£1.50 در هر kg"`/
   `"£1.50 لكل kg"` with the operator UI set to fa/ar) — RTL text embedded in
   a Latin shelf label, and liable to fold to `?` on a single-byte ESC/POS
   charset. **Fix:** use `httpx.DefaultLocale()` for the format string too,
   matching the price's own locale choice.
4. **LOW (fixed):** help text's "this isn't optional for a weighed item, UK
   and elsewhere alike" was an unsupported legal generalisation (it passed
   `guard-compliance-claims.sh`, which doesn't police this kind of claim) —
   reworded to a plain description of behaviour.

## Not a finding, logged for follow-through

- **Language-pack PRs owed.** `catalog.labels.price_per_unit` is a brand-new
  core key. Per `CLAUDE.md`/`reviewer`'s rule 4, core merges first (`main`
  goes red on `lang-pack-drift`, expected), and the `ut-plugin-language-de`
  / `ut-plugin-language-es` catch-up PRs land in this same cycle, before
  anything else — tracked as this cycle's own obligation, not a new card.

## Verified beyond the automated tests

- TDD claim re-verified independently in a separate worktree: reverting just
  the `IsWeighed` branch in `print_api.go` made
  `TestPostPrintLabels_WeighedItemPrintsPricePerUnit` fail with the real
  "£1.50 per kg not found" error; restoring it passed again.
- Checked every other caller of `ItemLabel`/`GetItemLabel`
  (`internal/pages/catalog/handlers.go`,
  `internal/pages/cloudsync_catalog_wire.go`,
  `internal/pages/common/barcode_conflict.go`) — none reads the removed
  `Unit` field or is affected by the new `is_weighed` column (NOT NULL in
  the schema, so the extra `Scan` can't newly fail).
- `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run ./...`
  (0 issues), full `go test ./...` (all packages green), and
  `guard-i18n.sh`, `guard-data-access.sh`, `guard-help-drift.sh`,
  `guard-help-topics.sh`, `guard-compliance-claims.sh`,
  `guard-core-neutral.sh`, `guard-competitor-naming.sh` all run and green,
  independently, by both the author and the reviewer.
- No real shop/client name in any seed or test data (generic "Bananas",
  "Mug", "Apples"); no secrets introduced.
- No UI (HTML) surface changed — nothing to screenshot or drive through
  Playwright; this is a backend print-path change verified end to end via
  the literal device bytes a real printer would receive.

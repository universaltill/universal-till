# Code review — a pre-packed item's shelf label prints its unit price (ut-docs#3391)

- **Date:** 2026-10-01
- **Ticket:** ut-docs#3391 (`complexity:medium`), the pre-packed-goods half
  split off ut-docs#3343 during BA — net quantity on items, a shop-level
  opt-in setting, and real unit-price division arithmetic. ut-docs#3343
  itself (the weighed-item half, merged as universal-till#1584) is a
  separate, always-on mechanism this card must never affect.
- **Branch:** `feat/3391-prepack-unit-price`
- **Author:** Opus 5.5 subagent (this cycle's build model, `complexity:medium`).
- **Reviewer:** independent pass, fresh-context Fable subagent in an
  isolated worktree (no visibility into the implementation reasoning), per
  `MODEL-ROUTING.md`'s medium tier (reviewer must differ from the author's
  model).
- **Verdict: SAFE TO MERGE**, after rebasing onto `main` (which gained
  #1584 mid-cycle) and fixing one test assertion the rebase invalidated.

## The gap

Price Marking Order 2004 (as amended, commencing 6 April 2026;
`reference/uk-compliance.md` §7, §14): pre-packed goods sold in a constant
quantity need a unit price too (per kg / per litre / per item), except
small shops (≤280 m²), for whom it is optional. `POST /api/print/labels`
had no concept of an item's net quantity at all, so a pre-packed item's
label could never carry one.

## What shipped

- **Migration** `internal/db/migrations/060_items_net_quantity.sql` (renumbered
  twice on rebase — #3340's `055_items_age_restricted.sql` claimed 055 first,
  then #3310's `059_fiscal_order_starts.sql` claimed 059 first): adds
  nullable `items.net_quantity_value INTEGER` and `items.net_quantity_unit
  TEXT CHECK (... IN ('g','ml','ea'))`. Both NULL = no net quantity
  configured. Append-only, checksum-pinned, replay-safe.
- **`internal/catalogtypes`**: `ValidNetQuantity` enforces both-nil or
  (value > 0 ∧ unit ∈ {g, ml, ea}) — applied at the handler, the repo
  write path, and the DB `CHECK`, in depth.
- **`internal/data/catalog_repo.go`**: `ItemLabel` gained
  `NetQuantityValue sql.NullInt64` / `NetQuantityUnit sql.NullString`;
  `GetItemLabel`'s query selects them alongside #3343's `is_weighed`.
  Variants deliberately get **no** net quantity of their own (a variant
  inheriting the parent's would print a wrong unit price for a different
  pack size) — `prePackUnitPriceText` returns `""` for any `VariantLabel`
  path, by construction (no net-quantity fields exist on `VariantLabel`).
- **New setting** `data.CatalogPrePackUnitPriceEnabledKey` =
  `"catalog_pre_pack_unit_price_enabled"`, cloned field-for-field from the
  existing `CatalogImportBarcodeFromSKUDefaultKey` precedent: elevation
  gate, audit entry (`catalog_pre_pack_unit_price_changed`), `demo_mode.go`
  allow-list, `settings_shopwide_lock.go` region, `uislot.CoreSettings`
  entry (Advanced category), role-matrix test row. **Default off**
  (register §14's recommended default — the small-shop exemption is the
  merchant's own call).
- **`internal/pages/print_api.go`**: `prePackUnitPriceText` computes the
  unit price via `money.Money.MulDiv` (half-up rounding, already the
  codebase's division convention) — `×1000/qty` for g/ml (→ per kg / per
  litre), `×1/qty` for ea (→ per item) — and returns `""` when the item is
  weighed, the setting is off, or no net quantity is configured. The
  weighed-item suffix from #3343 (baked into the double-size price line
  itself) and this card's pre-pack line (a separate normal-size line via
  the new `print.RenderLabelWithUnitPrice`) are mutually exclusive by the
  `IsWeighed` guard, so the two mechanisms never both fire for one label —
  this is the exact seam the rebase (see below) had to reconcile.
- **UI**: a net-quantity value + unit `<select>` on the item form
  (`web/ui/pages/catalog.html`), disabled (not hidden) with an inline hint
  when "Sold by weight" is checked, both on toggle and on loading an
  existing item into the form; a new settings card
  (`web/ui/pages/settings.html`) cloned from the barcode-default precedent.
- **i18n**: 14 new keys (`catalog.net_quantity*`, `catalog.labels.unit.*`,
  `settings.catalog_pre_pack_unit_price.*`, `elevation.summary.*`) across
  all four built-in packs (en/ar/fa/tr) — real translations, not pasted
  English, except `kg`/`g`/`ml` which are legitimately identical.
- **Help**: `web/help/{ar,de,en,fa,tr}/catalog.md` gained an accurate bullet
  (verified against the shipped code, including the worked £2.00/300g
  example and the "variants never show one" fact).
- **Tests**: migration (columns, CHECK, replay), repo round-trip
  (including partial-update preserves net quantity), handler validation
  (400 on `kg`, blank unit, 0, negative, 1.5), settings (elevation, audit
  count, on/off), and `print_labels_prepack_test.go` asserting literal
  ESC/POS bytes for four non-evenly-divisible worked examples (proving
  half-up rounding, not truncation) plus the off/no-quantity/weighed
  isolation cases.

## Review findings

### Must-fix (both fixed before merge)

1. **Branch was based on `main` before #3343 merged**, so it
   re-implemented `ItemLabel.IsWeighed` and the `print_api.go` call site
   that #3343 (merged as universal-till#1584, 23:02 UTC) now also owns —
   a real textual conflict, not a false positive (reviewer dry-ran #1584's
   diff onto the branch and confirmed two failing hunks). **Fix:** rebased
   onto `origin/main`; resolved by keeping `ItemLabel.IsWeighed` once and
   combining the two label mechanisms at the `print_api.go` call site —
   #3343's weighed suffix stays baked into the price line, this card's
   `prePackUnitPriceText` becomes the separate unit-price line
   `RenderLabelWithUnitPrice` prints below it, and the two never both fire
   for one label (`prePackUnitPriceText`'s `IsWeighed` guard).
2. **`TestPostPrintLabels_PrePackUnitPrice`'s "off by default" loop would
   fail after the rebase**: it asserted no item in its seed set ever prints
   `" per "` with the setting off, but the seed's weighed fixture
   (`bananas`) now legitimately prints `"£1.50 per kg"` via #3343's
   always-on path regardless of this setting — reviewer confirmed by
   hand-applying #1584's three-line change to a scratch copy and watching
   the assertion trip for exactly that reason. **Fix:** split the loop —
   the true pre-pack items keep the strict `" per "`-free assertion;
   `bananas` gets its own assertion that it DOES show its own per-kg line,
   and exactly one `" per "` occurrence (never a second, spurious pre-pack
   line). Re-ran: green. Full `go test ./...` re-run after the rebase: all
   76 packages `ok`, 0 `FAIL`.

### Verified correct, no action

Arithmetic/rounding, weighed-path isolation, server-side validation in
depth, no data-loss across every `items` writer (including the cloud
`SaveItem` read-modify-write path and admin sync column discovery), the
settings precedent followed key-for-key, raw SQL confined to
`internal/data`/`internal/db`, i18n real-translation quality, help-topic
accuracy, no `showModal`/no manual left-right CSS, no file-I/O path
concern, no secrets or real client names. `guard-data-access.sh`,
`guard-i18n.sh`, `guard-help-topics.sh`, `guard-help-drift.sh` all green,
both before and after the rebase. `golangci-lint run ./...`: 0 issues.

**TDD re-verified independently, twice** (once by Tester, once more by
Reviewer, both before the rebase and the fix above didn't touch the pinned
arithmetic): reverting `MulDiv` to truncating division reproduces the
pinned £6.66/£3.78/£0.41 wrong answers; reverting the `IsWeighed` guard
reproduces a spurious pre-pack line on the weighed fixture. Both restored
and re-verified green.

### Nitpicks / follow-ups (not blocking, filed separately)

- Cloud-managed catalog `SaveItem` (`catalog_save_repo.go`) does not expose
  net quantity, so a cloud-managed catalog cannot set one yet (data already
  preserved via read-modify-write — a gap, not a bug).
- CSV catalog import/export does not carry net quantity.
- `ea` with quantity 1 prints a visually redundant second price line;
  cosmetic, left as-is.
- No upper bound on `net_quantity_value` (not exploitable, `MulDiv` cannot
  overflow for any realistic price); a sanity cap would catch fat-fingered
  entry.

Both real scope gaps (cloud `SaveItem`, CSV import/export) filed as
Backlog cards: ut-docs#3402, ut-docs#3403.

## Language-pack follow-ups (same cycle, per CLAUDE.md "lane that merges owns the follow-up")

This PR's 14 new keys (plus #3343's already-merged `catalog.labels.price_per_unit`,
which no lane had yet caught up in the German/Spanish packs) are translated
in `ut-plugin-language-de` and `ut-plugin-language-es` in the same cycle —
PRs opened immediately after this one merges, since `UT_CORE_EN_JSON`-pointed
`check-key-drift.sh` runs confirm 2988/2988 keys covered, 0 drift, in both
packs once those land.

## What was verified beyond automated tests

Real driven run (`/run`, Playwright against the actual server, not a
rendered-HTML-only assertion): the item form's net-quantity row
disables/re-enables live when "Sold by weight" is toggled, with the hint
shown/hidden correctly; the settings toggle round-trips through the real
`POST` endpoint and survives a reload; both screenshotted and looked at —
not just captured — at 1024×600 (kiosk floor) and 360px, no clipping or
overlap. No real touch hardware (emulated viewports only, explicitly
noted); RTL checked at the markup/string level only (no manual left/right
CSS in the diff), not via a live shop-language switch (judged not worth
the shared e2e till's shop-wide, elevation-gated, reloading side effect for
this pass).

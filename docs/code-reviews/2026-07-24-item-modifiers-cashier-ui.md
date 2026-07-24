# 2026-07-24 — Cashier item-customization UI + receipt/basket sub-lines (ADR-0020)

## Context
Item modifiers (schema/repo/basket-engine plumbing shipped earlier this
session) get a real UI here: tapping a product with active modifier
groups opens a customization step instead of adding straight to the
basket. Also fixes, for real, the `Service.Remove`/`UpdateLine` SKU-keying
gap the earlier commit deliberately documented and deferred (with a
regression test forcing this UI work to deal with it explicitly).

## Design
- Native `<dialog>` modal (`#modifier-modal` in `index.html`), populated
  by `GET /ui/pos/modifiers?item=&code=` and submitted to
  `POST /api/pos/scan-with-modifiers`.
- **Server-authoritative validation and pricing**: the handler reads only
  option *IDs* from the submitted form and re-derives every name and
  price delta from `ModifierRepo.ListGroupsForItem`'s server-loaded
  catalog data — never a client-submitted name or price. Rejects: options
  from the wrong group, selection counts outside a group's min/max, and
  (after review) a `code`/`itemId` pair that doesn't actually resolve to
  the same item.
- **LineKey**: a stable per-line id assigned when a line is first
  appended (`BasketLine.LineKey`), plus `RemoveLine`/`UpdateLineByKey`.
  `basket.html`'s qty/discount/remove controls and their API handlers
  (`/api/pos/line`, `/api/pos/remove`) now address by key, not SKU — the
  actual fix for the gap the earlier commit's `TestRemove_KnownGap_*`
  test (now renamed to `TestRemove_LegacySKUMethod_*`) existed to force.
  Held-sale snapshots carry `LineKey` through, self-healing an empty key
  from any sale held before this field existed.
- `HasModifiers` on button tiles (one batch query per grid render via new
  `ModifierRepo.ItemIDsWithModifiers`, not N+1) routes between the plain
  scan flow and the new customization step.

## Independent review
Opus-model review, adversarial brief, weighted toward two questions:
does the server-side validation genuinely resist a manipulated
submission, and does the LineKey fix actually close the previously-
documented gap everywhere, with zero remaining live path back to the
unsafe SKU-based methods.

**Confirmed correct (reviewer verified independently):**
- Every consumer of a customized line's price/tax (`recomputeTotals`,
  `computeSaleTotals`, `CompleteSale`'s insert loop, the receipt-fallback
  calc) reads the line's own stored price — traced end-to-end, matching
  the money-correctness bar the earlier headless-slice review already
  set.
- All three basket controls now post `key`, not `code`; the `code`
  fallback kept in the two API handlers "for callers that predate
  LineKey" is genuinely unreachable from the current UI (confirmed by
  reading `basket.html`'s rows, which no longer carry a `code` value at
  all for `hx-include="closest tr"` to pick up).
- Held-sale recall correctly gives two same-SKU modifier-distinct lines
  independent keys, both via normal carry-through and the empty-key
  self-heal path.
- `HasModifiers` is one batch query, confirmed in `ButtonStore.Load()`;
  deactivating a group correctly reverts a tile to the plain scan flow
  (traced the `is_active = 1` filter).
- No XSS: plain `html/template` auto-escaping throughout, no
  `template.HTML` casts on item/group/option names anywhere in the new
  template or the basket sub-line rendering.
- Tests are meaningful — `TestScanWithModifiers_IgnoresClientSuppliedNamesAndPrices`
  would fail if a future change started trusting a client-submitted
  price or name.

**Fixed:**
- **MEDIUM (latent, not exploitable today) — no check that `code` and
  `itemId` refer to the same item.** They're two independent
  client-supplied form fields (the picker's hidden inputs); a manipulated
  or stale submission could send a mismatched pair, pulling one item's
  modifier catalog onto a different item's base price. Additive-only
  price deltas (DB `CHECK (price_delta_minor >= 0)`, migration 017) mean
  this could only ever overcharge today, never underprice — but the
  moment any future version allows a negative delta (a "no cheese"-style
  discount modifier, explicitly anticipated as out-of-scope-for-v1 in the
  original design doc), a mismatch would underprice. Fixed by rejecting
  the request when `base.ItemID != itemID`; regression tested
  (`TestScanWithModifiers_RejectsMismatchedCodeAndItemID`).

## Verification
`go build ./...`, `go vet ./...`, `go test ./...` (full suite, zero
regressions), `bash scripts/ci/guard-data-access.sh`,
`bash scripts/ci/guard-i18n.sh` — all green, both before and after the
review-driven fix, and independently by the reviewer. Live-verified
against a real built binary: seeded a real item with modifier groups,
walked the actual tap → modal → submit flow via curl, confirmed the
folded price and modifier sub-line rendered correctly, added the same
item both plain and customized (two distinct, correctly-priced lines),
and removed only the targeted line by its key while the differently-
customized sibling survived untouched. New/updated tests:
`TestScanWithModifiers_RejectsMismatchedCodeAndItemID`,
`TestGetModifiers_RendersPickerWithGroups`,
`TestGetModifiers_UnknownItemIs404`,
`TestScanWithModifiers_AddsLineWithFoldedPrice`,
`TestScanWithModifiers_IgnoresClientSuppliedNamesAndPrices`,
`TestScanWithModifiers_RejectsOptionFromWrongGroup`,
`TestScanWithModifiers_RejectsMissingRequiredGroup`,
`TestScanWithModifiers_RejectsTooManySelections`,
`TestRemoveLine_TargetsExactlyOneLineEvenWhenSKUsCollide`,
`TestUpdateLineByKey_TargetsExactlyOneLineEvenWhenSKUsCollide`.

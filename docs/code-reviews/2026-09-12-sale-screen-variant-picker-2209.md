# Code Review — sale-screen variant picker (ut-docs#2209)

Date: 2026-09-12
Issue: universaltill/ut-docs#2209 (p1, `complexity:medium`)
Branch: `feat/2209-variant-picker-sale-screen`
Reviewer: independent Opus subagent, read-only, reviewing code it did not write
Dev: Sonnet subagent (card re-sized `hard` → `medium` at design time — see below)

## What shipped

Tapping a sale-screen tile for an item with variants (small/regular/large) added it
straight to the basket at the **parent item's base price**, never asking which variant.
At the German pilot café every sized coffee therefore rang at one price and the
takings were wrong. This is a money-correctness defect, which is why it was p1.

The fix reuses what already existed rather than building a parallel path:

- `CatalogRepo.ItemIDsWithVariants` — one batched query (mirroring
  `ModifierRepo.ItemIDsWithModifiers`) telling a whole grid of tiles at once whether
  tapping should open the picker.
- `HasVariants` on `ui.Button`/`ButtonVM` and on the kiosk's `shopItem`; both grid
  templates branch on "has modifiers **or** variants".
- A variant `<fieldset>` at the top of both existing pickers — choose the size before
  the extras.
- Variant resolution inside **`resolveAndValidateModifiers`**, the validator the
  cashier and kiosk already share, so the two surfaces cannot drift.

## The security-critical part, and why it is shaped this way

`itemId` and `variantId` arrive as two independent client-supplied values — the
picker's hidden input and its radio group. The **sole** guard against selling item A's
variant priced as item B's is membership in *this item's own* server-loaded,
sellable-variant set. A bare "does this variant id exist" lookup would accept any real
variant id regardless of owner. That check now carries a `DO NOT REMOVE` comment.

A second assertion backs it: after resolving the chosen variant by its server-loaded
code, the resolved line must carry the exact `VariantID` picked, or it is a 500. The
reviewer tried to defeat the guard with a variant SKU colliding with another item's
barcode, with an item SKU, and with a `shortcut_buttons.barcode` — the resolver tries
all three tiers *before* `item_variants.sku` — and every one lands on that assertion.
**It never misprices.** Do not let anyone "simplify" that re-assertion away.

## What the independent review found

Two blockers, both fixed in this branch, both with a regression test that was
**verified to fail without its fix** (checked personally, by disabling each fix in turn
and re-running — file copies in a scratch directory, never `git checkout`, which
reverts to the last *commit* and silently discards uncommitted work):

1. **A variant deactivated while the picker is open silently added the parent line.**
   `len(variants)` is read at *submit* time but the picker rendered earlier, so if the
   item's last sellable variant was retired in between, the submitted `variantId` was
   discarded and the parent base line added — a £9.99 line for a £3.10 coffee, no
   error, no log. That is the original defect, now laundered through a dialog that
   appears to have asked the question. A submitted `variantId` with nothing to match
   is now a conflict, never a fallback.
   Test: `TestScanWithModifiers_VariantDeactivatedMidPickIsRejectedNotParentPriced`.

2. **The two predicates disagreed.** `ItemIDsWithVariants` decided whether the *tile*
   opens the picker (any active variant); `sellableVariants` decided what the picker
   *offers* (active **and** carrying a resolvable code). For an item whose variants
   were all codeless, the tile opened a picker with no variant fieldset and a live
   "Add to cart", and submitting added the parent base price. Reachable on real data:
   `item_variants.sku` is nullable, `CreateVariant` has only auto-generated SKUs since
   ut-docs#1900, and nothing backfills older rows. Both predicates now apply the same
   condition, in SQL and in Go, with a comment on each saying why they must agree.
   Test: `TestItemIDsWithVariants_ExcludesCodelessOnlyItem`. Follow-up: ut-docs#2230.

Also fixed here (not a blocker, but wrong to ship):

3. **New user-facing strings were hardcoded English** — on a German pilot till and on
   the *anonymous kiosk*, where the message reaches a customer. Now keyed
   (`modifiers.variant_required`, `modifiers.variant_unavailable`) in all four core
   locales. The "not a member" case also said *"Item not found"*, which points the
   operator at the wrong thing: the item resolved fine, the variant did not.
4. **A failed guard query silently restored the bug shop-wide.** `hasVariants, _ :=`
   meant that on a query error every tile reverted to parent-price add with nothing in
   the log. Both call sites now warn, matching what `ButtonsHTTP.List` already does for
   its own non-fatal error.
5. A code comment claiming the `label.Code == ""` branch caught deactivation was
   **factually wrong** (`ItemVariantsFor` filters `is_active = 1`, so a retired variant
   never reaches it). Corrected rather than deleted.

## Test verification

The reviewer traced every new test against "would this still pass if the behaviour it
guards were inverted?" and found **no test that cannot fail**. The cross-item guard
test asserts *no line was added*, not merely that a 400 came back — which is the
assertion that matters, since the failure mode is a silently-priced line.

One existing test broke and was fixed correctly rather than relaxed:
`TestButtonsUIFragment_HxValsSurvivesQuotedCode` seeds a button against `itm1`, and
`seedForPages` gives `itm1` a variant (the ut-docs#744 fixture) — so that tile now
correctly opens the picker instead of posting `hx-vals`. It seeds a variant-less item
instead, because what it guards (a quote-containing barcode round-tripping as valid
JSON) is a property of the *plain* tile; asserting it against the picker branch would
have left it green while testing nothing.

## Deferred, with cards — not dropped

- **ut-docs#2227** (p2, Ready) — the suggestion strip and manual code entry still add
  variant items at the parent base price. Pre-existing, but now an inconsistency on the
  same screen.
- **ut-docs#2230** (p2, Ready) — codeless variants from before ut-docs#1900 silently
  never reach the sale screen.
- **ut-docs#2228** (p2) — the picker shows the raw variant price, not what the basket
  charges under an active `price_history` row.
- **ut-docs#2229** (p3) — no variant-picker POST test exercises the real resolver; the
  guard's safety net is proven only against a stub.

## Process notes worth keeping

- **The Dev subagent reported "completed" three times without finishing**, each time
  idling on its own background tests. Killing those processes only made it relaunch
  them on the next notification; `TaskStop` on the agent was required. Its runs
  competed with the gate on the same checkout and produced three packages timing out
  at exactly 10 minutes — which reads like a hang in the code under test, not machine
  contention. A subagent reporting completion is not evidence its work has stopped.
- **`EXIT=$?` after a redirect-and-echo reported 0 while the suite had failed.** The
  real exit code is now written into the log file itself. This is the third time this
  trap has cost this pipeline something.
- The card was re-sized `hard` → `medium` before the build, on evidence rather than
  budget: the pricing engine, the persistence shape (ut-docs#744) and the shared
  modifier flow all already existed, leaving a picker plus wiring.

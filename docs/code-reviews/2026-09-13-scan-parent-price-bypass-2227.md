# Code review — suggestion strip / manual code entry add variant items at the parent base price (ut-docs#2227)

- **Date:** 2026-09-13
- **Ticket:** ut-docs#2227 (complexity:medium)
- **Branch:** `fix/2227-scan-parent-price-bypass`
- **Reviewer:** independent pass, different model (Opus) from the Sonnet
  implementation, per this card's `complexity:medium` routing, in an
  isolated worktree.
- **Verdict: SAFE TO MERGE**, after the fix pass below. Two blocker-class
  findings from the first review round were fixed and re-verified
  in this same round (money/data-loss class — the process rule for a
  second full round is a re-review of the *fix*, not the whole diff again,
  and this record documents exactly that).

## The bug

ut-docs#2209 made the sale-screen **tile** ask for a variant (small/regular/
large, ...) before adding a line for an item with sellable variants — a
sized drink can no longer ring at its parent's base price from a tile tap.
Two other routes onto the same basket bypassed that picker entirely:

1. The "customers also buy" suggestion strip (`web/ui/partials/
   suggestions.html`) posts `code`+`qty` straight to `/api/pos/scan`.
2. Manual code entry on the sale screen's scan row (`web/ui/pages/
   index.html`) posts the same way.

Both add at the **parent's own base price**, silently, for an item that
should never have a single price. A prior cycle's BA+Architect analysis
(recorded on the issue) found the real defect site is the shared
`/api/pos/scan` endpoint itself (both callers post there), not the two
callers individually, and flagged two implementation risks up front:
guard placement relative to the scan-cache/basket fast path (F1), and qty
silently dropping on redirect into the picker (F2) — both are addressed
below.

## What shipped

- `internal/pages/pos_api.go`: `/api/pos/scan` now resolves the code
  read-only (`d.Engine.ResolveBase`) BEFORE the `HasScanCache`/`HasLine`
  fast path. If it's a PARENT code (`VariantID == ""`, `ItemID != ""`, not
  a weight/price-embedded scale label) with ≥1 sellable variant, it
  redirects the request into the shared modifier picker via
  `HX-Retarget: #modifier-modal` / `HX-Reswap: innerHTML` /
  `HX-Trigger-After-Swap: open-modifier-modal` instead of adding a line —
  the requesting elements (chip, scan row) target `#basket`, so retargeting
  is what gets the picker markup into the modal at all. Fails closed
  (toast, no add) on a catalog-lookup error.
- `internal/pages/self_order_shop.go`: the kiosk's `/api/self-order/scan`
  gets the same guard, but REFUSES rather than prompts — this endpoint is
  anonymous/auth-exempt and the shipped grid never reaches it with a
  variant-bearing parent code (`self_order_grid.html`'s own `HasVariants`
  check), so this only defends a crafted POST; there's no UI path that
  would ever see a rendered picker here.
- `internal/pages/pos_modifiers_api.go`: extracted a shared
  `renderModifierPicker` used by both the tile's `GET /ui/pos/modifiers`
  route and the new scan guard, per the card's "reuse the shared picker,
  no third resolution path" acceptance criterion.
- `web/ui/partials/modifier_picker.html`: added a hidden `qty` field
  (previously absent entirely) so a manual `qty=3` entry survives the
  detour through the picker instead of collapsing to 1 on submit (F2).
- `web/public/app.js`: a small `open-modifier-modal` listener that calls
  `.showModal()` on `#modifier-modal`, consumed via `HX-Trigger-After-Swap`
  (not plain `HX-Trigger`, which would fire before the swap against an
  still-empty dialog).
- `web/help/{en,de,ar,fa,tr}/sell.md`: updated the "Customers also buy"
  section, which previously said the chip "always adds directly" and
  skips the customization picker. Now accurate: a variant-bearing item's
  chip opens the same picker the tile does; a modifier-only item (no
  variants) still skips it and adds directly — that residual inconsistency
  is real, known, and explicitly out of scope for this card per the design
  note (folding it in would double the diff and isn't money-correctness).
- Tests: `internal/pages/pos_scan_variant_guard_test.go` (new), plus
  fixture adjustments in `pos_api_test.go` / `line_order_type_test.go` /
  `pos_modifiers_variants_test.go` (see "Fixture collisions" below).
- No new i18n key — reused the existing `modifiers.variant_unavailable`
  (cashier fail-closed path) and `modifiers.variant_required` (kiosk
  refusal path, see Non-blocker 3 below), both already present in every
  locale, avoiding a `lang-pack-drift` cross-repo dependency for this fix.

## What the independent review found

**First pass verdict: NOT SAFE TO MERGE**, on two blocker-class findings.
Both were money-correctness regressions introduced BY this fix, not
pre-existing bugs — real, not hypothetical, confirmed by the reviewer with
a working repro before either was fixed.

### BLOCKER 1 — weighed/scale-label items silently lose their decoded weight

A weight- or price-embedded scale label (ADR-0059 §3, `QtyFromCode`) that
resolves to a PARENT item which ALSO has a sellable variant got redirected
into the picker like any other parent code. On submit, the picker resolves
the CHOSEN VARIANT's own plain code (`resolveAndValidateModifiers`) —
which carries no decoded weight/price at all. The reviewer reproduced this
against the unguarded build: a 1.234kg cheese label re-priced as a flat
qty=1 once routed through the picker. This is a real regression the fix
introduced (the tile path is never reached by a scale label at all, so
`/api/pos/scan` was the only path and it was correct before).

**Fix:** the guard now excludes any code that resolves with
`QtyFromCode == true`, on both the cashier and kiosk paths — a scale-label
scan is left completely untouched by this change, added directly exactly
as before. There is no correct "which variant" UI this diff can build for
a scale label in the same change; deferring that combination (if it's
ever a real product need) is the right scope, not guessing at it here.
Added `TestScanAPI_ScaleLabelWithVariants_AddsDirectlyUnaffected` as a
permanent regression test.

### BLOCKER 2 — the qty-carry-through test was a false pass

`TestScanAPI_ParentCodeWithVariants_CarriesQtyIntoPicker` asserted only
`strings.Contains(body, 'name="qty" value="3"')`. With the guard clause
disabled entirely, the item is added directly instead and the basket
partial's OWN line-qty control renders the identical substring — so the
test kept passing even with the fix removed, providing zero protection
against exactly the bug it was named for.

**Fix:** the test now asserts `HX-Retarget: #modifier-modal` first (same
anchor the sibling redirect test uses) before trusting the qty assertion,
so a neutered guard fails it immediately and unambiguously.

### Independent TDD re-verification (this session, after the fixes above)

For both blockers: reverted the fix, confirmed the now-added regression
test fails with the exact defect described, restored the fix, confirmed
green again. Full `internal/pages` package suite re-run clean afterward
(328s, ok).

### Non-blockers — fixed in this round

- **Non-blocker 3** (misleading kiosk toast): the kiosk's deterministic
  refusal path used `modifiers.variant_unavailable` ("the options just
  changed, try again") — nothing changed and retrying fails identically.
  Switched to the already-existing `modifiers.variant_required` ("please
  choose a variant"), accurate and free (no new key).
- **Non-blocker 4** (`showModal()` re-entrancy): a wedge/HID barcode
  scanner submits the scan row programmatically regardless of focus, so a
  second parent-code scan while a first picker is still open would call
  `.showModal()` on an already-open `<dialog>` and throw
  `InvalidStateError`. Guarded with `if (m && !m.open) m.showModal())`.
- **Non-blocker 5** (headers committed before a possible render failure):
  `HX-Retarget`/`HX-Reswap`/`HX-Trigger-After-Swap` were set before
  `renderModifierPicker`'s own DB queries, so a query error there could
  leave a 500 still carrying those headers. `renderModifierPicker` now
  takes already-loaded groups/variants as arguments instead of fetching
  them itself; both call sites fetch (and handle the error from) that data
  BEFORE writing any header. This also removes the guard's redundant
  second `ItemVariantsFor` call (the same variants slice now flows straight
  into the render).
- **Non-blocker 7** (overclaiming code comment): softened the guard's
  comment about `ResolveBase` "only resolving real item codes" — its name-
  substring fallback tier could theoretically catch a customer/voucher
  code, though it's already shadowed in practice by this handler's own
  item-resolution ordering. Comment now says that precisely instead of
  overclaiming a guarantee the code doesn't actually make.

### Non-blocker — noted, not fixed here (filed as a follow-up)

- **Non-blocker 6** (extra DB round-trip on every scan): the guard's
  `ResolveBase`/`ItemVariantsFor` calls now run ahead of the
  `HasScanCache`/`HasLine` fast path on every single scan, including a
  scan that fast path exists specifically to keep cheap. Correctness-wise
  the placement is required (see F1/BLOCKER 1's reasoning), so this is a
  performance follow-up, not a merge blocker. Filed as ut-docs#2244
  (`complexity:easy`, `performance`) rather than folded into this diff.

## Fixture collisions found and fixed during development (pre-review)

The widely-shared `seedForPages` fixture's `itm1`/`ABC` item (used across
most of `internal/pages`) already carries a real sellable variant
(`var1`/`VAR`, seeded for ut-docs#744) — so a bare HTTP scan of "ABC" now
correctly triggers this guard, which broke `line_order_type_test.go`'s six
call sites that scanned "ABC" via HTTP expecting a direct, ungated add for
unrelated per-line-order-type behavior. Fixed by seeding a second,
genuinely variant-free item (`itm-plain2`/"PLAIN") **locally inside
`newPOSTestDeps`** rather than in the shared `seedForPages` — an earlier
attempt to add it to the shared fixture broke `ask_api_test.go`'s
stock-level row-count assertions (an extra inventory row) and collided
with `buttons_api_test.go`'s own separately-seeded `itm-plain` id. Confined
to the one helper that actually needed it.

## Verified beyond automated tests

- Full repo suite (`go test ./internal/... ./cmd/...`) green, this round
  and the prior one.
- All four CI guard scripts green: `guard-kiosk-engine.sh` (kiosk path
  only ever touches `d.KioskEngine`), `guard-data-access.sh` (no inline SQL
  outside `internal/data`), `guard-i18n.sh` (no new/missing keys, no
  hardcoded strings), `guard-help-topics.sh`.
- Reviewer enumerated every basket-add-by-code path in the repo (camera
  barcode scan, AI-identify, the wedge scanner, both `scan-with-modifiers`
  endpoints) and confirmed all funnel through the two guarded endpoints —
  no third unguarded path found.
- Help-doc translations (de/ar/fa/tr) checked for structural parallelism
  with the English paragraph (same three claims, same order); not
  independently checked for idiomatic fluency.

## CI found a real gap this review's own e2e claim missed

The first pass of this review record claimed "e2e demo/seed data seeds no
`item_variants` rows at all, so this guard is a verified no-op for the
entire existing e2e suite" — **that was wrong**, caught only after this
PR's own CI ran. That check was based on grepping `e2e/` for
`item_variants` and finding nothing; the actual demo catalogue those
specs seed from is `internal/data/seeddata/demo_catalogue.sql` (loaded via
`go run ./e2e/seed_demo` in `run-till.sh`), outside the `e2e/` tree
entirely — a real blind spot in how that check was scoped, not a
difference of judgment.

That catalogue seeded `itm001` (Coca-Cola), `itm002` (Pepsi) and `itm005`
(Orange Juice) — the three demo items dozens of unrelated `e2e/tests/`
specs scan by their parent barcode as "just some item to add," entirely
unrelated to variant behavior — with a real, active, barcoded variant
each. Once this fix shipped, scanning those parent barcodes correctly
opened the picker instead of adding directly, which broke every one of
those specs (confirmed live: PR CI's "UI E2E" job ran far longer than this
repo's own `main`-branch baseline for the same workflow, and the "build"
job's `guard-docs-shots.sh` step failed for the same underlying reason —
`docs-shots.spec.ts`'s own basket-staging helper scans `itm001`/`itm002`
too).

**Fix:** rather than editing every affected spec (dozens of files, several
with test logic that depends on the item's exact price — editing each
correctly would have been slow and error-prone), moved the affected
variants in `demo_catalogue.sql` off `itm001`/`itm002`/`itm005` onto three
different items nothing in `e2e/` references by barcode (`itm006` Apple
Juice, `itm007` Semi-Skimmed Milk, `itm008` Whole Milk). That makes the
three commonly-scanned items plain again — every existing spec that scans
them goes back to working completely unmodified — while the guard itself,
and the money-correctness behavior it protects, is untouched. Verified: a
representative sample across the affected surface (`sale-screen-213`,
`catalog-inventory-category-filter-2119` — including its own two tests
whose comment specifically named Pepsi's variant row, `voucher-split-
tender-combine-1851`, `hold-modal-duplicate-accessible-name-1628`,
`sale.spec.ts`, `split-tender-underpayment-921`, `manual.spec.ts`; 34
tests total) all pass, including ones asserting the item's exact original
price (e.g. "payments (50) do not cover total (120)" for itm001).
Confirmed no e2e spec anywhere exercises the variant-picker flow through
these three items specifically, so nothing was relying on them having a
variant. One spec's own comment (`catalog-inventory-category-filter-2119`)
incorrectly claimed Pepsi has a variant row for a selector-disambiguation
reason — the selector itself doesn't need one (every stock row always
carries a `data-variant` attribute, empty or not), so only the comment
needed correcting, not the test.

**The lesson, stated plainly for next time:** "no e2e impact" is not a
claim a repo-subtree grep can support when the actual seed data a suite
depends on can live in `internal/`. Scope a data-dependency check to where
the runtime data actually loads from, not to the test directory that
consumes it.

## Explicitly deferred (not this card)

- Modifier-only (no variants) items still skip the picker via the
  suggestion chip/manual entry — a real, known inconsistency, but not a
  money-correctness bug (a skipped optional extra doesn't misprice the
  base item) and doubling this diff's scope. Wants its own card, per the
  design note this cycle inherited.
- The extra per-scan DB round-trip (non-blocker 6) — ut-docs#2244.

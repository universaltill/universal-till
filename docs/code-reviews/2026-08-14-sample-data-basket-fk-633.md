# Review: sample-data removal vs. a live/held basket (ut-docs#633)

**Date**: 2026-08-14
**Card**: universaltill/ut-docs#633 — "Sample-data removal (items and
customers) can delete a row a held/in-progress basket still references,
breaking tender"
**Complexity**: medium
**Reviewer model**: fresh-context Opus subagent, worktree-isolated (per
this card's `complexity:medium` tier — see `scrum-master` skill's model
routing)

## What shipped

ut-docs#567 closed this exact hazard for demo **customers**
(`held_sales.payload` LIKE check in `remove_demo_customers_promos.sql`).
The identical hazard existed for demo **catalogue items**, introduced by
migration 036 and never fixed — a parked basket holding a demo item,
followed by "Remove sample data," followed by recalling and tendering
that basket, hit a real FK constraint failure. There was also a second,
narrower gap ut-docs#567 didn't have to deal with: a demo item/customer
live in the **current** (not-yet-held) basket has no `held_sales` row for
any SQL check to catch at all.

- `internal/data/seeddata/remove_demo.sql` (and its byte-identical
  embedded copy in `internal/db/migrations/036_demo_seed_opt_in.sql`,
  guarded by `TestMigration036MatchesSeedData`): the `demo_seed_removable`
  predicate gained two `NOT EXISTS` clauses mirroring the customer fix —
  one for `held_sales.payload LIKE '%"item_id":"<id>"%'`, one joining
  through `item_variants` for `"variant_id":"<id>"`. Both are
  independently load-bearing (verified below) even though a real resolved
  variant line's payload carries both keys today, since the item_id clause
  alone already covers that case — the variant clause is deliberate
  defence-in-depth, documented as such after review.
- `internal/pages/settings_page.go`: new `demoDataInLiveBasket(engines
  ...*pos.Service) bool` helper, checked before either removal call in
  `POST /api/settings/remove-demo-catalogue`. Checks both `d.Engine`
  (cashier) and `d.KioskEngine` (self-order, ADR-0020 isolation respected
  — read-only, no route conflict) against the static
  `seeddata.ItemIDs`/`VariantIDs`/`DemoCustomerIDs` lists. nil-safe (kiosk
  engine is nil in some harnesses).
- `web/locales/{en,ar,fa,tr}.json`: new `settings.data.demo_in_basket` key.
- `web/help/{en,ar,fa,tr}/display.md`: step 7 (Sample data) now mentions
  the new blocked case — added after review flagged the manual was stale
  against the new behaviour (see below).
- Tests: `internal/data/demo_seed_repo_test.go` —
  `TestRemoveDemoCatalogueKeepsHeldSaleItem` and
  `...KeepsHeldSaleVariantItem` (held-sale item/variant references
  survive removal). `internal/pages/demo_seed_opt_in_test.go` —
  `TestSettingsRemoveDemoCatalogueEndpoint_BlocksWhileDemoItemInLiveBasket`
  and `...BlocksWhileDemoCustomerInLiveBasket` (live-basket guard actually
  blocks the HTTP endpoint, and the guard is proven to be a real gate, not
  an unconditional block, by clearing the basket and re-asserting
  removal then succeeds).

Manually driven end-to-end against a real running server (`go run .`,
real migrated SQLite DB, headless Chromium via Playwright): completed the
setup wizard with demo data on, scanned a real demo item into the live
cashier basket via `/api/pos/scan`, clicked "Remove sample data" and
confirmed the new blocked message rendered correctly inline (screenshot
reviewed — same DOM position/style as the sibling `demo_kept`/
`demo_removed` messages, no overlap/wrapping), then cleared the basket
and confirmed the same button then removed all 56 sample records with
the pre-existing success message. Did **not** check RTL/dark-theme/other-
locale rendering of the new string specifically — it reuses an identical,
already-shipped `<span>`/CSS-class pattern that already carries long
dynamic text (`%d could not be removed…`), so the incremental visual risk
is low, but that's a stated gap, not a verified pass.

## Independent review (fresh-context Opus, worktree-isolated)

**Verdict: safe to merge**, after fixing one blocker. Ran `go build`,
`go vet`, the full `go test ./...`, and all four required guards
(`guard-data-access`, `guard-i18n`, `guard-kiosk-engine`,
`guard-plugin-menu-read`) plus `guard-help-topics`/`guard-compliance-claims`
— all green both before and after fixes.

**Independent TDD re-verification (not just trusted on say-so):** reverted
each fix in its own isolated worktree, confirmed an on-topic failure, then
restored and confirmed green.
- Removing both new `held_sales` clauses from `remove_demo.sql`:
  `TestRemoveDemoCatalogueKeepsHeldSaleItem` and `...VariantItem` both
  failed (`removed 50, kept 0; want 49, 1`). Restoring only the `item_id`
  clause passed the first test but not the second, proving the variant
  clause independently necessary for its own test even though (per the
  finding below) it's redundant against what production actually sends.
- Neutralising the `demoDataInLiveBasket` call in the handler:
  `TestSettingsRemoveDemoCatalogueEndpoint_BlocksWhileDemoItemInLiveBasket`
  and `...Customer` both failed — the handler happily deleted all 50/6
  sample records while one was live in the basket. Restored, both pass.

### Findings

**Blocker (fixed):** the user manual (`web/help/{en,ar,fa,tr}/display.md`
step 7) described only two removal outcomes (removed/kept) and didn't
mention the new "blocked while in the basket" outcome — a standing
product-owner rule (ut-docs#324) requires the manual to ship with the
feature in the same branch. `guard-help-topics.sh` doesn't catch this
(it checks structure, not prose). **Fixed**: one added sentence in all
four locale files.

**Real but minor (fixed):** a new test's own comment claimed a real
variant-scan payload carries only `variant_id`, no `item_id` — the
reviewer disproved this by reading `internal/ui/buttons.go`'s `resolve()`
and `internal/data/pos_repo.go`'s `resolveVariant`, which set both fields
from the same resolved row. **Fixed**: rewrote the comment to correctly
describe the test as exercising the variant-only `NOT EXISTS` clause in
isolation (defence-in-depth), not a shape production actually sends.

**Real but minor (deferred to ut-docs#746):** the live-basket guard is
coarser than strictly necessary — it matches against static demo-ID lists
rather than actual removability, so a demo item that's already
permanently kept for an unrelated reason (e.g. already sold) still blocks
removal of everything else if it happens to be in the basket; one
combined guard blocks both the catalogue and customer/promo removal
together; the error message doesn't say which basket (cashier vs. kiosk)
is the culprit; and the kiosk-basket-blocks case has no test (the nil-skip
path for a *missing* kiosk engine is proven, the positive-match case
isn't). A narrow TOCTOU (basket changes between the guard check and the
delete) was also noted as probably not worth fixing given the single-till,
offline, low-value target. None of this makes the shipped guard unsafe —
it's strictly safer than the prior "no guard at all" — so deferred rather
than expanding this card's scope.

**Nitpick (fixed):** the new English string repeated "sample data" ("…in
the current basket — clear it before removing sample data") where the
ar/fa/tr translations already read more naturally; reworded to "clear the
basket first."

**Nitpick (accepted, no change):** the new error message is interpolated
into the HTMX fragment unescaped, unlike two sibling error branches in the
same handler that use `html.EscapeString`. Locale content is Ed25519-
verified at install and this string has no runtime-interpolated user
input, so there's no real injection risk — noted for symmetry only, not
fixed.

**Explicitly checked, none found:** repository pattern (all new SQL is in
`internal/data/seeddata`/`internal/db`), money type (not touched), i18n
(key present and consistent across all 4 locales, natural-reading
translations), offline-first (pure local state, no network), kiosk
isolation (ADR-0020 — read-only reference, no `/self-order` route
touched), real client/shop names in tests (none — reused pre-existing
demo-seed values), the migration-036-is-append-only concern (not a
violation — editing an existing migration's embedded shared-asset SQL is
this repo's established, test-guarded pattern; migration 038 already set
the precedent for the customer-side fix, and `TestMigration036/038MatchesSeedData`
exist specifically to keep the embedded copy honest), and the `sale_lines`/
`inventory`/etc. CHECK constraint (exactly one of item_id/variant_id) —
doesn't interact with this change, which only adds `NOT EXISTS` filters
and never writes those tables.

### Out of scope, filed as new Backlog cards

- **universaltill/ut-docs#744** (p2, likely a real pre-existing bug):
  review traced (and partly proved by running code) that a variant
  resolved by barcode scan carries both `ItemID` and `VariantID` all the
  way to `pos.CompleteSale`, which appears to reject that shape via a
  `validateLine` check and a `sale_lines` CHECK constraint that also
  requires exactly one of the two — meaning a variant-barcode-scan sale
  may currently be untenderable. Not proven end-to-end (the tender half is
  code-reading only), so filed as "confirm and fix" rather than acted on
  here — genuinely unrelated to this card's scope.
- **universaltill/ut-docs#745** (p3, docs only): document why the
  live-basket guard deliberately doesn't check an applied demo promo code
  (currently safe because `sale_discounts` doesn't record which code was
  used, but that reasoning isn't written down anywhere near the guard).
- **universaltill/ut-docs#746** (p3): the coarser-than-necessary guard
  findings above (over-blocking, combined-basket blast radius, message
  locality, missing kiosk-positive-match test, TOCTOU).

## Safe to merge

Yes. Blocker fixed, gate green, TDD claims independently re-verified by
reverting and restoring each fix in isolation, deferred items filed as
their own cards rather than silently dropped.

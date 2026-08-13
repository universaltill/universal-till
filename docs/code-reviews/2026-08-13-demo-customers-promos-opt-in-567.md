# Code review: demo customers/promo codes get the catalogue's opt-in treatment (ut-docs#567)

## What shipped

`001_init.sql` unconditionally seeded 3 demo customers (Alice Carter/Ben
Singh/Chloe Martin) and 3 live promo codes (PROMO50, PROMO500, DISC10 — 10%
off the whole basket) on every install. Migration 036 (ut-docs#539) already
made the demo *catalogue* opt-in with a Settings → Data "Remove sample data"
action but explicitly scoped itself to catalogue-only. This closes that gap:
customers/promotions get the identical opt-in/removal treatment.

- New migration `038_demo_customers_promos_opt_in.sql`: adds
  `is_sample_data` to `customers`/`promotions`, flags the legacy rows, and
  removes untouched ones — same outcome on a fresh install (nothing can
  reference the rows yet) and an upgrade (a real "untouched" safety check).
- New `internal/data/seeddata/demo_customers_promos.sql` (opt-in seed),
  `demo_customers_promos_ids.sql` (TEMP ID tables),
  `remove_demo_customers_promos.sql` (removal) — shared verbatim between
  the migration and `DemoSeedRepo`, mirroring the catalogue's own
  `demo_ids.sql`/`remove_demo.sql` convention (guarded by
  `TestMigration038MatchesSeedData`).
- `DemoSeedRepo` gains `SeedDemoCustomersPromos`, `RemoveDemoCustomersPromos`,
  `SampleCustomerPromoCount`; `SampleItemCount`/`RemoveDemoCatalogue` share a
  new `sampleCount(ctx, q, table)` helper (pure rename/generalization, zero
  behavior change — reviewer verified byte-for-byte).
- Setup wizard's existing "demo_data" checkbox now also seeds
  customers/promos (best-effort, same posture as the catalogue call next to
  it). Settings' "Remove sample data" now removes catalogue AND
  customers AND promo codes together, one combined removed/kept count.
- i18n copy (`settings.data.demo_*`, `setup.demo_data.*`) broadened from
  "item(s)" language to describe the full coverage, across all 4 locales.
- Manual: `web/help/{en,ar,fa,tr}/display.md` (Settings → Data) and
  `users.md` (setup wizard step) updated to describe the broadened scope.
  Screenshots regenerated via `make docs-shots` (the cloud toolchain fix
  from the immediately preceding card, ut-docs#622, is what made this
  possible in this session) — `manifest.json` updated; no PNG pixel content
  actually changed since neither page's rendered layout changed, only copy.

## Review

Independent review via an Opus subagent, isolated in a separate git
worktree — `complexity:medium`, so review runs at the stronger model per
the `scrum-master` skill's model-routing table. **Round 1 verdict: safe to
merge after two should-fix items** (no blocker in the strict sense, but two
findings were judged blocker-*class* — a checkout-breaking bug and a silent
permanent data-loss risk — earning a second, scoped round per the skill's
"a second round has to be earned by the first finding a blocker-class
issue" rule).

The reviewer ran the actual full gate (build, `go test ./... -race`, all
guards) and wrote and ran five throwaway probe tests directly against the
schema to empirically verify claims in the diff's own comments, rather than
taking them on trust — worth naming because it's what caught F3 and F4
below; a read-only pass wouldn't have.

### Round 1 findings — should-fix, all addressed

1. **F1 — the actual consent screen (setup wizard) still said "sample
   items."** The same checkbox now also creates 3 customer records and 3
   live redeemable discount codes (one 10% off the whole basket); the
   wizard's label/hint and the `users.md` manual topic never mentioned
   this. This is the AC "clearly labelled as sample data" — a checkbox that
   only names the catalogue reproduces the card's own bug at the opt-in
   end. **Fixed**: `setup.demo_data.label`/`.hint` (all 4 locales) and
   `users.md` (all 4 locales) now name the customers/promo codes
   explicitly, including the 10%-off code.

2. **F2 — a demo customer referenced only by a HELD (parked) sale could
   still be deleted, breaking tender.** `held_sales.payload` is JSON
   (`internal/pos/hold.go`'s `SnapshotPayload.CustomerID`) — a basket
   parked against a demo customer hasn't reached the `sales` table yet, so
   the original removability check (`sales.customer_id`,
   `promotions.customer_id`) missed it entirely. Reviewer proved it
   empirically: park a tab against a demo customer, run removal, the
   customer is deleted, and completing that sale later hits a real FK
   constraint failure — a raw 500 at tender, a direct breach of this
   product's "checkout must never be blocked" non-negotiable. **Fixed**:
   added `AND NOT EXISTS (SELECT 1 FROM held_sales h WHERE h.payload LIKE
   '%"customer_id":"' || c.id || '"%')` to the customer safety predicate
   (`remove_demo_customers_promos.sql`, mirrored in migration 038). New
   regression test `TestRemoveDemoCustomersPromosKeepsHeldSaleCustomer`
   reproduces the exact scenario and proves it's now kept.

3. **F3 — the promotion "untouched" proxy (`customer_id IS NULL`) was
   close to vacuous in practice.** Reviewer found two real problems: (a)
   this product has **no promotions management UI at all** — `customer_id`
   is literally the only field any shop could ever have set on a
   promotion, so in every real deployment every demo promo code would be
   deleted unconditionally on upgrade regardless of whether it was
   actually in daily use, silently and with no way to recreate it; (b) a
   promo edited in-place (value/description changed) with no targeting was
   still deleted — proven by editing DISC10 to a 15%-off "Summer sale" and
   watching removal delete it anyway. **Fixed**: the removability check now
   additionally requires every other field (`type`/`value`/`description`/
   `is_active`/`starts_at`/`ends_at`) to still match its exact 001 seed
   default per code — "does this row still read exactly as seeded" is a
   real, durable, schema-supported signal, unlike guessing at redemption
   history this schema genuinely cannot recover (confirmed: `sale_discounts`
   records only the resulting amount, never which code produced it — traced
   through `pos_api.go`'s `/api/pos/scan` → `FindActivePromo` →
   `SetDiscount`/`SetDiscountPercent` → `InsertSaleDiscount`). New
   regression tests `TestRemoveDemoCustomersPromosKeepsCustomizedPromotion`
   (repo level) and an extended
   `TestDemoCustomersPromosUpgradeKeepsTouchedRows` (migration/upgrade
   level, per the card's own AC that this be verified against an upgraded
   fixture) both prove a customized-but-untargeted promo now survives.
   This was judged the right in-scope fix rather than escalating to the
   product owner: it's a mechanical, schema-derived strengthening with a
   clear, conservative default (favor keeping data over losing it), not a
   business/scope call.

4. **F4 — a migration comment stated something false.** "None of the
   seeded demo promotions ever set customer_id" is untrue in general (a
   shop can target one — the project's own test does exactly that); the
   comment's real and sufficient reasoning was the sentence right after it
   (the customer-removable set is computed from the pre-delete state, so
   deletion order can't matter). **Fixed**: removed the false half, kept
   and expanded the correct one, in both the seeddata script and its
   migration mirror.

5. **F5 — a seed-script header claimed a Settings re-seed action that
   doesn't exist.** Settings → Data only ever *removes* sample data; the
   only caller of `SeedDemoCustomersPromos` is the setup wizard. **Fixed**
   the header comment and `seeddata.go`'s package doc (which also still
   described the package as catalogue-only).

6. **F6 — `docs/data-model.md` went stale the moment this shipped.**
   Still described PROMO50/PROMO500/DISC10 as unconditionally seeded, and
   neither the `customers` nor `promotions` ER block/column list carried
   the new `is_sample_data` column. **Fixed**: added the column to both,
   and a note on the opt-in/removal behavior matching migration 038.

### Round 1 findings — nits, deferred (not blocking, out of this card's
bounded scope)

- F7 (settings handler runs catalogue-removal and customers/promos-removal
  as two separate transactions — a partial-failure edge case with no
  today-observed trigger).
- F8 (`sampleCount`'s table name is string-interpolated — no injection
  risk today, all call sites are literals, but worth a small type later).
- F9 (a dead-value `map[string]int` iteration in a test — cosmetic).
- F10 (informational only: a mixed-version LAN sync window briefly
  re-introduces the demo rows on a not-yet-upgraded replica, self-heals —
  identical pre-existing behavior to 036's own item rows, not a regression
  this card introduces).
- F11 (the `/api/settings/remove-demo-catalogue` route name is now a
  misnomer — cosmetic, internal-only route).
- The general "a deleted sample row referenced by a held/in-progress
  basket can break tender" class (this card's F2 fix covers customers
  specifically; the identical hazard already existed for catalogue items
  since migration 036) and "the product has no promotions management UI at
  all" are real, separately-scoped follow-ups — tracked as new Backlog
  cards rather than grown into this one.

## Verified

- `go build ./...`, `go test ./... -race` (full suite, both before and
  after the round-2 fixes), all six `scripts/ci/guard-*.sh` scripts
  (data-access, kiosk-engine, plugin-menu-read, i18n, help-topics,
  docs-shots) — green throughout.
- New/updated tests, all passing: `TestSeedDemoCustomersPromos`,
  `TestRemoveDemoCustomersPromos`,
  `TestRemoveDemoCustomersPromosKeepsHeldSaleCustomer`,
  `TestRemoveDemoCustomersPromosKeepsCustomizedPromotion`,
  `TestRemoveDemoCustomersPromosEmpty`,
  `TestRemoveDemoCustomersPromosLeavesOwnRecordsAlone` (repo level);
  `TestDemoCustomersPromosUpgradeKeepsTouchedRows` (extended to cover the
  customized-promotion case), `TestDemoCustomersPromosUpgradeRemovesAllWhenUntouched`,
  `TestMigration038MatchesSeedData` (migration/upgrade level);
  `TestSettingsRemoveDemoCatalogueEndpoint` (updated for the new copy),
  `TestSettingsRemoveDemoCatalogueEndpointCoversCustomersPromos`,
  `TestSetupWizardShopTypeAndDemoOptIn`/
  `TestSetupWizardNoDemoByDefaultAndInvalidShopTypeIgnored` (extended to
  assert the customer/promo seeding too) (handler level). Also fixed three
  pre-existing tests (`barcode_seed_test.go`, `dead_seed_test.go`,
  `demo_seed_migration_test.go`) that rewind `schema_migrations` and
  manually drop columns to simulate a pre-migration DB — they needed the
  same `is_sample_data` column rewind for migration 038's new
  non-idempotent DDL, or replaying hit "duplicate column name."
- `make docs-shots` run end-to-end twice in this session (once for the
  round-1 diff, once after the round-2 `users.md`/`display.md` copy
  changes) — 68/68 passing both times; the 9 PNGs that came back with
  small binary diffs each run (alerts/designer/sell, confirmed
  pre-existing non-determinism, documented in ut-docs#622's own review
  record) were deliberately not committed — the manifest's `surface_sha256`
  and per-topic hashes are unaffected either way.
- No real client/shop name, no literal credential. UI-surface change
  (Settings/setup wizard copy only — no new template structure): checked
  against `reference/ux-guidelines.md`'s checklist — reuses existing
  design tokens/markup unchanged, no new modal blocker, RTL-safe (prose
  changes only, no new layout). Manual topics updated in the same branch
  per the standing product-owner instruction.

## Safe-to-merge verdict

Yes. No blockers remain; all five should-fix findings from round 1 are
fixed and independently re-verified (new regression tests reproduce and
disprove F2/F3 specifically); nits deferred as noted above rather than
expanding this card's scope.

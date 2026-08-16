# Code review — reset-archive-aware catalog cleanup / demo removal / customer erasure (ut-docs#640)

**Date:** 2026-08-16
**Card:** universaltill/ut-docs#640 (p2, compliance, `complexity:medium`)
**Branch:** `fix/reset-archive-consult-cleanup-erasure-640`
**Dev:** inline (Scrum Master pipeline cycle, Sonnet — medium-tier build model)
**Reviewer:** independent subagent, Opus (medium-tier review model), spawned
with `isolation: "worktree"`

## What shipped

`POSRepo.ResetTransactionHistory` (ADR-0042, ut-docs#187) archives
sales/sale_lines/stock_movements/held_sales/etc into `*_archive` tables
instead of deleting them, restorable via `RestoreResetBatch` until the till
trades again. Four other data-management actions decided what's safe to
remove/anonymise by checking only the LIVE tables — right after a reset,
those checks see nothing, so they could delete/anonymise something a
still-restorable archive batch depends on, and a later `RestoreResetBatch`
would then hit a live FK it can no longer satisfy. This was already a known,
documented gap (`ErrArchiveReferencesRemoved`'s doc comment,
`internal/data/reset_archive_repo.go` — the defensive backstop on the
restore side); this card is the root-cause fix on the delete/erase side.

- `internal/data/pos_repo.go` — `obsoleteItemsWhere` (used by
  `CleanupObsoleteItems`/`ListObsoleteItems`) extended with
  `sale_lines_archive`/`stock_movements_archive` clauses, mirroring the
  existing live-table clauses exactly (item_id direct + via variant_id →
  `item_variants.item_id`; `item_variants` itself is never archived).
- `internal/data/pos_repo.go` — `EraseCustomer` (GDPR erasure) now also
  `UPDATE`s `sales_archive.customer_id = NULL`, not just the live
  `sales.customer_id`, before deleting the customer row.
- `internal/data/seeddata/remove_demo.sql` (`DemoSeedRepo.RemoveDemoCatalogue`)
  — `demo_seed_removable` CTE gets matching `sale_lines_archive`/
  `stock_movements_archive`/`held_sales_archive` `NOT EXISTS` clauses.
- `internal/data/seeddata/remove_demo_customers_promos.sql`
  (`DemoSeedRepo.RemoveDemoCustomersPromos`) — `demo_seed_customers_removable`
  CTE gets matching `sales_archive`/`held_sales_archive` clauses.
- `internal/db/testdata/frozen_remove_demo_036.sql` +
  `frozen_remove_demo_customers_promos_038.sql` (new) — migrations 036/038
  each embed a byte-identical copy of the corresponding `seeddata/*.sql`
  script AS IT STOOD when the migration was authored (migrations are
  append-only per `CLAUDE.md`; 036/038 predate the reset-archive mechanism
  entirely, chronologically, so a pre-036/038 database can never hold
  archived demo references — staying on the pre-#640 rule there is
  correct, not stale). `internal/db/demo_seed_migration_test.go`'s
  `TestMigration036MatchesSeedData`/`TestMigration038MatchesSeedData` now
  pin against these frozen fixtures instead of the live, evolving
  `seeddata.RemoveDemoSQL`/`RemoveDemoCustomersPromosSQL` exports.
- Tests: `internal/data/reset_test.go` (2 new: obsolete-items archive-kept,
  erase-customer archive-anonymise-and-restore round trip) and
  `internal/data/demo_seed_repo_test.go` (7 new, covering sale_lines_archive,
  stock_movements_archive and held_sales_archive on both the item and
  customer predicates, item + variant shapes).

## Independent review (Opus, fresh context, worktree-isolated) — 2 blockers found and fixed, 2 should-fixes found and fixed

**First-round verdict: NO, not safe to merge as-is.** Full verbatim
findings on the issue; summary and disposition:

- **BLOCKER 1 (fixed) — the "Remove sample data" button's customer half
  still had the gap.** `remove_demo_customers_promos.sql` checked only
  live `sales.customer_id`/`held_sales.payload`/`promotions.customer_id`;
  a demo customer referenced only by an archived sale was still deleted.
  Reviewer proved it with a throwaway probe test. Fixed by adding a
  `sales_archive.customer_id` clause, plus the matching frozen-fixture
  treatment for migration 038 (the same append-only concern as 036).
- **BLOCKER 2 (fixed, design changed) — the original `EraseCustomer` fix
  refused erasure instead of anonymising, and that refusal was almost
  always permanently unactionable.** The first pass added
  `ErrCustomerReferencedByArchive` and told the operator to "restore or
  purge that batch." Reviewer traced the actual constraints: any batch
  that can trigger the refusal has `sales_count > 0`, so
  `DeleteResetBatch` refuses it for `GlobalArchiveMinDays` (10 years by
  default), and `RestoreResetBatch` refuses once the till has traded
  again (true after the shop's very next sale) — together making the
  named remedy nearly always impossible, turning a GDPR Article 17
  erasure request into a multi-year dead end. **Redesigned**: `EraseCustomer`
  now also `UPDATE`s `sales_archive.customer_id = NULL`, anonymising the
  archived copy the same way it already anonymises the live one, instead
  of refusing. This matches the function's own existing contract
  ("keeping the sales… but anonymous"), needs no new error type, no new
  HTTP status, no new locale key (all reverted), and a follow-up restore
  now succeeds with the sale correctly still-anonymous (proven by
  `TestEraseCustomer_AnonymisesArchivedSaleToo`'s round-trip assertion).
  Reviewer confirmed via grep that nothing else reads
  `sales_archive.customer_id`, and that archive tables carry no FK to
  live tables (migration 040's own header), so the anonymising `UPDATE`
  is safe.
- **SHOULD-FIX 3 (fixed) — `held_sales_archive` not consulted by either
  demo-removal predicate.** Reviewer proved (with a probe) that a demo
  item/customer parked in a basket swept into the archive by a reset,
  before ever being tendered, was still deleted — worse than the
  sale_lines/sales_archive gaps, since `held_sales_archive.payload`
  carries no FK at all, so `RestoreResetBatch` would succeed silently and
  the shop owner would only discover the break as a raw
  "FOREIGN KEY constraint failed" when tendering the restored basket.
  Fixed in both `remove_demo.sql` (item/variant) and
  `remove_demo_customers_promos.sql` (customer).
- **SHOULD-FIX 4 (moot after the BLOCKER 2 redesign) — no handler test for
  the new 409.** The 409/`ErrCustomerReferencedByArchive` path that
  needed the test was removed entirely by the BLOCKER 2 fix;
  `internal/pages/data_api.go`'s `POST /api/data/customers/erase` handler
  is now byte-identical to before this card, so nothing new needs a
  handler-level test.
- **NIT 5/6 (deferred, not blocking):** ar/fa/tr translation nits and the
  `ut-plugin-language-{de,es}` pack follow-up — moot, since the locale key
  they applied to no longer exists after the BLOCKER 2 redesign.

**Second-round re-verification (same session, not a fresh subagent —
scoped strictly to the fix, per the "second round earned by a
blocker-class finding, scoped to the fix" rule):** re-ran the full gate
(`go build`, `go vet`, `gofmt -l`, `go test ./... -p 1`, all 6 guard
scripts) clean; TDD-re-verified all four review-driven additions
personally (temporarily reverted each new clause/line in isolation, confirmed
the corresponding test fails with the exact expected assertion — including
catching and fixing a shell-heredoc quoting mishap that had silently
corrupted `remove_demo_customers_promos.sql` mid-revert-check, caught by
diffing against a pre-change backup before trusting the file state — then
restored and re-confirmed green).

## Verification beyond the automated suite

- Full gate run twice (once before the independent review, once after
  applying its fixes): `go build ./...`, `go vet ./...`,
  `gofmt -l internal/data internal/pages internal/db web/locales` (clean
  for every file this diff touches — the 6 files it lists are pre-existing
  drift on `main`, none touched here), `go test ./... -p 1` (all 38
  packages green), and all 6 guard scripts (`guard-data-access`,
  `guard-kiosk-engine`, `guard-plugin-menu-read`, `guard-i18n`,
  `guard-compliance-claims`, `guard-help-topics`).
- TDD claims re-verified personally, not taken on trust, for every new
  behaviour: reverted each production-code hunk in isolation (the original
  `sale_lines_archive`/`stock_movements_archive` clauses, then separately
  each review-driven addition — `held_sales_archive` in both scripts,
  `sales_archive` in the customer predicate, the `EraseCustomer` anonymise
  line), reran the specific new test, confirmed a real assertion failure
  (not a compile error masking the signal) each time, then restored and
  reconfirmed green.
- Migration frozen-fixture correctness verified directly: both
  `testdata/frozen_remove_demo_036.sql` and
  `testdata/frozen_remove_demo_customers_promos_038.sql` were captured via
  `git show` of the pre-change `seeddata/*.sql` content (not hand-typed),
  and `TestMigration036MatchesSeedData`/`TestMigration038MatchesSeedData`
  both pass, confirming each fixture is still byte-for-byte contained in
  its migration's embedded SQL text.
- `web/help/`: the GDPR erase flow has no help topic today (confirmed —
  `grep -rln "erase|GDPR" web/help/en/` returns nothing), so this change
  extends an already-undocumented backend flow rather than altering
  documented steps; `guard-help-topics.sh` passes (no route changed).
  Judged not blocking for this ticket — writing net-new manual coverage
  for the whole Settings → Data panel (reset archives, cleanup, GDPR
  erase — none of which are documented) is real, separate scope; filed as
  a new Backlog card rather than folded into this diff.
- No real client/shop name anywhere in the diff (`Ada Lovelace`/`ada@x.com`
  test fixtures only, matching the existing test file's own convention); no
  secret-shaped literal.
- Two recurring bug classes checked, both N/A: no filesystem writes in the
  diff at all (no `os.MkdirAll`/`os.WriteFile`/cwd-relative-path pattern to
  get wrong).

## Deferred / follow-up (new Backlog cards)

- `held_sales_archive` gap was closed here, but its discovery revealed the
  Settings → Data panel (reset archives / cleanup / GDPR erase) has zero
  `web/help/` coverage at all — worth a dedicated manual-writing card
  rather than scope-creeping this fix.

## Safe-to-merge verdict

Yes, after the blocker/should-fix round above.

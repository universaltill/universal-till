# Review: repair promotions pointing at a missing customer (ut-docs#5590)

Branch `fix/5590-promo-dangling-customer`. Author: Sonnet (Dev subagent).
Reviewer: Opus 5.5 (independent, read-only), one round. Tester: separate Sonnet subagent.

## What shipped
- `internal/db/migrations/097_promotions_dangling_customer.sql` is a new append-only migration. Older tills ran without foreign-key enforcement, so a promotion can name a customer that no longer exists. The migration finds every promotion whose `customer_id` has no `customers` row and sets `is_active = 0` and `customer_id = NULL` in one statement. A NULL `customer_id` means "any customer" (`activePromoWhere`), so NULLing the id alone would widen a one-customer discount to everyone. The query uses `NOT EXISTS`, not `NOT IN`, because `customers.id` is a nullable TEXT primary key. The checksum is pinned in `shipped_migrations_test.go`.
- `internal/data/sync_admin_repo.go`: admin-sync phase 2 applies an incoming promotion whose customer is missing on this till the same way the migration repairs it, inactive and unlinked. Customers are upserted before promotions, so a customer that arrives in the same bundle is not affected.
- Tests:
  - `internal/db/migration_097_promotions_dangling_customer_test.go` covers dangling, valid, global and already-inactive rows, a customers row with a NULL id, the sync generation bump (and no bump on a clean till), idempotence using the real file, `foreign_key_check` and a fresh boot.
  - `internal/data/promo_dangling_sync_5590_test.go`: an old main till sends an orphan to a repaired satellite, which the satellite clamps; a valid customer is not clamped.
  - `internal/data/promo_dangling_customer_5590_test.go` pins checkout behaviour. A repaired promo never resolves. A contrast test shows an active promo with a NULL customer applying to everyone.
- `web/help/en/promotions.md`: step 6 describes the one-time switch-off on update and says to choose the intended customer before activating the code again (since #5574 lets a manager change the customer on edit). The docs-shots manifest hash is refreshed; the screenshot itself is unchanged.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| H1 | High | A satellite that upgrades before its main till repairs the orphan to NULL. Each later admin pull carrying the old `customer_id` then failed the whole apply transaction (`FOREIGN KEY constraint failed (787)`), so no prices, users, tax or settings arrived until the main till upgraded. | Fixed: clamp in sync phase 2, plus regression test. The orchestrator re-verified it: with the fix reverted the test fails with 787, and with the fix restored it passes. |
| M1 | Medium | The help said any code whose customer no longer exists is switched off. Erasing a customer still leaves the code active for everyone (EraseCustomer, ut-docs#5591). | Fixed: sentence scoped to the one-time repair on update. The code fix is deferred to #5591. |
| L1 | Low | The idempotency check ran a copy of the SQL, not the shipped file. | Fixed: it now runs `loadMigrationVersion(t, 97).SQL`. |
| L2 | Low | The data-package checkout test passes without 097. | Accepted. It pins the checkout query and is not counted as red/green evidence. The migration test is the real coverage. |
| L3 | Low | The version number is a literal in several test lines. | Accepted. Renumbering touches these lines and the checksum key (see merge note). |

## Extra verification
- Reviewer: five mutations of the 097 SQL (unlink only, deactivate only, `NOT IN`, no `IS NOT NULL` guard, no-op) each fail the migration test (then numbered 096).
- Tester: five more mutations each fail. A real boot of the built till migrated to the new version. With an orphan promo injected and the migration re-run, the orphan came out NULL and inactive, the valid and global promos were untouched, `/promotions` returned 200 and `/healthz` returned 200. The page was not inspected visually (no UI change).
- Full gate: gofmt, build, vet, golangci-lint, every guard in the `ci.yml` build job, `go test ./... -timeout 30m`.

## Merge note: migration number
The migration was written as 096. `096_cloud_notices` (feat/5366) merged to main first, so before opening the PR this branch was rebased and renumbered to 097: the file, the test literals and the checksum key. `feat/4768-channel-order-actions` also adds a 096 and must renumber the same way when it merges. The checksum value stays the same because comments are not hashed. Never renumber once on main.

Pre-flight: help topic `web/help/en/promotions.md` updated; docs-shots n/a (no screen changed); `reference/` n/a (no documented behaviour); README n/a; en.json/meta n/a (no keys); ADR n/a (follows ADR-0100); checksum pin updated; sync apply updated (H1); EraseCustomer widening deferred → ut-docs#5591; review record this file.

Verdict: approve after fixes.

# Categories admin screen (ut-docs#1898)

**Date:** 2026-09-09
**Branch:** `feat/1898-categories-admin`
**Card:** ut-docs#1898 — "Categories have no management screen — a merchant
cannot create, rename, reorder or delete one."

## What shipped

A merchant can now manage their own categories from the till, without an
import or the API:

- Migration `017_categories_is_active.sql` adds `categories.is_active`.
  `categories` gains `hasIsActive: true` in `sync_admin_repo.go`'s
  `adminTables` (no `unique` entry — `categories.name` carries no DB
  UNIQUE constraint, unlike the six tables that use the retire-mangle
  mechanism). This closes the same gap ut-docs#1610 closed for `brands`:
  admin-sync's FK-blocked retire-in-place can now flag a
  pruned-but-still-referenced category (`is_active = 0`) instead of
  leaving a permanent, unflagged prune failure.
- `CatalogRepo` gains `CreateCategory`, `RenameCategory`,
  `SetCategoryActive` (blocks deactivation while active items remain,
  returning the count rather than cascading silently), `SetCategorySortOrder`
  (transactional), `ListActiveCategories` (is_active = 1, for sale-facing
  surfaces), and `ListCategoriesForAdmin` (one query, item counts via
  `LEFT JOIN` + `COUNT`, no N+1).
- New `internal/pages/categories_page.go`: `GET /categories` (full page,
  `requirePageManager`), `POST /api/categories` (create),
  `POST /api/categories/{id}` (rename), `POST /api/categories/{id}/active`
  (activate/deactivate), `POST /api/categories/reorder` — all four mutation
  routes gated `requireManager` + `requirePrimary` (categories is a synced
  admin table; the reorder route uses the fetch-driven 409 variant,
  `requirePrimaryFetch`, matching `buttons_api.go`'s own reorder gate,
  since a redirect would be silently followed by `fetch` and read back as
  success).
- `internal/ui/buttons.go` (sale-screen tabs) and
  `internal/pages/kitchen_stations_page.go` (routing grid) switched to
  `ListActiveCategories`.
- New `web/ui/pages/categories.html`: flat list, item counts, active/
  inactive badge, move-up/move-down reorder (no drag-and-drop, mirroring
  `buttons_api.go`'s ut-docs#1221 precedent), create/rename forms, all
  strings via `{{ T }}`, logical CSS only.
- 22 new `categories.*` i18n keys in all four shipped locales
  (`en`/`ar`/`fa`/`tr`), real translations. Help topic in all five
  `web/help/` locales (`en`/`ar`/`de`/`fa`/`tr`), `routes: [/categories]`,
  screenshots regenerated (`make docs-shots`).
- `/items`' Categories row now links to `/categories` (was a dead
  "coming soon" placeholder — see Finding F1 below).

## Explicit non-goals (unchanged, confirmed untouched)

Category color editing from the admin UI, nested `parent_id` hierarchy
(flat list only, `parent_id` always NULL on create), hard delete
(deactivate/reactivate only, mirroring `tables_page.go`), duplicate-name
validation (no UNIQUE constraint added), multi-language category names
(ut-docs#1429, tracked separately). The export-dispatch "categories card"
(#654) was grepped and found to be a placeholder string in a test comment
only — no real reader against the `categories` table exists today, so
there was nothing in scope to change.

## Independent review

A fresh-context **Opus** subagent (complexity:medium → Opus review per
`scrum-master`'s model-routing table), isolated in its own git worktree,
reviewed the diff independently of the Sonnet subagent that implemented
it. It read the diff, ran the actual gate itself (not trusting reported
results), and did real TDD re-verification (reverting three separate
production-code fixes one at a time, confirming each dependent test failed
for the right reason, then restoring).

**Findings:**

- **F1 (blocker, fixed):** `/items`' Categories row still had `Href: ""`
  and an explicit "not built by this card" comment — the new screen was
  reachable only by typing the URL directly, failing the card's headline
  requirement. Fixed: wired the link, updated a stale test that had pinned
  the old dead-link behaviour.
- **F2 (blocker, fixed by orchestrator after review):** Adding
  `categories.is_active` made `CatalogRepo.ReadLookup("categories")`
  start filtering inactive rows — and that reader feeds `/catalog`'s
  item-edit Category `<select>`. A `<select>` reset to unselected submits
  nothing, so the next save of an item still pointing at a
  since-deactivated category would silently null its `category_id`. Two
  paths reach this: (a) admin-sync's FK-blocked retire, which this same
  card enabled and which bypasses `SetCategoryActive`'s item-count guard
  entirely (proven via the card's own new sync-retire test fixture: an
  *active* local item referencing a category the primary just deleted);
  (b) a manually-deactivated category (correctly guarded to zero active
  items at that moment) later gaining a reference back if one of its
  inactive items is reactivated. Fixed by adding `categories` to
  `lookupUnfilteredByActive` (`catalog_repo.go`) — the exact, already-
  reviewed precedent `brands` (ut-docs#1610) established for the identical
  failure shape, and the same reason `ListAllTaxCodes` exists for tax
  codes. No template change needed (categories never had an "(inactive)"
  label on this `<select>`, and still doesn't — same as brands today,
  tracked as a possible follow-up, not a data-loss risk). Updated the two
  false/stale doc comments this left behind, corrected the help topic's
  step 5 across all five locales (it had claimed deactivating a category
  removes it from the item editor, which is no longer — and, per this
  finding, was never safely true), and added a regression test
  (`ReadLookup("categories")` still returns a retired-but-referenced row)
  plus fixed two pre-existing tests that had encoded the pre-fix (buggy)
  expectation. TDD-verified: reverted the `lookupUnfilteredByActive`
  entry, confirmed `TestAdminApply_CategoryRetiredInPlace` failed with the
  exact predicted symptom (`ReadLookup` dropping the retired-but-
  referenced row), restored, confirmed it passes again.
- **F3 (false-pass test, fixed):** `TestSetCategorySortOrder` and
  `TestCategoriesPageReorder` both used a fixture (Drinks/Snacks/Bakery)
  whose target order happened to equal alphabetical order — since both
  reads order by `sort_order, name`, a reorder implementation that wrote
  the same `sort_order` to every row (i.e. did nothing) still passed both
  tests via the name tie-break. Reviewer proved this by mutation. Fixed by
  choosing a non-alphabetical target order and asserting the literal
  `sort_order` integers, not just the resulting read order.

**Also reviewed and passed:** deactivate-blocked item counting (correct
predicate, no fan-out/N+1 in the count query), reorder transactionality
(real `BeginTx`/`Rollback`/`Commit`), the no-`unique` retire-in-place code
path, RTL/i18n quality (genuine ar/fa/tr translations, logical CSS only),
the manual (real prose, not a stub, present in all five locales),
migration correctness (additive, non-destructive, correct next-available
number), no real client/shop names or secret-shaped literals anywhere in
the diff, permission gating (`requirePageManager`/`requireManager`,
`UT_AUTH=off` is the only — dev/CI-only — bypass), `requirePrimary` on
every mutation route including the reorder route's 409 variant,
offline-first (no network dependency introduced anywhere on the checkout
path), and the two recurring bug classes this pipeline watches for
(missing `os.MkdirAll`, cwd-relative paths instead of `paths.Data`) — N/A,
this feature writes no files.

**Flagged, not fixed (follow-up candidates, not blockers):**
- No UNIQUE constraint on `categories.name` — two categories can share a
  name. Harmless (confusing at worst), matches today's pre-card behaviour
  exactly; a real fix is a separate, larger data-integrity question given
  potentially-existing duplicate data.
- No unknown-ID rejection in the reorder handler — an unknown id is
  silently a 0-row `UPDATE`. Not exploitable (route is manager+primary
  gated, the client always posts the full list); cheap hardening for a
  future pass.
- Lang-pack drift: the 22 new `en.json` keys need follow-up PRs in
  `ut-plugin-language-{de,es}` — advisory-only on this PR, **blocking on
  push to `main`**. Filed as a `blocked:env` follow-up rather than
  guessing a translation past the self-hosted-AI-only ADR (unreachable
  from this cloud sandbox) or merging ahead of the pack sync.

## Verified beyond automated tests

Full `go test ./...` (zero failures), `go vet ./...`, `gofmt -l .` (clean),
`golangci-lint run ./...` (0 issues), and every relevant guard —
`guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
`guard-page-http-error.sh`, `guard-i18n.sh`, `guard-compliance-claims.sh`,
`guard-help-topics.sh`, `guard-docs-shots.sh` — all green, run twice (once
before the F1/F2/F3 fixes landed, once after). `make docs-shots` actually
executed (real headless Chromium), all 108 screenshot specs passing,
including the new `categories` topic across all four shipped UI locales.
Three separate TDD claims independently re-verified via revert-confirm-
restore (F2's fix, plus the reviewer's own three mutation tests against
the deactivate guard, the sync-retire flag, and the reorder logic).

## Safe-to-merge verdict

**Yes**, with F1/F2/F3 applied (all three are in this branch). Follow-up
items above are tracked, not blocking.

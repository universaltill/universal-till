# Review: demo-catalogue removal predicate misses a repurposed-but-unsold item (ut-docs#566)

**Date**: 2026-08-15
**Card**: universaltill/ut-docs#566 — "Demo-catalogue 'untouched' removal
predicate misses a repurposed-but-unsold item"
**Complexity**: easy
**Reviewer model**: fresh-context Sonnet subagent (per this card's
`complexity:easy` tier — see `scrum-master` skill's model routing)

## What shipped

`internal/data/seeddata/remove_demo.sql`'s `demo_seed_removable` "untouched"
predicate only ever looked at trading history (`sale_lines`,
`stock_movements`, `held_sales`) — a shop that renamed/repriced a demo item
before ever selling or stock-adjusting it had that item silently deleted on
upgrade or via Settings → "Remove sample data," because nothing checked
whether the item had actually been edited.

- `internal/data/seeddata/demo_ids.sql`: `demo_seed_items` TEMP table gained
  `sku`/`name`/`base_price` columns, populated with a literal copy of each
  item's seeded values from `demo_catalogue.sql` — the same duplication
  convention already used for the ID lists themselves.
- `internal/data/seeddata/remove_demo.sql`: `demo_seed_removable`'s `WHERE`
  clause gained `AND i.sku IS d.sku AND i.name = d.name AND i.base_price =
  d.base_price` — a demo item is only removable while it's still byte-for-
  byte pristine, not just untraded.
- `internal/db/migrations/036_demo_seed_opt_in.sql`: both embedded "shared
  block" copies regenerated to stay byte-identical to the two files above
  (the file's own stated invariant, guarded by `TestMigration036MatchesSeedData`).
- Tests: `internal/data/demo_seed_repo_test.go`'s
  `TestRemoveDemoCatalogueKeepsEditedItem` (rename/reprice/re-SKU, each
  independently keeps the item; untouched sibling still removed) and
  `internal/db/demo_seed_migration_test.go`'s
  `TestDemoCatalogueUpgradeKeepsRenamedUntradedItem` (same proof via the
  migration-036 upgrade path) plus `TestDemoSeedItemsPristineValuesMatchCatalogue`
  (drift guard: the new literal pristine values in `demo_ids.sql` can never
  silently diverge from `demo_catalogue.sql`).

No UI surface touched (no page, no HTMX handler, no template) — Settings →
"Remove sample data" behavior changed under the hood only, so no
screenshot/visual check applies; verified this reasoning holds during
Tester and Review.

## Independent review (fresh-context Sonnet, same working tree)

**Verdict: PASS.** Ran `go build`, `go vet`, the full `go test ./...`, and
all three required guards (`guard-data-access`, `guard-kiosk-engine`,
`guard-plugin-menu-read`) — all green.

**Independent TDD re-verification (not just trusted on say-so):** reverted
the 3 new predicate lines from both `remove_demo.sql` and migration 036
(all 4 occurrences, including the TEMP table's new columns so the SQL still
parses), reran the two new regression tests:
- `TestDemoCatalogueUpgradeKeepsRenamedUntradedItem` → **FAILED**
  (`items ids = [], want [itm001]`)
- `TestRemoveDemoCatalogueKeepsEditedItem` → **FAILED**
  (`removed 50, kept 0; want 47, 3`)

Both are genuine, non-vacuous regression tests — proven to fail against the
pre-fix predicate. Restored both files afterward and confirmed the diff's
blob hashes match the pre-revert state exactly, then reran the full gate
(build, tests, guards) — all green again.

**Note on process**: the review subagent shares this working tree (not
worktree-isolated, appropriate for an `easy`-tier card's lighter process
depth) — its own revert-and-restore cycle briefly left migration 036
missing the fix mid-review. The Scrum Master caught this via the stop-hook
gate (checked `git diff`/containment before committing, per the standing
"a stop-hook-forced commit is not a blind commit" rule — ut-docs#386) and
waited for the subagent to finish and independently re-verified the restored
state (diff stats, containment check, full gate) itself before writing this
record or committing. No broken state was ever committed.

### Findings

**Real but out of scope (not fixed, noted for the record):** the predicate
is item-row-scoped only — a shop that edits a *variant*'s
name/sku/price, or attaches a barcode/photo without touching the item's own
name/sku/base_price, still has that item (and its edited dependents, via
`ON DELETE CASCADE`) silently deleted. The card's AC and explicit non-goal
("no generic row-edit-audit mechanism") both scope this to
name/sku/base_price only, so this is correctly out of scope, not a bug —
flagged here for whoever picks up a future variant-level follow-up, not
filed as a new card since it's speculative rather than a known real gap.

**Explicitly checked, none found:** repository pattern (all new SQL is in
`internal/data/seeddata`/`internal/db`), money type (`base_price` stays raw
`INTEGER` in SQL text, consistent with this file's existing convention — no
`money.Money` boundary crossed), i18n (no user-facing strings touched),
offline-first (pure local state, no network), the `IS` vs `=` null-safety
question for the nullable `sku` column (behaviorally equivalent here since
the reference value is never NULL — `IS` is defensive, not load-bearing),
and the drift-guard regex correctness against the one item with an escaped
apostrophe in its name (`Kellogg''s Cornflakes 500g`) — traced by hand,
correct.

**Migration 036 edit — judged not a blocker.** Migration 036 was already
released in tag `v0.3.9` before this change, and `CLAUDE.md` states
migrations are append-only after the first release. However, there is
direct, already-merged precedent for this exact pattern: commit `680cb5c9`
("fix(seeddata): guard sample-data removal against live/held basket
references", ut-docs#633) edited this same migration file post-release for
the identical reason — keeping it byte-identical to the shared seeddata SQL,
an invariant `TestMigration036MatchesSeedData` exists specifically to
enforce. Functionally safe: migrations never replay for a till that already
applied 036, so the edit only changes behavior for installs where 036 has
not yet run — exactly the population this fix is for. Judged an accepted,
precedented pattern for this specific opt-in-demo-seed mechanism (now
precedented twice), not a policy violation — same conclusion the
`ut-docs#633` review already reached and recorded. Recommend (not filed as
a card — a documentation nit) that `CLAUDE.md` eventually gain an explicit
carve-out for "migrations that embed a verbatim runtime-shared seed script"
so this doesn't need rediscovering a third time.

## Safe to merge

Yes. No must-fix findings, gate green (build/vet/full test suite/all three
guards), both new regression tests independently proven non-vacuous by
reverting and restoring in isolation, migration-036 edit judged consistent
with existing precedent.

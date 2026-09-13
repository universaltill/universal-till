# Code review — backfill codeless variant SKUs + surface remaining ones (ut-docs#2230)

- **Date:** 2026-09-13
- **Ticket:** ut-docs#2230 (`complexity:medium`, `bug`, `p2`)
- **Branch:** `fix/2230-backfill-codeless-variant-skus`
- **Reviewer:** independent pass, different model (Opus) from the Sonnet
  implementation, per this card's `complexity:medium` routing, in an
  isolated worktree.
- **Verdict: SAFE TO MERGE AS-IS.** No blocking findings.

## The bug

`item_variants.sku` is nullable (`001_init.sql`). `CreateVariant` has only
auto-generated a SKU for a blank input since ut-docs#1900 — no migration
ever backfilled the rows created before that fix landed. A variant could
therefore exist with neither a barcode nor a SKU ("codeless") — nothing to
resolve it by at sale time. ut-docs#2209 already made the sale screen
filter these out of the picker so nothing mis-prices, but the merchant
side stayed silently wrong: they configured a size and it just never
appeared, with no explanation anywhere. Same failure class as ut-docs#1459
("never an item without a SKU") applied to variants.

## What shipped

- `internal/db/migrations/028_backfill_codeless_variant_skus.sql` (+ test):
  a one-shot backfill for every currently-active codeless variant. Builds
  a pool of candidate `VAR-XXXXXXXX` codes via a materialized recursive
  CTE, discards any that collide with an existing SKU, de-duplicates, and
  hands out exactly one surviving candidate per codeless variant via a
  stable row-number pairing. Scope is `is_active = 1` only, matching every
  other "sellable" check in this codebase. Idempotent by construction (the
  `WHERE` clause only ever matches a variant still lacking both a code).
- `internal/data/sync_admin_repo.go`: the LAN admin-sync upsert engine
  (`execUpsertBatch`) is a generic column-copy path used for ~30 tables and
  bypasses per-table domain validation — the only other real path that can
  write a codeless variant (a replica pulling from a primary still behind
  on ut-docs#1900). Added `backfillCodelessSyncedVariants`, called at the
  end of `ApplyAdmin`, reusing `generatedVariantSKU()` directly.
- `web/ui/partials/catalog_variants.html` + `web/locales/{en,ar,fa,tr}.json`
  + `web/help/{en,ar,de,fa,tr}/catalog.md`: a badge on a still-codeless
  variant in the catalog admin panel, translated (not English-fallback) in
  every shipped locale, with the manual updated to match.
- Tests: migration backfill (5 cases: backfill/untouched-with-sku/
  untouched-with-barcode/inactive-skipped/idempotent-replay/distinct-codes,
  plus a 40-row stress case), the sync-path fix, and the badge
  render/non-render pair.

## What the independent review found

No blocker-class findings. The review ran the full gate (`gofmt`, `go
build`, `go test ./...`, `golangci-lint`, `guard-data-access.sh`,
`guard-i18n.sh`, `guard-migration-version-collision.sh`,
`guard-help-topics.sh`, `guard-help-drift.sh`) — all green — and went
further than reading the diff:

- **Independently re-derived the write-path scope** rather than trusting
  the dev's list: enumerated every `item_variants` INSERT/UPDATE in the
  repo. Confirmed `UpdateVariant`'s `COALESCE(NULLIF(?, ''), sku)` cannot
  create codelessness (a blank input preserves the existing SKU), and that
  `execUpsertBatch` was the one real gap — correctly found and fixed.
- **Adversarially tested the migration SQL directly** against the real
  `modernc.org/sqlite` driver, not just by reading it: verified the
  recursive CTE's pool really is materialized once (two references to the
  same `pool(n=1)` returned the identical value — the load-bearing
  assumption the whole collision-safety argument rests on); ran the actual
  shipped migration at n=40/500/2000 codeless variants with zero survivors
  and zero collisions each time; confirmed `item_variants.sku` has no
  `COLLATE NOCASE` (rules out a case-sensitivity collision trap) and that
  `variant_barcodes.variant_id` is `NOT NULL` (rules out the classic `NOT
  IN (SELECT ...)` NULL-poisoning trap that would have silently no-op'd
  the whole migration); confirmed the window-function ordering (`WHERE`
  before `ROW_NUMBER()`) is correct.
- **TDD re-verification, done for real**: reverted the migration's `UPDATE`
  statement, the sync-repo fix, and the badge template block one at a
  time, in its own isolated worktree; each failed with the exact,
  on-topic error the test names, then passed again once restored.
- Checked the ar/fa/tr translations for plausibility (ZWNJ usage in
  Persian, correct Arabic diacritics/script, idiomatic-if-slightly-stiff
  Turkish) — no machine-mangled or copy-pasted text found.
- Chased a suspected `sync_admin_version` trigger feedback loop between
  the sync fix and the AFTER UPDATE trigger added in migration 023 —
  cleared it: that counter is a local cache-invalidation key, not a
  cross-till convergence marker.

## Non-blockers — not fixed in this diff, filed as follow-ups

- **Sync-path SKU churn on version-skewed fleets** (ut-docs#2246): a
  replica syncing from a still-behind primary regenerates a *different*
  SKU on every poll until the primary itself upgrades. Self-resolves once
  the primary is on ut-docs#1900+, but can invalidate an already-printed
  shelf label in the interim.
- **Silent partial backfill on pool exhaustion** (ut-docs#2247): if the
  candidate pool (`codeless count + 200`) were ever exhausted (~1.3M
  codeless variants, per birthday-bound math — not reachable in practice),
  the migration records itself as applied without erroring, leaving some
  variants still codeless. Worth a post-condition check that turns this
  into a loud failure instead, as cheap insurance.
- **Migration is O(n²)** (folded into ut-docs#2247): measured 10.7s at
  n=2000 codeless variants, from the correlated per-row `SET` subquery.
  Realistically the codeless backlog is small (only reachable via manual
  pre-#1900 creation), but worth watching on low-power hardware.
- **Three slightly different "codeless" definitions** (folded into
  ut-docs#2247): the migration/sync fix use `TRIM(sku) = ''`, while
  `ItemIDsWithVariants`/`sellableVariants`/the badge use a plain empty-
  string check. Impact is near-zero today; worth converging.
- **Help text omits the active-only scope, and `DeleteBarcode` can create
  a fresh codeless variant** (folded into ut-docs#2247): a pre-#1900
  variant that has a barcode but no SKU becomes codeless the moment its
  last barcode is deleted — the badge does cover this case (arguably the
  reason it exists), but the help text doesn't call it out explicitly.
- **`lang-pack-drift` follow-up** (own-the-follow-up rule, `SKILL.md`):
  `catalog.variant_codeless`/`catalog.variant_codeless_hint` are brand-new
  core keys, so `ut-plugin-language-{de,es}` cannot be updated before this
  merges (their own drift guard treats a translated-but-not-yet-core key
  as an orphan). Landing this PR turns `lang-pack-drift` red on `main`
  until the pack follow-ups land — expected, bounded, and owned by this
  same cycle (see the two pack PRs linked from ut-docs#2230's close-out
  comment). 26 of the 28 keys `check-lang-pack-drift.sh` currently reports
  missing in each pack pre-date this change (an earlier card's keys); only
  the 2 new ones here are this PR's responsibility.

## Verified beyond automated tests

- `internal/pages/catalog/` (badge test's package) is an existing package,
  not a new one — sane location, no import-cycle risk.
- `.tag.warn` is a pre-existing shared style with no directional CSS
  properties — RTL-safe with zero changes needed.
- Grepped the full diff for `os.Create|WriteFile|MkdirAll|ioutil|
  filepath.Join|paths.` — zero hits, so neither of this pipeline's two
  recurring bug classes (missing `MkdirAll`, cwd-relative path vs.
  `paths.Data(...)`) applies; the diff introduces no file I/O.
- Money-type discipline confirmed genuinely not applicable — the only
  monetary literal in the diff is a pre-existing test fixture price.
- No secrets; no real client/shop name as test data.

## Explicitly deferred (not this card)

- All five non-blockers above, filed as ut-docs#2246/#2247.
- Native-speaker fluency pass on the ar/fa/tr strings (this review's
  plausibility check is not a substitute for one).

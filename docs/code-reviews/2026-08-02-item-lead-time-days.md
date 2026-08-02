# Code review: per-item reorder lead time drives inventory warn/reorder thresholds

**Date:** 2026-08-02
**Scope:** `universaltill/ut-docs#85` — replaces the inventory page's flat
7-day-warn/14-day-cover thresholds with per-item values derived from a new
`lead_time_days` catalog field.

## What shipped

- New append-only migration `internal/db/migrations/028_item_lead_time.sql`:
  `items.lead_time_days INTEGER NOT NULL DEFAULT 0` (0 = unset).
- `CatalogRepo.ItemLeadTimeDays`/`SetItemLeadTimeDays` (mirrors the existing
  `ItemCostPrice`/`SetItemCostPrice` shape exactly).
- `data.LowStockItem.EffectiveWarnDays()`: the days-left threshold below
  which an item counts as running out — its own lead time once set,
  otherwise the flat 7-day default. Added as a **method on the struct that
  already carries the field**, specifically so the three call sites that
  need this threshold can't drift out of sync with each other again (see
  "What the independent review found" below — they already had).
- `internal/pages/inventory_page.go`'s `stockLevelsForDisplay`: warn
  window and reorder-suggestion target (`effectiveWarnDays + 7`-day
  buffer) now call `EffectiveWarnDays()` instead of a flat constant.
  Byte-for-byte identical to the old 7/14 defaults when `lead_time_days`
  is unset — regression-tested, and independently re-verified (see below).
- `internal/alerts/alerts.go`'s `runningOutCount` (the daily low-stock
  digest pushed to the marketplace) and `internal/pages/reports_page.go`'s
  reports-header low-stock chip both now call `EffectiveWarnDays()` too —
  both previously duplicated the flat `<=7` check independently.
- New `POST /api/catalog/item-lead-time` handler + UI field in
  `web/ui/partials/catalog_variants.html`, structurally identical to the
  existing cost-price field (plain integer, no currency conversion needed).
  New i18n keys (`catalog.lead_time_days`, `catalog.lead_time_days_hint`)
  in all four locales.
- Two pre-existing `internal/db` upgrade-simulation tests
  (`barcode_seed_test.go`, `dead_seed_test.go`) updated to also
  `DROP COLUMN lead_time_days` when rewinding schema state, matching their
  own established pattern for every append-only migration since 024.

## What the independent review found

A different-model (Opus) subagent reviewed the diff independently, ran the
build/vet/test/guard gate itself, and reproduced every TDD claim by hand
rather than trusting them.

**Real, blocking finding (fixed):** `internal/pages/reports_page.go`'s
reports-header low-stock chip had a **third** independent copy of the flat
`<= 7` check — missed by the first fix pass, which only updated
`inventory_page.go` and `alerts.go`. The reviewer reproduced the
divergence with a throwaway test before reporting it: with an item at
`DaysLeft=8` and `lead_time_days=10`, `/inventory` warned (1 item) while
the `/reports` chip — which links straight to `/inventory` — showed
nothing. Fixed by routing all three sites through the new
`LowStockItem.EffectiveWarnDays()` method rather than patching a third
copy of the same duplicated constant. A new regression test,
`TestReportsPage_LowStockChipMatchesInventoryPageLeadTime`, reproduces the
reviewer's exact fixture and is confirmed to fail against the pre-fix code
(revert-and-rerun, see Verification below) and pass after.

The reviewer also correctly argued that duplicating the threshold logic
(the first fix pass's approach — copy the same `if l.LeadTimeDays > 0 {...}`
into `alerts.go`) was the wrong shape given it had already let one of three
sites drift: the shared `EffectiveWarnDays()` method makes that class of
drift structurally impossible instead of comment-enforced. Adopted as
written above.

**Non-blocking, deliberately deferred (each filed as a new Backlog card,
not folded into this change):**
- `alerts.go`/`reports_page.go` compare the raw float
  (`qty/rate <= float64(warn)`), while `inventory_page.go` floors first
  (`int(qty/rate) <= warn`) — a pre-existing inconsistency between the
  three sites (confirmed pre-existing by the reviewer, not introduced by
  this change) that can disagree at an exact boundary like `qty/rate=7.5`.
  Worth unifying now that all three share `EffectiveWarnDays()`, but out
  of scope for this card.
- No upper bound on `lead_time_days` (same gap the existing cost-price
  field already has — not a regression this change introduces, but worth
  fixing for both fields together).
- `internal/pages/ask_api.go`'s `stock_levels` AI tool returns
  `[]LowStockItem` with no JSON tags, so `LeadTimeDays` (like every other
  field on that struct) ships to the LLM as PascalCase against this
  repo's snake_case convention — pre-existing for the whole struct, this
  change just adds one more field to it.
- Comment said "Drop all four" in both modified `internal/db` upgrade
  tests; now five drops follow it — fixed as a trivial part of this
  change since the reviewer caught it.

**Confirmed clean:** raw-SQL placement (guard-data-access.sh, plus a
manual read of the diff), i18n key parity across all four locales
(guard-i18n.sh, plus the reviewer independently parsed all four files:
1045 keys each, zero drift), no file-writes/paths concern (none added —
DB-only feature), server-side rejection of a negative lead time (not just
the UI's `min="0"`), migration style matches its neighbors and backfills
existing rows correctly, no real client/shop name or secret-shaped
literal anywhere in the diff, and the two hand-rolled test schemas
(`internal/testsupport/sqlite_catalog.go`, `ui_smoke_test.go`) match the
real migration's column definition exactly (no drift risk).

## Verification (self, before independent review)

- `go build ./...`, `go vet ./...`: clean.
- `go test ./...`: green except one pre-existing, unrelated failure
  (`TestSaveCleansUpDirectoryOnWriteFailure`, `internal/issuereport`) —
  confirmed via `git stash -u` bisection against unmodified `origin/main`:
  fails identically with or without this diff.
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh`:
  both green.
- Real live-server round trip: booted the actual till binary (auth off,
  demo catalog seeded, same approach as `e2e/run-till.sh`), `POST`ed to
  the real `/api/catalog/item-lead-time` endpoint against the seeded
  `itm001`, confirmed the value persisted and rendered back through a
  fresh `GET` of `/api/catalog/item-variants`.
- `inventory_page.go`'s core logic: temporarily reverted to the old flat
  constants, confirmed `TestInventoryLeadTimeAwareWarnAndReorder` fails
  with the expected message, restored, confirmed it passes again.
- `alerts.go`'s digest fix: temporarily reverted the flat `<=7` back,
  confirmed `TestRunningOutCount_LeadTimeAware` fails, restored, confirmed
  it passes again.

## Independent review's own verification (Opus subagent, reproduced by hand)

- Pre-existing failure: `git stash -u` → ran the `issuereport` test on a
  clean tree → identical failure (file/line/message) → `git stash pop`.
- `alerts.go` regression test: hand-reverted the fix, confirmed a real
  assertion failure (not a compile error), restored, confirmed pass.
- `inventory_page.go`: reverted to flat constants, confirmed failure; then
  went further and reverted *only* the cover-days half (keeping warn-days
  lead-time-aware) to prove both halves of the fix are independently
  load-bearing — each half failed on its own distinct assertion when
  reverted alone.
- Regression safety at `lead_time_days=0`: ran a differential harness
  comparing the old and new formulas over 374 combinations (boundary
  values, zero/negative quantities and rates, NaN, +Inf, negative lead
  times) — zero differences. Byte-for-byte no-op for existing installs,
  verified exhaustively rather than by inspection alone.
- Confirmed the two `internal/db` upgrade-test drops are genuinely
  necessary by deleting them and re-running: both tests then fail with
  "duplicate column name: lead_time_days", proving the drops aren't
  copy-paste-wrong.
- Reproduced the reports-page divergence (see above) with a throwaway
  test before it was reported, then re-verified after the fix.

## Verdict

**Safe to merge.** One real, user-visible bug was found and fixed during
review (the reports-header chip); the threshold logic was reshaped into a
single shared method as a direct result of that finding, closing off the
duplication pattern that let it happen. Remaining nitpicks are genuinely
out of scope and filed as new Backlog cards rather than silently dropped.

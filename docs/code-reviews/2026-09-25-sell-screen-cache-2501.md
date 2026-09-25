# Review: sell-screen tile cache (ut-docs#2501)

- **Branch:** `perf/2501-sell-screen-cache`
- **Card:** universaltill/ut-docs#2501 (`complexity:hard`, lane:cloud-24)
- **Author:** Opus 5.5 (Dev subagent). **Reviewer:** Fable (independent, different model).

## What shipped

The cashier sell screen's tile fragment (`GET /ui/buttons` outside edit
mode, `GET /ui/buttons/category`) is served from a bounded in-memory cache.
Before this change every fetch re-read the whole catalog.

- **Key:** route and params, locale, currency, `Granted`, browsing mode and
  translations version, plus a data version.
- **Data version:** `sync_admin_version.generation` (triggers on every admin
  table) plus a new migration-042 counter, `sell_screen_version`. Triggers on
  `price_history` and `item_images` bump the new counter, because neither
  table is covered by the admin counter.
- **Version check:** both counters are read in one query on every request,
  so a write is visible on the very next fetch.
- **Expiry:** an entry expires at the next scheduled price boundary. That
  boundary is read before rendering, so one that passes mid-render makes
  `Put` refuse the entry. Entries also expire at a 5-minute max age, which
  covers image files that arrive on disk with no database write.
- **Memory:** LRU with a 4 MiB byte budget and an entry cap. Nothing is
  stored, and the cache is purged, while Linux `MemAvailable` is below
  64 MiB.
- **Never cached:** edit mode, search, `/all/more`, non-200 responses, a
  render with a top-level load error, a render whose inner lookup fell back
  (degraded), a template error, and any request where a counter row is
  missing or unreadable.
- **Docs:** `docs/performance.md` has a new "Sell-screen tile cache" section.
  No user-visible change and no new strings, so there is no help-manual or
  locale change.

Benchmark with 500 items and 20 categories: `/ui/buttons` went from
~12.1 ms, 26.4k allocs to ~0.75 ms, 8.6k allocs. The rest of the cost on a
hit is the per-request template clone (follow-up ut-docs#2670).

## Findings (Fable round 1)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | Inner lookups in `LoadAllActive`/`LoadWith` only log on failure, so a degraded render (for example hidden flags failing open, or missing prices) was cached for up to 5 min. | **Fixed.** Internal `loadAllActive`/`loadWith` return a `degraded` flag, which is folded into `clean`. Public wrappers are unchanged. Tests: `TestSellScreenCache_DegradedLookupRenderNotCached`, `TestSellScreenStore_LoadWithReportsDegradedLookup`. |
| 2 | should-fix | The price boundary was read after rendering, so a boundary passing mid-render left the pre-boundary price cached for the full max age. | **Fixed.** The boundary is now read before rendering. Test: `TestSellScreenCache_PriceBoundaryPassingMidRenderNotCached`. |
| 3 | nit | The "never cached" list in the docs was inaccurate, and the integration fixture read the host's `/proc/meminfo`. | **Fixed.** Docs corrected; the fixture injects `memAvailable: nil`. |
| 4 | nit | `TranslationsVersion` used the translator pointer (`%p`). | **Fixed.** It now uses a monotonic counter. Test: `TestTranslationsVersion_MonotonicAcrossInitI18n`. |
| 5 | nit | There was no review record. | This file. |

None of the findings was blocker-class (money/tax, data loss, security), so
per MODEL-ROUTING there was no second review round. The orchestrator checked
the fix diff itself. Each new test was seen failing before its fix, with the
messages recorded in the Dev report.

## Verified beyond unit tests

- **Reviewer's own TDD re-verification**, in its own worktree:
  - Forcing the sell counter to 0 fails the price and thumbnail tests.
  - Dropping `Granted` from the key fails the granted/not-granted test.
  - Disabling boundary expiry fails both boundary tests.
  - A probe that drops `item_barcodes` confirmed finding 1 before the fix.
- **Dev's mutation run:** 11 deliberate breakages, each caught by its test.
- **Real-mux handler tests:** a deactivation through the catalog's own route
  makes the item disappear on the next fetch, and the cache is keyed on
  locale.
- **Playwright:** 31/31 sell-screen and designer specs passed (sale,
  sale-screen-213, designer-wysiwyg-2174, designer-search,
  browsing-mode-2499, category-image-2500, strip-overflow-2307,
  stale-tile-1433, narrow-category-list).
- **Screenshots:** none reviewed. No visual change is intended, and the e2e
  geometry specs passed.
- **Gate:**
  - `gofmt`, build, vet and `golangci-lint` (0 issues) pass.
  - `go test -race` passes for all packages. `internal/pages` and
    `internal/db` need a long timeout under race, which CI already sets.
  - The data-access, i18n and other build-job guards pass.
  - `shellcheck` is not installed in the container. No shell files were
    touched.

## Merge with main

`origin/main` gained ut-docs#2613, which drops the strip's All tab and
removes `ButtonsHTTP.HideAllTab`, and ut-docs#2614, which adds the
Designer's show-all-hidden button. Merging them:

- The `renderList` conflict was resolved by keeping main's comment and the
  degraded-aware `loadAllActive`.
- `HideAllTab` was dropped from the cache key.
- `guard-deadcode-baseline.sh` then flagged `SellScreenCache.Len`/`Bytes`,
  which only tests call, so they moved to `sellscreen_cache_helpers_test.go`.
- #2614's unhide-all writes `items.sell_screen_hidden`, which the admin
  triggers already cover.

Re-run on the merged tree:

- `go test -race ./...` passed. `internal/ui` was re-run on its own after
  the helper move, because the full run compiled it mid-edit.
- `golangci-lint` reported 0 issues.
- The data-access, i18n, kiosk-engine, help, migration-collision,
  price-history-sync, deadcode and naming/compliance guards passed.
- The browser suites were re-run only by the PR's CI e2e and playwright
  jobs, not locally.

## Accepted / deferred

- An image file that lands on disk with no database write can take up to
  the 5-minute max age to show. This is accepted.
- The template clone on a hit and caching of `/all/more` are deferred to
  ut-docs#2670.
- Before/after timing on the Pi and the tablet is deferred to ut-docs#2669
  (`blocked:env`).

**Verdict:** safe to merge.

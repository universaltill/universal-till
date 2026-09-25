# Performance & Resilience Quickstart (008)

Target hardware: Raspberry Pi 4 (8GB) or equivalent mini PC. Adjust thresholds via env vars if your runner differs, but keep defaults for baseline checks.

## Thresholds (defaults)
- Sale completion: warn `4000ms`, fail `5000ms` (`UT_BENCHMARK_SALE_WARN_MS`, `UT_BENCHMARK_SALE_FAIL_MS`, legacy `UT_BENCHMARK_THRESHOLD_MS`).
- Micro interactions (lookup/cart add): warn `150ms`, fail `200ms` (`UT_BENCHMARK_INTERACT_WARN_MS`, `UT_BENCHMARK_INTERACT_FAIL_MS`).

## Sale benchmark (CI-backed)
```bash
# Runs in normal test suite; fails if average > fail threshold, logs warning if > warn
go test ./internal/pos -run TestSalePerformanceThresholds

# Optional benchmark mode for deeper sampling
go test -bench=BenchmarkCompleteSale -benchtime=10x ./internal/pos
# Override threshold
UT_BENCHMARK_SALE_FAIL_MS=4000 go test -bench=BenchmarkCompleteSale ./internal/pos
```

## Micro-interaction benchmark
```bash
go test ./internal/pos -run TestMicroInteractionLatency
# Override micro thresholds if needed
UT_BENCHMARK_INTERACT_FAIL_MS=250 go test ./internal/pos -run TestMicroInteractionLatency
```

## Sell-screen tile cache (ut-docs#2501)
The cashier sell screen's tile fragments — `GET /ui/buttons` (outside the
Designer's `?mode=edit`) and the category popup `GET /ui/buttons/category?id=`
— are served from an in-memory cache of the rendered bytes
(`internal/ui/sellscreen_cache.go`, one cache per `registerButtonsAPI` mux).
A hit costs one single-row SELECT instead of the whole catalog read + template
execution. Nothing else on any page has to know about it: every change is
picked up through the database's own counters.

- **Key**: route + its parameter (category id), request locale, the active
  currency code, `httpx.TranslationsVersion()` (a counter bumped on every
  translator swap + how often language-pack overlays / shop translation
  overrides were replaced),
  the session's `catalog_management` grant (lock badges), the browsing mode
  and the All-tab toggle.
- **Invalidation** — an entry is served only while all of these hold:
  1. `sync_admin_version.generation` (migration 023: triggers on every admin
     table — items, categories, shortcut_buttons, settings, modifiers,
     variants, barcodes, translation overrides, …) **and**
     `sell_screen_version.generation` (migration 042: triggers on
     `price_history` and `item_images`, which 023 does not cover) are
     unchanged. Both are read in one query (`data.SellScreenRepo.
     SellScreenVersion`) *before* rendering, so a write racing a render
     leaves the entry on the older version (one extra miss), never stale.
  2. The next `price_history` `starts_at`/`ends_at` (`NextPriceBoundary`,
     read *before* rendering, right after a cache miss) has not passed — a
     scheduled price goes live with no write.
  3. The entry is younger than 5 minutes — the safety net for inputs outside
     the database (an uploaded category/item image file arriving on disk).
- **Never cached** (still rendered and served, just not stored): edit mode,
  search, `/ui/buttons/all/more`, non-200 responses, a render where any
  catalog load or the template failed, a *degraded* render — any inner
  lookup of `LoadAllActive`/`LoadWith` that falls back instead of failing
  (hidden flags, barcodes, thumbnails, modifiers, variants, current prices,
  enabled barcode symbologies) — any request whose next-price-boundary read
  failed or whose boundary passed while it rendered, and any request where
  either counter row is missing or unreadable.
- **Memory bounds**: 4 MiB byte budget and 64 entries, LRU-evicted; an entry
  larger than the budget is never stored; on Linux, when `MemAvailable`
  (`/proc/meminfo`, re-read at most every 5 s) is below 64 MiB nothing is
  stored and the cache is emptied. Elsewhere the budget alone bounds it.
- A new sell-screen input must be covered by one of the two counters (a
  trigger in a new migration), by the key, or by the max age — otherwise
  tiles go stale. Benchmark:
  `go test ./internal/ui -run '^$' -bench 'BenchmarkButtonsList_'`.

## Offline smoke (sale flow)
```bash
go run ./scripts/smoke-offline-sale/main.go               # uses ./data/smoke-offline-sale.db
go run ./scripts/smoke-offline-sale/main.go /tmp/smoke.db # custom path
```
- Fails on setup/flow errors; exits with warning code if duration exceeds 5000ms.

## Event dispatch rules (summary)
- Default: non-blocking plugin events with audit of outcomes; failures should not block core flow.
- Blocking events must be explicitly marked, wrapped in a transaction, and roll back on handler failure while emitting audit entries.

## CI behavior & semantics
- **Warnings**: Tests log warning to stdout but **exit 0** (pass). Warnings indicate potential regression; investigate before merge.
- **Failures**: Tests **exit 1** and **block PR merge**. Must fix code or justify threshold increase with hardware evidence.
- **Overrides**: Set env vars in GH Actions workflow to adjust for runner differences:
  ```yaml
  env:
    UT_BENCHMARK_SALE_FAIL_MS: 6000  # Slower CI runner
    UT_BENCHMARK_INTERACT_FAIL_MS: 250
  ```
- **Local dev**: Override thresholds without affecting CI defaults.

## CI expectations
- `go test ./...` runs both performance checks with defaults on GH runners.
- Treat warnings as regressions to investigate; failures must be fixed or thresholds justified for slower hardware with explicit env overrides.

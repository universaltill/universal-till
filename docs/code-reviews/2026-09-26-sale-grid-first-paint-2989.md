# 2026-09-26 — Sale-screen tiles in the first paint (ut-docs#2989)

## What shipped
- **The grid comes with `GET /`.** The sale page renders the tile grid inline instead of an empty `hx-trigger="load"` placeholder that fetched `/ui/buttons` after paint.
  - The inline grid is built by the same `saleScreenButtons` helper as `/ui/buttons`.
  - It is served from the same #2501 cache entry and key. The cache moved onto `common.Deps` (`SellScreenCache()`, still one bounded instance).
  - A render failure, or no button store, falls back to the old lazy placeholder.
- **Live refresh knows the grid's version.** The grid root carries `data-sell-version`. `sell-screen-watch.js` (#2765) seeds from it, and the header path stays.
- **Sale-screen tiles no longer carry edit badges.** One `<template id="tile-badges-tpl">` per grid holds them. `utTileJiggle` fills them in on entering edit mode, and after a swap while the mode is on:
  - it clones the template;
  - it fills the id and label into attributes via `setAttribute`, never innerHTML;
  - it runs `htmx.process`.

  The Designer is unchanged.
- **Measured on the seeded catalog (230 tiles):** the grid went from 773 KB to 181 KB for a manager (23%), and from 1,062 KB to 182 KB for a cashier (17%). On the owner's tablet the grid was 902 KB, 52% of it badges.

## Review
Independent reviewer: Fable 5.1. The change was built by Opus 5.5.

- **Cache key parity, version embedding, grant separation:** verified correct. The same key is built by construction, and `Get` needs an exact version match.
- **XSS path, jiggle re-entry, locked variant, delegation selectors:** verified correct.
- **Redirects, nil store, hx-boost:** verified correct.
- **TDD re-verified:** the reviewer reverted `index.html`, and `TestIndex_InlinesSaleScreenGrid` failed with "GET / still ships the lazy products placeholder".

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The renderer (a clone of the whole base+index+buttons template set) was built before the cache was consulted, so every `GET /` paid for it even on a cache hit. | **Fixed:** `lazyButtonsRenderer` builds on the first `Render` only. |
| 2 | minor | The badge `hx-vals` fill rebuilt exactly `{itemId}`, so a future extra key would be silently dropped. | **Fixed:** parse, fill string values, re-serialise. |
| 3 | nit | `ResolveLocale` runs twice on `GET /?lang=`, so the page sends two identical cookies. | Accepted (harmless). |
| 4 | nit | The package-var test seam `saleGridFirstPaint` is unsafe under `t.Parallel`. | Accepted (no parallel tests in the package). |
| 5 | nit | `materialiseBadges` runs on every settle while in jiggle mode. | Accepted (cheap and idempotent). |

Side effect of fix 1: if the template files themselves fail to parse (a broken install), `/ui/buttons` now answers 200 with an empty body, not the localized 500. `GET /` still falls back to the placeholder. Every Go template test fails first in that state, so it cannot ship.

## Verified
- `go build ./...` and `go vet ./internal/...` pass. Full `go test ./...` is green, and `internal/ui` and `internal/pages` are green again after the fixes.
- Playwright:
  - First run: 121 specs (jiggle, hidden tiles, hide/delete, locked cashier, live refresh, page and popup zoom, Designer, sale).
  - After the fixes: 14 edit-mode specs plus the locked cashier spec (auth project), all pass.
- `make docs-shots` regenerated.
- **Not verified:** real first-paint time on the Android tablet. That needs a release on the device; the owner compares before and after.

## Verdict
Safe to merge.

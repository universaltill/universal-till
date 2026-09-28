# Code review — sell-screen chip survives a grid refresh (ut-docs#3089)

- **Date:** 2026-09-28
- **Card:** universaltill/ut-docs#3089 (split from #3072)
- **Branch:** `fix/3089-chip-survives-refresh`
- **Author:** Dev subagent (Sonnet 5). **Reviewer:** independent subagent (Opus 5.5, different model), then fixes by the orchestrating lane (`lane:cloud-54`).

## What shipped

In the `all_filter_chips` browsing mode, a `buttons-changed` / `modifiers-changed` refresh swaps the whole `.products` block. It used to drop the pressed category chip back to **All** mid-sale. Now:

- **`/ui/buttons` accepts `?category=`**, with `AllMore`'s existing meaning: no value or `all` is the whole catalog, `""` is the uncategorized bucket, anything else is a category id. The refresh carries the value with `hx-include="#browsing-chip-input"` (a hidden input in the chip row, server-rendered `value` plus Alpine `:value="chip"`) and `hx-disinherit="hx-include"`.
- **The render reuses the tap path.** `renderList` resolves the chip against the rendered `CategoryTiles`; an unknown id quietly becomes All. It builds the grid through `allFilterPage`, a helper now shared with `AllMore`, and renders it with the same `all-more-fragment` template. The refreshed grid is therefore byte-identical to what tapping the chip fetches, including Pos and the `data-all-filter` marker that jiggle mode depends on.
- **No flash of All.** The static `aria-pressed` on each chip and Alpine's initial `chip` value are both server-rendered.
- **Alpine `chip` now matches the server values** (`all` / `""` / id). Before, All and Uncategorized were both `''`, so both chips read as pressed at once.
- **#2501 sell-screen cache.** The chip is part of the key (`chipCacheKeyParam`: bounded length and charset; anything else is normalised to All before rendering). A requested chip that no longer resolves is not cached. `ListFragment` (the first paint for `GET /`) and EditMode always render All.
- **Help:** `web/help/en/sell.md` now says the selected category stays selected when the grid refreshes itself.

## Review findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Major (latent) | The cache key folded an out-of-charset id (e.g. `cat.snacks`, which cloud sync doesn't format-check) into All's key, but the render still honoured the id. A filtered grid could then be cached as All and served as `GET /`'s first paint. Reproduced by the reviewer. | **Fixed:** `List` normalises such a chip to `all` *before* keying and rendering. Test `TestSellScreenCache_OutOfCharsetChipNeverPoisonsAllEntry`. |
| 2 | Minor | htmx inherits `hx-include`, so every request inside `.products` (scan, modifiers, search, badges) carried `category=`. Harmless today, a trap for any future handler. | **Fixed:** `hx-disinherit="hx-include"` on the root. |
| 3 | Minor (already on `main`) | Load more under the Uncategorized chip dropped `category=` (`{{ with "" }}`), so page 2 was unfiltered. | **Fixed:** `all-more-button` takes `Filtered` and always emits `&category=` when filtered. Test `TestAllMoreButton_UncategorizedKeepsEmptyCategory`. |
| 4 | Minor | Every well-formed unknown id got its own entry in the 64-entry cache and could evict real entries. | **Fixed:** an unresolved chip render is marked not clean and never stored. Test `TestSellScreenCache_UnknownChipIsNotCached`. |
| 5 | Minor | e2e could assert on the old DOM (`waitForResponse` resolves before the swap). | **Fixed:** the spec tags the old root and waits for it to be gone, and asserts the refresh URL carried `category=<id>`. |
| 6 | Nit | An empty catalog shows no chip row, but the root still had `hx-include`, which logs an htmx console error on each refresh. | **Fixed:** the attribute is emitted only when `.AllButtons` is non-empty (the same condition as `$empty`). |
| 7 | Nit | `visibleOnly(allBtns)` runs twice on the unfiltered path. | **Accepted:** O(n) on an in-memory slice, and keeping `allFilterPage`'s signature the same as `AllMore`'s is worth more. |

## Verification

- **TDD:** the Dev tests were written first and failed before the implementation. The reviewer reverted the production files and confirmed 5 behavioural tests fail, then pass again once restored. The three post-review tests each fail with `buttons.go` and `buttons.html` stashed and pass with the fix.
- **Gate:** `gofmt -l` clean; `go build ./...`; `go vet`; `go test ./...`; `golangci-lint run ./...` 0 issues. Guards all exit 0: i18n, kiosk-engine, data-access, help-topics, help-drift, compliance-claims, competitor-naming, core-neutral, htmx-loaded. `shellcheck` isn't installed in the cloud container; no shell scripts changed.
- **e2e (real Chromium, `--project=default`), 14 passed:** `sell-chip-refresh-3089`, `sell-screen-browsing-mode-2499`, `sell-all-grid-jiggle-2534`, `sell-all-grid-jiggle-locked-cashier-2534`, `sell-null-sku-tile-3072`, `sell-tile-jiggle-mode-2339`. The 3089 spec drives the real flow: pick a chip, fire `buttons-changed`, the chip and filtered grid stay; deactivate that category, fire again, it falls back to All with no console error.
- **UX:** no new strings, no modal, and the existing chip pattern is unchanged (44px floor, tick plus `aria-pressed`, wraps). Only the pressed state now survives the swap.

## Verdict

Safe to merge.

## Deferred

None.

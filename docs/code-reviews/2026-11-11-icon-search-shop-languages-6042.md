# Review — icon picker search in the shop's languages (ut-docs#6042)

Date: 2026-11-11 · Author: Sonnet (Dev) · Reviewer: Opus 5.5 (independent) · Tester: separate agent

## What shipped

- **`catimport.PickerIconGroups(locales, lookup)`** (`internal/catimport/icons.go`):
  the picker's tile data is now `BuiltinIconGroups` with each icon's `Keywords`
  replaced by its search text. That text is made of the English keywords, the
  English label and the label in each shop language (exact locale through
  `httpx.TExact`, with no fallback and no key echo). It is folded on the server
  and each word appears once. A language with no label adds nothing.
- **`catimport.FoldForSearch`** (`placeholder_norm.go`): the same fold as
  `normalizeWord` from #2654 (case, Turkish I forms, ß, NFD mark stripping, tatweel,
  Arabic/Persian letter variants), applied to the whole string with separators kept.
- **`common.ShopPickerIconGroups`** (`internal/pages/common/icon_matcher.go`): reads
  `store.staff_locales` once per request through the shared `shopLanguages` helper
  (`ShopIconMatcher` now uses the same helper). A failed read is logged and the
  default language is used, so the page still renders.
- **Wiring:** the item editor (`internal/pages/catalog/handlers.go`) and the category
  dialog (`internal/pages/categories_page.go`) pass `ShopPickerIconGroups` to
  `data-keywords` (escaped by html/template).
- **JS** (`web/public/icon-picker-filter.js`): `fold` ports the Go fold one code
  point at a time (same replacement table, non-locale lower-casing, NFD, all `Mn` plus
  tatweel stripped, NFC, then the table again). A query word that starts with `ال` and
  has at least 4 code points also matches with the article stripped. The folded text
  of each tile is cached in a `WeakMap`. The script exports `fold`/`wordMatches` for
  Node and `window.utIconFold` for the e2e parity check. It still contains no strings.
- **Parity:** `internal/catimport/testdata/search_fold_parity.json` is read by the Go
  test, by `e2e/unit/icon-picker-fold_test.js` (`node --test`, a new step early in
  ci.yml `build`) and by the Chromium e2e spec.
- Help: `web/help/en/catalog.md` and `categories.md` say the search box also finds
  images by their names in the shop's languages.
- No new `en.json` keys, no SQL and no migration.

## Findings

| # | Sev | Finding | Disposition |
|---|---|---|---|
| F1 | Medium | The help topics `catalog` and `categories` changed, but `web/help/img/manifest.json` was not regenerated. `guard-docs-shots.sh` failed on the branch, although it passes on `origin/main`. `run-docs-shots-guard.sh` only warns on PRs, so this would have turned `main` red right after the merge. | **Fixed:** ran `make docs-shots`. It changed the `catalog`/`categories` topic hashes and `surface_sha256` in the manifest. The catalog and categories PNGs were byte-identical, which confirms there is no visual change. The run also rewrote `channel-orders` and `reports` PNGs in all 4 locales. Those pages are not touched by this branch (the drift comes from the date) and the manifest does not hash PNGs, so they were restored to keep the PR's footprint small. The guard is green afterwards. |
| F2 | Low | The parity fixture did not cover the fold's second replacement pass: removing the final `rep(...)` in JS still passed all 39 node tests. I compared Go and JS over every Unicode scalar (1,112,064 code points plus combining and compatibility samples) and found one input that needs that pass: `ẞ` (U+1E9E) lower-cases to `ß`, which only the second pass turns into `ss`. | **Fixed:** added the fixture row `"GROẞ"` → `"gross"`. The Go parity test passes, node passes (40/40), and the same mutation now fails `fold "GROẞ"`. |
| R1 | Info | Fold divergence: across every code point, Go `FoldForSearch` and JS `fold` give **0 differences** on Node 22. Areas checked: JS `toLowerCase` uses full special-casing (`İ` would become `i̇`, but the table maps it first), final sigma (per code point, so σ on both sides), astral and surrogate-pair input (`for…of` walks code points), and NFD input versus precomposed input (both fold the same). The article rule counts code points (`Array.from`) like `stripArticle`'s runes, and `ال` is in the BMP, so `slice(0,2)` is correct. CI runs Node 26, whose ICU may have a newer Unicode version than Go's x/text. That could only differ on code points added in that newer version, and none of them appears in icon labels. | Accepted. |
| R2 | Info | Cost per request: `PickerIconGroups` with 3 shop languages takes about 0.27 ms and 121 KB, with 2.1k allocations (benchmark on this container). It runs on manager-only editor renders. | Accepted. A cache keyed by language list could come later if a Pi profile shows it. |
| R3 | Info | Labels are split on whitespace only, not on `/` and `,` as the #2654 matcher does (Tester note). A label `A/B` becomes the word `a/b`, which typing either part still finds as a substring. | Accepted. |
| R4 | Info | Core neutrality: there is no language list. `en` is core's own English (ADR-0160), and the rest comes from settings. `guard-core-neutral.sh` passes. There is no raw SQL: `data.NewSettingsRepo`. Checkout and offline use are unaffected: this is a server-side render plus client-side filtering. | Accepted. |

Recurring bugs: no file is written and no path is built, so `os.MkdirAll` and `paths.Data` do not apply. The ci.yml step runs after `actions/checkout` and the existing `setup-node` (Node 26). It uses a repo-relative path, and Playwright does not pick up the file, because its `testDir` is `./tests` and `_test.js` does not match `.test.js`.

## Verification

- Reviewer (in the review worktree, after the fixes): `go build ./...` passed. `go vet` on catimport, pages/common, pages/catalog and pages passed. `go test ./internal/catimport/... ./internal/pages/common/... ./internal/pages/catalog/...` passed. `go test ./internal/pages -run 'Categor.*Icon|6042|IconPicker'` passed. `node --test e2e/unit/icon-picker-fold_test.js` gave 40/40. `golangci-lint run` on catimport and pages reported 0 issues. `gofmt -l` produced no output. The i18n, fixed-dates, core-neutral, help-topics, data-access, rtl-logical-css and docs-shots guards all exit 0.
- **TDD re-verified by the reviewer:**
  - With `handlers.go` reverted to `tillimport.BuiltinIconGroups()`, 4 tests fail: `TestCatalogIconPicker_{SearchTextInShopLanguages,ArabicShopEmitsCharactersNotEntities,ShopOverrideSearchableAndEscaped,SettingsReadFailureStillRenders}`.
  - With the JS final `rep` removed, the run passed (F2). After the fixture fix it fails on `fold "GROẞ"`.
  - Both changes were restored, and `git diff` shows only the review's own changes.
- Tester (PASS):
  - Mutation M1 (shop-language lookup disabled) was killed by 10 Go tests across catimport, common, catalog and pages.
  - Mutation M2 (one JS table entry broken) was killed by 6 node tests.
  - `go test ./internal/pages/...` passed. Under `-race`, only the slow `internal/pages` package hit the default 10m timeout, which is not caused by this change. The targeted `-race` run was clean.
  - Playwright: 23 tests passed (6042 spec plus icon-library-2506).
- **Tester visual attestation** (Chromium, light theme, deviceScaleFactor 1): item editor, Image tab, Arabic UI in RTL, query `قهوه` against the label `قهوة`. At 1280×800 and 360×740 exactly the No image, coffee and coffee-to-go tiles show. Tiles start at the inline start (right). Nothing is clipped. `scrollWidth` equals the viewport width, so there is no horizontal scroll. The unfiltered 360-wide view also looks normal. Not looked at: dark theme, a screenshot of the category dialog (covered by e2e assertions), kiosk sizes.

Pre-flight: help topic updated (`web/help/en/catalog.md`, `categories.md`); docs-shots
n/a (no visual change; the catalog and categories PNGs are byte-identical; manifest
topic and surface hashes regenerated, F1); `reference/` n/a (no reference page covers
picker search); README n/a; en.json/en.meta n/a (no new keys); ADR n/a (fits
ADR-0160 / #2654); plugin/manifest mirrors n/a; follow-ups deferred → cards below.

## Verdict

**Approve** with F1 and F2 applied. Both are uncommitted in the review worktree:
`web/help/img/manifest.json` and `internal/catimport/testdata/search_fold_parity.json`.
Squash them into the feature commit.

## Deferred

1. Icon synonym keys `catalog.icon_keywords.<key>` → ut-docs#6041 (ar "كعك"/"كيك",
   and similar).
2. German compound / head-noun matching ("Milchkaffee") → ut-docs#6043.
3. Persian input without ZWNJ: a label such as "قهوه‌ای" keeps its ZWNJ (U+200C is
   `Cf`, not `Mn`, so the fold does not remove it), so typing it without the ZWNJ
   does not match. This needs a **new card** (not yet filed): decide whether the fold
   should drop ZWNJ on both sides (Go and JS, plus a fixture row) without breaking
   #2654's word splitting.

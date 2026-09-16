# Code review — Sell screen All tab lists every active catalog item, server-side search, settings toggle (ut-docs#2294)

- **Date:** 2026-09-16
- **Ticket:** ut-docs#2294 (`complexity:medium`)
- **Branch:** `feat/2294-all-tab-active-items`
- **Reviewer:** independent pass, Opus subagent working in an isolated
  worktree (per this card's `complexity:medium` routing — different model
  from the Sonnet implementation, never saw the dev reasoning).
- **Verdict: SAFE TO MERGE, WITH FIXES ALREADY FOLDED IN.** One blocker
  found and fixed before this commit; one should-fix (test coverage gap
  named by the issue itself) also fixed. Three non-blocking findings filed
  as follow-up cards. One CI guard (`guard-docs-shots.sh`) is expected red
  in this environment — see "Known-red guard" below; **this PR is
  deliberately not merged this cycle regardless — see "Not merging" below.**

## What shipped

Product-owner request (2026-09-16, item 8 of the design batch): the sell
screen's All tab previously reused the per-category quick-button panels,
so an active catalog item with no Designer quick button was invisible and
unsearchable from the sell screen.

- `internal/ui/buttons.go`: `ButtonStore.LoadAllActive` (every active item,
  flat A–Z, batched lookups matching `Load()`'s existing style) and
  `ButtonStore.SearchSellable` (server-side search across all active
  items, tile-ready results); `ButtonsHTTP.HideAllTab` (inverted-boolean
  settings gate) and a new `Search` handler.
- `internal/pages/buttons_api.go`: wires the new setting into `/ui/buttons`
  and adds `GET /ui/buttons/search`.
- `web/ui/partials/buttons.html`: dedicated `#buttons-grid-all` grid
  reusing the shared tile partial; category panels `x-cloak`'d unless
  default when the setting is off (restores pre-#2212 shape); search input
  now does a real debounced `hx-get` to `/ui/buttons/search`, replacing
  (not client-filtering) the grid while a query is active; clearing the
  query restores whichever tab was active.
- Settings: `sale.show_all_tab` (default **on**), following
  `pos.allow_negative_inventory`'s full elevation-gated plumbing pattern —
  no pre-existing `settings.sale.*` boolean existed to copy verbatim.
- `internal/data/pos_repo.go`: new `itemIDCodePrefix` resolution tier so an
  All-tab/search tile for an item with neither a barcode nor a SKU (and no
  quick button) is still addable — mirrors the existing
  `synthesizedButtonCodePrefix` convention from ut-docs#1459.
- i18n: new keys in all four core-shipped locales (`en`/`ar`/`fa`/`tr`) —
  these are gated by `guard-i18n.sh`, not the external
  `ut-plugin-language-*` packs, so they had to ship in this same change.
- Tests: 8 new/rewritten cases in `internal/ui/buttons_all_tab_test.go`
  plus updates to `buttons_http_test.go`/`buttons_search_visibility_test.go`
  (three tests pinned to the setting-off fallback explicitly, since All
  now widens the tab bar by default) and new resolve-tier tests in
  `internal/data/pos_repo_resolve_test.go`.

Explicitly deferred by the issue itself (not flagged as missing): A–Z jump
strip, the 250-item Playwright e2e, `ut-plugin-language-{de,es}` follow-up
PRs, `web/help/en/sell.md` update, `make docs-shots` regeneration.

## What the independent review found

Ran the full gate itself: `gofmt -l`, `go build ./...`, `go vet ./...`,
`go test ./internal/ui/... ./internal/data/... ./internal/pages/...
./internal/uislot/...` (all green — `internal/pages` 291s,
`internal/data` 79s), `guard-i18n.sh`, `guard-data-access.sh`,
`guard-help-topics.sh`, `guard-help-drift.sh`, `guard-compliance-claims.sh`,
`guard-page-http-error.sh`, `guard-htmx-loaded.sh`, `guard-kiosk-engine.sh`
— all green. `guard-docs-shots.sh` fails (expected, see below).

**TDD re-verification, done for real, twice**: the reviewer subagent
reverted `LoadAllActive`'s call site back to the pre-fix "quick-button
items only" behaviour and confirmed
`TestButtonsHTTPList_AllTabShowsItemWithNoQuickButton` failed with the
exact predicted shape (the no-quick-button item missing from
`#buttons-grid-all`), then restored and confirmed it passed again. I
independently re-did the same discipline on the fix for BL-1 below: `git
stash`'d just the reordering fix, re-ran the new regression test, watched
it fail with the predicted wrong name (`"Catalog Name"` instead of
`"Operator Label"`), then `git stash pop`'d and confirmed it passed again.

### Blocker — found and fixed

**BL-1 — the new `itemIDCodePrefix` tier silently dropped a codeless quick
button's operator-chosen label off the basket line and receipt.**
`internal/data/pos_repo.go`, `ResolveShortcutLineDecoded`.

The tier was originally checked *first*, returning immediately either way,
on the premise that its synthetic code "can never collide with a real
barcode/shortcut-code/SKU/name." That premise was false: ut-docs#1459's
own codeless-quick-button feature writes this exact `item:<id>` literal as
the `shortcut_buttons.barcode` **primary key**, carrying the operator's
chosen `Label` — so the `resolveShortcut` tier immediately below it *does*
legitimately match, and is what normally applies that label
(`row.ItemName = row.Label.String`). With the new tier checked first, a
shop with a labelled codeless quick button would see the basket
line/receipt/journal silently switch from the operator's label (e.g.
"Flat White") to the raw catalog name (e.g. "Beverage Item 4471") the
moment this feature shipped — price unaffected (`resolveRowPrice` keys off
`ItemID` in both tiers), but a real, silent regression to shipped
functionality on the Germany-pilot fiscal receipt surface, and nothing in
the original diff's own tests could see it (neither existing
`itemIDCodePrefix` test seeds a competing `shortcut_buttons` row).

**Fixed**: reordered so `resolveShortcut` is checked before the
`itemIDCodePrefix` tier — a real `shortcut_buttons` row (including a
codeless one) wins and keeps its label; only a catalog item with no row at
all falls through to resolve-by-id. Added
`TestResolveShortcutLine_ItemIDCodePrefixLosesToRealShortcutRow`, which
seeds both a codeless item *and* a differently-labelled `shortcut_buttons`
row for it and asserts the label wins. Corrected the now-wrong "can never
collide" doc comment.

### Should-fix — found and fixed

**SF-3 — the query-count test named by the issue's own requirement (5)
didn't cover the property it was required to cover.** The original test
proved the All grid has no N+1 over catalog size (worthwhile), but never
mutated a basket and never pinned *what* re-fetches `/ui/buttons` — a
regression that wired a basket/scan event into the re-fetch trigger would
have sailed past it unnoticed.

**Fixed**: renamed to
`TestButtonsHTTPList_AllTabDoesNotScaleWithCatalogSize` (honestly
describes what it asserts), kept the scaling assertion, and added a second
assertion pinning the actual mechanism: the rendered body must contain
`.products`'s own `hx-get="/ui/buttons" hx-trigger="modifiers-changed
from:body"` and must not contain any basket/scan-event trigger wiring
(`basket-changed`, `sale-changed`, `scan-changed`, `from:#basket`).
Cross-checked by grep across `internal/` and `web/`: no template outside
`buttons.html` itself references `#buttons-grid`/`#buttons-grid-all`, so
`POST /api/pos/scan`'s response cannot OOB-swap it either.

After folding both fixes in: re-ran `go build`, `go vet`, `gofmt -l`, the
full `internal/ui` and `internal/data` packages, and `guard-i18n.sh` —
all green again (see "Verification after fixes" below).

## Non-blockers — not fixed here, filed as follow-ups

- **ut-docs#2318** — `LoadAllActive`'s three batched lookups
  (`ItemIDsWithModifiers`/`ItemIDsWithVariants`/`ItemCurrentPrices`) have
  no chunking and fail silently (non-fatal, logged-and-degraded) above
  ~16,383 active items — a modifier/variant prompt or a price-history
  override can be silently skipped at that scale, with no error surfaced.
  Real, but far above this pilot's scale; correctness-tier fix, not a
  blocker for this card.
- **ut-docs#2319** — the All grid response is unbounded (~1 MB HTML at
  2000 active items) and re-shipped whole on every `modifiers-changed`.
  In-scope-adjacent to the issue's explicitly-deferred A-Z-strip/paging
  work, not a spec miss.
- **ut-docs#2320** — a shop with zero categories now shows a 2-tab bar
  ("All | Uncategorized") where it previously showed a flat, tab-less
  grid. Needs a product-owner call on the label/whether to suppress the
  tab bar in that case, not an engineering guess — filed to Admin Review.

## Known-red guard — not fixable in this environment

`guard-docs-shots.sh` fails: this diff genuinely changes rendered pixels
(the new All grid, the search-results UI, the new "Sell screen" settings
card), so it wants `make docs-shots` regenerated. This sandbox has no
Chromium available (`e2e/scripts/resolve-chromium.sh` found none, and
installing one needs network + apt access this environment doesn't have)
— the issue itself named this as an explicitly deferred item for exactly
this reason. **Required before this PR can actually merge**: run `make
docs-shots` on a Chromium-capable machine/CI runner and commit the result
(not `update-docs-shots-surface-hash.sh` — this is a real pixel change,
not a no-op edit).

## Other checks the review ran

- **Settings toggle round-trip**: traced by hand through the 0/1/2+-category
  × on/off matrix in the template; default `true` is pinned in the right
  place (`LoadState`'s defaults path, not just the struct zero value);
  elevation-gated identically to `allow-negative-inventory`; persists and
  reloads correctly; off genuinely restores first-category-default-select.
- **Search**: clearing the query restores whichever tab (category or All)
  was active before searching, not always All. No XSS — the query string
  is used only as a template branch condition, never rendered into the
  response.
- **i18n quality**: read all three non-English additions personally —
  real, idiomatic, correctly-inflected translations (Arabic/Farsi/Turkish),
  not machine-garbled or English left in place; each uses the same word
  its locale already uses for `products.all`, so the setting's own label
  text agrees with the tab it describes.
- **RTL safety**: no new hardcoded color/spacing; existing design tokens
  reused; no physical (`left`/`right`) properties introduced.
- **Auth**: `/ui/buttons/search` is session-gated like every other `/ui/`
  route (not in `middleware.go`'s exempt list).
- **Recurring bug classes** (missing `os.MkdirAll`, cwd-relative path vs.
  `paths.Data(...)`): N/A — no new file-write handler in this diff.
- **Test data / secrets**: clean — fixtures are `Bread`, `Cola`,
  `Sourdough Loaf`, `Loose Sweet`, `Discontinued Soda`; no real client/shop
  name; no secret-shaped literals.

## Nits (not fixed, non-blocking)

- `web/help/en/sell.md` not updated (explicitly deferred by the issue;
  CLAUDE.md's "manual ships with the feature" rule makes it an owed
  follow-up, not a nothing) — same for the `ut-plugin-language-{de,es}`
  packs, which will show `lang-pack-drift`'s advisory warning on this PR
  for the 5 new `en.json` keys (new keys, so core merges first per the
  guard's own documented two-case rule — not a blocker, but the lane that
  eventually merges this owns the pack follow-up in the same cycle).
- `product-tile-result` in `buttons.html` is a verbatim copy of
  `product-tile` minus one `x-show` — currently identical behaviorally
  (diffed line by line), but two copies will drift over time.
- Some now-dead client-side `matches()`/`sectionHasMatch()` Alpine logic
  left in the template (never evaluated while a search query is active) —
  harmless, mildly misleading to a future reader.

## Verification after fixes

`gofmt -l .` clean; `go build ./...` clean; `go vet ./...` clean; `go test
./internal/data/... ./internal/ui/... ./internal/pages/...
./internal/uislot/...` all green; `guard-i18n.sh`, `guard-data-access.sh`,
`guard-help-topics.sh`, `guard-help-drift.sh` (pre-existing baselined
drift only, tracked ut-docs#1962/#1973), `guard-compliance-claims.sh`,
`guard-page-http-error.sh`, `guard-htmx-loaded.sh`, `guard-kiosk-engine.sh`
all green.

## Not merging this cycle — deliberate, not an oversight

Separate from `guard-docs-shots.sh` above: this pipeline opened
ut-docs#2277 this same cycle (Admin Review, unresolved) re-confirming
whether the standing auto-push/auto-release authorization — explicitly
scoped to "no real users yet" — still holds, given evidence of a live
production till fleet now in the field. Until a human answers that, this
cycle is treating merges to `universal-till`/`ut-cloud` as paused, the
same restraint `universal-till` PR #1183 already applied this cycle. This
PR is pushed and ready for CI (reversible, no live effect) but
deliberately left unmerged.

## Explicitly deferred (not this card)

- `web/help/en/sell.md`, `ut-plugin-language-{de,es}` follow-ups,
  `make docs-shots` regeneration — see above.
- ut-docs#2318, #2319, #2320 — filed follow-ups above.

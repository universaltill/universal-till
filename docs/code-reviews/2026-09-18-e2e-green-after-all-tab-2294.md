# Code review — e2e suite green after the All-tab/server-search change (ut-docs#2294 follow-up)

- **Date:** 2026-09-18
- **Ticket:** ut-docs#2294 follow-up (closing the "no Chromium in the dev
  session" gap the original PR's own review record and commit message both
  flagged explicitly).
- **Branch:** `feat/2294-all-tab-active-items` (same PR, universal-till#1200).
- **Reviewer:** this session, running the real Playwright suite against a
  real Chromium for the first time on this branch (`/opt/pw-browsers`,
  `e2e/scripts/resolve-chromium.sh`) — the prior sessions on this branch
  only ever ran `go test`.

## What this pass found and fixed

Running the suite for real surfaced 17 failing assertions across 9 spec
files, all downstream of one root cause: `sale.show_all_tab` defaults ON,
so every till (including every existing fixture) now shows the All tab's
own dedicated grid (`#buttons-grid-all`) by default, and Alpine's `x-show`
never removes a hidden category panel from the DOM — a quick-button item
now genuinely exists twice at once (its own category panel, and the All
grid), so a plain by-name/by-attribute Playwright locator that used to
resolve to exactly one node now resolves to two.

### Category 1 — locator-scoping ambiguity (7 of the 9 files)

`catalog-price-history-edit-2314.spec.ts`,
`category-switch-stale-tile-add-1433.spec.ts`,
`codeless-item-shortcut-1459.spec.ts`,
`order-type-prompt-placement-2282.spec.ts`,
`sale-screen-scan-focus-search-423.spec.ts`,
`sale-screen-search-strip-2173.spec.ts`,
`sell-tile-jiggle-mode-2339.spec.ts`,
`sell-tile-jiggle-mode-locked-cashier-2312.spec.ts`. Fixed by disambiguating
each locator to the one instance the test actually means — `:visible` where
either copy is functionally equivalent (a plain scan/click test), or an
explicit container scope (`.products-tab-panel …` / `#buttons-grid-all …`)
where the test's own intent is specifically about the quick-button/category
copy (price-history's basket-line label, and — see below — every jiggle
test). No app behavior changed for this category; the app's "exactly one
instance visible at a time" behavior was already correct.

`catalog-price-history-edit-2314.spec.ts` needed more than `:visible`: its
tile's two copies aren't interchangeable — the All-grid copy is built
straight from the catalog item (`LoadAllActive`) and carries the item's own
primary barcode + raw `Name`, while the quick-button copy carries the
shortcut's own distinct barcode + `Label`; confirmed live that clicking the
wrong one rings up a basket line with the wrong label. Fixed by selecting
the item's own category tab first, matching the test's actual intent.

### Category 2 — assertions pinned to now-superseded behavior

`sale-screen-category-tabs-search-418.spec.ts` — rewritten in full. Its
comments and assertions described the pre-#2294 "All reuses every category
panel" and "search is a client-side filter over already-rendered tiles"
mechanics, both explicitly superseded by this PR (see
`web/ui/partials/buttons.html`'s own `panelVisible()`/`showAllGrid()`
comments). Rewritten to assert the new contract instead — All's own flat,
header-less `#buttons-grid-all`; a category panel hidden the whole time
All is selected; search as a real round trip into `#search-results` while
`#buttons-grid` hides in its entirety — while preserving every genuine
underlying behavior the file was actually testing (tab switching,
ut-docs#2181's cross-category search reach, RTL).

## Two genuine app bugs found and fixed (not test workarounds)

Both surfaced only once the suite ran against a real browser, and both are
fixed in app code, not papered over in a test:

1. **`web/public/app.js` — jiggle edit mode had no awareness of
   `#buttons-grid-all`.** Every jiggle-mode DOM query
   (`tileFor`/`badgeFor`/`orderedCodes`/`refreshPositions`) scopes to
   `#buttons-grid .btn-tile[data-code]` — and `#buttons-grid-all` renders
   *inside* `#buttons-grid`. Since All is the default tab, a long-press or
   right-click on the very first screen an operator sees would wobble/badge
   every active catalog item (not just quick buttons) and let a drag
   "reorder" them — `sort_order` has no meaning for the All grid's fixed
   alphabetical listing — and `orderedCodes()` would post the codes of
   every catalog item on the till on every Done, not just real shortcuts.
   Fixed with a small `inAllGrid()` exclusion gating every entry point and
   every code-gathering helper, so the mode simply never arms from an
   All-grid tile, and never picks up an All-grid tile's code even when
   entered legitimately from a real category panel elsewhere in the same
   `#buttons-grid` subtree.

   **Coverage correction (independent review, 2026-09-18):** the original
   version of this record claimed `sell-tile-jiggle-mode-2339.spec.ts` and
   `…-locked-cashier-2312.spec.ts` regression-tested this fix — reverting
   just the `inAllGrid()` guard showed that claim was only half true:
   `-2312.spec.ts` still passes unchanged with the guard fully reverted (it
   never long-presses an All-grid tile at all), and `-2339.spec.ts`'s own
   existing tests deliberately switch OFF the default All tab before
   long-pressing anything (see their own comments), so the actual
   entry-point guard (a long-press starting FROM an All-grid tile) had zero
   coverage. Added a new, dedicated case —
   `-2339.spec.ts`'s "a long-press on the default All tab never arms
   jiggle mode (ut-docs#2402)" — that stays on the default All tab and
   long-presses the All-grid copy directly; verified by the same
   revert→run→restore method (fails with the guard reverted: `#buttons-grid`
   gains `.jiggle-mode`; passes restored).
2. **`web/ui/partials/buttons.html` — the search input's missing
   `hx-swap` inherited `outerHTML` from the page root.** `.products` (this
   file's own root) sets `hx-swap="outerHTML"` for its own
   modifiers-changed/buttons-changed self-refresh. The new search input had
   no `hx-swap` of its own, so htmx's attribute-inheritance walked up and
   used that same `outerHTML` for the search request too — replacing
   `#search-results` *itself* (id, `x-show="q"` binding, everything) with
   the response's bare `<div class="grid">` on the very first keystroke.
   Confirmed live: `id="search-results"` was gone from the DOM afterwards,
   so it could never hide again on an empty query, and every later
   keystroke's `hx-get` had nothing left to target — the whole ut-docs#2294
   live-search feature was broken beyond its first query, in every real
   browser, since the day it shipped (no Chromium in that dev session).
   Fixed with an explicit `hx-swap="innerHTML"` on the input itself.

## Independent review (Opus, isolated worktree)

Verdict: **safe to merge, no blocking findings.** Ran the full suite and
Go gate independently, revert→run→restore-verified both app-code bugs
above for real (confirmed both fail without their fix and pass with it),
and confirmed the `catalog-price-history-edit-2314.spec.ts`/
`sale-screen-category-tabs-search-418.spec.ts` fixes preserve real
underlying behavior rather than just asserting whatever the new code
happens to do. Non-blocking findings, addressed as noted:

- **Coverage gap in the app.js jiggle-mode fix** (see the correction above)
  — fixed by adding the missing test.
- `web/public/app.js`'s `exit()` focus fallback
  (`g.querySelector('.btn-tile[data-code]')`) can resolve to a hidden
  All-grid tile and silently drop keyboard focus to `<body>` on Done —
  **pre-existing since `211ec98`, not introduced by this diff**; filed as
  ut-docs#2417 rather than fixed here (out of this pass's scope).
- Dead/unreachable Alpine branches left over from #2294
  (`panelVisible()`'s `q` branch, an unreachable `<p class="empty">` inside
  `#buttons-grid`) — cosmetic, not fixed here; filed as ut-docs#2418.
- No help-topic/screenshot drift found; the flat-search-results
  category-labelling question is confirmed genuinely unresolved by
  ut-docs#2294's own acceptance criteria (no labelling requirement stated),
  correctly left to product rather than guessed at here.

## Verification

- Each of the 9 originally-failing files, plus the full suite (twice —
  once by this pass, once independently by review), run for
  real against `/opt/pw-browsers` Chromium via this repo's own
  `e2e/scripts/resolve-chromium.sh` + `npx playwright test`: **608 passed,
  0 failed** (`default`/`auth`/`ai-identify`/`layout`/`diagnostics`
  projects all green) after the coverage-gap test was added; 607 before it.
- `gofmt -l .` clean; `go build ./...`, `go vet ./...`, `go test ./...`
  all green (no `.go` files touched by this pass).
- Relevant `scripts/ci/` guards re-run directly: `guard-i18n.sh`,
  `guard-htmx-loaded.sh`, `guard-e2e-fixtures-import.sh`,
  `guard-page-http-error.sh`, `guard-data-access.sh`,
  `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
  `guard-compliance-claims.sh`, `guard-help-topics.sh`,
  `guard-help-drift.sh`, `guard-webkit-version.sh`,
  `guard-kiosk-launch-flags.sh`, `guard-android-status-address.sh`,
  `guard-android-i18n.sh`, `guard-emoji-font.sh`,
  `guard-autofill-suppression.sh`, `guard-makefile-version.sh`,
  `check-brand-assets.sh` — all pass. `guard-docs-shots.sh` initially
  failed on the `app.js`/`buttons.html` surface change; confirmed neither
  edit alters a rendered pixel (a JS reorder-scoping guard and an
  `hx-swap` attribute) and refreshed `web/help/img/manifest.json`'s
  `surface_sha256` via its own documented escape hatch
  (`scripts/ci/update-docs-shots-surface-hash.sh`) rather than a full
  `make docs-shots` regeneration — guard re-run green afterwards.
  `guard-shellcheck-version.sh` fails in this sandbox only because no
  `shellcheck` binary is installed here at all; unrelated to this change
  (no `.sh` files touched) and not something this pass can fix.

## Open question flagged, not resolved here

The old cross-category search view (ut-docs#2181) labelled each matching
group with its own category header so a flattened-in result stayed
attributable to a category. The new server-search `#search-results` grid
(and the All tab's own grid) render a flat list with **no** category
labelling at all. This may be an intentional simplification (the All tab
itself is equally flat/unlabelled) or a dropped requirement — a product
call, not a test-fixing one. Not changed here; flagged for the product
owner/BA to confirm.

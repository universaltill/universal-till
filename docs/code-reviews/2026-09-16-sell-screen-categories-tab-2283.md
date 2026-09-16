# Code review: optional sell-screen Categories tab (ut-docs#2283)

**Date:** 2026-09-16
**Author (implementation):** Dev subagent, isolated worktree, orchestrated by the cloud pipeline lane
**Independent reviewer:** Opus subagent, fresh context, isolated worktree, model deliberately different from the implementer per `MODEL-ROUTING.md`
**Card:** universaltill/ut-docs#2283
**Branch:** `feat/2283-sell-screen-categories-tab` @ `5804a1a` (WIP `4da8bd0` + hand-resolved merge of `origin/main` `ee2fb14`, which carries universal-till#1197 / ut-docs#2308)

## What shipped

An optional **Categories** tab on the cashier sell screen, off by default, toggled
in Settings → Display.

- `internal/ui/buttons.go`: new `CategoryTile` / `TopLevelCategoryTiles(buttons,
  cats)` — a flat, top-level-only tile list (`ParentID == ""`), excluding any
  top-level category with zero active buttons anywhere in its subtree
  (`subtreeButtonCount`, depth-first with a local seen-set so a malformed cyclic
  `ParentID` chain terminates). Deliberately not a reuse of `BuildCategoryGroups`,
  which builds the nested pruned tree the existing tab panels render.
- `ButtonsHTTP.CategoriesTabEnabled` threaded from the per-till setting by
  `internal/pages/buttons_api.go`; `/ui/buttons` now also renders `CategoryTiles`.
- `internal/pages/common/{state.go,deps.go}`: new `KeyCategoriesTabEnabled`
  (`display.categories_tab_enabled`) + `RuntimeState.CategoriesTabEnabled`,
  wired into `LoadState`/`SaveState` alongside the sibling `display.*` settings.
- `internal/pages/settings_page.go`: `POST /api/settings/categories-tab`.
- `web/ui/partials/buttons.html`: the tab (leading, before **All**, selecting a
  synthetic `__categories__` sentinel), its panel, and a new `category-tile`
  template. `category-group` now emits `id="cat-panel-<ID>"` for a real category.
- `web/ui/pages/index.html`: `#category-picker-modal`, non-modal `.show()`.
- `web/public/app.js`: `utCategoryPicker` — tapping a tile copies that category's
  **already-rendered** grid out of `#cat-panel-<id>` (no fetch), wraps the
  `innerHTML` assignment in `Alpine.mutateDom()` so Alpine's observer never tries
  to evaluate `matches($el)` outside the sale screen's `x-data` scope, clears any
  search-filtered `display:none`, then calls `htmx.process()` so both a plain
  item's `hx-post` scan and a modifier/variant item's own nested `hx-get` picker
  work inside the modal.
- `web/public/app.css`: `.category-tiles-grid` / `.category-tile` (10rem column
  floor and 10rem min-height, both strictly larger than `.btn-tile`'s) and the
  modal's sizing/scroll rules.
- i18n: 5 new keys × `{en,ar,fa,tr}`, real translations.
- Help: the new sentence appended to step 1 of `web/help/{en,ar,fa,tr}/sell.md`.
- Tests: `internal/ui/buttons_category_tiles_test.go` (5 cases),
  `internal/pages/settings_categories_tab_test.go` (3 tests incl. an end-to-end
  gated-render test against a real `/ui/buttons`), and
  `e2e/tests/sell-screen-categories-tab-2283.spec.ts`.

## Independent review — findings

| # | Severity | Finding | Disposition |
|---|---|---|---|
| F1 | **Blocking** | The WIP commit regenerated **108 manual screenshots** (27 topics × 4 locales) with an off-pin Chromium (sandbox 141.0.7390.37 vs the `@playwright/test` pin), shipping a visibly degraded manual. `web/help/img/en/inventory.png` went 87,780 → 67,351 bytes: the stock table's LOCATION column now wraps ("Main / Store" over two lines), the REORDER AT / DAYS LEFT headers wrap, and only 6 rows fit where 9 did. None of the 108 is required by this card. `guard-docs-shots.sh` cannot see it — it hashes source surfaces and topic markdown, never PNG bytes — and `web/help` is `//go:embed`'d into every shipped binary, so this would have merged green. | **Fixed.** Restored all 108 PNGs to `ee2fb14`'s content. `manifest.json` deliberately left as the branch has it: its new `surface_sha256` and `sell` topic hashes are both legitimate (source surfaces and `sell.md` really did change) and neither depends on PNG bytes. Guard re-run green. Same class as F2 in `2026-09-16-sell-screen-drag-divider-2308.md`. |
| F2 | Low | `web/public/app.js`, `utCategoryPicker`'s document-level `pointerdown` handler closes `#category-picker-modal` for any pointerdown it does not `contains()`. A modifier/variant tile inside the picker opens `#modifier-modal` on top via `showModal()`; the first pointerdown on one of its options is outside the picker, so the picker closes underneath. The operator then lands back on the Categories tab, where the plain-item path would have left the picker open for repeat adds. | **Accepted.** Inconsistent, not incorrect. Found by code reading, not reproduced — the e2e spec closes the modifier modal with Escape and never asserts the picker's state. A one-line "bail if the pointerdown is inside another open dialog" would fix it if the inconsistency ever bites. |
| F3 | Low | `copyInto` copies `panel.innerHTML` verbatim. For a top-level category with subcategories that markup contains nested `<section id="cat-panel-<childID>">` (from `category-group`, `web/ui/partials/buttons.html`), so those ids exist twice in the document while the picker is open. | **Accepted.** No functional impact today — only top-level ids are looked up, and those live on the panel `div`, which is not copied — but it is invalid HTML and would silently break a future lookup of a subcategory panel. Cheap mitigation: strip `id` attributes inside the body after the copy. |
| F4 | Note | The new comment above `category-group` in `web/ui/partials/buttons.html` claims the `id` is needed "in EVERY branch this template is used from — including the single-real-category `else` branch". It isn't: the Categories tab renders only inside `{{ if $hasTabs }}` (≥2 top-level groups), so in the single-real-category branch no tile ever renders and that id is unreachable by the picker. | **Accepted.** Harmless; only the stated justification is wrong. |
| F5 | Note | `internal/ui/buttons.go`, `ButtonsHTTP.List` computes `TopLevelCategoryTiles` on every `/ui/buttons` render even when the tab is disabled — pure waste on the default-off path. | **Accepted.** O(buttons+cats), negligible. |
| F6 | Note | `e2e/tests/sell-screen-categories-tab-2283.spec.ts` step (7)'s `waitForResponse` carries no explicit timeout, so it inherits the test timeout. Observed during the TDD re-verification below: a real regression in the nested-modifier path surfaces as a whole-test timeout attributed to the `finally` cleanup line, not a pinpointed assertion. | **Accepted.** The suite still goes red, so it is not a false-pass. `{ timeout: 10_000 }` would make it diagnose itself. |
| F7 | Note | `elevation.summary.categories_tab_{on,off}` were inserted between `backup_now` and `backup_restore` in all four locale files, breaking the otherwise-alphabetical ordering of that block. | **Accepted.** Verified mechanically: all four files carry identical 2,447-key sets, zero duplicates, and exactly two out-of-order pairs — this one plus a pre-existing `settings.printer.receipt_policy.*` one. Consistent across locales, guard green, sorting is not an enforced convention here. |

**Verdict: PASS-WITH-FIXES — safe to merge.** The one blocking finding is fixed
and re-verified; the rest are accepted as noted, none of them user-blocking.

## TDD re-verification (done personally, not taken on the implementer's word)

The card's central technical claim is that this vendored htmx (1.9.12) has no
MutationObserver auto-processing, so the copied grid is inert without an explicit
`htmx.process()`. Verified by breaking it rather than by reading it.

1. **Baseline.** All four suites green: the new spec plus the three neighbours
   touching the same files — 14 tests, 14 passed (50.9s).
2. **Broke it.** `web/public/app.js`: `if (window.htmx) htmx.process(body);` →
   `if (false) htmx.process(body);`. Re-ran the new spec: **FAILED in 9.7s with a
   real assertion error**, not a compile or syntax error —
   `expect(locator('.basket .line-name')).toHaveCount(expected)`, expected 1,
   received 0, at spec line 165. That is step (6): a plain item's tile tapped
   inside the modal no longer scans to the basket, because its `hx-post` was never
   processed.
3. **Isolated the modifier branch too.** Step (6) fails first and masks step (7),
   so with `htmx.process` still disabled I temporarily removed step (6)'s two
   basket assertions and re-ran with `--timeout=120000`. The test then hung for
   the full 120s on step (7)'s `waitForResponse('/ui/pos/modifiers')` — the nested
   `hx-get` modifier picker never fires either. Both branches the card calls out
   are genuinely covered. (This is also finding F6.)
4. **Reverted both files** (`git checkout -- web/public/app.js`, spec restored
   from a byte copy) and re-ran: **1 passed (21.6s)**. Working tree contains
   neither temporary edit.

## Merge-conflict resolution audit

`internal/pages/common/state.go` was called out as the risky hand-resolved file —
the shape where one feature's Load/Save wiring silently disappears while resolving
the other's. Audited both features explicitly:

- **This card:** `KeyCategoriesTabEnabled` read in `LoadState` (~:356) **and**
  written in `SaveState` (~:526). ✓
- **The concurrently-merged ut-docs#2308:** `KeyBasketPanelWidth` still read in
  `LoadState` (~:336, via `ClampBasketPanelWidthRem`) **and** written in
  `SaveState` (~:544–548), including the `BasketPanelWidthRemChanged`
  reset-to-empty branch. ✓
- `internal/pages/common/deps.go`: both `CategoriesTabEnabled` and
  `BasketPanelWidthRem`/`BasketPanelWidthRemChanged` present; the latter is still
  cleared in both places (:309, :339). ✓
- No conflict markers anywhere in the tree (`.go`/`.html`/`.json`/`.js`/`.css`/
  `.md`/`.ts`).
- `web/ui/pages/settings.html`: both cards present and adjacent, neither clobbered.
- Locales: identical key sets across `{en,ar,fa,tr}`, no duplicates, no orphaned
  partial JSON.
- **Behavioural proof, not just structural:** ut-docs#2308's own 8-test
  `pos-divider-resize-2308.spec.ts` passes post-merge, as do
  `sale-screen-category-tabs-search-418` (4) and `sell-tile-long-press-2285` (1).

## Other checks

- **Repository pattern** — no new SQL. `TopLevelCategoryTiles` is pure Go over
  already-fetched `[]Button` / `[]data.CategoryNode`; no new query, no new repo
  method needed. `guard-data-access.sh` green (the raw `INSERT`s in
  `settings_categories_tab_test.go` are in a `_test.go` file and accepted).
- **The two recurring pipeline bug classes** — both genuinely N/A and confirmed,
  not assumed: the Go-side diff matches no `os.` / `filepath.` / `paths.` /
  `WriteFile` / `MkdirAll` / `Create(`. The feature writes one KV setting and
  renders in memory; there is no new disk I/O at all, so there is no
  `os.MkdirAll` to miss and no cwd-relative path to get wrong.
- **Offline-first** — nothing added to the sell-screen hot path. The picker
  copies already-rendered DOM instead of fetching (explicitly, and that is
  precisely why `htmx.process()` is needed); the only network call is the local
  settings POST from the Settings page. The modal is a non-modal `.show()` sized
  `min-width: min(40rem, 92vw)` at `inset-inline: 0; margin-inline: auto`, so it
  never covers the nav rail — status / lock / exit-to-OS stay reachable,
  CLAUDE.md §10, same family as `#table-add-modal`.
- **Kiosk isolation** — N/A and confirmed: nothing here registers under
  `/self-order` or `/api/self-order/*`; `guard-kiosk-engine.sh` green. No
  `kiosk-engine-guard:allow` needed.
- **RTL** — zero physical `left`/`right` in the added CSS or templates. The new
  rules use `border-inline-start`, `inset-block-start`, `inset-inline`,
  `margin-inline`; the only direction-ish declaration is `text-align: center`,
  which is neutral. The tab-bar arrow-key handler already flips on computed
  `direction`, and `focusTab` selects `[role=tab]` generically, so the new tab
  joins keyboard nav with no index assumption.
- **Elevation gating** — `POST /api/settings/categories-tab` is the same shape as
  its neighbour `POST /api/settings/allow-negative-inventory`, step for step:
  `ParseBool` → 400, `checkOrElevate` → `renderElevationPrompt` with on/off
  summary keys and the `enabled` hidden field, `SaveState`-then-`SetState` (not
  `UpdateState`, per ut-docs#157), `settingsAudit`, `settingsRespondSaved`. The
  settings.html checkbox mirrors the sibling's markup and page-local `T` lookup
  exactly. Consistent — no deviation to flag.
- **Test data** — `Sheet2283 …` / `S2283…` prefixes with a per-run suffix.
  Synthetic; no real shop or client name anywhere.
- **Help manual** — the English sentence is appended to step 1, the same step that
  already explains the tab strip, which is the findable place for it; it reads
  naturally and names both the setting's location and its exact label. The ar, fa
  and tr versions are real translations of the new sentence (not the English
  string left in place, not machine garble), each naming Settings → Display and
  using the same wording as that locale's own
  `settings.display.categories_tab_label`. Note the *rest* of step 1 is still
  untranslated English in ar/fa/tr — pre-existing, already recorded in the
  help-drift baseline, not introduced here.
- **Screenshots** — see F1. After the fix, `manifest.json` is internally
  consistent and `guard-docs-shots.sh` passes for real (green output, not a skip).

## Gate results (final, after the F1 fix)

| Gate | Result |
|---|---|
| `gofmt -l .` | clean (no output) |
| `go build ./...` | pass |
| `go vet ./...` | pass |
| `go test ./...` | pass (full suite, exit 0) |
| `golangci-lint run ./...` | **0 issues** |
| `guard-i18n.sh` | pass — 1,715 keys resolve, all locales match en.json, no duplicates |
| `guard-data-access.sh` | pass |
| `guard-kiosk-engine.sh` | pass |
| `guard-help-topics.sh` | pass |
| `guard-help-drift.sh` | pass (exit 0; output is pre-existing baselined drift) |
| `guard-docs-shots.sh` | pass — 31 topics × 4 locales fresh |
| `guard-compliance-claims.sh` | pass — 343 files scanned |
| Playwright ×4 suites | **14 passed** (new spec + `pos-divider-resize-2308`, `sale-screen-category-tabs-search-418`, `sell-tile-long-press-2285`) |

## Deferred / out of scope

- F2, F3, F4, F5, F6, F7 above — accepted, none blocking.
- Drilling into subcategories from a category tile: out of scope per the card's
  own UX decision (flat, top-level only). The nested `cat-panel-<childID>` ids
  already exist if that is ever picked up — see F3 first.
- The Categories tab does not render when the till has fewer than two top-level
  groups (`$hasTabs`), since there is no tab bar at all in that case. Degenerate
  and consistent with the existing **All** tab; noted, not changed.
- `web/locales/en.json` gained 5 keys, so `ut-plugin-language-{de,es}` need
  follow-up PRs; `lang-pack-drift` is advisory on this PR and blocking on push to
  `main`, so `main` shows red on that check until the pack PRs land — expected,
  owned by the same pipeline lane.

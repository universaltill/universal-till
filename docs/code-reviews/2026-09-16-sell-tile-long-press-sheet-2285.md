# Review: sell-screen tile long-press sheet (ut-docs#2285)

**Card:** universaltill/ut-docs#2285 · **Lane:** `lane:local` · **Branch:** `feat/2285-tile-long-press-sheet`
**Dev:** Sonnet subagent (complexity:medium) · **Independent review:** Opus subagent in an isolated worktree, on the pre-review snapshot `6add9f14`

## What shipped

A long-press (~500 ms hold, cancelled by >10 px movement / pointerup / pointercancel) or a right-click on a sell-screen tile opens a small **non-modal** sheet (`#tile-sheet`, `.show()` — never `showModal()`, so the nav rail's status/lock/exit stay reachable, ut-docs#1385/#1999) with:

- **Move earlier / Move later** — `POST /api/buttons/move code dir` — server-authoritative: `ui.ButtonStore.Move` relocates the tile next to its nearest **same-category** neighbour in the global quick-button order (`sameCategoryNeighborIndex` is the single definition both the sheet's edge-disabling and the move use). The sheet re-renders in place after each move.
- **Remove from quick buttons** — the existing `POST /api/buttons/remove`, with a localized `hx-confirm`; reversible from the Designer.
- **Edit in catalog** — `/catalog?item=<id>&return=/` (ut-docs#1914's existing deep-link); `catalog.html` navigates back on the dialog's `close` event (explicit Close, and the 1.5 s post-save auto-close).
- Close / tap-outside / Escape. Satellite tills get every action disabled + `designer.error.replica_use_primary`.
- Every add/remove/reorder/move response sets `HX-Trigger: buttons-changed`; the `.products` root listens and refetches.
- i18n: 5 new keys in en/ar/fa/tr (translated in-session per ut-docs#2291); packs de/es follow in the same cycle. Help topic `till-designer` gains a "hold a tile" paragraph in all five help locales; docs-shots regenerated.

Deferred by BA (filed, not dropped): **#2311** in-place item dialog over the sell screen (hard; collides with #2211), **#2312** a `catalog_management` permission — today `/designer`, `/api/buttons/*` and `/catalog` mutations carry no gate at all, so "same elevation as the Designer" is vacuous and gating only the sheet would be inconsistent.

## Independent review findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| M1 | Major | Click-swallow window was stamped when the sheet **opened** (t+0.5 s), not when the finger **released**; a hold ≥1.5 s on a tile not covered by the sheet added the item AND opened the sheet. Reproduced live by the reviewer at 1.8 s. | **Fixed** — `pointerup` re-arms the window from the release. e2e step (2b) holds 1.8 s on a tile whose centre is outside the sheet and asserts the basket is unchanged; verified failing with the fix reverted (basket 2). |
| M2 | Major | `return` guard rejected `//host` but not `/\host`, which the WHATWG parser treats as `//host` — Chromium verifiably left the origin. | **Fixed** — resolve against `location.origin`, keep only same-origin path+query+hash. Bypass shapes (`//`, `/\`, scheme, `javascript:`) checked in node. |
| M3 | Major | Move from inside a category tab reset the grid to All and cleared search (`buttons-changed` outerHTML-swaps the Alpine root). New to the sale screen: `modifiers-changed` was never emitted while an operator was on `/`. | **Fixed** — `restoreGridState()` `x-init` restores tab/q from a page-level stash the `$watch`es keep current, only for a tab that still exists. e2e (4b) asserts the tab stays selected; verified failing with the fix reverted. |
| m1 | Minor | Gesture handlers matched the Designer's admin tiles too (right-click blocked, slow ▲▼ taps eaten, haptic). | **Fixed** — every entry point bails when the page has no `#tile-sheet`. |
| m2 | Minor | A 404 for the sheet still opened it showing the previous tile's actions. | **Fixed** — blank first, `show()` only when the swap produced content. e2e (2c) routes the fetch to 404 and asserts the dialog stays hidden and empty. |
| m3 | Minor | Comments claimed `HX-Trigger` reaches another open tab (it only dispatches in the requesting document). | **Fixed** — comments corrected in `buttons.go` and `buttons.html`. |
| m4 | Minor | No committed test for the `nb < idx` insert branch of `Move`. Reviewer's 14-case throwaway matrix passed. | **Fixed** — `TestButtonsMove_EarlierOverOtherCategory` (C earlier over B → C,A,B; then adjacent). |
| m5 | Minor | de/es packs lack the new keys until their PRs land. | Expected for brand-new keys (dev skill rule); pack PRs land this cycle. |
| nit | — | dead `pointerleave` listener; help-drift baseline regenerated with a 1,076-line reorder. | **Fixed** — listener removed; baseline rebuilt with only the four changed entries in `main`'s order (12/12 lines). |
| nit | — | `TestTileSheet_RendersActions` contains-`"A"` assertion is weak; `.btn:disabled` is opacity-only (pre-existing global rule). | Accepted; disabled state is native and announced by AT. |

Clean (reviewer): Move arithmetic in all four cases incl. the uncategorised bucket; html/template escaping with a hostile label (`"`, `'`, `<b>`, `%s`, `&`) in `<h3>`, `hx-confirm`, `hx-vals`, `href`; listener registration once on document; `htmx.ajax` promise semantics in vendored 1.9.12; capture-phase ordering; `pointercancel` on scroll takeover; `close`-event navigation never precedes the save response; layout at 360×640 and 1024×600 in tr/fa/ar, RTL chevrons mirror, targets ≥51 px; no SQL outside `internal/data`, no file writes.

TDD re-verification (reviewer, in the worktree): `sameCategoryNeighborIndex` made to ignore `CategoryID` → `TestSameCategoryNeighborIndex` (5 subtests), `…_UncategorizedShareAnEmptyCategory`, `TestTileSheet_RendersActions`, `TestButtonsMove_WithinSameCategoryOnly` fail; restored → pass.

## Verified beyond automated tests

- **Real touch hardware:** TECLAST P50T (Android, 1280×800), the till served from this branch on the LAN and driven in the tablet's Chrome via `adb shell input`. Tap adds to basket; 0.8 s hold opens the sheet without adding; Move later swaps and persists (sheet stays open, edge disabled); a drag starting on a tile scrolls the grid, no sheet, no add; Edit opens the catalog item dialog with `?item=…&return=/`; Save returns to `/` with the save persisted (`items.base_price` 210→230 in the DB). After the review fixes: a 2.2 s hold on the Drinks tab → sheet open, basket 0, and the Drinks tab stays selected after Move later.
- Screenshots looked at: Playwright at 1024×600 and 360×640 (nav rail reachable; sheet above the phone-width pay bar); tablet screenshots of every step above.
- Found on the tablet, pre-existing, filed as **#2314**: a catalog price edit never ends an active `price_history` row, so the demo item kept charging £2.10 after being saved at £2.30 — display consistency was fixed by #2228/#2258/#2260, the edit path was not.

## Gate

`go build ./... && go vet ./...` clean; `gofmt -l internal/` empty; guards i18n / data-access / help-topics / help-drift / htmx-loaded / page-http-error / docs-shots green; `go test ./internal/ui/ ./internal/httpx/ ./internal/pages/` green (pages 214 s); e2e `sell-tile-long-press-2285`, `designer-reorder-1221`, `category-switch-stale-tile-add-1433`, `catalog-item-form-1956`, `catalog-row-oob-1363` green (26/26 before the fixes, the extended spec green after, and verified red with M1/M3 reverted against a rebuilt server). Pre-existing, unrelated: 4 `internal/pages/catalog` image_upload_test failures on Go 1.27/darwin (identical on `main`).

## Verdict

Safe to merge (regular merge). Same-cycle follow-ups: de/es pack PRs for the 5 keys (`main` red on `lang-pack-drift` until they land — expected for brand-new keys); no per-card release (v0.17.0 batch rule).

# Code review — ut-docs#2339: sell-screen quick-button "jiggle" edit mode

**Date:** 2026-09-17
**Branch:** `fix/2339-jiggle-mode-reorder`
**Reviewer:** independent review (different model), diff read cold — no access to the implementation reasoning.
**Verdict:** **Safe to merge** after the three fixes recorded below (all applied and re-verified on this branch).

---

## 1. What shipped

The ut-docs#2285 per-tile long-press **action sheet** is fully removed and replaced with an
iOS-springboard-style **edit mode over the whole grid**.

**Interaction.** A ~500 ms hold (or a right-click / `contextmenu`) on any `#buttons-grid` tile puts the
grid into `.jiggle-mode`: every tile wobbles in place (`@keyframes ut-jiggle`, rotate-only), grows two
corner badges — **edit** (a plain `<a>` into `/catalog?item=…&return=/`) and **remove** (the same
`hx-post /api/buttons/remove` + `hx-confirm` the retired sheet used) — and becomes drag-reorderable.
**Done / Escape / a tap outside the grid** exits, and that is the one moment the order is persisted via
the pre-existing `POST /api/buttons/reorder`.

**Drag is Pointer Events, never HTML5 DnD** (HTML5 DnD was already proven unusable on this product's
touchscreen hardware — ut-docs#1221). Same shape as `tables.html`'s floor-plan editor, adapted from a
freeform XY canvas to a grid: each move hit-tests the pointer against sibling cells and moves the
dragged cell in the DOM once it crosses a sibling's midpoint **along the reading direction**, so RTL
flips for free. Siblings FLIP-animate on their `.tile-cell` wrappers.

**Global-order bookkeeping.** `/api/buttons/reorder` rewrites `sort_order = index` for every code it
receives, so it needs the full global list — but the sale grid's DOM order is grouped by category.
`ui.ButtonVM.Pos` (new, set by `BuildCategoryGroups`) renders as `data-pos`; `orderedCodes()` re-deals
each `.grid`'s tiles into the global slots that grid already occupied, so a drag inside one category
never disturbs another's — the same outcome the retired server-side `ButtonStore.Move`
("nearest same-category neighbour") produced, computed client-side.

**Markup.** Each tile is now wrapped in `<div class="tile-cell">` holding the tile **and its two badges
as siblings**. At rest the wrapper is `display: contents`, so the resting grid is pixel-identical to
before; Alpine's `x-show="matches($el)"` moved to the wrapper.

**Removed as dead code:** `GET /ui/pos/tile-sheet`, `POST /api/buttons/move`,
`web/ui/partials/tile_sheet.html`, `<dialog id="tile-sheet">`, `ButtonStore.Move`, `TileSheetView`,
`BuildTileSheetView`, `ErrButtonNotFound`, `findButtonIndex`, `sameCategoryNeighborIndex`,
`tileSheetRenderer`, `renderTileSheet`, the `.tile-sheet*` CSS and its RTL mirroring rule,
and `e2e/tests/sell-tile-long-press-2285.spec.ts`.

---

## 2. Independent findings

| # | Severity | Finding | Disposition |
|---|---|---|---|
| **R1** | **Medium** | **Unsaved reorder silently lost when leaving via the edit badge.** `web/public/app.js` — the edit badge is a plain `<a href>`; following it tears the document down. The author closed every *other* teardown path in this family (`htmx:confirm` for the remove badge, `htmx:beforeRequest` for a grid refetch) but not a plain navigation. Reproduced live: drag to reorder, tap the pencil, **zero** `/api/buttons/reorder` POSTs, order reverted on return. The shipped manual explicitly promises the order is saved — this flow quietly breaks that. | **Fixed** — new delegated click handler on `#buttons-grid .tile-badge-edit` persists first, then navigates. Covered by a new e2e step (5a); proven red without the fix. |
| **R2** | **Medium** | **False-pass test guarding the nested-interactive-element bug class.** `internal/pages/buttons_api_test.go` — the sibling assertion was `strings.Contains(body, "<button class=\"btn-tile") && !strings.Contains(body, "</button>\n    <span class=\"tile-badges\"")`. **Both** substrings are absent from real output (product-tile renders `class=` on the *next* line), so the condition short-circuits to `false` and can never fire. Proven: moving the badges *inside* the tile's own `<button>` — exactly the invalid HTML the test claims to guard — still passed green. The shipped markup is correct; the guard protecting it was inert. | **Fixed** — rewritten against a real `golang.org/x/net/html` parse (same WHATWG tree-construction parser, and the same precedent `internal/pages/elevation_test.go` already sets). Asserts no `<a>`/`<button>` strictly inside any `.btn-tile`, and that each badge sits in `.tile-cell > .tile-badges` with no `.btn-tile` ancestor. Proven red against the nesting probe. |
| **R3** | Low (visual) | **The dragged tile's own badges stay behind mid-drag.** `web/public/app.css` — badges anchor to `.tile-cell`, which stays put while the tile rides an inline `translate` under the finger. Measured live: tile moved 25 px, badges moved **0 px** and remained visible, hovering over the empty slot the tile just left. | **Fixed** — `.jiggle-mode .tile-cell > .btn-tile.dragging ~ .tile-badges { display: none; }`. A general-sibling selector, not `:has()`, so it needs nothing extra from WebKitGTK. Verified: mid-drag the dragged badges hide, siblings keep theirs, and they return on drop. |
| n1 | Nit (accepted) | The badges reuse the `tile_sheet.*` locale keys (`.aria`/`.edit`/`.remove`/`.remove_confirm`) although the sheet is gone. The keys are correct and actively used; only the namespace name is now historical. | **Accepted** — renaming would churn 4 locale files and the external `ut-plugin-language-{de,es}` packs for zero user-visible gain. |
| n2 | Nit (accepted) | `web/help/img/manifest.json`'s `algorithm` string describes the surface fileset as `web/ui/** + non-test internal/pages/**.go`, but `web/public/**` is clearly hashed too (this branch changed only `web/public/**` and the hash moved). The guard's own error message names `web/public/**`; the manifest's prose is stale. | **Accepted** — cosmetic, in generated metadata. Noted for a future docs-shots touch. |
| D1 | Deferred — **suggest a new Backlog card** | **`make docs-shots` is not byte-deterministic across days.** A full regeneration on this tree rewrote **43 PNGs on pages this change cannot touch** (`reports`, `catalog`, `invoices`, `users`, `option-sets`, `order-status`, `open-orders`, `my-reports`, `voucher-import`, `kiosk-counter-orders`, `bluetooth-devices`) with a handful of bytes' difference each — consistent with date/clock-dependent rendered content, not layout. Notably `sell.png` (the page this card *does* change) came back **byte-identical**. `docs-shots-determinism.yml` (ut-docs#2184) compares two runs on the same *day*, so this class of drift is invisible to it. | **Deferred, out of scope** — pre-existing, unrelated to this card. Worth a card to freeze the clock (or mask date-bearing regions) in the docs harness. |

### Checks that came back clean

- **(a) Nested interactive elements** — verified in the *rendered* tree, not just the comment: badges are
  real siblings of `.btn-tile` inside `.tile-cell`; no `<a>`/`<button>` anywhere inside a tile button.
  (The *claim* was true; the *test* asserting it was not — see R2.)
- **(b) Request-per-user-action** — read the call site, didn't trust the comment. `exit()` is the single
  persist point and is guarded by `if (dirty)`; `enter()` and every drag step are pure DOM. The e2e
  asserts `toHaveLength(0)` on entering and on dragging, and `toHaveLength(1)` on Done. The two htmx
  hooks each clear `dirty` before persisting, so no path double-posts. Verified live.
- **(c) RTL** — every new/changed rule for the badges, jiggle mode and the Done bar uses logical
  properties (`inset-inline-start/-end`, `inset-block-start`, `inline-size`, `block-size`). Grepped the
  new CSS block for `left:`/`right:`/`margin-left` &c. — **none**. The `.left`/`.right` reads in `app.js`
  are `getBoundingClientRect()` pointer geometry (viewport-absolute, correct), and `reorderAt()` flips
  its midpoint test through `isRTL(gridEl)`. The retired `.tile-sheet-move` RTL mirroring rule went with
  the sheet.
- **(d) i18n** — one new key, `buttons.edit_mode.title`, present in **all four** of
  `web/locales/{en,ar,fa,tr}.json` with genuine translations (not English literals). `common.done` is
  reused. `guard-i18n.sh` passes, including its inline-JS check — the JS surfaces errors through
  `#pos-alert`'s template-populated `data-msg-server`/`data-msg-network`, never a hardcoded string.
- **(e) Touch targets** — the visible dot is 22 px but the badge **element** is 44×44 CSS px, asserted
  on the real box in e2e (`eB.width/height >= 44`). Checked the two failure modes the comment doesn't:
  `.grid` columns are `minmax(7rem, 1fr)` = **112 px** minimum, so the two 44 px boxes (spanning `[-8, 36]`
  and `[W-36, W+8]`) cannot overlap each other; across the 8 px grid gap neighbouring boxes do overlap,
  but the *dots* stop 3 px inside each cell edge, so a finger on a dot always hits that dot's badge.
  Confirmed no ancestor clips the 8 px overhang (`.products` is `overflow: auto` but the sticky Done bar
  keeps row 1 clear).
- **(f) Accessibility** — both badges carry real `aria-label` **and** `title`; the edit badge is an `<a>`
  and remove a `<button>`, so both are natively tabbable. The full keyboard path is real and I drove it
  end to end: **Shift+F10** on a focused tile fires `contextmenu` → enters the mode (confirmed live,
  `jiggle-mode = true`), **ArrowLeft/ArrowRight** moves a focused tile (RTL-flipped, covered by e2e step
  4a), **Escape** or tabbing to Done exits and persists. `exit()` also re-homes focus off the Done button
  before hiding it. The Done bar is `role="status" aria-live="polite"`.
- **(g) Dead-code removal** — grepped the whole repo for every removed route path, template name,
  element id and Go symbol. The only surviving hits are explanatory comments, the historical
  `docs/code-reviews/2026-09-16-…-2285.md` record, the still-used `tile_sheet.*` locale keys, and a new
  regression assertion that the retired routes are gone. **Nothing live still references them.**
- **(h) Offline-first** — `persistOrder()` is a plain `fetch` to the till's own localhost Go backend,
  never a cloud round-trip; nothing on the sell screen gained a network dependency. Failure paths
  surface the server's own localized text (or the generic network message) and refetch the grid so the
  DOM can't keep showing an order that never took. Nothing overlays the screen, so the nav rail's
  status/lock/exit stay reachable throughout (ut-docs#1999).
- **(i) Repository pattern / secrets / test data** — `guard-data-access.sh` passes; no raw SQL added
  outside `internal/data`. No hardcoded real shop or client names (e2e fixtures are
  `Jiggle2339 Item A <run-suffix>`), no literal secrets anywhere in the diff.
- **(j) Manual** — see §4.

---

## 3. TDD re-verification (performed personally, in this worktree)

Each claim was reverted, re-run to confirm a **red** with the right symptom, then restored and
re-confirmed **green**.

### 3.1 The `lostpointercapture` bug (the card's headline TDD claim) — **confirmed genuine**

The claim: moving an element in the DOM makes the browser drop pointer capture, so ending the drag on
`lostpointercapture` (as `tables.html` does) killed every drag after its first crossing; fixed by ending
only on `pointerup`/`pointercancel` and re-acquiring capture after each DOM move.

**Revert applied** to `web/public/app.js`: added a `lostpointercapture` listener in `capture()` that
calls `endDrag(ev)` (the pre-fix, `tables.html` shape), and removed the `capture()` re-acquire in
`moveCell()`.

```
npx playwright test tests/sell-tile-jiggle-mode-2339.spec.ts   →  1 failed, 1 passed
  Error: expect(received).toEqual(expected)
    Array [
      "JIG2339BC-B-…",
  -   "JIG2339BC-C-…",
      "JIG2339BC-A-…",
  +   "JIG2339BC-C-…",
    ]
  at sell-tile-jiggle-mode-2339.spec.ts:245  (after dragPast(tileA, tileC))
```

Dragging A past B *and* C produced **[B, A, C]** instead of **[B, C, A]** — the drag died after the
**first** crossing, exactly the symptom the code comment records. (The second test still passed: with
only two tiles it needs just one crossing, which is consistent.)

**Restored** (`git checkout web/public/app.js`) → `2 passed (30.6s)`.

### 3.2 `ButtonVM.Pos` global-sort-index test — **confirmed genuine**

Revert: replaced `vm.Pos = i` in `BuildCategoryGroups` (`internal/ui/buttons.go`) with `_ = i`.

```
go test ./internal/ui/ -run TestBuildCategoryGroups_PosIsGlobalSortIndex
  --- FAIL: Pos[C] = 0, want 2 (global sort index, not the in-group index);
            all: map[A:0 B:0 C:0 U:0]
```

Restored → `ok github.com/universaltill/universal-till/internal/ui`.

### 3.3 Template-markup test — **partly genuine, partly false-pass (R2)**

- *Genuine half:* stripping `data-pos="{{ .Pos }}"` from `product-tile` →
  `FAIL … missing "data-code=\"J1\" data-item-id=\"itm1\" data-pos=\"0\""`. Restored → `ok`.
- *False-pass half:* moving the badge block **inside** the tile's own `<button>` → **still `ok`**.
  That is R2. After the rewrite, the same probe → `FAIL: a .btn-tile must contain no nested <a>
  (invalid HTML the parser restructures), got 1`. Restored → `ok`.

### 3.4 The R1 fix's own new coverage — proven red without the fix

With the persist-before-navigate handler short-circuited, the new e2e step (5a) fails by timing out on
the `/api/buttons/reorder` POST that never arrives. Restored → green.

---

## 4. User manual

The manual ships with the feature and **describes the new interaction, not the old sheet**. Verified by
reading all five locales.

- `web/help/{en,ar,de,fa,tr}/sell.md` each gained a real, genuinely translated
  *"Rearranging the quick buttons"* section covering: hold-or-right-click to enter, the wobble, both
  corner badges (and that removing a tile leaves the item in the catalog), drag-to-reorder with tiles
  moving aside, the within-category-only constraint, all three exits, **"saved once, at that moment …
  nothing is sent while you are still dragging"**, the replica refusal, and *"None of this needs the
  network"*.
- `web/help/en/till-designer.md`'s cross-reference was rewritten — it no longer describes "move it,
  remove it, or edit it" via a sheet.
- No stale references to the popup sheet survive anywhere under `web/help/`.
- Screenshots: `web/help/img/{en,ar,fa,tr}/sell.png` were regenerated in the branch. I re-ran the real
  `make docs-shots` (124 shots, 2.3 min) and **`sell.png` came back byte-identical in all four
  locales** — the author's regeneration is correct and current. (`guard-help-drift.sh` baseline entries
  for `sell` were updated in step with the new heading, and the guard passes.)

---

## 5. Gate — commands actually run, and their results

Run in this worktree at the reviewed tree (post-fix):

| Command | Result |
|---|---|
| `gofmt -l .` | clean (no output) |
| `go build ./...` | OK |
| `go vet ./...` | OK |
| `go test ./...` | **PASS** (exit 0, whole module) |
| `golangci-lint run ./...` | **0 issues** |
| all 28 `scripts/ci` guards from `ci.yml`'s `build` job | **26 PASS**, 2 environment-only failures (below) |
| `make docs-shots` | 124 passed; `sell.png` byte-identical (see §4 / D1) |
| `bash scripts/ci/update-docs-shots-surface-hash.sh` + `guard-docs-shots.sh` | manifest `surface_sha256` bumped `fdb613c26e3e…` → `894e95b083d9…` (one field only); guard **passes** |
| `npx playwright test tests/sell-tile-jiggle-mode-2339.spec.ts` | **2 passed** |
| e2e regression batch — 13 specs touching `.btn-tile` / `#buttons-grid` / `/api/buttons` | **58 passed** |
| final e2e re-run (jiggle + 8 regression specs, post-fix) | **40 passed** |

Guards passing individually include `guard-i18n.sh`, `guard-data-access.sh`, `guard-htmx-loaded.sh`,
`guard-help-topics.sh`, `guard-help-drift.sh`, `guard-docs-shots.sh`, `guard-compliance-claims.sh`,
`guard-kiosk-engine.sh`, `guard-page-http-error.sh`, `guard-e2e-fixtures-import.sh`,
`check-brand-assets.sh`, `guard-makefile-version.sh`.

**The two guard failures are environmental, not diff-related:**

- `guard-shellcheck-version.sh` — *"no 'shellcheck' binary found on PATH"*. Not installed in this
  sandbox; CI's `ubuntu-latest` preinstalls it. This diff adds no shell script.
- `guard-deadcode-baseline.sh` — `deadcode` itself fails to load `cmd/unitill-desktop` /
  `internal/thirdparty/webview_go`: *"Package 'gtk+-3.0' … not found"*. No GTK/WebKit headers here; CI
  runs this in a separate job that `apt-get`s them. Same reason `.golangci.yml` excludes that package
  (ut-docs#1581).

The e2e regression batch matters most for this diff: wrapping every tile in `.tile-cell` and moving
Alpine's `x-show` onto the wrapper is the riskiest structural change here, and the category-tab,
search-filter, Categories-picker, Designer-reorder and category-colour specs all still pass.

---

## 6. What I could **not** verify

Stated plainly, so nobody reads more into this review than it earned:

- **Real touchscreen hardware.** Everything here was driven by Playwright's synthetic pointer in
  headless Chromium — real `PointerEvent`s, but `pointerType: "mouse"`, not a finger, and **not
  WebKitGTK**. Untested: palm rejection, a second finger mid-drag, the products panel's own momentum
  scroll competing with `touch-action: none`, and whether the 500 ms hold feels right under a real
  thumb on the pilot till. The spec's own HONESTY NOTE says the same. **This needs a pass on the
  hardware lane before it reaches a shop.**
- **The wobble's *look*.** Asserted structurally (`animationName === 'ut-jiggle'`, and that
  `animation-duration` is `0s` under `prefers-reduced-motion` and non-zero without it) — nobody
  eyeballed the motion.
- **RTL visually.** Verified by code (logical properties only, `isRTL()`-flipped drag and Arrow keys)
  and by the existing fa/ar specs passing — but no one looked at jiggle mode rendered in Farsi or Arabic.
- **`guard-shellcheck-version.sh` / `guard-deadcode-baseline.sh`** — could not be executed here (§5).
  `deadcode` in particular is the one guard that would independently confirm the large dead-code removal
  left nothing orphaned; I substituted an exhaustive manual grep (§2g), which found nothing.

---

## 7. Verdict

**Safe to merge.** The design is sound and notably careful: zero network calls until Done, the
`data-pos` global-slot re-deal preserving the retired server-side `Move` semantics exactly, logical
properties throughout, a real keyboard path, and a genuinely translated manual shipped in the same
branch. The dead-code removal is complete and verified.

Three real defects were found and fixed here — one silent data loss (R1), one inert test guarding the
exact bug class this pipeline keeps finding (R2), and one visual defect during the drag (R3) — each
re-verified red-then-green. D1 (docs-shots non-determinism) is real but pre-existing and out of scope;
it should become its own Backlog card.

Merge gated only on the standing hold tracked separately on **ut-docs#2277**, and on a real-hardware
pass before this reaches a pilot shop.

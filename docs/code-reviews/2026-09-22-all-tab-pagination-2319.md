# 2026-09-22 — Sell-screen All-tab pagination (ut-docs#2319)

- **Repo / branch:** `universal-till` @ `fix/2319-all-tab-pagination`
- **Reviewer:** independent Opus subagent, fresh context, did not write this code
- **Verdict:** **yes-with-fixes** — safe to merge with the two fixes below,
  applied and re-verified.

---

## 1. What shipped

Caps the sell screen's All tab (`#buttons-grid-all`) at `AllTabPageSize = 200`
items per response, with a "Load more" button that pulls subsequent pages from
a new `GET /ui/buttons/all/more?offset=N`. Before this: `GET /ui/buttons`
inlined EVERY active catalog item into the All grid on every render —
including the whole-document `modifiers-changed`/`buttons-changed` refetch
buttons.html's root wires up on any modifier/button-config edit — measured
~998KB of HTML at 2000 active items, a visible stall on a Raspberry Pi kiosk.

- `internal/ui/buttons.go` — new `AllTabPageSize` const, `pageButtons(all,
  offset) (page, hasMore)` helper, `List` now renders `ToVM(allPage)` plus
  `AllHasMore`/`AllNextOffset`, new `AllMore` handler.
- `internal/pages/buttons_api.go` — registers `/ui/buttons/all/more`,
  byte-for-byte the same renderer-construction shape as the `/ui/buttons` and
  `/ui/buttons/search` registrations beside it.
- `web/ui/partials/buttons.html` — new `all-more-button` and
  `all-more-fragment` defines; the All grid calls the former when
  `.AllHasMore`.
- `web/public/app.css` — `.all-more-btn { grid-column: 1 / -1; }` (one
  property).
- `web/public/app.js` — an `htmx:afterSwap` listener restoring keyboard focus
  after the button's self-replacing swap (a real bug found during this card's
  own UX pass: htmx drops focus to `<body>` once the button is exhausted).
- `web/locales/{en,ar,fa,tr}.json` — new `products.load_more` key.
- `internal/ui/buttons_all_tab_pagination_test.go` — new.
- `web/help/{en,de,fa,ar,tr}/sell.md` + `web/help/img/manifest.json`.

The choice of 200 is justified against ut-docs#2294's own "usable with a
200+ item catalog" bar, so a catalog at or under the cap renders
byte-identically to before (no button at all, verified by
`TestButtonsHTTPList_AllTabUnderPageSizeIsUnchanged`).

---

## 2. What was actually run (not just read)

All commands run against the real checkout.

```
go build ./...                                                    OK
go vet ./internal/ui/... ./internal/pages/...                     OK
gofmt -l internal/ui/buttons.go internal/pages/buttons_api.go \
        internal/ui/buttons_all_tab_pagination_test.go            OK (no output)
go test ./internal/ui/... ./internal/pages/...                    ok (all packages)
go test ./...                                                     ok (whole repo, zero failures)
golangci-lint run ./internal/ui/... ./internal/pages/...           0 issues.
node --check web/public/app.js                                     JS SYNTAX OK
```

Guards:

```
✓ i18n guard: 1841 template keys resolve; all locales match en.json; ...
✓ data-access guard: no inline SQL outside internal/data / internal/db
✓ htmx guard: 3 standalone template(s) using hx-* all load htmx.min.js
✓ help-topics guard: no route conflicts, every topic parses, all shipped locales complete
✓ help-drift guard: every translated topic's structure matches English (or is recorded)
✓ compliance-claims guard: 345 file(s) scanned, no forbidden fiscal-compliance claims found
✓ docs-shots guard: 31 routed topics × 4 locales screenshotted and fresh
✓ kiosk-engine guard: no self-order route handler references the cashier's Engine
✓ autofill guard: 6 standalone document(s) all load autofill.js, and its sweep is intact
✓ emoji-font guard: CSS fallback present
✓ page-http-error guard: OK
```

`shellcheck` not installed in this environment; not touched by this diff regardless.

---

## 3. Independent re-verification of the TDD claim

Two separate reverts, each from a confirmed-clean tree, restored between each.

**Revert A — ship the whole catalog again** (`List`: `allPage, allHasMore =
pageButtons(allBtns, 0)` → `allPage, allHasMore = allBtns, false`):

```
--- FAIL: TestButtonsHTTPList_AllTabCapsInitialPageAndOffersLoadMore (0.02s)
    buttons_all_tab_pagination_test.go:112: initial All grid rendered 220 tiles, want exactly AllTabPageSize (200) — the whole 220-item catalog is leaking into one response again
```

The claimed symptom exactly (220 tiles instead of 200), not an unrelated
compile/fixture error.

**Revert B — `AllTabPageSize = 999999`:**

```
--- FAIL: TestPageButtons_Bounds/first_page,_more_remain
    pageButtons(offset=0): len = 250, want 999999
--- FAIL: TestButtonsHTTPList_AllTabCapsInitialPageAndOffersLoadMore
    initial All grid rendered 220 tiles, want exactly AllTabPageSize (999999) — the whole 220-item catalog is leaking into one response again
```

**Restored and green:** all 9 All-tab tests pass, including the pre-existing
`TestButtonsHTTPList_AllTabDoesNotScaleWithCatalogSize` (that one asserts
SELECT count, not response size — no overlap, no contradiction with this
card's own claim) and `TestButtonsHTTPList_AllTabUnderPageSizeIsUnchanged`
(correctly stays green under both reverts — it pins the "small catalogs
untouched" half of scope, not the defect this card fixes).

**Verdict on the TDD claim: genuine.**

---

## 4. Findings

### F1 — `htmx:afterSwap` listener runs up to 201× per click — should-fix — **FIXED**

`web/public/app.js`. htmx fires `htmx:afterSwap` once per **inserted
element**, not once per swap (traced in the vendored `htmx.min.js` 1.9.12:
the outerHTML handler pushes every inserted node into `settleInfo.elts`, then
dispatches once per element, all carrying the same `detail.target`).
`all-more-fragment` inserts up to 200 `.tile-cell` divs plus one replacement
button, so one "Load more" tap fired the listener up to 201 times. In the
exhausted branch that re-ran `#buttons-grid-all →
querySelectorAll('.btn-tile[data-code]')` over the fully-loaded All grid 201
times — for a 2000-item catalog, ~400k element visits and 201 NodeList
allocations, on the exact Pi kiosk this card exists to speed up.

**Fix:** wrapped the listener in an IIFE (no new global) and made it
idempotent per swap using `evt.detail.xhr` as a once-per-response token.
Every dispatch happens after all nodes are inserted and after the old button
is removed, so acting on the first dispatch is correct.

### F2 — listener's own comment overstated the bug — nit — **comment corrected, no behavior change**

htmx 1.9.12 already restores focus to a same-id replacement itself (saves
`document.activeElement` pre-swap, and if it left the document and carries an
`id`, calls `getElementById(id).focus()`). So the "more remain" branch was
defence-in-depth, not load-bearing — only the **exhausted** case (no
replacement button at all) is a real gap, which the last-tile fallback
correctly covers. Comment rewritten to record this; branch kept as harmless
defence-in-depth.

Also confirmed: no stale/detached `getElementById` risk (old button is
removed before `afterSwap` fires), the last-tile fallback can't throw on an
empty grid (`if (last)` guards `tiles[-1] === undefined`), and a double-tap
is safe (htmx's own per-element queue plus a `body.contains` bail on the
detached issuing button — no duplicate tiles).

### F3 — new test's grid slice over-reads; its own comment was wrong — should-fix — **FIXED**

`internal/ui/buttons_all_tab_pagination_test.go` sliced the response from
`id="buttons-grid-all"` to the **end of the body**, reasoning "no sibling
grid after it to bleed into the count." False: `buttons.html` renders
`{{ range $g := .Groups }}` — one `#cat-panel-*` per category, each full of
`product-tile`s — immediately after `#buttons-grid-all`. The assertions only
held because the fixtures seed `items` but no `shortcut_buttons`, so
`.Groups` is empty. Proved empirically with a throwaway fixture carrying one
quick button: the old slice counted 3 tiles where the All tab really had 2.

**Fix:** extracted `allGridSlice(t, body)`, bounding the slice at the first
`id="cat-panel-` after the grid (end-of-body when there are none — correct
either way), used by both HTTP tests, with the corrected reasoning in its own
doc comment.

### F4 — `/ui/buttons/all/more` ignores `HideAllTab` — accepted as-is

Consistent with the sibling `/ui/buttons/search` registration (same file),
which is built identically and serves the full catalog with no such check
either. `show_all_tab` is a UI preference, not an access control — this
route exposes nothing an authenticated operator session can't already see.
Not a blocker; a follow-up card should cover both routes together if the
product owner wants the setting to be a real gate.

### F5 — All-tab tiles never `Locked`-stamped — pre-existing, accepted

Verified: `stampLocked(groups, h.Granted)` walks `[]*CategoryGroup` only;
`AllButtons` never passes through it. Predates this diff (ut-docs#2361 gap),
unchanged by it. `AllMore` omitting `Granted` is consistent with both the
already-rendered All tiles and `/ui/buttons/search`'s results.

### F6 — `data-pos="0"` on loaded-more tiles — pre-existing non-issue, verified

All-tab tiles were never draggable: `utTileJiggle` excludes `#buttons-grid-all`
at every entry point (`inAllGrid()` in `tileFor()`/`badgeFor()`,
`orderedCodes()`, `refreshPositions()`). The new button itself never wobbles
or gets badged (`.jiggle-mode` CSS is scoped to `.tile-cell`; the button is
neither `.btn-tile` nor `[data-code]`), and clicking it doesn't exit jiggle
mode (it's nested inside `#buttons-grid`).

### F7 — RTL safety of `grid-column: 1 / -1` — safe, verified

Grid line numbers are flow-relative (line 1 = inline-start, -1 = inline-end),
both mirror correctly under `dir="rtl"`. `.grid` uses
`grid-template-columns: repeat(auto-fill, minmax(7rem, 1fr))`, generating
explicit tracks, so the `-1` gotcha (resolving against an implicit grid)
doesn't bite. No `left`/`right` introduced.

### F8 — `.btn secondary compact all-more-btn` touch target — claim verified, sound

`.btn.compact`'s 2.5rem/40px floor is a real, product-owner-approved
(2026-08-30) deliberate touch-target convention already established
elsewhere in `app.css` (independently corroborated at the
`.order-type-option` precedent). `.btn.compact` (specificity 0-2-0) does
outrank `.all-more-btn` (0-1-0) regardless of source order, so the new rule
correctly adds only `grid-column` and no dead `min-height`.

### F9 — `pageButtons` / offset-parsing edge cases — correct, no change needed

Traced by hand: `offset < 0`, `offset == len(all)`, `offset > len(all)` all
return `nil, false`; `end >= len(all)` (not `>`) makes an exact multiple of
200 terminate cleanly with no trailing empty page. `AllMore`'s
`strconv.Atoi` failure or a negative value both fall back to `offset = 0` —
unreachable in normal use since the button always carries a server-computed
offset. A double-tap is safe (verified against htmx's own per-element queue
and detached-element bail).

### F10 — template reuse of `product-tile` — correct choice, verified

`all-more-fragment` correctly reuses `product-tile` (which carries the
client-side search filter `x-show="matches($el)")`, not
`product-tile-result` (which deliberately drops it for server-truth search
matches). All-tab tiles should filter exactly like the tiles already in the
grid, and do.

### F11 — no loading affordance on "Load more" — nit, deferred

The sell screen's tiles/search/basket all swap with no `hx-indicator`
convention either (only 5 admin/import/setup/report templates use one), so
this button matches its own screen's existing convention. A busy affordance
would be a UX design decision, not a reviewer edit — follow-up card if wanted.

### F12 — `AllMore` silently retires the button on a store error — nit, deferred

Consistent with `List`/`Search`'s existing log-and-degrade convention in
this file. Changing the error semantics (e.g. re-render the button at the
same offset so a retry is possible) is a deliberate follow-up, not a silent
reviewer edit.

### F13 — the two recurring pipeline bug classes — N/A, checked not assumed

No file writes anywhere in this diff (grepped for
`os.Create`/`os.WriteFile`/`os.OpenFile`/`os.MkdirAll`/`ioutil`: zero hits).
The `filepath.Join("web", "ui", …)` calls are shipped template assets
resolved through `ui.NewRenderer`'s embedded-FS path (already pinned by
`TestButtonsNewRenderer_WorksFromAnyWorkingDirectory`), not user data —
`paths.Data(...)` doesn't apply here, identical to the two sibling route
registrations beside it.

### F14 — secrets / real shop names — clean

Grepped for API-key/secret/password/token/private-key shapes: zero hits.
Test fixtures use generic `Item %05d`/`Bread`/`Cola`. No real shop, customer
or operator names anywhere in the diff.

### F15 — `err` shadowing in `List` — nit, no change

`allBtns, err := h.Store.LoadAllActive(r.Context())` declares a new `err`
scoped to the `if !h.HideAllTab` block. Consumed by the very next log
statement, nothing downstream reads it — no behavior change; `go vet` and
`golangci-lint` both clean.

### F16 — i18n values — fine

`products.load_more` present and non-empty/non-placeholder in all four
shipped locales. Longest (tr, "Daha fazla yükle", 17 chars) is safe because
the button spans the full grid row — no single-tile width to overflow.
`guard-i18n.sh` (which scans `web/public/**/*.js` too) passes.

### F17 — help manual — fine

All five `sell.md` locales updated in the same branch per CLAUDE.md's
"manual ships with the feature." `/ui/buttons/all/more` needs no help topic
of its own — `/ui/` is a denylisted non-page namespace in the route-coverage
check, which passes. `guard-help-topics.sh`/`guard-help-drift.sh` both green.

---

## 5. Changes made during review

1. **`web/public/app.js`** (F1/F2) — IIFE wrapper, once-per-swap dedup via
   `evt.detail.xhr`, corrected comment.
2. **`internal/ui/buttons_all_tab_pagination_test.go`** (F3) — `allGridSlice`
   helper bounding the count to the All grid's own content.
3. **`web/help/img/manifest.json`** — consequence of (1): `web/public/**` is
   part of the docs-shots surface fileset. The app.js change is a
   comment+dedupe fix, not a pixel change (docs fixtures never reach 200
   items, so the button never appears in a screenshot) — confirmed via
   `scripts/ci/update-docs-shots-surface-hash.sh`, whose diff touches only
   `surface_sha256`. The commit carries a `Docs-Shots-Unchanged: true`
   trailer per that script's own convention.

All re-verified after fixes: full gate green (see §2), all 9 All-tab tests
pass, `docs-shots` guard green.

---

## 6. Explicitly deferred

- **F4** — `/ui/buttons/all/more` not gated on `show_all_tab`; follow-up
  card if the product owner wants the setting to be a real access gate
  (should cover `/ui/buttons/search` too, for the same reason).
- **F5** — All-tab tiles never `Locked`-stamped (pre-existing ut-docs#2361
  gap, out of this card's scope).
- **F11** — no busy/loading affordance on "Load more" (needs a UX decision).
- **F12** — `AllMore` silently retires the button on a store error (error-
  semantics change, needs a deliberate follow-up).
- No real WebKitGTK/physical-touch-hardware pass — verified via Chromium
  (Playwright) at 1024×600/360px and via reading the vendored htmx source,
  not on the actual Pi kiosk hardware.

## 7. Verdict

**Safe to merge: yes.** Core change is well-reasoned, correctly bounded at
every slice boundary, consistent with this file's existing conventions,
documented in the manual and all four locales, and backed by tests proven
(by independent revert/restore) to go red on the real defect and green on
the real fix. Both should-fix findings (F1, F3) are applied and re-verified;
no blockers remain.

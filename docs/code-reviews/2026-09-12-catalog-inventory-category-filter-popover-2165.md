# Code review — ut-docs#2165: collapse the category-filter chip row into a filter icon + popover

- **Date:** 2026-09-12
- **Branch:** `feat/2165-collapse-category-filter-popover`
- **Reviewer:** independent (different model from the implementer); reviewed
  fresh against `origin/main`, having not written the change.
- **Verdict:** **Safe to merge** after the three fixes recorded below, which
  are applied in this branch's working tree.

## What shipped

Replaces the always-visible category chip row on `/catalog` and `/inventory`
(shipped by ut-docs#2119) with a small filter-icon trigger that opens a
`<dialog>` popover containing the same, unchanged chip row.

- `internal/httpx/icons.go` — new `"filter"` entry (Lucide funnel path).
- `web/ui/partials/category_filter_popover.html` — NEW partial: trigger
  button + `<dialog>` wrapping the **unchanged** `category_filter.html`.
- `web/public/category-filter.js` — adds `bindPopover(controlEl)` and
  `paintTriggerState(controlEl, active)` alongside the untouched
  `expand`/`matches`/`bind`.
- `web/public/app.css` — `.category-filter-control` /`-dialog` /`-dialog-head`
  /`-badge`, reusing the existing non-modal `.show()` dialog family
  (`#hold-modal`/`#pfand-modal`/`#elevation-modal`/`#table-add-modal`).
- `web/ui/pages/{catalog,inventory}.html` — call site swapped to the new
  partial; `bindCategoryFilter()` extended to wire the popover and paint the
  trigger's active state.
- `internal/httpx/httpx.go` + `internal/pages/catalog/handlers.go` — the new
  partial registered in both template file sets.
- New pinning test `internal/httpx/category_filter_popover_partial_test.go`;
  `sync_banner_test.go` file sets extended.
- `web/locales/{en,ar,fa,tr}.json` — new key `filter.category_active`.
- `web/help/{en,ar,fa,tr,de}/{catalog,inventory}.md` + regenerated
  `web/help/img/**`.
- `e2e/tests/catalog-inventory-category-filter-2119.spec.ts` — existing tests
  updated to open the popover first; new `describe` block for open → select →
  narrow → close → state-still-shown, Escape, focus return, and an RTL
  on-screen bounding-box check.

## Findings

### F1 — Trigger parked on its own row instead of in the list header (**medium — FIXED**)

The AC reads "a small filter icon button **in the list header** (not a
full-width row)", and the card exists because the old chip row "eats vertical
space on a 10.1" tablet". The delivered call site left the control exactly
where the chip row had been — a standalone block *below* `.page-head` — so it
still cost a whole row.

Measured at the 1024x600 kiosk floor, before the fix:

```
/catalog   page-head  y 21.25 → 72.25  (h 51, w 905)
/catalog   control    y 89.25 → 140.25 (h 51, w 51)
/inventory first card y 140.25   ← list started here
```

A 51px button occupying a 905px-wide row of its own: **68px (11% of the
kiosk viewport's height)** spent to save the chip row, on pages whose header
row had ~250px of empty horizontal space sitting unused.

It also **contradicted the help text this same branch ships**. All five
locales of `catalog.md`/`inventory.md` were updated to say "tap the filter
icon **beside the search box**" — but it was not beside the search box.

Fixed by moving the `{{ template "category_filter_popover" ... }}` call into
each page's header row, immediately beside the search input. After the fix:

```
/catalog   search ends x 943.25, control x 951.75  (8.5px apart)
/inventory search ends x 679.34, control x 687.84  (8.5px apart)
/inventory .catalog-layout now starts at y 89.25   (was y 140.25)
```

51px of vertical list space returned on both pages, and the regenerated
screenshots show one extra stock row fitting in the same viewport. The
header row stayed 51px tall on both pages — the control is now free.

RTL re-verified under `?lang=fa`: the trigger mirrors to x 21.25, left of the
search box at x 80.75, and the popover opens fully on-screen (x 325→699,
y 48→275 in a 1024x600 viewport).

### F2 — `/inventory` header overflowed at phone width, and F1 would have pushed the new control off-screen (**medium — FIXED**)

`/inventory`'s `.page-head` never carried the `page-head-wrap` override that
ut-docs#2092 added to `catalog.html` for this exact reason, so at a 360px
viewport its children were squeezed onto one never-wrapping line. This is
**pre-existing** — `h1` + a 260px-min search box + the "+" button already
totalled 456px — but F1's fix adds 59.5px to that row, and measurement
confirmed the consequence:

```
before wrap fix, 360px:  #stock-search  x 146.95 (+260 → 406.95)  OFF-SCREEN
                         #inventory-category-filter-trigger x 415.45  OFF-SCREEN
```

Fixed by adding the shared `page-head-wrap` class to `/inventory`'s head.
After: all three controls on-screen at 360px, and the popover itself opens at
x 18→342 inside the 360px viewport. Nothing wraps at 1024x600 or above, so
the kiosk floor and the screenshots are unaffected. The page's residual
`scrollWidth` of 515px is the stock table, unchanged by this branch.

Supporting CSS added: `.page-head-search-group` (a flex group so
`.page-head`'s `justify-content: space-between` does not spread the search
box and its trigger a full gap apart). Logical properties only —
`min-inline-size`, `gap` — nothing directional to mirror under RTL.

### F3 — `guard-shellcheck-version.sh` fails in this environment (**not a defect — environmental**)

`❌ no 'shellcheck' binary found on PATH — expected 0.9.0`. The guard is
reporting a missing tool in the review container, not a finding. This branch
touches no shell script, so there is nothing for `shellcheck scripts/ci/*.sh`
to flag. Every other CI-blocking guard passes (listed below).

## Checked and found clean (no change needed)

- **Registration sites.** Grepped the whole repo: only two Go sites reference
  the partial set — `internal/httpx/httpx.go`'s `renderFiles` (which covers
  `/inventory` via `inventory_page.go`'s `Render`/`RenderContentFragment`) and
  `internal/pages/catalog/handlers.go`'s bespoke `RenderWith`. Both updated;
  there is no third site. Confirmed live by both pages rendering in e2e.
- **Icon-name collision.** `"filter"` is new in `railIcons`; a duplicate map
  key would not compile, and `go build` plus the whole `internal/httpx` suite
  (including `TestNoTwoNavDestinationsShareAnIcon`) pass — no nav destination
  uses it.
- **Accessibility wiring**, read off the real server render, not the test:
  `aria-haspopup="dialog"`; `aria-controls="catalog-category-filter-dialog"`
  matching `<dialog id="catalog-category-filter-dialog">`;
  `aria-expanded="false"` initially; `aria-labelledby=
  "catalog-category-filter-dialog-title"` matching the `<h3>` id; close button
  with a translated `aria-label`. Consistent on both pages' controlIDs.
- **More than colour (WCAG 1.4.11).** The active state is a real `hidden`
  attribute toggle on a badge element (presence/absence, a shape), plus an
  accessible-name swap via `data-label-idle`/`data-label-active` — not a
  colour change. Matches the chip row's own `chip-check` precedent.
- **Badge positioning scope.** `.category-filter-trigger .btn-ico { position:
  relative }` is scoped to the one trigger class, which appears nowhere else;
  no other `.btn-ico` is affected.
- **z-index 500.** Every other dialog reachable on these two pages sits at
  500 or above *and* later in DOM order (`#item-form-modal` 500 full-bleed,
  `.modifier-groups-modal` 520, `#stock-dialog` `.record-dialog` 500
  full-bleed), so each paints over the popover rather than under it.
  `#barcode-backfill-modal` uses `showModal()` and lands in the top layer
  regardless. No collision.
- **`position: fixed` containment.** No ancestor of the control on either
  page (`.catalog-layout`, `.items-panel`, `.page-head`) applies
  `transform`/`filter`/`will-change`, so the popover is genuinely
  viewport-positioned — which is what makes the RTL on-screen assertion sound
  rather than accidental.
- **Double-binding / listener leak.** `bindCategoryFilter()` runs once per
  script-block execution; on an `/items` rail htmx swap the block re-executes
  against freshly swapped DOM, so the trigger is a new element each time and
  no listener accumulates on a live node. Verified by the existing rail-swap
  e2e tests.
- **On-screen keyboard.** `#osk` is not rendered on this till configuration
  at all, and structurally the popover is `.show()`n (non-modal), which is
  precisely the documented reason the OSK stays reachable; opening the
  popover moves focus to a `<button>`, which `osk.js` treats as a hide
  trigger. No overlap path found.
- **The two recurring pipeline bug classes** — a file-write handler missing
  `os.MkdirAll`, and a cwd-relative path where `paths.Data(...)` belongs — do
  not apply: this branch performs no file I/O.
- **RTL / logical properties.** No `left`/`right`/`margin-left`/`text-align:
  left|right` anywhere in the branch diff or in my fixes.
- **No secrets and no real client/shop names** introduced. Demo data in the
  screenshots and specs is the pre-existing seeded catalogue.

## Accepted / deferred (not fixed here)

- **`filter.category_active` is literal English in `ar.json`/`fa.json`/
  `tr.json`.** Its two siblings on the very same control
  (`filter.category_all`, `filter.category_label`) are already untranslated
  English in those three locales. Translating only the new key would make the
  popover read half-translated, which is worse than consistent. `guard-i18n`
  passes (key sets match). **Recommend a follow-up card to translate the
  whole `filter.*` block** rather than papering over one key.
- **Escape only closes the popover while focus is inside the dialog.** The
  handler is bound on the dialog element, matching `record-dialog.js`'s
  stated convention ("bound on each dialog element, not on document"), and
  `open()` focuses the close button so the keyboard path always works.
  `/inventory`'s `#stock-dialog` uses the document-bound variant instead —
  the two patterns coexist in the codebase already. Left as the implementer
  chose.
- **No `dialog.addEventListener('close', …)` to re-sync `aria-expanded`.** I
  looked for a path that closes this dialog outside the tracked
  trigger/close-button/Escape routes and found none: nothing else calls
  `.close()` on it, it is not an htmx swap target, and it contains no
  `method="dialog"` form. So there is no desync today. A `close`-event sync
  would still be cheap insurance against a future one — worth doing when this
  file is next touched, not worth churning now.
- **No close-on-outside-click.** Deliberate and documented in the source: a
  `.show()` dialog paints no `::backdrop` to hit-test, and the AC specifies
  only the close button and Escape.

## Verification performed

Beyond reading the diff:

- **TDD claim re-verified independently.** I commented out `dialog.show()` in
  `bindPopover()`'s `open()` myself (not trusting the implementer's report),
  re-ran the spec, and got **6 of 7 tests failing** with real assertion
  errors (`expect(locator).toBeVisible() failed — unexpected value "hidden"`
  at the popover/chip-row visibility lines). The 7th, the pure-function test
  for `expand()`/`matches()`, correctly stayed green — it does not touch the
  popover. Restored the file and confirmed the SHA-256 matched the original
  (`a8f6fdd1…`) and `git status` was clean again.
- **Live geometry measurement** at 1024x600 and 360x800, and under
  `?lang=fa`, via a throwaway Playwright probe (deleted before handoff) —
  this is where F1 and F2 were found; reading the diff alone would not have
  surfaced either.
- **Rendered-HTML accessibility check** against a running server
  (`curl /catalog`), not just the Go partial test.
- **Full `make docs-shots` re-run** after the layout fixes: 124 screenshots,
  all locales and topics, manifest surface now `447071c65b99…`.

### Gate results (after the fixes)

```
gofmt -l .              (no output)
go build ./...          build OK
go vet ./...            vet OK
golangci-lint run ./... 0 issues.
go test ./...           exit 0 — 57 packages ok, no FAIL, no panic
```

Guards (all under `scripts/ci/`):

```
guard-data-access             ✓ no inline SQL outside internal/data / internal/db
guard-kiosk-engine            ✓ no self-order handler references the cashier's Engine
guard-plugin-menu-read        ✓ no unlocked reads under internal/pages
guard-page-http-error         ✓ no bare http.Error in a page-route handler
guard-i18n                    ✓ 1654 keys resolve; all locales match en.json
guard-compliance-claims       ✓ 338 files scanned, no forbidden claims
guard-docs-shots              ✓ 31 topics × 4 locales fresh (surface 447071c65b99…)
guard-help-topics             ✓ no route conflicts, every page route claimed
guard-help-drift              ✓ every translated topic matches English or baseline
guard-webkit-version          ✓        guard-kiosk-launch-flags     ✓
guard-android-status-address  ✓        guard-android-i18n           ✓
guard-emoji-font              ✓        guard-htmx-loaded            ✓
guard-autofill-suppression    ✓        guard-e2e-fixtures-import    ✓
check-brand-assets            ✓        guard-makefile-version       ✓
guard-shellcheck-version      ❌ environmental only — no shellcheck binary in
                                 the review container; branch touches no *.sh
```

E2E (Playwright, chromium-headless-shell 141.0.7390.37 — the environment's
reused browser, one minor version behind the `@playwright/test` pin, as
`resolve-chromium.sh` itself warns):

```
Running 500 tests using 1 worker
  500 passed (7.5m)
E2E EXIT=0
```

That is the **entire** suite, not just the touched spec — run deliberately
because F1/F2 changed two shared page headers and added a shared CSS class.
The targeted subset (`catalog-inventory-category-filter-2119`,
`catalog-toprow-icons-2092`, `items-shell-catalog-top-actions-rail-2090`,
`inventory-stock-dialog-2011`, `inventory-to-till`,
`inventory-cost-currency-decimals-1282`, `items-admin-panel-title-2162`,
`items-shell-catalog-import-taxcodes-dialog-2095`,
`catalog-tab-strip-fade-2024`, `catalog-row-oob-1363`) was also run on its
own: 47 passed. Notably ut-docs#2092's own "the row fits one line at 1024x600
with no wrap" and "no control runs off-screen or overlaps at phone width
(360x800)" both still pass with the filter trigger added to that row.

## Verdict

**Safe to merge.** The feature is correct, the filtering semantics are
genuinely untouched (the chip row partial, `expand()`, `matches()` and
`bind()` are byte-for-byte unchanged and still covered by their original
assertions), the accessibility wiring is real and verified against rendered
output, and the popover-specific tests fail for the right reason when the
behaviour is broken. F1 and F2 were the substantive gaps — the card's own
headline requirement and a phone-width regression it would have introduced —
and both are fixed in this working tree with screenshots regenerated. One
follow-up worth filing: translate the `filter.*` locale block into ar/fa/tr.

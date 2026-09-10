# Catalog item-form tab strip: fade recompute on resize (ut-docs#2032)

## What shipped

Follow-up to ut-docs#2024 (`docs/code-reviews/2026-09-10-catalog-tab-strip-scroll-shadow-2024.md`),
which deferred this exact gap: `tabBarFade()` only ran off the tab bar's own
`scroll` event and once on dialog open, so a viewport resize/rotation that
crosses the `@media (max-width: 700px)` breakpoint while the item-form
dialog stays open left the `.tab-bar--fade-start`/`-end` classes stale
until the next scroll or reopen.

`web/ui/pages/catalog.html` — added `resize`/`orientationchange` handling,
registered **at most once per page load** via a `window.__catalogTabBarFadeResizeWired`
guard, resolving the *live* `#item-form-modal`/`.tab-bar` from the document
at event time rather than closing over the render's own `modal`/`tabBar`
variables:

```js
if (!window.__catalogTabBarFadeResizeWired) {
  window.__catalogTabBarFadeResizeWired = true;
  var recomputeLiveTabBarFade = function () {
    var liveModal = document.getElementById('item-form-modal');
    if (!liveModal || !liveModal.open) return;
    tabBarFade(liveModal.querySelector('.catalog-form-head .tab-bar'));
  };
  window.addEventListener('resize', recomputeLiveTabBarFade);
  window.addEventListener('orientationchange', recomputeLiveTabBarFade);
}
```

`e2e/tests/catalog-tab-strip-fade-2024.spec.ts` — two new tests: the
resize-recomputes-the-fade behavior itself, and a leak regression test (see
below). `web/help/img/manifest.json` regenerated (`make docs-shots`) —
`guard-docs-shots.sh` hashes the whole of `web/ui/**`/`web/public/**`
regardless of visible pixel impact; no screenshot actually changed.

## Independent review

A fresh-context Sonnet subagent (per Model Routing for `complexity:easy`),
isolated in its own git worktree, reviewed the diff independently.

- **1 blocking finding, fixed before merge.** The first draft registered
  `window.addEventListener('resize', ...)`/`orientationchange` directly
  inside the per-render IIFE, closing over that render's own `modal`/
  `tabBar`. This entire `<script>` is the "content" block
  `internal/pages/catalog/handlers.go` re-renders on **every htmx fragment
  swap** — which is exactly what the `/items` rail
  (`web/ui/partials/items_rail.html`, `hx-get`/`hx-target="#items-panel"`,
  at the `>=52rem` `.items-layout` breakpoint) does on every
  Catalog/Modifiers/etc. click. Unlike the pre-existing `scroll` listener
  (scoped to the `tabBar` element itself, so it's collected with the
  detached DOM), a `window`-scoped listener outlives the swap — every
  revisit to Catalog via the rail permanently pinned one more resize +
  one more orientationchange listener, each closing over an ever-growing
  chain of detached dialog subtrees. Reviewer verified this empirically
  (instrumented `addEventListener`, 1024x600, `/items` → Catalog →
  {Modifiers → Catalog} ×3): baseline 2 → 6 listeners, unbounded growth
  confirmed. **Fixed** by registering the listener once per page load
  (module-scope guard flag) and resolving the live modal/tab-bar fresh at
  event time instead of closing over a specific render's elements — this
  fixes both the leak (exactly one listener pair, ever) and a subtler
  correctness bug the leak would have caused (a stale `modal.open` on a
  detached node could otherwise keep firing `tabBarFade` on a dead
  element forever).
- No other blocking findings. `modal.open` confirmed a correct, real
  `<dialog>` property; no double-registration within one dialog-open
  lifecycle (elements are grabbed once at IIFE init, same convention as
  the pre-existing `scroll` listener); no i18n/RTL concerns (no new
  user-facing text); no suspicious content in either changed file.
- Reviewer independently re-verified the TDD claim: stripped just the two
  new `addEventListener` lines, re-ran the resize test, got the exact
  predicted assertion mismatch (`Expected: "1", Received: "0"`, real
  mismatch not a flake), restored the fix, confirmed green again.

## Regression test for the leak, added after the finding

`repeated htmx panel swaps via the /items rail never register more than
one resize/orientationchange listener` — patches `window.addEventListener`
via `page.addInitScript` to count registrations by event type, navigates
`/items` at `1024x700` (above the `52rem` rail-swap breakpoint), clicks
into Catalog once to get a baseline count, then round-trips
Catalog→Modifiers→Catalog three times via the rail (in-place htmx swaps,
never a full reload), and asserts the count is unchanged from baseline.
**Verified against the pre-fix code** (temporarily restored via `git
stash`, this test file left in place): baseline 3 → 6 after 3 round trips,
matching the reviewer's own empirical numbers exactly; restored the fix,
re-ran, count stayed at 3. Also re-confirms the fade still recomputes
correctly on the *current* dialog after several swaps (not just once).

## Verified beyond automated tests

- `go build ./...`, `gofmt -l .` (no `.go` files touched), `golangci-lint
  run ./...` (0 issues) — all clean, run both before and after the fix.
- Full `e2e/tests/catalog-tab-strip-fade-2024.spec.ts` (5 tests, including
  the pre-existing #2024 LTR/RTL/kiosk-floor cases) green.
- `guard-docs-shots.sh`, `guard-e2e-fixtures-import.sh`, `guard-i18n.sh` —
  all green; `make docs-shots` re-run after the post-review fix (the
  second edit to `catalog.html` needed a second regen) — no screenshot
  pixel changed, only the manifest's recorded surface hash.

## Deferred / not in scope

- Nothing further deferred from this card. ut-docs#2024's own two
  non-blocking findings (the resize gap fixed here, and a cosmetically
  irrelevant ~2px dead-zone in the fade thresholds) are both closed or
  already accepted as-is.

## Verdict

Safe to merge.

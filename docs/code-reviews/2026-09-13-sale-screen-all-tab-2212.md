# Sale screen: "All" category tab (ut-docs#2212)

**Card:** universaltill/ut-docs#2212 — "Sale screen: the category strip
should start with an 'All' tab, selected by default."
**Complexity:** easy. **Branch:** `feat/2212-sale-screen-all-tab`.

## What shipped

The sale screen's category strip (`web/ui/partials/buttons.html`) now
renders a leading **All** tab, selected by default whenever a tab bar
exists (>=2 top-level categories). Selecting it — or loading the page for
the first time — shows every category's items at once, each still under
its own visible header, so an operator who has tapped into one category
always has an escape hatch back to the whole catalogue without hunting
for which category something lives in.

Implementation is entirely template/Alpine-side, no Go changes:

- A sentinel tab id `__all__` (never a real category ID — those are
  always `uuid.NewString()`, verified against every category-writing path
  in `internal/data`) is the new default value of the component's `tab`
  state.
- `panelVisible(id, panelEl)` now also returns true for every panel when
  `tab === '__all__'`, reusing the exact multi-panel-visible machinery
  ut-docs#2181 already built for cross-category search — no new render
  path, no duplicate rendering of the catalogue.
- Each panel's `:role`/`:aria-labelledby` relax to `'group'`/`null` while
  All is active, mirroring the relaxation ut-docs#2181 already applied
  during search (the strict one-tab-one-panel WAI-ARIA pattern doesn't
  hold once more than one panel is visible at once).
- The All tab itself deliberately carries no `aria-controls` (it doesn't
  own one single panel) and no `--cat-color` (it isn't one category).
- `x-cloak` removed from every per-category panel AND (post-review fix,
  see below) from the top-level bucket's own category header — both are
  visible by default now that All is the default tab, so cloaking either
  would hide real content pre-hydration, or permanently on a client where
  Alpine never loads.
- New i18n key `products.all`: en "All", ar "الكل", fa "همه", tr "Tümü".
- `web/help/*/sell.md` (all 5 shipped locale copies) updated to describe
  the new tab; screenshots regenerated for the `sell` topic only.

## TDD

`internal/ui/buttons_all_tab_test.go` was written first and confirmed to
fail against the pre-change template (real assertion failures, not a
compile error) before implementing, then confirmed to pass after. Two
pre-existing tests (`buttons_http_test.go`,
`buttons_search_visibility_test.go`) that pinned the OLD "first real
category is the default active tab" invariant were updated — this card
deliberately supersedes that invariant, the same way ut-docs#2181 itself
superseded ut-docs#418's original test.

## Independent review

Fresh-context Sonnet subagent, isolated worktree, read-only. Re-ran the
TDD claim itself (reverted `buttons.html` to the pre-change version,
confirmed the new test fails with a real error, restored, confirmed it
passes), re-ran the full verification gate and e2e suite independently,
and independently re-derived the `__all__`-collision-safety and
`guard-docs-shots.sh` PNG-existence-only claims from the actual source
rather than taking them on trust.

**Found (should-fix, real):** the per-category-group header
(`<h3 class="category-header">`) still carried `x-cloak` even though its
`x-show` condition (`q || tab === '__all__'`) is true by default now —
inconsistent with the panels two lines above it, which had correctly
dropped `x-cloak` for the identical reason. On a client where Alpine
fails to load or loads slowly (a real kiosk risk: CDN hiccup, cold cache,
low-power hardware), every category's tiles would render flattened
together with **no category label at all** — worse than the pre-#2212
no-JS behavior, which needed no label since only one category was ever
shown. **Fixed**: `x-cloak` removed from that header too; both tests
that asserted the exact markup string were updated to the corrected
(no-`x-cloak`) literal, plus an explicit assertion added that the header
carries no `x-cloak`. Re-verified: `make docs-shots` re-run after the fix
produced byte-identical PNGs to the pre-fix run (confirmed via `cmp`),
proving the fix genuinely changes no rendered pixel — only pre-hydration
behavior — so no screenshot regen was actually owed for this specific
follow-up (only the tiny `surface_sha256` bump in `manifest.json`).

**Found (note only, not fixed in this PR):** a stale comment in
`e2e/tests/products-scroll-affordance-1313.spec.ts` (not touched by this
diff) still attributed its overflow precondition to "Food's
default-active tab." Test assertions were unaffected (still pass — All
shows even more content than Food alone did) — picked up as a same-branch
drive-by fix since it was one line and directly about the fact this card
changes.

No blockers: no money/tax, data-loss, security, or false-passing-test
issues found.

## Verified (beyond automated tests)

- `go build ./...`, `go vet ./...`, `gofmt -l` (clean), `golangci-lint run
  ./internal/ui/...` (0 issues), full `go test ./internal/...` (all
  packages green) — run by both dev and reviewer independently.
- `scripts/ci/guard-i18n.sh`, `guard-data-access.sh`,
  `guard-help-topics.sh`, `guard-help-drift.sh`, `guard-docs-shots.sh` —
  all green.
- e2e: every spec touching `.tab-bar`/`cat-tab-`/`category-header`/
  `getByRole('tab'` across the whole `e2e/tests/` directory was located
  by grep (not just the files this diff touched) and run — 38+ tests
  green across the sale-screen/tab-bar/payment-overlay/category-color
  specs, both before and after the review's fix.
- Two screenshots (en, fa/RTL) visually inspected: All tab renders first
  and active, category headers (e.g. "Dairy") show correctly, no
  clipping/overlap at the 1024×600 kiosk floor, RTL mirrors correctly.
- `guard-docs-shots.sh`'s PNG-existence-only (never byte-content) check
  was independently re-derived from the script's own source by the
  reviewer, confirming that reverting ~120 screenshots untouched by this
  change back to their original committed bytes cannot fail the guard.

## Deferred (new Backlog-worthy items)

None — the one real finding was fixed in this same PR; the stale-comment
note was cheap enough to fix inline rather than deferred.

## Verdict

Safe to merge.

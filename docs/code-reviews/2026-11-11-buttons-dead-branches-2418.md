# Review: buttons.html dead Alpine branches (ut-docs#2418)

Date: 2026-11-11 · Branch: `chore/2418-buttons-dead-branches` · Lane: `lane:cloud-54`

## What shipped

`web/ui/partials/buttons.html` loses code that could not run since the
search results moved into their own `#search-results` container:

- `panelVisible(id, panelEl)` → `panelVisible(id)`; it was
  `q ? sectionHasMatch(panelEl) : tab === id`, now `tab === id`.
- `#cat-panel-*`: the `:role="q ? 'group' : 'tabpanel'"` and
  `:aria-labelledby="q ? null : …"` ternaries are now static
  `role="tabpanel"` / `aria-labelledby="cat-tab-…"`.
- Three `<p class="empty" x-show="!sectionHasMatch($el.parentElement)">`
  "no matches" messages inside `#buttons-grid` (tabbed, single-category
  and flat branches) are removed.
- The comments that explained the removed behaviour are shortened and
  made accurate.

Why they were dead: `#buttons-grid` is `x-show="!q"` in every browsing
mode (no transition). `q` is set only by the `#products-search` x-model
and the `st.q` restore, both behind `not .EditMode`, and cleared by
`closeSearch()`. Nothing in `web/public/*.js` or another template writes
it. So nothing inside the grid is visible while `q` is set, and with `q`
empty `sectionHasMatch` is always true. The "no matches" text users see
comes from `#search-results`, which is unchanged.

`internal/ui/buttons_all_tab_test.go` and
`internal/ui/buttons_search_visibility_test.go` now assert the new markup
and that the dead branches are gone.

## Review (Opus 5.5, independent; author Sonnet)

Verdict: approve. Checked that `q` has no other writer, that the grid has
no transition, and that every `cat-panel-X` always has a `cat-tab-X`
(both render from the same `.Groups` under `strip_overflow`). Findings:

1. Nit: two test names still said "AlsoCarriesNoMatchesMessage" while
   asserting absence. **Fixed** (renamed to `…HasNoInGridNoMatchesMessage`).
2. Nit: the negative assertions reject `products.no_matches` anywhere in
   the `/ui/buttons` body, wider than `#buttons-grid`. **Accepted**: the
   fixtures are strip-mode list renders that carry no other use of the
   key today; a future fixture that does would fail loudly, not silently.
3. Minor, out of scope: the `x-show="q"` headers and ancestor span in
   `category-group` / `category-group-body-tabbed`, and a stale "search
   open in edit mode" comment, are the same family of dead code.
   **Deferred** → Backlog card (filed with this cycle's close-out).

## Verification

- TDD/regression: with the parent's `buttons.html` restored, 5 tests in
  `internal/ui` fail; with the change they pass (Tester and Reviewer each
  ran this).
- `gofmt -l .` clean; `go build ./...`, `go vet ./...` pass; `go test
  ./...` passes (`internal/plugins` needs >600s in the sandbox; passed
  with `-timeout 25m`, untouched by this change).
- Guards: i18n, i18n-meta, data-access, rtl-logical-css, no-showmodal,
  help-topics, fixed-dates, docs-shots: all pass.
- e2e (Chromium, `--project=default`): 77 passed, 0 failed across 18
  specs covering sell-screen tabs, search, overflow/ARIA, Designer, phone
  sell and voucher tiles.
- Visual (Tester): real till, `strip_overflow`, 1280×800 and 1024×600,
  light and dark, at rest / matching query / non-matching query. Grid at
  rest unchanged; a non-matching query still shows "No matching
  products." from `#search-results`. Not looked at: RTL and long locales,
  the other browsing modes, the Designer by eye (covered by e2e).

Pre-flight: web/help topic — n/a (no visible change; docs-shots surface
hash refreshed with `update-docs-shots-surface-hash.sh`, no PNG change);
`reference/` — n/a; README — n/a; en.json/en.meta — n/a (no key added or
removed; `products.no_matches` still used elsewhere); ADR — n/a;
plan/price, platform claim, plugin taxonomy — n/a; follow-up dead `q`
headers — deferred → Backlog card; review record — this file.

# Code review: sale-screen + tender tab-bar overflow/ARIA (ut-docs#424)

**Date:** 2026-08-25
**Card:** universaltill/ut-docs#424 — "Sale-screen + tender tab bars: no
wrap/scroll for many tabs, ARIA incomplete"
**Branch:** `feat/424-tab-bar-overflow-aria`
**Complexity:** medium (build: Sonnet inline; review: Opus, fresh context,
isolated worktree)

## What shipped

Both `.tab-bar` instances in the codebase — the tender Pay/Split tabs
(`web/ui/pages/index.html`) and the sale-screen category tabs
(`web/ui/partials/buttons.html`) — get:

1. **Overflow handling.** `.tab-bar { flex-wrap: wrap }` plus
   `white-space: nowrap` on each `.tab` (`web/public/app.css`): a shop with
   12+ top-level categories now wraps onto additional tab-bar rows instead
   of squashing labels. `white-space: nowrap` matters on its own —
   `flex-wrap: wrap` alone still let a multi-word label wrap *inside* a
   narrowing tab before the row ran out of tabs to push down, since a
   wrappable label's min-content floor is only its widest word, not the
   whole string.
2. **A complete WAI-ARIA tabs pattern** on both bars: `role="tab"` on every
   tab (was already present on the category tabs, added to the tender
   tabs), `aria-selected`, roving `tabindex` (0 on the active tab, -1 on
   the rest), `role="tabpanel"` + `aria-labelledby` on each panel,
   `aria-controls` on each tab, and a shared `focusTab(dir)` Alpine method
   (one per component's own `x-data` scope — no shared JS module system in
   this codebase to factor it into) that moves focus AND activates the
   target tab via its own existing `@click` handler, on Left/Right.
3. Regression coverage: `e2e/tests/tab-bar-overflow-aria-424.spec.ts` — a
   12-category wrap-geometry test, a full ARIA-pattern + arrow-key test on
   the category tabs, one on the tender tabs, and an RTL arrow-direction
   test (added post-review, see below).
4. `web/help/img/{tr/sell,fa/translations}.png` + `manifest.json`
   regenerated via `make docs-shots` (guard-required after any
   `web/ui/**`/`web/public/**` change); both diffs are few-byte
   antialiasing noise on unrelated screen regions, confirmed by viewing
   them, not a rendering regression.

## Independent review

Spawned an Opus subagent, fresh context, isolated git worktree
(`isolation: "worktree"`), briefed with the diff, the ticket's acceptance
criteria, and specific correctness risks to check rather than a generic
"look for problems" ask (RTL, the `focusTab` "nothing focused" edge case,
the synthetic uncategorized-category id path, whether `:aria-selected`
actually renders as the literal string `"true"`/`"false"` via Alpine, the
`split-tender-card` id's load-bearing status for `app.js`, e2e cleanup
completeness).

**Verdict: safe to merge with notes.** Findings and disposition:

1. **should-fix, fixed in this branch.** Arrow keys moved in raw DOM order
   regardless of direction — under `dir="rtl"` DOM-next renders to the
   visual *left*, so ArrowRight moved focus/selection left, the opposite
   of the WAI-ARIA APG's visually-relative Left/Right and this repo's
   non-negotiable RTL rule (`universal-till/CLAUDE.md`). Reviewer measured
   it live on `/?lang=fa`. Fixed by reading `getComputedStyle($el).direction`
   at the `@keydown.right`/`.left` call site and flipping the sign passed
   into `focusTab` — `focusTab` itself stays a plain DOM-order stepper, so
   the RTL awareness lives at the one place that actually knows about
   screen direction. Added a dedicated RTL regression test
   (`arrow keys follow the VISUAL direction under RTL, not raw DOM order`)
   asserting the real visual x-position of the DOM-first/DOM-second tabs
   and that ArrowRight/Left move toward the correct one.
2. **should-fix (design call, not a correctness bug) — deferred to a new
   Backlog card, not fixed in this branch.** At the repo's documented
   1024×600 kiosk floor, 12 wrapped category-tab rows push the products
   grid (search box + tiles) below the fold — measured: tab-bar height
   247px, 0 tabs fully visible in the `.products` panel, 223px of scroll
   to reach the first tile (vs. 49px pre-fix, when the same 0-tiles-visible
   condition already existed for a different reason — squashed labels, not
   height). Real degradation, but not a new failure class this fix
   introduces, and the original ticket's own "Suggested fix" text already
   flagged wrap-vs-scroll as "Architect's call once scoped" — now that it
   *is* scoped with real numbers, a horizontal-scrolling strip (or a
   `max-height` + internal scroll on `.tab-bar`) is the likely next step.
   Filed as a new Backlog card carrying the reviewer's measurements rather
   than redesigning the CSS mid-review — this ticket's own AC (readable
   single-line labels at 12+ categories) is met; the deeper "products grid
   needs guaranteed screen space independent of tab count" problem is
   pre-existing and larger than this ticket's scope.
3. **nit, fixed in this branch.** `focusTab`'s `tabs.indexOf(document.activeElement)`
   returns `-1` when nothing is focused; not reachable via real keyboard
   input (the handler lives on `.tab-bar`, whose only focusable descendants
   are the tabs), but cheap to close: `if (!tabs.length) return;` plus
   clamping the index to `0` when `indexOf` returns `-1`, so a value that
   was merely "can't happen today" doesn't become "can't happen, probably"
   after some future refactor of what's focusable inside the bar.
4. **nit, fixed in this branch.** The new spec's cleanup comment claimed to
   undo "everything setup created" but never removed the 12 categories
   each test's import creates (only the shortcut button and the catalog
   item). Harmless in practice — a button-less category renders no tab,
   and the e2e server's DB is a fresh temp dir per run — but the comment
   overstated what actually happens. Reworded rather than added a third
   cleanup round-trip per item.
5. **nit, deferred.** Neither `role="tablist"` carries an accessible name
   (`aria-label`/`aria-labelledby`), which the WAI-ARIA APG lists as part
   of the pattern. Pre-existing gap elsewhere in this repo too
   (`reports.html`), and closing it needs a new i18n key (→
   `lang-pack-drift` advisory on the PR touching `en.json`) for one
   incremental a11y improvement — reasonable to defer rather than pull
   into this diff's scope.
6. **nit, deferred.** The manual (`web/help/en/sell.md`) isn't touched.
   Reviewer's own read: this is an assistive-tech affordance with no
   procedural change to what a shop owner clicks or sees — the existing
   "switch between category tabs" prose still holds, arrow-key nav is
   additive. Noted rather than blocked.

Also independently verified and confirmed *correct* (no fix needed): the
synthetic "uncategorized" category's `id="cat-tab-uncategorized"` can never
collide with a real category (both non-test category-insert paths —
`EnsureCategory`, `EnsureCategoryUnder` — always assign a `uuid.NewString()`
id); tab/panel id wiring is consistent between the tab-generating and
panel-generating template loops; `id="split-tender-card"` (load-bearing per
its own existing comment, referenced by `web/public/app.js`) was only ever
had attributes added, never renamed; `:aria-selected="tab === '...'"`
renders the literal strings `"true"`/`"false"` (checked against Alpine's
vendored source's boolean-attribute allowlist, which explicitly excludes
`aria-selected`, and confirmed live by the e2e assertions); `flex-wrap` and
`white-space: nowrap` have no RTL-specific gotcha themselves (the RTL bug
was entirely in the JS arrow-direction mapping, finding #1 above); the new
spec is a genuine (non-false-pass) regression test, confirmed by reverting
each half of the fix in isolation and watching the matching assertions
fail; the e2e cleanup actually works in practice (a full 170-spec run with
this card's tests running mid-suite showed no state leak into neighboring
specs).

## Verified beyond automated tests

- `gofmt -l .` clean; `go build ./...` clean (no Go changed, run anyway per
  the standing gate discipline).
- `bash scripts/ci/guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-docs-shots.sh`, `guard-htmx-loaded.sh` all pass (the guards this
  `web/ui`/`web/public` diff can actually affect).
- e2e, driven through a real Chromium against the real server (not just
  the Go test suite): the new spec (4 tests, including the post-review RTL
  addition), the pre-existing `sale-screen-category-tabs-search-418.spec.ts`
  (proves no regression on the #418 category-tab/search feature this
  change shares templates with) and `catalog-import-friendly-errors.spec.ts`
  (exercises the same CSV-import path the new spec's setup uses) all pass.
  A full sweep of the entire `default` e2e project (170 specs — layout,
  contrast, RTL, i18n, tables, tender, kiosk, phone-width, etc.) passes
  clean, confirming the CSS/ARIA changes don't regress any other surface
  that shares `.tab-bar`/`.tab`/`app.css`.
- **A real bug was caught and fixed during this verification, not by the
  independent review**: the first RTL-fix draft's own code comment
  contained a literal `"not found"` inside the double-quoted Alpine
  `x-data="..."` attribute, which — being genuine unescaped HTML, not a
  templating-engine quirk — silently truncated that attribute for both
  `.tab-bar` components at the HTML level. `html/template`'s parse step
  didn't catch it (the malformed markup is still syntactically parseable
  HTML, just wrong), but `ExecuteTemplate` failed at render time, and the
  `/ui/buttons` handler discards that error (`_ = h.View.Render(...)`),
  so every page embedding either tab bar rendered a genuinely empty
  response body — caught immediately by re-running the e2e suite (both the
  new spec and the pre-existing `#418` spec started failing on
  `.tab-bar` never appearing), root-caused by parsing/executing the
  templates directly in an isolated Go test against real request data
  (not by reading the diff), and fixed by rewording the comment to avoid
  literal double quotes. Worth calling out explicitly because it's exactly
  the "don't just trust the diff looks right — run it for real" discipline
  this pipeline's reviewer step exists to enforce, and in this instance it
  was self-caught, not review-caught — the independent review ran on the
  version *after* this bug had already been fixed and never saw it.

## CI found a second real regression, after review and merge-conflict resolution

After merging `main` (see below) and pushing, the PR's `e2e` check
(`tests/e2e/` — a smaller, separate legacy Playwright suite from the
`e2e/` one this PR's own regression coverage lives in, run by a different
CI job) failed:
`pos_ui_mvp.spec.ts`'s "accessibility baseline for primary actions" test
located the tender's Split tab via `getByRole('button', { name: 'Split' })`.
Giving that button `role="tab"` (this PR's whole point, per the WAI-ARIA
tabs pattern) overrides its native `<button>` element's implicit
`role="button"`, so the old locator stopped matching — correctly, by
design, not a bug in the shipped change. Fixed by updating the test to
`getByRole('tab', ...)`, verified locally by booting a server the same way
this CI job does (`scripts/e2e_seed` + `UT_AUTH=off UT_DEV_MODE=true go run .`)
and running the full legacy suite (21 passed, 4 skipped for the missing
`DOCS_READ_TOKEN` secret locally — same as CI without it).

Worth naming explicitly: this is the second distinct e2e test surface this
change touched (`e2e/` and `tests/e2e/` are two separate suites, run by two
separate CI jobs), and this session's own pre-push verification only ever
exercised the first — the second was found by CI, not by review. No
pre-existing single command runs both from this repo; noting it here as a
gap in this session's own verification discipline, not just the ticket's.

## Merge conflict with `main`

Between opening this PR and its own CI running, PR #515 (import problem
grid) merged to `main` first, landing a conflict — solely in the generated
`web/help/img/manifest.json` (both PRs touched `web/ui/**`, triggering
docs-shots regeneration). Merged `main` into the branch, regenerated the
manifest fresh via `make docs-shots` rather than hand-resolving the
conflict, and re-ran the full verification pass (build, guards, the new
spec + regression specs, a full 170-spec sweep of the `e2e/` suite) against
the merged tree before pushing.

## Deferred / follow-up

- New Backlog card: kiosk-floor (1024×600) tab-bar height regression at
  12+ categories (review finding #2 above), carrying the reviewer's
  measured numbers, proposing a horizontal-scroll or capped-height variant.
- `role="tablist"` accessible name (finding #5) and manual prose (finding
  #6) — noted, not blocking, no follow-up card filed (both are small,
  easily bundled into whatever change next touches these templates).

## Safe-to-merge verdict

**Yes.** One should-fix finding (RTL arrow direction) was a real
correctness/accessibility bug and is fixed in this branch with its own
regression test; the other should-fix (kiosk-floor height) is a genuine
but out-of-this-ticket's-scope UX tradeoff, tracked as a follow-up rather
than blocking; all nits are either fixed or explicitly, defensibly
deferred. Full e2e sweep green, all relevant CI guards green.

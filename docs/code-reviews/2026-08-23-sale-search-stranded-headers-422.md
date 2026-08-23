# Code review — ut-docs#422: sale-screen search stranded headers + missing no-matches feedback

**Date:** 2026-08-23
**Card:** [ut-docs#422](https://github.com/universaltill/ut-docs/issues/422)
**Complexity:** easy
**Build model:** Sonnet (inline) — **Review model:** Sonnet, fresh-context subagent, worktree-isolated (per the easy-tier routing: an independent instance is the "different model" for this tier)

## What shipped

Follow-up from the ut-docs#418 review: `web/ui/partials/buttons.html`'s
recursive `category-group` template rendered a subcategory's `<h3
class="category-header">` unconditionally, while each `product-tile`
filtered itself client-side against the search query `q` via its own
`x-show`. Two visible consequences: (1) a search query that emptied one
subcategory but not a sibling (within the same tab) left the empty
subcategory's header rendered with nothing under it — a stranded header;
(2) a search matching nothing anywhere in the active tab left a blank
panel with no feedback.

Fix: two methods added to the existing Alpine `x-data` scope on
`.products-finder` — `matches(tileEl)` (the same per-tile check, now
shared) and `sectionHasMatch(el)` (does any `.btn-tile` descendant of `el`
currently match). Each `category-group` `<section>` now carries
`x-show="sectionHasMatch($el)"`, so an emptied subcategory hides itself
instead of stranding its header. Each tab panel, and the single-real-
category (no-tab-bar) branch, gained a `<p class="empty"
x-show="!sectionHasMatch($el.parentElement)">{{ T "products.no_matches"
}}</p>` — shown exactly when nothing in that panel currently matches.

Explicitly out of scope (BA/Architect decision, unchanged by review): the
`$flat` branch (a till with zero categories assigned to anything) still
has no no-matches message on a non-matching search — a pre-existing gap,
not a regression, since that branch was never touched. Filed as a
follow-up: see "Follow-up" below.

## What the independent review found

One fresh-context Sonnet subagent, spawned in an isolated git worktree
(so its revert-then-restore TDD re-verification never touched the shared
checkout), read the diff cold and:

- Independently reverted `buttons.html`, reran the new Go test, confirmed
  it fails with a real assertion error (not a compile error) — TDD claim
  genuinely reproduced.
- Traced `sectionHasMatch`'s short-circuit on empty `q` — confirmed the
  unsearched view is unaffected (no regression to the normal case).
- Traced the recursive `x-show` on nested subcategories — confirmed a
  parent section can never hide while a child still matches (DOM
  descendant `querySelectorAll` always includes grandchildren).
- Confirmed `$el.parentElement` on the tab-panel message correctly
  resolves to the `.products-tab-panel` div per the template's actual
  structure.
- Checked the `.ID` interpolation inside the now-multi-line `x-data`
  object literal for a new injection surface — none found; the pre-
  existing safety comment's constraint (server-generated UUID/fixed
  literal only) still holds, and the new method bodies contain no
  template interpolation at all.
- Verified `guard-docs-shots.sh` only checks `surface_sha256` freshness
  and per-topic PNG *existence*, never pixel content — confirming it was
  correct to discard the incidentally-regenerated `alerts.png`/
  `designer.png` (unrelated topics, changed only because this session's
  reused Chromium (141.0.7390.37) doesn't match the pinned
  `@playwright/test` version (149.0.7827.55)) rather than commit stale-
  browser-rendered images for screens this PR never touched.
- Read all three new translated strings (ar/fa/tr `products.no_matches`)
  for register/grammar consistency against sibling keys, and confirmed
  clean UTF-8 encoding (no mojibake, no stray entities) via a codepoint
  dump.

**One real, confirmed finding — fixed, not deferred:** applying
`x-show="sectionHasMatch($el)"` generically to the `category-group`
template also affects the single-real-category, no-tab-bar branch (the
`else` branch two lines below the `$hasTabs` branch), which reuses that
same template — but the no-matches `<p>` had only been added inside the
tabbed branch. For a till with exactly one real category, a non-matching
search now hid the section (header included) with *zero* replacement
feedback — strictly worse than the pre-fix behavior for that topology
(which at least left the header visible). This was a genuine regression
introduced by this PR, not the pre-existing (and explicitly deferred)
`$flat`-branch gap.

**Fix applied post-review:** added the same no-matches `<p>` to the
`else` branch, scoped to `$el.parentElement` (`#buttons-grid` itself,
this branch's only content). New regression test
`TestButtonsHTTPList_SingleCategoryNoTabsAlsoCarriesNoMatchesMessage`
added and TDD-verified myself (mutated the fix away, confirmed the test
fails with a real assertion error; restored, confirmed green) — same
worktree-safe discipline the review subagent used, done inline here since
it's a small, mechanical addition to an already-reviewed diff, not a new
independent pass.

**Also noted, not treated as blocking:**
- No e2e/browser test exercises the stranded-header fix (bug #1) at
  runtime — only the Go template test confirms the markup wiring is
  present; the demo/e2e-seeded catalog can't produce two live
  subcategories in one tab without seeding a shortcut at runtime (the Go
  test's own comment explains this). The Tester step separately drove
  this scenario for real in a browser (see below) by seeding a Bakery
  shortcut via `/api/buttons/add` at runtime — that verification isn't a
  committed test, so there's no standing regression guard for it; noted
  as a real, accepted gap rather than silently left untested.
- The ar/fa/tr translations for `products.no_matches` were **not**
  produced via the standing NAS Ollama pipeline
  (`ut-docs/reference/translation.md`, 192.168.1.231:11434) — that
  endpoint is unreachable from this cloud session (confirmed via a timed-
  out `curl`). Translated directly instead, matching the tone/register of
  existing sibling keys (`designer.no_matches`, `plugins.store.no_matches`,
  `products.empty`) in each locale. The review's read found these
  grammatically correct and register-consistent, but this is a genuine
  process deviation — recommend re-running these three keys through the
  documented pipeline once the NAS is reachable from wherever does the
  check, to confirm nothing drifts from what that process would have
  produced.

## What was verified beyond automated tests

- Tester drove the real running app (real Chromium via Playwright) for
  the actual stranding scenario: seeded a live Bakery subcategory under
  Food at runtime, searched "Milk" (matches Dairy only) — confirmed via
  screenshot that Bakery's header disappears cleanly with Dairy's tiles
  still showing, no stranding.
- Screenshots of the no-matches message actually inspected in English,
  Farsi/RTL (message renders right-aligned, correctly mirrored, no
  layout breakage), and at the 10-inch kiosk viewport (1024×600) —
  confirmed present and cleanly laid out once scrolled into view (the
  products panel's own internal `overflow-y: auto` at short viewports is
  pre-existing, documented behavior, unrelated to this change).
- Full `e2e/` Playwright suite run twice (once by Dev, once after the
  post-review fix): 152–153/153 passed both times; the one failure
  (`catalog-image-to-till.spec.ts`) reproduces identically on unmodified
  `main`, confirmed unrelated to this diff.
- `go build ./...`, `go vet ./...`, `gofmt -l .`, full `go test ./...` —
  all clean, both before and after the post-review fix.
- `guard-i18n.sh`, `guard-docs-shots.sh`, `guard-data-access.sh`,
  `guard-compliance-claims.sh` — all pass.

## Safe to merge

Yes, after the post-review fix above. No blockers remain.

## Follow-up (new Backlog cards, not built here)

- The `$flat` branch (zero categories assigned to anything) still has no
  no-matches feedback on a non-matching search — pre-existing, explicitly
  deferred by BA/Architect at scoping time, unchanged by this PR.
- Real e2e/browser coverage for the stranded-header fix specifically
  (bug #1), using a runtime-seeded fixture (e.g. the `/api/buttons/add`
  approach Tester used manually) rather than relying solely on the Go
  template test's markup-wiring check.
- Re-run the `products.no_matches` ar/fa/tr translations through the
  standing NAS Ollama pipeline once reachable, to confirm no drift from
  the direct-translation fallback used here.

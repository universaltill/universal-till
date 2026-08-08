# Code review: self-order kiosk — remove search, category browsing only (ut-docs#419)

**Date:** 2026-08-08
**Card:** [ut-docs#419](https://github.com/universaltill/ut-docs/issues/419) (split from #402; sibling #418, till-side search, already merged)
**Complexity:** easy
**Author (Dev):** scrum-master pipeline, inline (Sonnet)
**Reviewer:** independent Sonnet subagent, fresh context, isolated worktree

## What shipped

The self-order kiosk previously had both a text search box
(`GET /api/self-order/search?q=...`) and category chips, and the two
didn't compose — a text search silently dropped the active category
filter. Per the product owner's explicit request ("for self-ordering
only go to categories and find them"), the search affordance is removed
entirely; category chips are the sole find mechanism.

- `internal/pages/self_order_shop.go`: renamed `GET /api/self-order/search`
  → `GET /api/self-order/grid` (it no longer searches anything, just loads
  the grid) and dropped the `q`-param filtering path from `loadShopItems`.
- `web/ui/pages/self_order_shop.html`: removed the search `<input>` and its
  CSS/htmx wiring; grid's `hx-trigger="load"` now targets `/api/self-order/grid`;
  the "All" chip's i18n key changed from a borrowed `plugins.store.all` to
  a semantically correct `selforder.all_categories`.
- `web/locales/{en,ar,fa,tr}.json`: removed `selforder.search_ph`, added
  `selforder.all_categories` (translated) — all four in lockstep.
- `web/help/{en,ar,fa,tr}/self-order.md`: one clarifying sentence added —
  customers browse by category, no search box. (No docs-shots screenshot
  exists for this page — confirmed via `e2e/tests-docs/docs-shots.spec.ts`,
  zero hits — so no screenshot regen was needed.)
- `internal/pages/self_order_shop_test.go`: updated existing tests for the
  renamed endpoint; added `TestSelfOrderShop_GridIgnoresQueryParam`,
  `TestSelfOrderShop_PageHasNoSearchInput`,
  `TestSelfOrderShop_TilesCarryCategoryForChipFiltering`, and
  `TestSelfOrderShop_HiddenTileRuleOverridesTileDisplay`.

## A real bug found while testing, not just in the diff

Driving the kiosk in a real headless browser (not just asserting on
rendered HTML strings) surfaced that category-chip filtering — the
acceptance criteria's "works correctly on its own" requirement — was
**silently broken already**, independent of this change: `.selforder-tile`'s
own `display:flex` (an author rule) overrides the UA stylesheet's default
`[hidden] { display:none }`, so the chip-filter script's `tile.hidden = true`
had no visual effect. Screenshotted before/after:

- Before the fix: clicking the "Bakery" chip left all 11 items on screen
  (the `hidden` IDL property was set correctly in the DOM — 46/50 tiles —
  but nothing disappeared visually).
- After adding `.selforder-tile[hidden] { display:none; }`: the same click
  correctly narrowed the grid to the 4 seeded bakery items.

Regression-covered at the Go level (`TestSelfOrderShop_HiddenTileRuleOverridesTileDisplay`,
which pins the CSS rule's presence) since a Go `httptest` can't observe
rendered pixels directly, but the fix itself was verified visually
against a real running instance.

## Independent review

Spawned a fresh-context Sonnet subagent in an isolated git worktree (per
`complexity:easy` routing — different instance is sufficient independence
at this tier) with the full diff, task context, and an instruction to
actually run things, not just read.

**Findings:** none blocking. One nit — the CSS-bug explanatory comment
described the override as "equal specificity, later in cascade" when the
more precise mechanism is "author stylesheet overrides UA stylesheet
regardless of specificity, and the new rule also wins on specificity
against the sibling `.selforder-tile` rule." Comment reworded before
commit; no code change.

**What the reviewer actually ran** (not just read):
- `go build ./...`, `go vet ./...` — clean.
- `go test ./internal/pages/... -run TestSelfOrderShop -v` — all 20 tests
  pass. Full `go test ./...` also run: everything green except a
  **pre-existing, unrelated** failure in `internal/issuereport`
  (`TestSaveCleansUpDirectoryOnWriteFailure`, already tracked as
  ut-docs#415/#258 — fails under a root-run sandbox; `internal/issuereport`
  has zero diff on this branch).
- `bash scripts/ci/guard-i18n.sh`, `guard-data-access.sh`,
  `guard-help-topics.sh` — all pass.
- **TDD re-verification, done by reverting and re-running, not taken on
  faith:** removed the `.selforder-tile[hidden]` CSS rule and reran
  `TestSelfOrderShop_HiddenTileRuleOverridesTileDisplay` → failed with the
  expected message; restored, reran → passed. Reverted the endpoint
  rename and reran `TestSelfOrderShop_GridIgnoresQueryParam` → failed with
  a 404 (the test correctly targets the new route); restored, reran →
  passed.
- Repo-wide grep for `self-order/search` — only remaining hits are an
  explanatory code comment and a negative-assertion test.
- Confirmed scope: `git diff main..HEAD --stat` touches only the 11
  expected files; the till sale screen (#418), #173, and #210 are
  untouched.

**Verdict: safe to merge.**

## Verified beyond automated tests

Drove the kiosk against a fresh build with the real (migration-seeded)
demo catalog, headless Chromium, three states:
- **Desktop (1024×700), English** — chip row renders, "All" chip active by
  default, no search box present, `console --errors` clean.
- **Bakery-chip filter click** — before the CSS fix: no visual change
  (bug). After: grid narrows from 11 visible items down to the 4 real
  bakery items (Brown Bread Loaf, Chocolate Muffin x2, Croissant Pack x4,
  White Bread Loaf).
- **RTL (fa) desktop** — `dir="rtl"` on `<html>`, chip row and header
  correctly mirrored (Back/lock on the right, chips right-to-left ending
  in "همه"), cart panel and grid swap sides, no layout breakage, no search
  box.
- **Tablet-ish viewport (800×480)** — header and chip row hold up with no
  overlap; 2-column grid with cart alongside, no search box.
- **Not checked:** dark theme (this page doesn't carry a dark-theme
  variant distinct from its default surface tokens — same as before this
  change, not a regression) and the ar/tr locales specifically (fa was
  used as the RTL representative; ar shares the same `dir=rtl` codepath so
  risk is low, but it was not independently screenshotted this round).

## Deferred / explicitly out of scope

- ut-docs#173 (barcode-less items unreachable as tiles) — related,
  untouched, as the card specifies.
- ut-docs#210 (self-order barcode scanner) — untouched; its body's stale
  premise ("no search/barcode-input field anywhere") is now even more
  stale since the search box is gone — left as a note for whoever picks
  up #210 next, per the card's own instruction.
- `internal/issuereport`'s pre-existing root-sandbox test failure
  (#415/#258) — unrelated to this diff, not fixed here.

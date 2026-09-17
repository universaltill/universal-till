# Code review: Categories tab item-picker modal (ut-docs#2283)

**Date:** 2026-09-16
**Card:** universaltill/ut-docs#2283 — "Sell screen: optional 'Categories' tab (first tab) showing category tiles that open an item-picker modal"
**Complexity:** medium
**Implementer:** Sonnet (Dev subagent, isolated worktree)
**Reviewer:** Opus (independent subagent, isolated worktree, fresh context)

## What shipped

A settings-gated (default **off**) "Categories" tab, shown first among the sell
screen's existing tabs. When enabled, tapping it shows a grid of top-level
category tiles (zero-active-item categories hidden, nested categories
flattened); tapping a tile opens a modal — cloned from `#parked-orders-modal`'s
shell — containing an item-picker for that category, built by cloning
already-rendered `.btn-tile` markup and re-wiring it with `htmx.process()`.

Key files: `internal/data/sell_screen_settings.go` (new), `internal/ui/buttons.go`
(`CategoriesTabEnabled`, `pruneEmptyCategoryGroup`), `internal/pages/settings_page.go`
(`POST /api/settings/categories-tab`), `internal/uislot/slot.go`,
`web/ui/pages/settings.html`, `web/ui/partials/buttons.html`
(tab/grid/`openCategoryPicker()`), `web/ui/pages/index.html`
(`#category-items-modal`), `web/public/app.css`, locale files (en/ar/fa/tr —
6 new keys, real translations; de/es untouched), help docs + screenshots.

Tests (TDD-first): `internal/ui/buttons_categories_tab_test.go` (off-by-default,
first-tab ordering, zero-active-item hiding, nested flattening),
`internal/pages/settings_categories_tab_test.go` (elevation/persistence),
`e2e/tests/sell-screen-categories-tab-2283.spec.ts` (tab visibility toggle,
item-picker + a modifier item opening its own picker from inside the modal,
touch-target geometry at 1024×600/360px).

## Independent review — verdict: PASS-WITH-FIXES

Spawned as an Opus subagent in an isolated git worktree with no visibility
into the Dev subagent's own reasoning. Findings, all fixed before merge:

1. **False-pass regression test (real defect).**
   `TestButtonsHTTPList_CategoriesTabOffByDefault` ran against a DB fixture
   with no categories/buttons seeded, so `buttons.html`'s `$hasTabs` guard
   suppressed the tab bar regardless of the setting — the test passed even
   with the settings gate ripped out (verified live). Fixed by seeding two
   real categories and adding a positive-control assertion that the tab bar
   renders when the setting is on.
2. **Invalid tablist ARIA relationship.** The tab carried
   `aria-controls="cat-panel-categories"` pointing at a `role="group"` panel
   (not a tabpanel) — the template's own comment claimed no `aria-controls`
   was needed while one was set anyway. Fixed: panel is now
   `role="tabpanel" aria-labelledby="cat-tab-categories"`, comment corrected,
   locked in with new assertions.
3. **Docs-shots surface hash** refreshed via the documented
   `update-docs-shots-surface-hash.sh` escape hatch after fix 2 (an
   attribute/comment-only template edit, zero rendered-pixel change,
   independently confirmed by comparing against a full `make docs-shots`
   regeneration's own computed hash before discarding the unrelated
   ~120-screenshot diff that run produced in this sandbox).
4. **Test fixture drift**: a new `settings` table DDL in a test helper
   dropped `DEFAULT CURRENT_TIMESTAMP` present in the real
   `001_init.sql` migration — copied verbatim from the migration.

### TDD re-verification performed personally
- Reverted the `htmx.process(body)` call in `openCategoryPicker()` →
  re-ran the picker e2e test → failed with a real timeout waiting on the
  cloned tile's now-inert `hx-post` (`/api/pos/scan` never fired) → restored
  → passes again.
- Reverted `ButtonStore.CategoriesTabEnabled` to return `true`
  unconditionally → confirmed `TestButtonsHTTPList_CategoriesTabOffByDefault`
  still passed (proving finding 1's vacuity) → after fixing the test, the
  same revert now fails on-topic → restored → passes again.

### Checklist (kiosk isolation, i18n, RTL, touch targets, settings
persistence, money/data-access, help manual, scope discipline) — all
verified PASS; full detail in the subagent's own report (this file
summarizes it). Touch-target floor (9.5rem) independently measured via a
throwaway Playwright probe against a worst-case tile (uploaded thumbnail +
2-line name + price): category tile 161.50px vs. tallest possible product
tile ≈129px intrinsic — claim holds with real margin.

## Noted, not fixed (out of scope for this card)

- `web/help/{ar,fa,tr}/sell.md` step 1 was already entirely English on
  `main`; the new sentence was appended in the same (pre-existing,
  untranslated) English paragraph rather than creating a new gap.
  `guard-help-drift.sh` passes (step count unchanged). Worth a dedicated
  translation pass separately.
- `make docs-shots` is not byte-reproducible in this sandbox vs. whatever
  environment produced the committed screenshot set (~120 of 124 shots
  differed on an otherwise-unrelated full run) — likely a font/rendering
  difference in this container. `docs-shots-determinism.yml` only proves
  two runs agree on the *same* machine, so it can't see this; flagged for
  awareness, not actioned here.
- `class="category-tile-grid"` has no CSS rule selecting it — harmless
  dead hook, left in case it's a deliberate future styling seam.
- Cloned tiles can go stale if `.products` re-renders while the picker
  modal is open — consistent with `#parked-orders-modal`'s existing
  behavior, not a new defect introduced here.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l .` clean.
- Full `go test ./...` (not just touched packages) — green, twice
  (pre- and post-merge of `main`).
- `golangci-lint run ./...` — 0 issues.
- `guard-data-access`, `guard-kiosk-engine`, `guard-i18n`,
  `guard-compliance-claims`, `guard-docs-shots`, `guard-help-topics`,
  `guard-help-drift`, `guard-page-http-error` — all green.
- e2e spec run for real via Playwright (not assumed): 3/3 pass, plus a
  12-test regression sweep across tab-bar overflow/ARIA, product-tile
  category color, and sale-screen category-tab search specs.
- `shellcheck`/`guard-shellcheck-version` and `guard-deadcode-baseline`
  could not run in this sandbox (no `shellcheck` binary, no GTK/WebKit
  headers) — pre-existing environment gaps unrelated to this diff; real
  CI (`ubuntu-latest`) has both.

## Safe to merge

Yes. `merge_method: "merge"` per this repo's own standing rule
(ut-docs#250) — preserves the real commit author/committer/signature.

## Deferred (Backlog-worthy, not blocking)

- A dedicated translation pass for `web/help/{ar,fa,tr}/sell.md`'s
  already-English step-1 paragraph.
- Investigate the docs-shots sandbox-vs-CI rendering drift found during
  this review (separate from this card's own scope).

# Code review — product tiles show their category's color accent

- **Date:** 2026-08-31
- **Ticket:** ut-docs#1325
- **Branch:** `feat/1325-tile-category-color`
- **Reviewed commit:** `21297b3` (the feature commit; the reviewer's one
  finding was fixed by a follow-up commit regenerating manual screenshots)
- **Reviewer:** independent pass, fresh-context Sonnet subagent (never saw
  the dev reasoning). Complexity: `easy`, so per "Model routing by
  complexity" this is the one tier where "different model" relaxes to
  "different instance" — Sonnet built it, a clean-context Sonnet reviewed
  it.
- **Verdict: SAFE TO MERGE**, after fixing the one blocking finding (below).

## What shipped

CSS + one template attribute, plus TDD coverage at two layers:

- `web/public/app.css`: `.btn-tile` gets `border-inline-start: 4px solid
  var(--cat-color, var(--accent))`, reusing the exact custom-property/
  fallback pattern `.category-header` and the tab bar already use — no new
  design token, no new mechanism. `padding-inline-start` is trimmed by the
  matching 3px (border grew 1px→4px) so the extra width doesn't visibly
  nudge the centered tile content off-axis.
- `web/ui/partials/buttons.html`: one added attribute,
  `style="--cat-color: {{ $g.Color }}"`, on the per-tab
  `<div id="cat-panel-...">` wrapper. This closes a real inheritance gap
  found while scoping the ticket: a top-level category's own (non-nested)
  buttons render inside that div via the `"category-group-body"` template,
  a **sibling** of the tab `<button>` that already carried `--cat-color`,
  not its descendant — CSS custom properties only inherit down the tree, so
  without this, every top-level category's own tiles (the common case —
  only a nested subcategory's own `"category-group"` wrapper already
  carried the property) silently fell back to `--accent` for everyone.
- `internal/ui/buttons_http_test.go`:
  `TestButtonsHTTPList_TopLevelTabPanelCarriesColorForDirectTiles` pins the
  panel-inheritance fix at the server-rendered-markup level, scoping its
  assertions to each tab panel's own substring so it can't pass by matching
  the sibling tab button's already-correct color instead.
- `e2e/tests/product-tile-category-color-1325.spec.ts`: drives a real
  browser against the real demo-seeded catalog (`Coca-Cola 330ml` /
  `itm001`, genuinely top-level under `cat_drink`, no subcategory nesting)
  and asserts the tile's actual computed `border-inline-start-color`
  resolves to the same value as its tab panel's `--cat-color` and differs
  from the `--accent` fallback — real cascade/inheritance, not just markup
  presence.
- `web/help/img/{en,fa,ar,tr}/sell.png` + `manifest.json` (`make
  docs-shots`) — `sell.md` claims `/ui/buttons`, the screen this ticket
  visually changed. Added by the fix-up commit below.

No Go/data-layer changes — pure CSS + template + test.

## Requirements traced (ut-docs#1325 acceptance criteria)

- ✅ `.btn-tile` picks up its category's `--cat-color` as a visible accent
  (left/inline-start border), matching `.category-header`'s existing
  treatment.
- ✅ Falls back gracefully for uncategorized items: the flat (`$flat`)
  render branch — used only when every button is in the synthetic
  uncategorized bucket — has no bordered `--cat-color` wrapper at all, so
  its tiles correctly keep resolving `var(--cat-color, var(--accent))` to
  `--accent`. Confirmed this is the intended fallback, not an oversight,
  by design and by the reviewer independently.
- ✅ Checked in both the flat view and the per-category tab view — the tab
  view specifically needed the panel-`div` fix above; the flat view needed
  no change (see above); the single-real-category view (no tabs) was
  already correct pre-diff (`"category-group"`, unchanged by this diff).
- ✅ WCAG: color is an accent only, not the sole differentiator — tile
  name/price/thumbnail and the section header/tab above it all still
  identify the category (satisfies 1.4.1). Contrast-clamping an
  admin-set hex color for 1.4.11 is a pre-existing, shared risk with the
  `--cat-color` header/tab treatment this reuses (any category color has
  always been admin-settable with only hex-format validation, no
  luminance check) — explicitly scoped OUT of this ticket at BA/Architect
  time rather than silently left unconsidered.
- ✅ RTL: `border-inline-start`/`padding-inline-start` only, no
  `left`/`right` literals. Re-confirmed by the existing
  `sale-screen-category-tabs-search-418.spec.ts` Farsi/RTL test, which
  still passes.

## TDD verification

Independently re-verified by both the developer and the reviewer,
separately, via revert-then-restore on `web/ui/partials/buttons.html` +
`web/public/app.css`:

- **Go test** (`TestButtonsHTTPList_TopLevelTabPanelCarriesColorForDirectTiles`):
  fails without the fix (panel markup carries no `--cat-color`), passes
  with it. Confirmed twice, independently.
- **e2e test** (real browser, real running till): without the fix, the
  tile's computed `border-inline-start-color` resolves to the pre-existing
  plain `--border` color (`rgb(226, 232, 240)`), not even `--accent` —
  because before this diff `.btn-tile` had no inline-start accent rule at
  all. With the fix, it resolves to the same `rgb(...)` value as the tab
  panel's own `--cat-color`. Confirmed by the developer; the reviewer
  verified the test's structure and demo-data assumptions but did not
  execute it (no Playwright browser installed in the review's isolated
  environment) — noted as a limitation, not a defect.

## What the independent review found

**Blocker (fixed):** `guard-docs-shots.sh` failed on this branch —
`sell.md`'s manual topic claims `/ui/buttons`, and this ticket visibly
changed that screen (every product tile now shows a colored left-border
accent) without regenerating the screenshot, per `CLAUDE.md`'s "the manual
ships with the feature" rule. Verified by the reviewer against a fresh
`main` checkout that the guard passes cleanly there — the failure was
caused by this diff, not pre-existing noise.

**Fix:** ran `make docs-shots` (92 screenshots regenerated across 23
routed topics × 4 locales); committed the 4 that actually changed
(`en/fa/ar/tr` `sell.png` + `manifest.json`) plus one unrelated
`tr/invoices.png` that came back with a handful of changed encoder bytes
(same pixels, no route this diff touches — pre-existing screenshot-capture
non-determinism in an unrelated topic, not a regression). `guard-docs-shots.sh`
re-run after the fix: clean.

**Everything else the reviewer checked was solid, no other nits:**
correctness against every acceptance criterion, CSS cascade/specificity
(the new `border-inline-start`/`padding-inline-start` declarations
correctly override only the inline-start side of the pre-existing uniform
`.btn-tile` border via source order, no specificity bug), color-injection
safety (`{{ $g.Color }}` reuses the exact pattern already shipped and
reviewed for `.category-header`/`.tab`; `resolveCategoryColor` only ever
returns a regex-validated `^#[0-9a-fA-F]{6}$` value or one of 9 fixed
palette constants), RTL, i18n (no new user-facing strings — pure CSS/
markup), data-access (no SQL involved), and every other CI-blocking guard
in `.github/workflows/ci.yml`'s `build` job.

## Verification

| Check | Result |
|---|---|
| `gofmt -l .` | clean |
| `go build ./...` | clean |
| `go test ./...` | all packages pass |
| `go test ./internal/ui/...` (targeted, both before/after revert) | pass |
| e2e: `product-tile-category-color-1325.spec.ts` | pass (real browser) |
| e2e: `sale-screen-category-tabs-search-418.spec.ts`, `products-scroll-affordance-1313.spec.ts`, `catalog-image-to-till.spec.ts` (regression check) | all pass |
| `guard-data-access.sh` / `guard-i18n.sh` / `guard-compliance-claims.sh` / `guard-help-topics.sh` | pass |
| `guard-docs-shots.sh` | pass (after the fix-up commit) |
| Every other CI-blocking guard (`guard-kiosk-engine`, `guard-plugin-menu-read`, `guard-webkit-version`, `guard-kiosk-launch-flags`, `guard-android-status-address`, `guard-android-i18n`, `guard-emoji-font`, `guard-htmx-loaded`, `guard-autofill-suppression`, `guard-e2e-fixtures-import`, `check-brand-assets`, `guard-makefile-version`) | pass |

## Scope note

Contrast-clamping admin-set category colors for WCAG 1.4.11 (a pale color
becoming a near-invisible accent) is a pre-existing condition shared by the
`.category-header`/`.tab` treatment this ticket reuses, not newly
introduced — deliberately out of scope here (BA/Architect judgment,
confirmed by the reviewer): building real contrast-aware color adjustment
is a materially larger feature than an `easy`-tier tile accent, and color
is never this UI's sole differentiator for a category (name/grouping
remain). Worth a dedicated backlog card if a real shop ever hits it.

## Addendum — stale-PR sweep, merge conflict + CI fix (2026-08-31, later cycle)

The reviewed diff above sat open for ~1.5h after `#685` merged to `main`
(unrelated `tax.rate.ask` logging fix for ut-docs#1370), which turned
`mergeable_state` `dirty`: both branches independently ran `make
docs-shots`, so the generated `web/help/img/**` binaries and
`manifest.json` collided on the merge, with no code-level conflict (`git
diff` confirmed `web/public/app.css`/`web/ui/partials/buttons.html`
untouched by `#685`). Resolved by merging `main` in and re-running `make
docs-shots` against the merged tree per this repo's own
generated-file rule, rather than hand-picking either side's binaries.

That re-run's own output was committed in a follow-up commit
(`b9eb573`) — the first push (`7a5c3be`) accidentally left it staged but
uncommitted, which CI's `guard-docs-shots` correctly caught (`build` job,
exit 1) since the pushed commit still carried `#685`'s screenshots, stale
against this branch's `buttons.html` change. No code fix was needed, only
committing the already-regenerated files; `guard-docs-shots.sh` re-run
locally against `b9eb573` confirms clean (`surface 03840734a943…`).

# 2026-09-12 — Sale screen: compact category strip with icon-collapsed search (ut-docs#2173, closes ut-docs#993)

## What shipped

The sale screen's product panel spent **two full rows** of vertical height
before the first product tile: a `.products-header` row (`<h2>PRODUCTS</h2>`
+ an always-expanded `<input type="search">` + a compact `+` link to
`/designer`), then `.tab-bar` — one `role="tab"` per top-level category with
`flex-wrap: wrap` and unbounded rows. Measured at the documented 1024×600
kiosk floor with 12 categories: the tab bar alone was **247px over 5 rows**
and **zero** product tiles were visible (ut-docs#993's own measurement).

Both rows are now one `.products-strip`:

- the category tab bar stays a real WAI-ARIA tablist but becomes
  **single-row and horizontally scrolling at every width**, with the
  scroll-shadow recipe ported from `.catalog-form-head .tab-bar`
  (ut-docs#2024);
- **search and edit are icon buttons** at the strip's trailing end
  (`{{ icon "search" }}` / `{{ icon "pencil" }}`), each with the mandatory
  `aria-label` + `title` pair (the ut-docs#2010 list/dialog standard);
- tapping search **swaps the strip for the search input in place** — same
  row, same height — with a new `arrow-left` back control at the leading
  end that restores the strip and clears the query;
- the removed visible `<h2>` is replaced by a `.visually-hidden` one plus
  `<section aria-labelledby>`, keeping **both** the landmark and the
  heading-navigation outline at zero pixels.

Filtering semantics are deliberately unchanged (`matches()` /
`sectionHasMatch()` still filter within the active tab) — see "Deferred",
which is the one consequence of that decision.

`window.utTabBarFade` was extracted from `catalog.html`'s inline script into
`web/public/app.js`, with `catalog.html` keeping a same-named delegator, so
the subtle LTR/RTL scroll-origin handling (`Math.abs(scrollLeft)`, because
Chromium/Firefox/Safari use `0→+max` for LTR and the mirrored `0→−max` for
RTL) exists once rather than twice.

## Verified beyond automated tests

- **The TDD claim was re-verified independently**, not taken on the
  implementer's word. Reverting only the implementation files while keeping
  the specs, the inverted ut-docs#424 test fails with
  `Expected: 1, Received: 7` (seven wrapped rows). The review then went
  further and disabled the row assertions to prove the **ut-docs#993
  geometry assertion is independently load-bearing** rather than a
  passenger: `first tile bottom (585.02) should be <= .products panel
  bottom (349.42)`.
- **Screenshots were taken and actually looked at** at 1024×600 and 360px,
  LTR and RTL, at rest and with search open, plus the regenerated manual
  screenshot. Not just asserted on.
- **Geometry measured, not assumed**: `.products-strip` is **51.64px in
  both states** (identical to <1px, pinned by a spec) and the tab bar is a
  single 51.64px row, down from 247px over 5 rows. One full tile row is
  visible inside the panel without scrolling.
- **The shared `.tab-bar` component was probed live**, not reasoned about:
  `.tender .tab-bar` and `.catalog-form-head .tab-bar` still compute
  `flex-wrap: wrap` / `overflow-x: visible` at 1024px, while
  `.products-finder .tab-bar` computes `nowrap` / `auto`. No leak into the
  two other screens that share the class.
- **The one full-suite failure was proven pre-existing**, not assumed: a
  worktree at the clean base commit `ad884494`, with none of this change
  present, fails `catalog-tab-strip-fade-2024.spec.ts`'s resize test **2 of
  6 runs**. Filed as ut-docs#2182.

## What the independent review found

Reviewed at Opus (card is `complexity:medium`; Sonnet implemented) in an
isolated worktree. Verdict was **not safe to merge** until three defects
were fixed. All three are fixed on this branch, each with a negative
control proving the new assertion can fail:

| # | Severity | Finding | Resolution |
|---|---|---|---|
| **F1** | should-fix | **Empty-catalog header regressed.** The diff deleted `.products-header` / `.products-header h2` / `.products-add` from `app.css`, but `buttons.html`'s `{{ if not .Groups }}` branch still renders those classes. Reproduced: the header went from one ~42px flex row to a **77.8px stacked block** (`display: block`, title and add-link on separate rows) — the opposite of this card's goal, on the first screen a brand-new shop sees, covered by no test. | Fixed — rules restored, scoped and commented as empty-state-only (the `input[type="search"]` rule was genuinely dead and stayed deleted). New spec added. Negative control: deleting them again fails with `Expected "flex", Received "block"`. |
| **F2** | should-fix | **Keyboard focus dropped on close.** `closeSearch()` hid the focused element with nothing taking focus; `activeElement` became `<body>`, so a keyboard/screen-reader operator had to Tab in from the top of the document on the till's primary screen. **Confirmed real**, but only with a *real tap* — see "The F2 detour" below. | Fixed — `closeSearch()` mirrors `openSearch()` and returns focus to the search icon via `$nextTick` + `x-ref`. Escape also now closes from the back button, not only from the input. Verified with real clicks: focus is `#products-search` after opening and the search icon after closing, where both were `BODY` before. |
| **F3** | should-fix | **False-pass test.** `keyboard Tab reaches the search and edit controls` used `locator.focus()` + `toBeFocused()`, which succeeds on a `tabindex="-1"` element, and never pressed Tab. Reproduced: the test passed with **both controls removed from the tab order**. | Fixed — now asserts `not.toHaveAttribute('tabindex','-1')` (as ut-docs#1702's spec does) **and** drives a real `Tab` press from the search icon to the edit link. Negative control: injecting `tabindex="-1"` now fails with `Expected: not "-1", Received: "-1"`. |
| **F6** | nice-to-have | `.products-strip`'s `margin-bottom: .6rem` sat inside `.products-finder`'s own `gap: .6rem`, giving ~19px where ~10px was intended — on a card whose entire purpose is reclaiming vertical pixels. | Fixed — margin removed; the parent gap provides the spacing. Visible in the regenerated `sell.png`: the second tile row now shows its label. |
| **F9** | nice-to-have | Two stale/incorrect comments: a line reference that went stale inside the same cycle, and the `+4px` justification claiming a font-size dependency that does not exist. | Fixed. The review decomposed the 8px into pure px constants (bar `padding-block` 2+2 + `border-bottom` 2 + tab `border-block-start` 3 + `border-bottom` 3 − `margin-bottom` 2 − input borders 1+1), so it is font-size- and rem-independent and brittle only to **border-width** changes. The comment now says that, and the line reference was dropped rather than re-pinned. |

Also raised and **checked clean, so it needn't be re-checked**: no
`os.MkdirAll`/`paths.Data` exposure (no file-writing code in the diff), no
offline-first impact, no physical `left`/`right` properties (the two
physical usages — the fade gradients and `scaleX(-1)` — are both
deliberately `dir`-overridden and both covered by the RTL spec),
`data-testid="products-add-link"` preserved with `payment-overlay-focus-
sweep-1702.spec.ts` green at both viewports, `x-cloak` correct with no
pre-hydration flash, the `utTabBarFade` move safe (all three `catalog.html`
call sites are inside event/rAF callbacks, and `app.js` is `defer`-loaded in
`<head>`), and the scroll-fade surviving a search round-trip
(`scrollLeft` preserved at 1723 across the hide/show).

## A gap the orchestrator found before review

The new spec asserted the strip scrolls but **never asserted the
scroll-fade** — which is the only on-screen cue that more categories exist
past the edge, and precisely the gap ut-docs#2024 was filed for on the
catalog strip. Added assertions on the `::before`/`::after` **computed
opacity** (the user-visible outcome, so a broken class-to-pseudo wiring
fails too), and confirmed by negative control that disabling the
`index.html` wiring fails them with `Expected "1", Received "0"`.

Separately, the review noted that the ut-docs#424 assertion kept from the
old test (`tab height < 60`) **can no longer fail** — with `flex-wrap:
nowrap` plus the pre-existing `white-space: nowrap`, a tab cannot wrap its
own label. It is kept (it would catch a reintroduction of wrapping) but is
no longer live coverage, so it is now paired with an assertion that *can*
fail in the nowrap world: no tab's `scrollWidth` exceeds its `clientWidth`,
i.e. no label is **clipped** inside its own box.

## The F2 detour — three wrong turns, recorded because they cost the most

Fixing a two-line focus bug took three attempts. All three failure modes are
silent, which is why they are written down rather than quietly corrected.

1. **`this.$el.querySelector(...)` does not resolve here; `$refs` does.** The
   first fix looked correct and did nothing. `focus()` on an element you
   didn't actually find throws nothing, and `focus()` on a still-hidden
   element is also a silent no-op — so a broken fix and a working one are
   indistinguishable without measuring `document.activeElement`. `x-ref` +
   `$refs` is the pattern the input already used in this same file, and is
   what works.

2. **A double `requestAnimationFrame` was the wrong theory.** The second
   attempt assumed the ut-docs#1956 trap (Alpine's `x-show` write landing on
   the *second* animation frame, per `catalog.html`'s `afterTabPaint`). A
   browser probe disproved it — the input's `display` flips at the **first**
   frame — and the change made things worse, breaking `openSearch()`, which
   had been working. `$nextTick` is sufficient. The comment in the template
   now says this, because the first version of that comment confidently
   explained a mechanism that wasn't the cause.

3. **The bug only reproduces with a real tap.** Driving the controls with
   `element.click()` from a script never moves focus off the `autofocus`ed
   barcode scan input, so the defect is invisible that way: the probe showed
   focus sitting on `input[name=code]` throughout and looked healthy. Only
   `page.click()` (a real tap, which focuses the button first) shows focus
   falling to `BODY`. Anyone re-checking this by hand must use a real click.

## A self-inflicted break worth recording

While fixing F2, the explanatory comment was written **inside** the
`x-data="{ ... }"` attribute, and it quoted a word and mentioned a `<body>`
tag. A literal `"` in template *source* terminates the attribute —
`html/template`'s contextual escaping covers `{{ }}` interpolations, not
literal text the author typed. The failure was silent and badly misleading:
`/ui/buttons` returned **HTTP 200 with zero bytes**, the products panel
disappeared, and **28 sale-screen e2e specs** failed with null-element and
click-timeout errors that pointed nowhere near the cause.

`internal/pages/buttons_api_test.go` already covers this class and would have
caught it in about a second — it seeds a button and asserts the label appears
in the fragment. It was missed because only `go build` / `go vet` were re-run
after that edit instead of the full gate. No new test was added: the existing
one is adequate and the gap was gate discipline, not coverage. Long prose,
quotes and angle brackets now live in a `{{/* */}}` comment above the
element.

## Deferred

- **ut-docs#2181 — search is confined to the active category.** Because
  search now *replaces* the strip, the operator cannot see or change
  category while searching, and closing search clears the query. Searching
  for an item in another category returns "No matching products." with no
  way to widen. This follows from the card's own acceptance criteria
  (filtering semantics frozen, back arrow clears the query), and the
  reviewer reached the same conclusion independently. The product owner's
  stated reference — SumUp — searches the whole catalogue, so the
  recommendation on the card is to match that. **Not a defect in this
  change; a product decision this change surfaces.**
- **ut-docs#2182** — the pre-existing ~50%-flaky
  `catalog-tab-strip-fade-2024` resize test (proven pre-existing at base
  commit `ad884494`).
- **ut-docs#2184** — `make docs-shots` is non-deterministic: a
  sale-screen-only change produced a 50-file diff of which 5 were real, the
  other 43 differing by <20 bytes on pages the change cannot affect. This
  is the mechanism behind the recurring cross-lane docs-shots conflict.
  Not hand-pruned here: `manifest.json` records per-topic content hashes
  and says *"Do not edit by hand."*
- **Review F7** (in the `$flat`/single-category branches, with no tab bar,
  the strip is sized by `.btn.compact` at rest and by the padded input in
  search mode, a ~9-11px jump) — reasoned from the CSS, **not reproduced**,
  and not reachable on the demo till. Low impact.
- **Review F8** — the new `overflow-x: auto` is new exposure to a classic
  (non-overlay) horizontal scrollbar on a real kiosk build. The reviewer
  tried and failed to force one in headless Chromium, so there is **no
  positive evidence of a problem**; wants one eyeball on the real tablet.
  Recorded here rather than silently assumed fine.

## Not verified

- **Not checked on the real pilot tablet.** The card asks for it; the
  tablet is on v0.14.13 and this change is not released. Stated plainly
  rather than left to read as "checked and fine" — this is the ut-docs#300
  failure mode. Review F8 is the specific thing to look at when it is.
- `golangci-lint` was run scoped to the touched packages, not repo-wide.
- Two local gate failures are **environmental, not from this diff**:
  `guard-shellcheck-version` (local shellcheck 0.11.0 vs the CI-pinned
  0.9.0) and the SC2329 findings that only exist in 0.11 — **zero `.sh`
  files are in this diff**.

## Language packs

The three new keys (`products.search_open`, `products.search_close`,
`products.edit`) are **brand new**, so pack-first is not achievable — each
pack's key-drift guard treats a translated key with no matching core key as
an orphan. Per the corrected rule (ut-docs#1934), core merges first and
`main` goes red on `lang-pack-drift` until the pack PRs land. Both are
prepared, translated against each pack's **own** established vocabulary
rather than literally (`Schnellwahltasten` / `Botones rápidos` from
`nav.designer`; `Schließen` / `Cerrar` from `common.close`), with the
manifest version bumps their `CLAUDE.md` requires, and land immediately
after this merge.

## Verdict

**Safe to merge** after the three review fixes above, each proven by a
negative control. Everything the card set out to do is verified: the strip
is one row, a full tile row is visible at the 1024×600 kiosk floor, the
shared `.tab-bar` is untouched on the two screens that also use it, the
`utTabBarFade` extraction is safe, and the rest/search height match holds
to the pixel.

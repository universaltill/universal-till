# Code review: variants grid Save/Save Variant reachable without horizontal scroll (ut-docs#1967)

**Date:** 2026-09-10
**Card:** ut-docs#1967 — "Variants grid: the Save Variant button is
off-screen at every shipped viewport — reachable only by
horizontal-scrolling a nested container."
**Author:** Dev phase, this pipeline (Sonnet, `complexity:medium`)
**Reviewer:** independent Opus subagent, isolated worktree (per
`scrum-master`'s "Model routing by complexity")

## What shipped

The catalog item's variants panel (`web/ui/partials/catalog_variants.html`)
renders a grid of variant rows (`.vg-cols`) whose natural width (~54rem)
exceeds the panel's available column at every shipped viewport (1024×600
kiosk floor, 1280×800 pilot tablet, and narrower). The grid scrolls
horizontally on its own (`.variant-grid { overflow-x: auto }`, armed under
1300px), which absorbed the overflow well enough that no *page*-level
overflow assertion ever caught it — but it meant the row's own rightmost
column (the commit action: "Save" on an existing variant row, "Save
Variant" on the add-new row) sat off past the visible edge, reachable only
by discovering and performing a horizontal drag inside a nested scroller.

**Fix** (`web/public/app.css`, inside the existing
`@media (max-width: 1300px)` block that already arms the scroller): make
`.vg-cols`' own rightmost column `position: sticky; inset-inline-end: 0`,
so the commit action stays pinned to the scroll container's trailing edge
regardless of horizontal scroll position — a no-op above 1300px, where
there's no overflow to stick against. `inset-inline-end` is a logical
property (this repo's standing RTL rule), so it resolves to `left: 0` under
`dir="rtl"` with no separate RTL-specific rule needed.

This was chosen over the ticket's own "approach 1" (give the grid the
panel's full width by moving the aside/left-rail content above it) because
sticky guarantees the acceptance criteria unconditionally at every
viewport without depending on exact pixel math, and it doesn't touch the
aside/rail layout that two *other* in-flight cards (ut-docs#1956,
ut-docs#1957) are already mid-restructuring — avoiding stepping on their
work. The ticket explicitly names sticky positioning as its second-choice
option.

**Regression test:** `e2e/tests/variant-grid-save-reachable-1967.spec.ts` —
asserts the button's real bounding box against the real viewport (not
page-level overflow — the acceptance criteria's own explicit distinction,
since page-level overflow is exactly what this defect already passed) plus
a real `elementFromPoint` hit-test, at 1024×600, 1280×800, 360px, for both
the add-variant row's own button and an existing variant row's own Save,
plus (added in review) an RTL case.

`make docs-shots` was re-run (twice — once pre-review, once after the
review fix landed) since `web/public/**` changed; `guard-docs-shots.sh`
passes.

## Independent review

Spawned as an Opus subagent in an isolated git worktree (branched from a
WIP commit on the feature branch), per `complexity:medium`'s model-routing
rule (Sonnet builds, Opus reviews).

### Blocking finding — fixed

**The first draft made the exact button this card is about illegible.**
`.vg-new > :last-child` is not a wrapper cell around the "Save Variant"
button — it **is** the `<button class="btn">` itself. That selector's
specificity `(0,2,0)` beats the bare `.btn` rule `(0,1,0)`, so giving it
its own `background: var(--surface-2)` overrode `.btn`'s accent fill while
`color: var(--accent-contrast)` (white) remained — measured live as white
text on `#f8fafc`, ~1.04:1 contrast. The existing-variant row's own Save
button escaped only by luck (`.btn.primary` is also `(0,2,0)` but sits
later in the stylesheet, so it won on source order — not a rule anyone
should rely on holding).

Root cause: the trailing cell's own background was never actually needed
for the two button rows — each button already paints its own opaque
background over that exact box. Only the header row's trailing cell (a
genuinely empty `<div>`) needed one, so scrolling content underneath it
wouldn't show through.

**Two secondary issues folded into the same fix:** the rules had been
placed *after* the closing `}` of `@media (max-width: 1300px)` rather than
inside it — contradicting the fix's own "no-op above 1300px" reasoning —
so `position: sticky`/`z-index` applied at every viewport width, not just
the ones with anything to stick against. Moved inside the media query and
confirmed `position: static; z-index: auto` at 1440px.

Neither the bounding-box check nor the hit-test in the original e2e draft
could have caught the contrast bug — both pass cleanly on a fully
on-screen, fully clickable, invisible button. This is a real illustration
of why an independent review pass looks for problems geometry-based tests
structurally cannot see.

### Verified, not just fixed

- **TDD claim re-verified independently, twice**, not taken on the
  implementer's word: reverted `app.css` to its pre-fix state, confirmed
  all tests went red with the exact predicted failure mode
  (`withinViewport: false` / hit-test occluded), restored, confirmed
  green — then repeated the same red/green cycle a second time against
  the reviewer's own amended fix.
- **RTL genuinely verified, not assumed**: measured the computed style
  under `dir="rtl"` (`left: 0px; right: auto`) rather than trusting that
  "logical property" implies correctness, and added a 5th test case
  asserting the computed `left` value directly — a physical `right: 0`
  would have passed all four original (LTR-only) tests while stranding
  the button off the RTL edge.
- **`.vg-row:not(.vg-new) > button[type=submit]`'s direct-child
  combinator re-derived against the template**, not assumed: the Save
  button is the row's 8th (last) grid child; the barcode "＋" button lives
  nested inside `.chip-row > form.chip-add`, earlier in DOM order, so a
  descendant selector without `>` would silently match the wrong button.
- **Screenshot non-determinism confirmed with evidence, not just
  plausibility**: pixel-diffed the regenerated PNGs against pre-change
  versions (7–8 px differ out of 614,400, in antialiasing regions only);
  confirmed the `/catalog` docs screenshot has no item selected, so the
  variants grid is never in frame — not a regression from this change.
- **Blast radius confirmed**: `.vg-cols`/`.vg-head`/`.vg-new` are used
  only in `catalog_variants.html` — no other template shares them.
- No SQL/repository-pattern, money, or new i18n-key concerns (pure CSS +
  a test file). No real client/shop name used as demo data (generic
  "Save Reach Probe" / "330ml"). `guard-i18n.sh`, `guard-compliance-
  claims.sh`, `guard-e2e-fixtures-import.sh`, `guard-help-topics.sh` all
  green.

### Non-blocking, deliberately not fixed

- No leading-edge shadow/separator on the sticky column when content
  scrolls underneath it — there's no logical/RTL-safe `box-shadow`
  offset property, and the accent-colored buttons already read clearly
  as a distinct, pinned control. Cosmetic only; equally true of the
  pre-review draft.
- `geometry()`'s `withinViewport` helper in the test file checks
  horizontal bounds only (deliberately, since vertical reachability is a
  separate, already-solved concern — the page's own normal scroll) — a
  note for whoever reuses that helper elsewhere, not a defect here.

### Residual gap — hardware verification

The card's own acceptance criteria includes "verified on the real pilot
tablet, not only in a headless browser." No cloud cycle has physical
device access (same class of gap as ut-docs#1281's kiosk lock-task
verification). Residual risk is assessed as low: `position: sticky` is
long-supported on the webkit2gtk-4.1 target this product ships
(ADR-0028; `guard-webkit-version.sh` passes), and the mechanism was
verified in a real Chromium engine, not just read. One thing worth a
human eyeball on the real tablet: confirming the pinned column doesn't
visually obscure the Active checkbox when the grid is scrolled fully to
its start. Not filing a separate `blocked:env` follow-up card for this —
unlike ut-docs#1281's kiosk-lock verification (which needed physical
gesture input `adb` cannot synthesize), this is a low-risk, purely visual
check a developer can fold into their next hands-on-hardware session
rather than a dedicated card.

## Verdict

**Safe to merge.** Build, `go test ./...`, and every listed guard are
green; the regression suite (5 cases, including RTL) is green with the
fix and red without it (independently re-verified); the one real defect
the review found (illegible button text) was fixed and re-verified before
this record was written.

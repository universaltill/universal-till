# Code review: item form header height at 360px (ut-docs#2000)

**Date:** 2026-09-10
**Card:** ut-docs#2000 — "At 360px the item form's pinned header takes
42% of the screen."
**Author:** Dev phase, this pipeline (Sonnet, `complexity:medium`)
**Reviewer:** independent Opus subagent, isolated worktree (per
`scrum-master`'s "Model routing by complexity")

## What shipped

The item add/edit dialog's pinned `.catalog-form-head` (title+delete row,
then +New/Close/Save, then a five-tab strip) measured 312px of a 360×740
viewport (42%) — passing, non-overlapping, but too much chrome for a
merchant editing an item on a phone.

**Fix**, scoped inside the existing `@media (max-width: 700px)` block
(`web/public/app.css`), so the 1024×600 kiosk floor is untouched:

1. **Close/Save go icon-only** at phone width, same pattern as the
   dialog's existing icon-only Delete button — an icon span plus a
   `.btn-label` span that's visually hidden under this query, while the
   button's own `aria-label`/`title` (always present) carry the
   accessible name regardless of width. Two new Lucide icons ("x",
   "check") added to `internal/httpx/icons.go`'s shared icon map.
   `+New` stays text-only at every width (see F2 below).
2. **The tab strip stops wrapping** — `.catalog-form-head .tab-bar`
   (a selector scoped to this one dialog, not the generic `.tab-bar`
   component tills.html/index.html also use) goes from `flex-wrap: wrap`
   to a single horizontally-scrollable row. The existing roving-tabindex
   arrow-key handler still works; browsers scroll a focused element into
   view automatically.

Result: 312px/42% → ~180px/24% at 360×740 (English; verified identical in
fa, since icon-only removes the text-length dependency entirely). No new
i18n keys — Close/Save/+New's `aria-label`/`title` reuse the existing `T`
calls already rendered as their visible text.

## Independent review (Opus, isolated worktree)

Ran the diff's own gate independently (`gofmt`, `go build`, `go vet`,
targeted `go test`, `golangci-lint`) plus its own from-scratch TDD
re-verification (reverted the CSS, confirmed the new Go test's assertions
fail with 12 concrete mismatches, restored it). Findings, triaged:

- **F1 (blocker, fixed):** `guard-docs-shots.sh` was not run —
  `web/ui/pages/catalog.html`/`web/public/app.css` are in its tracked
  surface. Ran `make docs-shots` (112 screenshots across 28 topics × 4
  locales); only `en/sell.png` changed, and only by a 1-byte PNG encoding
  difference (re-run nondeterminism, not a visual regression) — re-ran
  after the F7 help-doc edit too, since that also touches the guard's
  tracked topic-markdown hash.
- **F2 (fixed):** pairing a new "plus" icon with the `+New` button
  doubled its glyph — every shipped locale's `catalog.plus_new` string
  already bakes in a "+" (`"+ New"`/`"+ Yeni"`/…), so the button rendered
  "＋ + New" above the breakpoint (where +New is visible — it's hidden in
  create mode, which is why the Tester's screenshot missed it). Fix:
  dropped the icon and the `.btn-label` wrapper for `+New` entirely — it
  stays a plain text button at every width, since its label is already
  short and doesn't need collapsing. Icon removed from `icons.go`; a new
  Go test (`TestItemForm_ResetIsTextOnlyNotDoubleIconed`) pins the
  negative — no `.btn-ico`/`.btn-label` inside `#item-form-reset`.
- **F3 (fixed):** the sibling Delete button has its own
  `:focus-visible` ring (its label is aria-label/title only); Close/Save
  land in exactly that state at ≤700px and had no matching rule. Added
  `.catalog-form-head-actions .btn:focus-visible`. Verified via real
  Shift+Tab keyboard navigation (not `.focus()`, which doesn't reliably
  arm `:focus-visible` in this Chromium build) — both buttons render a
  visible 2px accent ring.
- **F4 (fixed):** the tab strip's new `overflow-y: hidden` (needed so
  the horizontal scrollbar doesn't add height) was clipping the active
  tab's underline and the roving-tabindex focus ring, since `.tab` uses
  a negative `margin-bottom` to overlap the bar's border. Added
  `padding-block: 2px` on the bar and an inset `:focus-visible` ring on
  `.tab`. Verified by screenshot with a real keyboard-focused tab — ring
  is visible and un-clipped. (`getComputedStyle` reported an anomalous
  0px width/offset for this one rule in the live page despite being the
  only matching rule and the ring rendering correctly on screen and via
  real Tab navigation on the *sibling* Close/Save buttons — isolated
  minimal repros of the same selector/offset outside the live page
  couldn't reproduce it either. Treated as a `getComputedStyle`/Alpine-
  reactivity measurement artifact, not a real defect, since the actual
  paint — the only thing an operator sees — is correct.)
- **F8 (fixed):** the Go test's `aria-label`/`title` checks only asserted
  attribute *presence*, which `aria-label=""` would have passed. Tightened
  to require a non-empty value.
- **F7 (fixed, nit):** `web/help/en/catalog.md` described the bar's
  buttons by their text labels only; added one clause noting Save/Close
  collapse to icon-only on a narrower screen. `make docs-shots` re-run
  after this edit (see F1); `guard-help-drift` unaffected (no new
  heading/bullet/step, so no new structural-drift entry).
- **F5 (deferred, judgement call — not fixed here):** two of the five
  tabs (Print labels, Map a physical keypad) are now entirely off-screen
  at 360px with no visible scroll affordance (fade/shadow/scrollbar
  hint) — reachable by swipe or keyboard, but not obviously discoverable
  by sight. The acceptance criteria ("every control still reachable")
  are met; discoverability is a UX polish question the reviewer
  recommended routing through the `ux` role rather than deciding here.
  **Follow-up card to be filed on the board.**
- **F6 (verified, not a defect):** the icon additions widen the
  Close/Save buttons by ~20px each at *every* width, not just ≤700px
  (only the label-hiding is scoped to the breakpoint). Checked the
  longest-shipped-locale concern this raises directly: opened an
  existing item in Turkish at the 1024×600 kiosk floor (Turkish's
  `catalog.save_changes` — "Değişiklikleri kaydet" — is the longest
  shipped Save string) and confirmed all three visible buttons
  (+New/Close/Save) still sit on one row with no wrap or overlap.
  Screenshot-verified, not just measured.
- **F9, F10:** no action — F9 (an `aria-hidden` on the icon span nested
  inside an `aria-label`led button) is redundant but correct, matching
  the pre-existing Delete button's own pattern; F10 (a further row-fit
  optimization for the title/actions groups) is an opportunity beyond
  this card's acceptance criteria, not a defect.

The two recurring bug classes this pipeline watches for (a file-write
handler missing `os.MkdirAll`; a cwd-relative path instead of
`paths.Data(...)`) are confirmed not applicable — the diff performs no
file I/O.

## Verified beyond automated tests

- Screenshots taken and read at 360×740 in English, fa (RTL — mirrors
  correctly, tab strip still scrolls, Save/Close swap sides correctly)
  and, for the kiosk-width/long-locale check (F6), Turkish at 1024×600.
- Real keyboard navigation (Shift+Tab from the barcode field, not
  `.focus()`) exercised for the F3/F4 focus-ring fixes.
- TDD discipline: `git stash` on `web/public/app.css` alone, confirmed
  all three new Playwright assertions fail with the pre-fix numbers
  (42%-scale height, visible label, wrapped tab strip), `git stash pop`
  restored green — done independently a second time by the Opus
  reviewer via a from-scratch revert in its own isolated worktree.
- Not verified on real touch hardware — Chromium/Playwright emulation
  only (no kiosk Pi available in this session). Flagged per the `ux`
  skill's disclosure rule.

## Gate (re-run after every review fix)

`gofmt -l .` clean · `go build ./...` clean · `go test ./...` (full
suite) green · `golangci-lint run ./...` 0 issues · `guard-i18n.sh` /
`guard-data-access.sh` / `guard-page-http-error.sh` / `guard-docs-shots.sh`
/ `guard-help-drift.sh` / `guard-help-topics.sh` all green ·
`e2e/tests/catalog-item-form-1956.spec.ts` (pre-existing regression
suite for this dialog) + `catalog-item-form-2000.spec.ts` (new) — 15/15.

## Verdict

**Safe to merge.** No client/shop name used as seed data; no
secret-shaped values in the diff.

## Deferred

- Tab-strip scroll discoverability at 360px (F5) — new Backlog card,
  routed to the `ux` role.

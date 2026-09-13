# Code review: held-sales placement decision (ut-docs#2142)

**Date:** 2026-09-12
**Card:** universaltill/ut-docs#2142
**Branch:** `fix/2142-held-sales-placement-decision`
**Complexity:** medium (Dev: Sonnet inline, Review: Opus 5, isolated worktree,
different model from the implementation)

## What shipped

ut-docs#2142 asked whether `#held-sales` should move out of `.tender-scroll`
(a shared, scrollable box holding the barcode scan row + the held strip) into
its own always-visible grid region in `.pos-container`, now that ut-docs#2128's
live measurement found `.tender-scroll` scrolls at held-sale count ≥1 at both
1280×800 and 1024×600 — i.e. scrolling is the normal case, not the edge case
the original grid-ratio tuning assumed. The card explicitly allowed "leave
as-is" as a legitimate outcome.

The decision reached was **leave as-is**, and the entire diff is the record of
that decision:

- a documentation comment in `web/public/app.css`, sitting between
  `.tender-scroll`'s closing brace and `.scan-row`, giving the reasoning below;
- a one-field `surface_sha256` refresh in `web/help/img/manifest.json` via the
  repo's own documented escape hatch
  (`scripts/ci/update-docs-shots-surface-hash.sh`), with the
  `Docs-Shots-Unchanged: true` commit trailer that hatch's header prescribes.

Two files. No Go, no template, no locale, no help topic, no behaviour.

## Independent review (Opus 5, isolated worktree)

**Initial verdict: NOT ready as it arrived — one must-fix and one should-fix,
both documentation-accuracy defects in the comment itself, no code risk
whatsoever (the diff was proven inert). Both fixed on this branch before
merge. Final verdict: safe to merge.**

Because the comment *is* the deliverable here, an inaccurate citation in it is
not cosmetic — it is the defect. A future engineer following this trail is
exactly who the comment exists for.

### F1 — MUST FIX (fixed): two of the three cited issues did not support the claim

The comment originally stated:

> `.pos-container`'s grid-row-ratio math has independently been re-measured
> four times already (ut-docs#161/#1336/#2128) for unrelated height budgets

The **count of four was correct**; **the issue numbers were wrong on two of
three**, verified by reading every occurrence rather than trusting the
citation:

- **ut-docs#161 is not a grid-row-ratio measurement.** Its `app.css`
  appearances are the fluid type scale, `.tender`'s own `overflow-y: auto`
  last-resort fallback, `.tab-panel`'s collapse, and kiosk control sizing —
  none inside `.pos-container`'s own comment block.
- **ut-docs#2128 explicitly declined to touch that ratio**, so citing it as
  one of the re-measurements contradicted the very record it points at
  (`docs/code-reviews/2026-09-11-held-strip-scroll-affordance-2128.md`:
  "Deliberately does **not** touch the `@media (max-height: 710px)`
  grid-row-ratio override — that budget has been independently re-measured
  four times already"). #2128 is the *source* of the count, not a member of
  the set.
- **ut-docs#1336 was the one correct citation.**

The four re-measurements actually documented in `.pos-container`'s own
comment, each with a surviving review record: **ut-docs#213** (basket becomes
a full-height first-class column), **ut-docs#1231** (products `1fr` vs tender
`auto` → 4fr:3fr), the **2026-08-30 compact-UI pass** (4fr:3fr → 5fr:2fr,
`@media (max-height: 700px)` override removed), and **ut-docs#1336** (the
height-scoped override returns, threshold narrowed 720px → 710px).

**Fixed**: citation corrected to
`(ut-docs#213/#1231/#1336, plus the 2026-08-30 compact-UI pass)`.

### F2 — SHOULD FIX (fixed): the decision's strongest evidence was missing, and the "no data" framing was overstated

Two facts landed in this repo **the same day** as this decision and were both
absent from the original comment. Together they change the argument from "we
have no evidence either way, so don't touch it" to "the need is already met,
so don't touch it" — a materially stronger and more durable justification:

- **ut-docs#2137 already shipped the always-visible affordance.**
  `web/ui/pages/index.html` renders a permanent "Open orders" button in
  `.tender-quickpay` — **outside** `.tender-scroll`, always rendered even when
  nothing is parked — opening a popup that lists and resumes every parked
  order. That card's own comment: "A popup needs no vertical budget, which is
  exactly why it is the fix and the strip's CSS work ... is not a
  prerequisite." The cheaper middle-ground a reviewer would go looking for
  **already exists and is shipped**; the held strip is now a secondary
  convenience, not the only path to a parked order. This is the single most
  decisive fact for #2142 and the original comment did not mention it.
- **The "no data" premise was narrower than stated.** Strictly true for
  cross-shop telemetry, but `docs/code-reviews/2026-09-12-parked-orders-popup-2137.md`
  records a concrete pilot-device observation of real held-sale accumulation
  (multiple concurrent parked orders, one stranded well over three hours). A
  future reader finding that record would see real data and conclude this
  comment was written without checking.

**Fixed**: the comment now leads with #2137's already-shipped popup as the
load-bearing reason (reachability is already solved at zero vertical-budget
cost), demotes "no production telemetry" to a secondary point rather than the
main argument, and adds a one-line pointer to the cheap future move (a
parked-count badge on the #2137 button) if the strip's own visibility is ever
revisited.

### F3 — informational, not acted on: the regression-risk claim is even better supported than its own citations show

ut-docs#2137's own independent review caught a blocker where merely adding
**one sibling button** to the quick-pay row blew the 1024×600 budget in
German (a wrapped label pushed `.tender` into overflow). A 2026-09-12
demonstration, in this exact vertical budget, that a change far smaller than
the one #2142 contemplated causes a real regression. Not added to the comment
(already adequately supported by the four cited re-measurements); noted here
for anyone auditing this decision later.

### F4 — non-blocking, pre-existing, NOT introduced by this change: filed separately

`web/help/img/manifest.json`'s own `algorithm` field describes the hashed
fileset as "web/ui/** + non-test internal/pages/**.go" but
`scripts/ci/guard-docs-shots.sh` actually also walks `web/public/**` — the
exact tree this diff touched. Present identically on `main`; fixing it needs
a `make docs-shots` regeneration, out of scope here. Filed as
universaltill/ut-docs#2220.

### Confirmed sound, no change needed

- "now that scrolling is the normal case" — fully supported by #2128's own
  finding that the chips are "not real hit-test targets the moment there is
  even 1 held sale" at both 1280×800 and 1024×600.
- The comment makes no RTL or hit-testability claims — correct restraint,
  those are #2128's own conclusions about its own gradient fix, not this
  card's to re-assert.
- `#held-sales` is genuinely a child of `.tender-scroll` (structural premise
  verified directly in `index.html`).
- No client/shop name, no secret-shaped literal in the added text.

## Verification beyond automated tests

- **Proved the diff is comment-only, not merely read as such.** Stripped
  every CSS comment from `main`'s `app.css` and from the branch's, then
  compared the remaining declaration-bearing lines: identical. No rule,
  selector, property, media query or declaration differs anywhere in the
  file — a stronger guarantee than reading the diff, and the exact
  precondition the escape hatch's own header requires the author to confirm
  manually before using it.
- **Verified placement and file integrity directly**: the comment sits
  entirely outside every rule body, between `.tender-scroll`'s closing brace
  and `.scan-row`; comment delimiters and braces balance; no premature `*/`.
- **Proved the `surface_sha256` refresh is exactly and solely attributable to
  this edit** by reverting `app.css` to `main`'s copy, recomputing the
  guard's own hash (`GUARD_DOCS_SHOTS_PRINT_SURFACE_ONLY=1`), and confirming
  it matches the value this commit replaces; restoring the file reproduces
  the value this commit writes. Re-run after the F1/F2 fix, with the same
  result each time: nothing else piggybacked on the bump.
- **Guards run, not assumed**: `guard-docs-shots.sh` ✓ (and its three
  self-tests), `guard-help-topics.sh` ✓, `guard-help-drift.sh` ✓ (two
  pre-existing baselined `vouchers` drifts, unrelated, tracked as
  ut-docs#1973), `guard-i18n.sh` ✓, `guard-compliance-claims.sh` ✓,
  `guard-htmx-loaded.sh` ✓, `guard-emoji-font.sh` ✓, `guard-osk-loaded.sh` ✓,
  `guard-autofill-suppression.sh` ✓, `guard-data-access.sh` ✓,
  `guard-kiosk-engine.sh` ✓, `guard-plugin-menu-read.sh` ✓,
  `guard-page-http-error.sh` ✓, `guard-webkit-version.sh` ✓,
  `guard-kiosk-launch-flags.sh` ✓, `guard-android-status-address.sh` ✓,
  `guard-android-i18n.sh` ✓, `guard-e2e-fixtures-import.sh` ✓,
  `check-brand-assets.sh` ✓, `guard-makefile-version.sh` ✓.
  `guard-shellcheck-version.sh` could not be run in this build environment
  (no `shellcheck` binary on `PATH` at all — an environment gap, not
  diff-related; unaffected by this diff since no shell script changed, and
  CI's own `ubuntu-latest` runner carries it preinstalled).
- **Full Go gate run despite no Go being touched, rather than assumed
  clean**: `gofmt -l .` (no output), `go build ./...`, `go vet ./...`,
  `go test ./...`, `golangci-lint run ./...` (0 issues).
- **Confirmed no user-facing change requiring a help topic or i18n key.** The
  comment-strip identity check above is the proof: nothing rendered changed,
  no control was added, removed or altered, no template or locale file was
  touched. A CSS comment is never parsed, never rendered, never user-visible.

## On the merits: is "leave as-is" the right call?

**Yes — and the fixed comment now argues it more clearly than the original
draft did.** The case *for* moving `#held-sales` rested on #2128's finding
that the strip is unreachable at rest from 1 held sale upward. That is a
reachability problem, and **reachability has already been solved** by
ut-docs#2137's always-visible popup button beside Card — a control that
costs `.pos-container`'s height budget nothing, chosen for exactly that
reason. Moving the strip into its own grid row would now buy only
*at-a-glance* visibility of parked orders, against a fourth reopening of a
ratio whose last three adjustments each required live measurement. That is a
poor trade. The cheaper middle grounds a reviewer would look for are either
already shipped (the popup, #2137) or already ruled out (sticky positioning
was tried and produced a confirmed regression — overlapped the scan row).

## Safe-to-merge verdict

**Yes, after the F1/F2 fix landed on this branch.** The diff is provably
comment-only (comment-stripped CSS byte-identical to `main`), the
`surface_sha256` refresh is proven exactly attributable to this edit and
nothing else, the escape hatch was used as documented with the prescribed
`Docs-Shots-Unchanged: true` trailer, all applicable CI guards pass, the full
Go gate is clean, and no secret or client name is present. The reasoning the
comment now records is accurate and, per the independent review, actually the
strongest available case for the decision reached.

## Follow-ups filed (not this card's scope)

- universaltill/ut-docs#2218 — `.held-chip`'s touch target measures well
  under the 44px baseline (`min-height: 0` override) — found during this
  card's UX sanity-check, unrelated to the placement question itself.
- universaltill/ut-docs#2220 — `web/help/img/manifest.json`'s own
  `algorithm` self-description omits `web/public/**` from the hashed
  fileset, pre-existing on `main`, misleading to exactly the reader auditing
  a CSS-only `surface_sha256` bump like this one. Needs a `make docs-shots`
  regeneration to correct.
- If the held strip's own at-a-glance visibility is ever revisited: the
  cheap first move is a parked-count badge on ut-docs#2137's existing
  always-visible button, not relocating `#held-sales` or touching the grid
  ratio.

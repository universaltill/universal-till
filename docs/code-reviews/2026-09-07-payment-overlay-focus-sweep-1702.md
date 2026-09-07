# Code review: geometry-driven payment-overlay focus sweep (ut-docs#1702)

**Branch:** `fix/1702-payment-overlay-focus-sweep`
**Complexity:** medium (Sonnet dev, Opus review — independent subagent,
isolation: worktree)

## What shipped

`web/public/app.js`'s payment-overlay focus-obscured mechanism (WCAG 2.2
SC 2.4.11, Focus Not Obscured) previously hand-maintained a 5-element
`targets` array of controls to give `tabindex="-1"` while geometrically
covered by the open, non-modal `#payment-overlay` (ut-docs#1629/#1674). A
prior review (`2026-09-07-payment-overlay-focus-obscured-1674.md`)
measured 11 (1024×600) / 8 (1280×800) / 4 (1920×1080) *other* focusable
controls still covered and still reachable, none of them in the array,
and deferred the generalization here as ut-docs#1702.

This change replaces the hardcoded array with a `candidates()` function
that queries every focusable element outside `#payment-overlay` **fresh
on every run** (never a load-time snapshot — the product grid and
held-sales list are dynamic), reusing the exact same `isCoveredByOverlay()`
hit-test unchanged. Exclusions: elements inside the overlay itself
(`overlay.contains(el)`), disabled elements, and elements already
`tabindex="-1"` for an unrelated reason (e.g. the roving-tabindex ARIA
tabs pattern on inactive category tabs) that don't carry our own save
marker. The `resize` listener is now coalesced to at most one sweep per
animation frame (a full-DOM sweep costs more per call than the old
5-element array; a drag-resize can fire many `resize` events/second).

New test file: `e2e/tests/payment-overlay-focus-sweep-1702.spec.ts` (5
tests) — representative controls from the review's 11/8/4 measurements at
1024×600/1920×1080, an inactive-tab non-regression check, the 4 of 5
pre-existing explicit targets reachable at a desktop viewport (the 5th,
the phone-width New Sale duplicate, stays covered by the retained #1674
spec), and a dynamically-appearing held-sale chip proving the
fresh-query-not-cached design decision.

## TDD

Confirmed independently, twice: once by Dev/Tester (revert `app.js` →
2/4 assertions fail with the exact expected "received empty string"
tabindex mismatch; restore → passes), and again by the independent Opus
reviewer from a clean worktree (revert to the pre-change commit → 3/5
tests fail on the right assertions, with the `isCovered()` geometry
precondition passing first in each case — genuine "covered yet still
tabbable" failures, not a crash/timeout masking something; restore →
green).

## Independent review (Opus, isolated worktree)

Full independent pass — see the agent's report for the complete command
log. Verdict: **safe to merge, no blocking findings.**

**Independently verified, not trusted:**
- `gofmt`, `go build`, `go vet`, `go test ./...`, `golangci-lint` — clean
  (confirmed 0 `.go` files in the diff).
- All 17 CI-blocking guards + 4 guard self-tests — green, including
  `guard-docs-shots.sh`.
- New spec: 5/5. Targeted regression set (23 specs matching
  `quick-pay|payment-open|kiosk-checkout-start-phone|tabindex|payment-overlay`):
  128/128. **Full default e2e suite: 344/344** (run given the change's
  scope went from 5 elements to the whole document).
- **Blast radius measured live in the browser**: a
  `[data-a11y-tabindex-saved]` census at overlay-open found 14/11/4
  elements marked at 1024×600/1280×800/1920×1080 — matches the prior
  review's predicted 11/8/4 *other* controls plus the 3 originals
  measurable at each width. Nav rail, status chips, lock, exit, help,
  bug-report: zero swept up at any width — `CLAUDE.md`'s "Status/lock/exit
  must always be reachable" is not violated.
- PNG diffs (`web/help/img/`) pixel-diffed with PIL against the pre-change
  commit: 2/17/42/7 changed pixels out of 614,400 per image (≤0.007%), max
  channel delta 28, scattered singletons — antialiasing noise, not a
  regression. Also viewed the images directly: normal, correct
  screenshots.
- Selector robustness: `.products-finder [role="tab"].active` cannot
  match the tender Pay/Split tabs (`#tab-pay`/`#tab-split`, outside
  `.products-finder`) — confirmed statically and via a live DOM census.
- N/A checks confirmed rather than assumed: zero Go files/file I/O in the
  diff (both recurring bug classes — missing `os.MkdirAll`, cwd-relative
  path vs `paths.Data(...)` — don't apply); zero CSS/markup/locale change,
  so no `web/help/` prose update is owed (grepped `web/help/en/*.md` for
  tab-order/keyboard-focus content and found none); no secret-shaped
  literal, no real client/shop name.

**Findings — all low severity, none blocking:**

1. **(Fixed here.)** The original spec asserted `not.toHaveAttribute('tabindex','-1')` on restore but never checked the *exact* restored value or that the `data-a11y-tabindex-saved` marker was cleaned up. The reviewer's own adversarial mutations — restore always writing `tabindex="0"` regardless of the real baseline, and leaving the save-marker behind after restore — both survived the full 28-test payment-overlay/tabs regression set. The reviewer drove the second one to a concrete, real regression class: leaving the marker behind lets a *second* cover/restore cycle after a category-tab switch resurrect a stale saved value, leaving two tabs simultaneously `tabindex="0"` — breaking the roving-tabindex pattern `tab-bar-overflow-aria-424.spec.ts` exists to protect (that spec never catches it because it never crosses an overlay open/close cycle). The shipped `app.js` code itself was already correct (re-verified: reviewer's own probe against the real implementation gave the correct single-reachable-tab result) — this was a test-coverage gap, not a live bug. Fixed by capturing each control's real baseline `tabindex` before the overlay opens and asserting an exact round-trip plus marker cleanup after close, in this same PR. Independently re-verified after the fix: the "always restore to 0" mutation now fails the strengthened assertion with the exact expected mismatch; reverting the fix and re-running confirms green.
2. **(Accepted, not fixed — latent, no live instance.)** `isCoveredByOverlay()`'s `r.width === 0 && r.height === 0` early-return does less than the #1674 review's record implied: removing it entirely doesn't change the measured blast radius at all (a `display:none` element's zero rect already misses the overlay via `elementFromPoint(0,0)` landing on the nav rail, independent of this guard). The guard would only matter for a zero-*size*, non-zero-*position* element inside the overlay's footprint — not possible under the old 5-button array, newly plausible now that the sweep is document-wide, but no such control exists on the sale screen today. Noting for the next person touching this file rather than adding speculative coverage for a control that doesn't exist.
3. **(Accepted, not fixed — measured, no checkout-path impact.)** Per-sweep cost scales with catalogue size: ~2.6ms median on the demo catalogue (46 candidates), ~16.2ms median / 28.5ms max on a synthetic ~300-product catalogue (346 candidates), on an x86 CI runner — a Pi kiosk will be slower. The reviewer tested and ruled out a read/write-batching fix (no improvement — the cost is intrinsic to the `elementFromPoint()` hit-test count, not layout thrashing); the `requestAnimationFrame` coalescing already in this diff is the correct, and largely only, mitigation at this design, since it bounds *frequency* (once per open/close/resize-settle) not per-sweep cost. Not a checkout-path concern (never fires per keystroke, only on Payment-tap/overlay-close/resize). Cheap future optimization if a large-catalogue shop ever reports a hitch on tapping Payment: prefilter candidates by rect-intersection against the overlay's own rect (pure arithmetic) before spending an `elementFromPoint()` call on each, cutting ~350 hit-tests to ~20-40. No card opened for this — it's a known, bounded, non-blocking tradeoff, not an active defect.
4. **(Fixed here.)** Two cosmetic test-naming/robustness nits: the "5 pre-existing explicit targets" test only asserted 3 of them (missing `quick-pay`, and the phone-only 5th target it never claimed to cover) — retitled and extended to assert `quick-pay` too (4 of 5 at a desktop viewport; the phone-width 5th stays covered by the retained #1674 spec, noted in a comment). The inactive-category-tab test hardcoded `toHaveCount(1)`, an incidental fact about the demo catalogue's current category count rather than what the test claims to verify — relaxed to `.first()` with no count assertion.

Also credited: removing the old `if (!footer) return;` guard (subsumed by
the broader `candidates()` query) incidentally resolves the #1674 review's
own recorded nit about `quick-pay`/the phone duplicate's latent, unneeded
coupling to `.tender-default-footer`'s existence.

**Investigated, found not to reproduce (recorded so it isn't re-derived):**
a hypothesised gap where content swapped into the DOM *while the overlay
is already open* (nothing observes the document itself, only the
overlay's own `open` attribute and `resize`) could leave a newly-covered
control reachable until the next open/close/resize. The reviewer drove
the one plausible live path — a rejected split-tender swap, which patches
`#basket` in place without closing the overlay — and found no reachable
regression (the basket column isn't covered at the tested viewport). The
architectural gap is real in principle but no concrete instance exists on
the sale screen today; not worth a defensive fix without a reachable
case.

## Verified beyond automated tests

- Full default e2e suite (344 tests) green, not just the touched-surface
  subset.
- Manual visual check at 1024×600 with the overlay open: light theme and
  RTL (fa) both screenshotted and read — no overlap, misalignment, or
  cut-off text; layout is pixel-identical to pre-change (expected: this
  diff is `tabindex`-only, zero CSS/markup touched). **Dark theme not
  independently verified** — this app's dark theme is plugin-gated
  (`#145`'s curated theme plugins), not a simple attribute toggle, and
  installing one was out of scope given zero CSS is touched by this
  change. Recorded as an accepted gap, not a silent skip.
- `web/help/`: correctly not updated — tab order is not something a shop
  owner reads about, confirmed by grep, not assumed.

## Safe to merge

Yes.

## Deferred / accepted (not new Backlog cards — bounded, no live instance)

- Findings 2 and 3 above: documented, latent/measured-but-non-blocking,
  revisit only if a concrete instance or a real performance complaint
  ever surfaces.

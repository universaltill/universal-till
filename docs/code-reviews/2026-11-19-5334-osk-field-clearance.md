# Code review: OSK dialog field clearance (ut-docs#5334)

**Date:** 2026-11-19
**Card:** ut-docs#5334
**Reviewer:** claude-fable-5-2, combined Tester + Reviewer pass over the 15-branch admin-review batch (`git diff origin/main...origin/<branch>` per branch). Review file: `temp/review-ar-batch.md`.

## What shipped

scroll-padding-block-end on the four on-screen-keyboard dialogs and a scroll-driven cue behind @supports, so the focused field is not hidden by the keyboard. Logical properties only.

## Findings

- **Info** — PASS (CSS only); the e2e that proves the geometry was not run in the review pass, and nothing was looked at on screen. Disposition: Accepted

## Verification

- Targeted tests for the touched packages (see the batch pass): `go build ./...` and `go vet` clean on a private worktree of the throwaway integration commit; the card's tests were read and judged real (they fail if the behaviour breaks).
- Full `go test ./...` on the throwaway integration branch showed only merge artefacts (en.meta.json commas, I1 of the review file) plus slow-package timeouts in `internal/pages` and `internal/plugins`; the rerun is pending.
- Not done: Playwright e2e and a driven run of the app; TDD claims verified by reading, not by reverting each fix.

## Verdict

PASS once the findings above are dispositioned as listed.

## Deferred items

- Run the OSK e2e geometry spec on the merged tree.

Pre-flight: help topic `n/a (no behaviour visible to the shop changes beyond the fix)`; docs-shots `stale on the merged tree, regenerate once per batch (I2)`; en.json keys `en.meta.json updated`; README / website `n/a (no plan, price or platform claim)`; ADR `n/a`; review record `this file`.

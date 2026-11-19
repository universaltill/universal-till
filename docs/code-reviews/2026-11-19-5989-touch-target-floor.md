# Code review: Touch target floor sweep (ut-docs#5989)

**Date:** 2026-11-19
**Card:** ut-docs#5989
**Reviewer:** claude-fable-5-2, combined Tester + Reviewer pass over the 15-branch admin-review batch (`git diff origin/main...origin/<branch>` per branch). Review file: `temp/review-ar-batch.md`.

## What shipped

A scanner-based sweep test with its own sanity tests (a 30px button is reported, a hidden one is not, an empty data-touch-ok does not exempt) and an exact, shrink-only baseline. Test only.

## Findings

- **Low** — The baseline was last matched against main alone; a sibling adding or fixing a control on a swept route (5877 /shifts, 5607 /kitchen-stations, 4846 /catalog) could fail it. Disposition: Deferred: run touch-target-floor-5989 on the merged tree before the last merge. From reading, none should fail.

## Verification

- Targeted tests for the touched packages (see the batch pass): `go build ./...` and `go vet` clean on a private worktree of the throwaway integration commit; the card's tests were read and judged real (they fail if the behaviour breaks).
- Full `go test ./...` on the throwaway integration branch showed only merge artefacts (en.meta.json commas, I1 of the review file) plus slow-package timeouts in `internal/pages` and `internal/plugins`; the rerun is pending.
- Not done: Playwright e2e and a driven run of the app; TDD claims verified by reading, not by reverting each fix.

## Verdict

PASS once the findings above are dispositioned as listed.

## Deferred items

- Run the sweep spec on the merged tree.

Pre-flight: help topic `n/a (test only)`; docs-shots `stale on the merged tree, regenerate once per batch (I2)`; en.json keys `en.meta.json updated`; README / website `n/a (no plan, price or platform claim)`; ADR `n/a`; review record `this file`.

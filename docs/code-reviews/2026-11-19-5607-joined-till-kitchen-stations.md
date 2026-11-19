# Code review: Joined till kitchen stations (ut-docs#5607)

**Date:** 2026-11-19
**Card:** ut-docs#5607
**Reviewer:** claude-fable-5-2, combined Tester + Reviewer pass over the 15-branch admin-review batch (`git diff origin/main...origin/<branch>` per branch). Review file: `temp/review-ar-batch.md`.

## What shipped

On a joined (additional) till the Kitchen stations page is read-only: + disabled with a visible hint, Name read-only, Destination disabled with a hidden mirror so an address-only save still validates, Deactivate/Activate not rendered. The server refusal is unchanged.

## Findings

- **Info** — PASS: Go tests check joined and main-till renders both ways. Dialog behaviour is covered by e2e only (not run in the review pass). Disposition: Accepted

## Verification

- Targeted tests for the touched packages (see the batch pass): `go build ./...` and `go vet` clean on a private worktree of the throwaway integration commit; the card's tests were read and judged real (they fail if the behaviour breaks).
- Full `go test ./...` on the throwaway integration branch showed only merge artefacts (en.meta.json commas, I1 of the review file) plus slow-package timeouts in `internal/pages` and `internal/plugins`; the rerun is pending.
- Not done: Playwright e2e and a driven run of the app; TDD claims verified by reading, not by reverting each fix.

## Verdict

PASS once the findings above are dispositioned as listed.

## Deferred items

- None.

Pre-flight: help topic `updated`; docs-shots `stale on the merged tree, regenerate once per batch (I2)`; en.json keys `en.meta.json updated`; README / website `n/a (no plan, price or platform claim)`; ADR `n/a`; review record `this file`.

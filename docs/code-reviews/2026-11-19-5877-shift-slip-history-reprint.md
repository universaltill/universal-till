# Code review: Shift slip history reprint (ut-docs#5877)

**Date:** 2026-11-19
**Card:** ut-docs#5877
**Reviewer:** claude-fable-5-2, combined Tester + Reviewer pass over the 15-branch admin-review batch (`git diff origin/main...origin/<branch>` per branch). Review file: `temp/review-ar-batch.md`.

## What shipped

A Reprint action in shift history; the PIN gate, 404-before-PIN and 502-without-audit are unchanged. Reprint is read from PostForm exactly '1', carried as a hidden field in the elevation prompt; the row target is derived from the DB id; the audit payload gets reprint=true.

## Findings

- **Low** — The 'Reprinted' line and the audit flag depend on the client sending reprint=1; a crafted POST without it prints an old slip unmarked. Still PIN-gated and audited as shift_slip_printed, as before the card. Disposition: Accepted
- **Info** — The docs/code-reviews/wip-5877/ notes named on the card are not in the diff. Disposition: Accepted

## Verification

- Targeted tests for the touched packages (see the batch pass): `go build ./...` and `go vet` clean on a private worktree of the throwaway integration commit; the card's tests were read and judged real (they fail if the behaviour breaks).
- Full `go test ./...` on the throwaway integration branch showed only merge artefacts (en.meta.json commas, I1 of the review file) plus slow-package timeouts in `internal/pages` and `internal/plugins`; the rerun is pending.
- Not done: Playwright e2e and a driven run of the app; TDD claims verified by reading, not by reverting each fix.

## Verdict

PASS once the findings above are dispositioned as listed.

## Deferred items

- None.

Pre-flight: help topic `updated`; docs-shots `stale on the merged tree, regenerate once per batch (I2)`; en.json keys `en.meta.json updated`; README / website `n/a (no plan, price or platform claim)`; ADR `n/a`; review record `this file`.

# Code review: Window mode help (ut-docs#3468)

**Date:** 2026-11-19
**Card:** ut-docs#3468
**Reviewer:** claude-fable-5-2, combined Tester + Reviewer pass over the 15-branch admin-review batch (`git diff origin/main...origin/<branch>` per branch). Review file: `temp/review-ar-batch.md`.

## What shipped

Help and hints for the Display window mode (Normal / Fullscreen / Kiosk): two new English keys, the display help topic, and the Settings hint.

## Findings

- **Medium** — Non-English strings added in a code PR (ar/fa/tr keys, translated help/{ar,de,fa,tr}/display.md) breach ADR-0160. Disposition: Fixed: non-English locale and help edits removed; English, en.meta.json and tests remain.
- **Low** — settings.display.window_mode_kiosk_help is shown on every non-Pi till and says the till fills the whole screen; on macOS or without the desktop app the window stays normal, so the hint is false. Disposition: Fixed: the hint now says what Fullscreen and Kiosk do in the desktop app (English + meta only), and the test pins the new wording.

## Verification

- Targeted tests for the touched packages (see the batch pass): `go build ./...` and `go vet` clean on a private worktree of the throwaway integration commit; the card's tests were read and judged real (they fail if the behaviour breaks).
- Full `go test ./...` on the throwaway integration branch showed only merge artefacts (en.meta.json commas, I1 of the review file) plus slow-package timeouts in `internal/pages` and `internal/plugins`; the rerun is pending.
- Not done: Playwright e2e and a driven run of the app; TDD claims verified by reading, not by reverting each fix.

## Verdict

PASS once the findings above are dispositioned as listed.

## Deferred items

- Weblate to deliver the translations of the two keys and display.md; macOS window control is not claimed.

Pre-flight: help topic `updated`; docs-shots `stale on the merged tree, regenerate once per batch (I2)`; en.json keys `en.meta.json updated`; README / website `n/a (no plan, price or platform claim)`; ADR `n/a`; review record `this file`.

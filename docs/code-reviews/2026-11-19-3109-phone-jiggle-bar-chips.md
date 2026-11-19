# Code review: Phone jiggle bar keeps category chips tappable (ut-docs#3109)

**Date:** 2026-11-19
**Card:** ut-docs#3109
**Reviewer:** claude-fable-5-2, combined Tester + Reviewer pass over the 15-branch admin-review batch (`git diff origin/main...origin/<branch>` per branch). Review file: `temp/review-ar-batch.md`.

## What shipped

On a phone, the All-grid edit bar is fixed-height and the category chip row sticks under it; a compact English title replaces the long one, swapped by CSS in the 480px tier only (logical properties).

## Findings

- **Medium** — Non-English strings added in a code PR (ar/fa/tr key buttons.edit_mode.title_all_grid_compact, translated help/{ar,de,fa,tr}/sell.md edits) breach ADR-0160. Disposition: Fixed: all non-English locale and help edits removed; en.json, en.meta.json, English help, CSS and tests remain. The e2e now injects fa/tr/ar compact text like the de/es/pt injected-pack test, so the two-line fit check still runs without shipping translations.

## Verification

- Targeted tests for the touched packages (see the batch pass): `go build ./...` and `go vet` clean on a private worktree of the throwaway integration commit; the card's tests were read and judged real (they fail if the behaviour breaks).
- Full `go test ./...` on the throwaway integration branch showed only merge artefacts (en.meta.json commas, I1 of the review file) plus slow-package timeouts in `internal/pages` and `internal/plugins`; the rerun is pending.
- Not done: Playwright e2e and a driven run of the app; TDD claims verified by reading, not by reverting each fix.

## Verdict

PASS once the findings above are dispositioned as listed.

## Deferred items

- Weblate to deliver the compact title in ar/de/fa/tr; the translated help sell.md pages follow from Weblate.

Pre-flight: help topic `updated`; docs-shots `stale on the merged tree, regenerate once per batch (I2)`; en.json keys `en.meta.json updated`; README / website `n/a (no plan, price or platform claim)`; ADR `n/a`; review record `this file`.

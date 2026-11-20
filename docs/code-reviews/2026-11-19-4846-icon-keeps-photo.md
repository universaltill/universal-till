# Code review: Picking an icon keeps the item photo (ut-docs#4846)

**Date:** 2026-11-19
**Card:** ut-docs#4846
**Reviewer:** claude-fable-5-2, combined Tester + Reviewer pass over the 15-branch admin-review batch (`git diff origin/main...origin/<branch>` per branch). Review file: `temp/review-ar-batch.md`.

## What shipped

PickItemIcon reads the thumbnail and writes in one transaction; a photo item keeps its row and file and stores only items.icon; the handler deletes the uploaded file only when the photo was not kept.

## Findings

- **Info** — PASS: catalog.builtin_icon.custom_active is still used by categories.html:527, so it is not a leftover.

## Verification

- Targeted tests for the touched packages (see the batch pass): `go build ./...` and `go vet` clean on a private worktree of the throwaway integration commit; the card's tests were read and judged real (they fail if the behaviour breaks).
- Full `go test ./...` on the throwaway integration branch showed only merge artefacts (en.meta.json commas, I1 of the review file) plus slow-package timeouts in `internal/pages` and `internal/plugins`; the rerun is pending.
- Not done: Playwright e2e and a driven run of the app; TDD claims verified by reading, not by reverting each fix.

## Addendum: merge of main, Tester and spec fix (lane:cloud-41, 2026-11-19)

- **Merge-damage check** (Sonnet subagent; the author was Opus, the first review Fable): CLEAN. After merging 213 commits of `main` (no conflicts), the change that lands is line-for-line the reviewed change (`git diff origin/main...678a2f3f` = `git diff origin/main HEAD`). `main` has no new callers of `PickItemIcon` or `iconid.IsLibraryTile`. The 4 new keys appear once each in `en.json` and in `en.meta.json`. **Medium:** the docs-shots hashes went stale after the merge, so the screenshots were regenerated with `make docs-shots`.
- **Tester** (fresh subagent): the earlier record's gaps are now closed.
  - `go test ./...`: 123 packages ok. `golangci-lint`: 0 issues. All 70 `build`-job guards pass.
  - Revert check: with `main`'s `catalog_repo.go` and `handlers.go` in place, the new data tests fail (no `PickItemIcon`, so the package doesn't build) and 4 catalog tests fail. Forcing `PickItemIcon` back to overwrite fails the two keep-photo data tests.
  - e2e: the touched icon-grid specs pass 30/30.
  - Driven run: 17/17 checks, in English at 1280×800 and 1024×600 and Persian RTL at 1024×600. Covered: the notice, the badge, the sell tile still showing the photo, the generic tile writing nothing, and No image followed by an icon.
- **D1 (high, fixed):** `e2e/tests/photo-cutout-3127.spec.ts:630` still asserted the old rule, "an icon pick removes the thumb", on a sample item whose monogram thumb now counts as a photo. That is the owner's decision C ("photo/initials thumbnail", 2026-11-12). The spec now clears the item with No image before checking that an icon gets no Remove background button. It also asserts that an icon over a photo keeps the button and gets the fallback badge. Result: 14 passed, 1 skipped (the screenshot-only test). Reviewed by the orchestrator: the test keeps its intent, and the only change is the rule it asserts.
- **N1 (answered, no change):** sample-catalogue monograms count as photos. This follows the owner's wording on the card.
- Not seen: dark theme (the forced media query didn't switch the app's theme), phone width, forced-colors.

## Addendum 2: second merge of main (PR sweep, lane:cloud-41, 2026-11-20)

- `main` moved again and `web/help/img/manifest.json` conflicted (only `surface_sha256`). Resolved by running `make docs-shots` on the merged tree (204 shots passed); `guard-docs-shots.sh` passes.
- **Merge-damage check** (Sonnet subagent; author and first reviewers were other models): CLEAN. Excluding `web/help/img/**`, `git diff origin/main...c80d16e7` and `git diff origin/main 988d531a` cover the same 16 files with identical `+`/`-` lines; only index and hunk-offset lines differ. `en.json` and `en.meta.json` parse with no duplicate keys. `main` has no callers of `PickItemIcon` or `iconid.IsLibraryTile`.
- Local gate on the merge: `gofmt` clean, `go build ./...` ok, build-job guards pass except four that need tools this container lacks (qemu-arm, shellcheck, deadcode's Go version; catimport passes once tags are fetched). CI is the gate for those.

## Verdict

PASS once the findings above are dispositioned as listed.

## Deferred items

- None.

Pre-flight: help topic `updated`; docs-shots `regenerated after the merge of main (make docs-shots)`; en.json keys `en.meta.json updated`; README / website `n/a (no plan, price or platform claim)`; ADR `n/a`; review record `this file`.

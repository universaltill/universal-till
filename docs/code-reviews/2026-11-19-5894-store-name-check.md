# Code review: Store name check (ut-docs#5894)

**Date:** 2026-11-19
**Card:** ut-docs#5894
**Reviewer:** claude-fable-5-2, combined Tester + Reviewer pass over the 15-branch admin-review batch (`git diff origin/main...origin/<branch>` per branch). Review file: `temp/review-ar-batch.md`.

## What shipped

config.CheckStoreName (single-line rule, then NormalizeStoreName) is used by the Shop name card, the raw editor, the wizard, first sign-in, the cloud-pair prefill and the main till's /api/sync/settings/apply; a refused UT_STORE_NAME is ignored at boot and never saved. The error text never contains the value.

## Findings

- **Info** — PASS: each writer is driven through its handler with U+202E, U+E0100 and U+2028 and asserts nothing was stored and no audit row written.
- **Low** — The cloud set_setting directive still writes store.name unchecked (cloudsync_wire.go:1232, pinned by TestCloudSetSetting_StoreNameAppliedUnchanged_5894); the park comment calls this follow-up F1 and no card exists. Disposition: Deferred: file a card (F1) before this merges.

## Verification

- Targeted tests for the touched packages (see the batch pass): `go build ./...` and `go vet` clean on a private worktree of the throwaway integration commit; the card's tests were read and judged real (they fail if the behaviour breaks).
- Full `go test ./...` on the throwaway integration branch showed only merge artefacts (en.meta.json commas, I1 of the review file) plus slow-package timeouts in `internal/pages` and `internal/plugins`; the rerun is pending.
- Not done: Playwright e2e and a driven run of the app; TDD claims verified by reading, not by reverting each fix.

## Verdict

PASS once the findings above are dispositioned as listed.

## Deferred items

- F1: check store.name in the cloud set_setting directive (no card yet).

Pre-flight: help topic `updated`; docs-shots `stale on the merged tree, regenerate once per batch (I2)`; en.json keys `en.meta.json updated`; README / website `n/a (no plan, price or platform claim)`; ADR `n/a`; review record `this file`.

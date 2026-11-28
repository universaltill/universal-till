# Review record: docs-shots regeneration for feat/2630-till-credheal (PR #2803)

Part of ut-docs#2630. Reviewer: none independent — `[no-subagent: regenerated screenshots only]`
(the diff is generated PNGs + `web/help/img/manifest.json`; no source, test or doc prose edited).

## What shipped
- `make docs-shots` run in the `ut-docshots` Linux container (the harness font pin
  rejects macOS fonts: `.SF NS` instead of Inter). 228 Playwright tests passed.
- 221 files changed: PNGs under `web/help/img/<locale>/` plus the regenerated `manifest.json`
  (topic hashes for `claim` and `reports`, per-file surface records for the files this PR and
  main merges changed). Nothing hand-edited.

## Verification
- `scripts/ci/run-docs-shots-guard.sh`: 39 topics fresh, 18 extra shots, 678 surface files.
- Green: `run-docs-shots-guard_test.sh`, `guard-docs-shots_test.sh`,
  `guard-docs-shots-cross-check_test.sh`, `update-docs-shots-surface-hash_test.sh`,
  `docs-shots-manifest-merge_test.sh`.

## Findings
- Only `en` is guarded; other locale PNGs were regenerated in the same run and committed
  together for consistency. Pixel content of those was not visually inspected.

## Verdict
Accept. Pre-flight: help topics n/a (no prose change); manifest regenerated; en.json keys n/a; ADR n/a.

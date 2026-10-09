# Review: release notes v0.31.4

Date: 2026-10-09. Written by Opus 5.5 (lane:cloud-54) and reviewed by Sonnet against every first-parent merge since v0.31.3.

## What shipped

Owner-facing notes in `web/release-notes/{en,de,ar,fa,tr}/v0.31.4.md`. The release was due after six feat/fix merges since v0.31.3:
- low-disk floor and manager chip;
- daily-backup Problems after a restart and on total failure;
- backup names kept out of card-number masking;
- slot-gated plugin menu tiles;
- slot-gated plugin Docs buttons;
- translated Settings refusal messages (display mode, report retention), added after rebasing onto #1793.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | The menu/Docs bullet overclaimed. Only plugin pages drawn inside another screen (content slots) are newly gated. | **Fixed**: the bullet is reworded in all five languages. |
| 2 | Medium | The low-disk bullet overclaimed and left things out. The check is hourly, not "straight away". The code guarantees only that selling is never blocked. The clean-up also deletes unsent problem reports and trims restore copies. | **Fixed**: reworded with the real chip label, "within the hour", "selling is never interrupted", and the full clean-up list. |
| 3 | Low | The always-on cap (backups never use more than 25% of free space, newest 3 kept) was missing. | **Fixed**: one sentence added. |
| 4 | Low | The ar and tr Docs-button wording did not match the UI (`plugins.manage.action.docs`). | **Fixed**: now الدليل and Kılavuz. fa (راهنما) already matched. de keeps "Doku": its label lives in the de pack, which is not checked here. |
| 5 | Low | The backup bullet now says "photos the shop has". | **Fixed.** |

## Verified

- `scripts/ci/guard-release-notes.sh v0.31.4` passes.
- The `internal/releasenotes` tests pass, including the owner-language check.
- The compliance-claims, competitor-naming and i18n guards pass.
- Every file has four bullets.

**Verdict:** safe to merge.

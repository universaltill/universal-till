# Review: release notes v0.31.3

Date: 2026-10-09 · Written by Opus 5.5 (lane:cloud-54), reviewed by Sonnet against every merge diff since v0.31.2.

## What shipped

Owner-facing notes `web/release-notes/{en,de,ar,fa,tr}/v0.31.3.md` for the
release due after six feat/fix merges since v0.31.2: crash leftovers swept,
coalesced plugin activity log, backup photos warning, plugin panel gate,
quick-tap navigation fix.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | The customer phone/notes bullet overclaimed: the E.164 column and notes column have no UI yet, so nothing changes for the owner. | **Fixed** — bullet dropped in all five languages. |
| 2 | Low | Panel-gate bullet names Reports/Settings/Administration but the gate also covers End of day and the item edit window. | Accepted — the text is true as written. |

Button names checked against each locale's `settings.backup.now`
(de from `ut-plugin-language-de`). No card/PR numbers or jargon.

## Verified

`scripts/ci/guard-release-notes.sh v0.31.3`, `internal/releasenotes`
tests (owner-language check), compliance-claims guard.

**Verdict:** safe to merge.

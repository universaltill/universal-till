# Review: release notes v0.36.0

Scope: `web/release-notes/{en,de,ar,fa,tr}/v0.36.0.md`. The v0.35.4 notes were
never released (last tag v0.35.3), so they are renamed to v0.36.0 and extended
with everything merged since (minor bump: many notable features).

Reviewer: independent Opus subagent, checked each claim against the merged PRs.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | High | Assistant item announced a feature whose plugin (ut-plugin-integration-ai catalogue/settings tools) is unreleased | Fixed: item removed from all five files (RELEASING.md step 1) |
| 2 | High | "Card payment no longer cut off at 30 s" described a regression that never shipped | Fixed: rewritten to the real new behaviour (may/may not have completed) |
| 3 | Med | "any other button" overclaimed | Fixed ("a button"; long actions never cut off) |
| 4 | Med | Language auto-install is main/standalone till only | Fixed ("A main till") |
| 5 | Med | Notices wording loose (critical strip, partners) | Accepted: off by default, kept short |
| 6 | Low | tr dropped "and admins"; tr awkward phrase | Fixed |
| 7 | Low | de rejoin quote differed from pack string | Fixed |
| 8 | Low | ar/fa admin term differed from UI ("مدير عام"/"مدیر کل") | Fixed |
| 9-10, 12 | Low | promotions label, PIN spelling variants | Accepted (consistent within file) |
| 11 | Low | "Last 24 hours" shows English until Weblate catches up | Accepted |

Verified: `scripts/ci/guard-release-notes.sh v0.36.0` ok; `go test ./internal/releasenotes` ok;
same 32 items in every locale; no PR/card numbers.

Verdict: safe to merge. Deferred: announce the AI assistant tools in the release that ships the plugin.

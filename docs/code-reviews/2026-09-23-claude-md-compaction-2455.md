# CLAUDE.md compaction — ut-docs#2455

## What shipped
`CLAUDE.md` rewritten rules-only: 20.6KB → 7.5KB. Every pipeline cycle that
touches this repo loads it on every turn; ~90% of it was incident history
and rationale. Rules keep their ut-docs issue reference; full prior wording
stays in `git log -p CLAUDE.md`. The 20-name "guards to run before committing"
list is replaced by a pointer to `ci.yml`'s `build` job (the old text itself
said the list drifts and the job is authoritative).

## Independent review
Fresh-context Sonnet subagent (author: Opus) compared the original and new
files rule by rule, verifying claims against `.github/workflows/*.yml` and
`scripts/ci/`. It confirmed all 20 formerly-listed guards are in ci.yml's
`build` job, so the pointer is safe. Findings — all fixed:

| # | Finding | Severity | Fix |
|---|---|---|---|
| 1 | compliance-claims guard scan scope dropped | missing | restored (`web/locales`, `web/help`, `web/ui`) |
| 2 | "never a blanket shellcheck suppression" dropped | missing | restored |
| 3 | kiosk-engine guard: receiver-name-agnostic, comments exempt | weakened | restored |
| 4 | `UT_LOCALE_AUDIT_STRICT=1` / `audit-locale-render.sh` dropped | missing | restored |
| 5 | `android-ci.yml` wrongly grouped under "most PRs get no run" — it runs on every PR | **contradicted** | split out, correct description |
| 6 | docs-shots-determinism trigger paths dropped with no pointer | weakened | pointer to its `on:` block + manifest.json |
| 7 | "only the `unused` linter is enabled" dropped | weakened | restored |

## Verified beyond automated tests
Only comments in `scripts/ci/*` and two workflows reference this file; the
section names they cite ("Agent worktree hygiene", the test-support carve-out)
are kept. No code or CI change.

## Verdict
Safe to merge. Docs-only.

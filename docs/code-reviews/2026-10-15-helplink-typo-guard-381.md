# Review: helpLink typo guard (ut-docs#381)

**Date:** 2026-10-15 · **Lane:** lane:local-mahshid76 · **Author model:** Sonnet 5.5 · **Reviewer:** Opus 5.5 (independent subagent)

## What shipped

`scripts/ci/checkhelptopics` (the Go program behind `guard-help-topics.sh`) gains check #5: every literal `{{ helpLink "id" }}` / ``{{ helpLink `id` }}`` under `web/ui/**/*.html` must name a real manual topic (`lib.IDs()`, the same set `helpLinkHTML` uses at runtime). A bad id fails CI with `file:line: helpLink "x" matches no manual topic (did you mean "y"?)`. Only template actions are scanned, so prose, HTML comments and `{{/* */}}` never trip it; computed arguments (`helpLink .`, `confirm_prompt.html`) are skipped by design. No runtime change, nothing under `web/ui`, `web/public` or `internal/pages` touched (docs-shots surface hash unaffected). Placed in the Go checker rather than `guard-i18n.sh` because it needs the real topic set and `checkhelptopics` already exists to avoid re-implementing that in shell.

Today: 41 literal calls, all valid; 1 computed call and 2 prose mentions skipped.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | low | Comment claimed `\b` on both sides; the regex has none. The `(?:^|[^.\w])` prefix guard was not covered: only `{{ .helpLink }}` (no argument) was in the negative fixture, so deleting the prefix kept every test green | Fixed: comment corrected; fixture now has `{{ .helpLink "field-call" }}` and `{{ $.helpLink "root-field-call" }}` |
| 2 | low | A string argument of another function containing the word `helpLink` followed by a backtick-quoted word would be read as a call (false positive). Nothing in `web/ui` does this; it fails loudly | Accepted, documented here |
| 3 | minor | Known false negatives: piped form `{{ "id" \| helpLink }}` (documented in the file header; no template uses it) and ids containing an escaped quote | Accepted |
| 4 | cosmetic | `Line` comment said "line of the helpLink token", code gives the line of the id | Fixed |
| 5 | optional | The `>= 30 literal calls` floor in the real-tree test is brittle against big template refactors (41 today) | Kept deliberately: it stops the real-tree test passing on a broken scanner; its message explains itself |

Verified by the reviewer: gofmt/vet clean; package tests and the whole `guard-help-topics.sh` pass; a planted `backupz` in `settings.html` fails with exit 1 and the right message (the old code exits 0 on it); five of six mutations are caught (the sixth is finding 1, now covered); synthetic probe table (multi-line actions, trim markers, `with`/`if` bodies, nested template args, comments, `helpLinkNot`, empty id) behaves as intended; no `helpLink` use outside `web/ui` in universal-till or local plugin checkouts.

## Not verified

- `shellcheck` on `guard-help-topics.sh` (comment-only edit; not installed in WSL). CI runs it.
- After the final comment/fixture edit the package tests were re-run green in WSL; the prefix-guard mutation was not re-run (reasoned: without the prefix, `.helpLink "field-call"` would match and fail the test).

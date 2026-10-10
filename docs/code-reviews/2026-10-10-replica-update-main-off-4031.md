# Review: additional till says when the main till's automatic updates are off (ut-docs#4031)

Date: 2026-10-10. Lane: `lane:cloud-54`. Built by Sonnet (complexity:easy); reviewed by Opus 5.5 in a fresh context.

## What shipped

- Settings → Software update on an additional till: when the replicated `update.auto_enabled` is exactly `"false"` (the same trimmed read and comparison as `followInputsOf`/`followCanInstall`), the line now reads "Automatic updates are off on the main till, so this till won't update by itself. Switch them on at the main till so this till follows it again." instead of claiming it follows the main till's version. Other states (version known/unknown, main/standalone till) unchanged.
- New key `settings.update.follows_main_off` in en/ar/fa/tr; de/es/pt pack PRs follow.
- Help `updates.md` step 3 (en/de/ar/fa/tr): one sentence for the same state.
- `TestSettingsPage_AdditionalTillSaysMainAutoUpdatesOff`: off with main version unknown and known (v1.4.2), and back on.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Low | "stays on its current version" is not strictly true: an "Update now" press on the main till's Tills page (ut-docs#2945) overrides `auto_enabled=false` | **Fixed**: "won't update by itself" in the key (all locales + packs) and the help sentence |
| 2 | Low | Other non-following states still say "follows": `UT_UPDATE_CHECK` off on this till, an install that can't replace itself, a settled failed attempt | Pre-existing, out of scope: Backlog follow-up card |
| 3 | Nit | "Turn them on on the main till" reads awkwardly | **Fixed** |

Reviewer confirmed the condition matches the follow rule exactly (not `autoUpdateSchedule`, which treats any odd value as off), and the precedence off > known version > unknown.

## Verification

- TDD re-verified by the reviewer in an isolated worktree: with only `settings_page.go` and `settings.html` reverted, the new test fails (`settings_page_test.go:1149: main version unknown: expected the 'automatic updates are off on the main till' line`); restored, all 14 AdditionalTill tests pass.
- `gofmt`, `go build ./...`, `golangci-lint run ./...` (0 issues), `go test ./internal/pages/`, and the build-job guards (i18n, help-topics, help-drift, compliance, competitor naming, core-neutral, data-access, no-showmodal, kiosk-engine, …) pass.
- Rendering is covered through the real template via httptest. No driven browser run: the state needs a paired replica with a replicated setting, and the change is one text swap inside an existing paragraph with no layout or CSS change.
- Docs-shots: no screenshot shows an additional till's Settings page; surface hash refreshed with `update-docs-shots-surface-hash.sh` (as in #2949).
- Packs: `check-key-drift.sh` against this branch's `en.json` is 0 drift in de/es/pt.

## Verdict

Safe to merge; then merge the three pack PRs.

# Review: additional till shows "follows the main till" instead of the auto-update box (ut-docs#2949)

**Date:** 2026-10-09 · **Branch:** `fix/2949-replica-update-follows-main` · **Built by:** Opus 5.5 · **Reviewed by:** Fable (independent subagent)

## What shipped

- `internal/pages/settings_page.go`: on an additional till (`sync.primary_url` set) the page passes
  `autoUpdateFollowsMain` and the main till's version (`followTarget`: live link hello, else the stored
  `sync.main_version`). The version is device input, so it is printed only when `releaseVersion` accepts it.
- `web/ui/pages/settings.html`: on an additional till, Settings → Software update shows
  "This till follows the main till's version (v…). Change automatic updates on the main till." (or the
  version-less variant) instead of the "Update automatically" box + time. Main/standalone tills unchanged;
  the one `settings-update-schedule` lock fieldset stays in the template.
- New keys `settings.update.follows_main` / `settings.update.follows_main_unknown` in en/ar/fa/tr; the
  de/es/pt language-pack PRs follow in the same cycle.
- Help `updates.md` step 3 (en, de, fa, tr, ar) says what an additional till shows.
- `update_api.go` comment on `autoUpdateSchedule` updated; docs-shots surface hash refreshed
  (docs-shots render a standalone till, so no pixels change).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | `guard-docs-shots.sh` fails: settings page is in the surface hash | Fixed: hash refreshed, commit carries `Docs-Shots-Unchanged: true` |
| 2 | Low | With the main till's auto-update Off, a replica no longer follows (`followCanInstall`), but the line still says it follows | Accepted; out of the card's AC, filed as a Backlog follow-up |
| 3 | Low | Main till unreachable: the version shown is the last one seen | Accepted; the status chip already says the main till is unreachable |
| 4 | Nit | Turkish word order | Fixed: "…sürümünü (%s) izler." |
| 5 | Nit | First half of the `autoUpdateSchedule` comment is historical | Accepted, still accurate |

Reviewer also checked: escaping of the version (html/template + `releaseVersion` regexp), the lock-region
test, test strength, bidi of `(vX.Y.Z)` in fa/ar, and terminology vs. neighbouring keys. Verdict: safe to merge.

## Verification

- TDD: `TestSettingsPage_AdditionalTillShowsFollowsMainInsteadOfAutoUpdateBox` failed with the
  production change stashed ("no 'follows the main till' line") and passes with it.
- `go build ./...`, `go vet ./internal/pages`, gofmt clean; `go test ./...` green except `internal/plugins`,
  which hit the default 10-min package timeout locally (untouched package; CI runs it in its own
  wider-timeout step).
- Guards: i18n, help-drift, help-topics, compliance-claims, competitor-naming, no-showmodal,
  no-inline-handlers, data-access, core-neutral, kiosk-engine, page-http-error, autofill, osk, htmx,
  docs-shots: all pass.
- Driven run: a throwaway till booted from the branch binary, marked as an additional till in its DB
  (`sync.primary_url`, `sync.main_version=1.4.2`), Settings → Software update screenshotted at 1024×600 in
  en (light), fa (light, dark): the line renders muted under "Check for updates", wraps inside the card, no
  box/time, `(v1.4.2)` reads correctly in RTL. Not looked at: de (pack not installed locally), ar, phone width.

## Deferred

- Line wording when the main till has automatic updates off (finding 2) → Backlog card.

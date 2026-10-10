# Review: additional till says why it doesn't follow the main till (ut-docs#4053)

Date: 2026-10-10 · Lane: cloud-54 · Author: Opus 5.5 · Reviewer: Fable (independent, fresh context)

## What shipped

Settings → Software update on an additional till used to say "This till
follows the main till's version…" even in states where the follow rule
(`followDecision` / `followCanInstall`) does not follow. #4031 fixed only
"automatic updates off on the main till". Now:

- `followLine(followInputs)` (`internal/pages/update_follow.go`) is a pure
  state function in `followDecision`'s order: `main_off` → `checks_off`
  (`UT_UPDATE_CHECK` off on this till) → `follows` when not behind →
  `manual` (behind, install can't replace itself) → `failed` (behind, the
  last attempt at this target failed or didn't take) → `follows`.
- The settings handler reads `followInputsOf` (the same reader the status
  chip uses every 5 s, so there are no new side effects or probes) and passes
  `autoUpdateFollowLine`. `settings.html` picks the line and exposes
  `data-follow-line` for tests.
- New keys `settings.update.follows_main_{checks_off,manual,failed}` are in
  en/ar/fa/tr, with ut-plugin-language-{de,es,pt} follow-up PRs
  (`i18n/4053-follow-line-states`, patch bumps).
- Manual: `web/help/*/updates.md` step 4 has one extra sentence (en/de/ar/fa/tr).
- The docs-shots surface hash is refreshed without regenerating screenshots.
  No docs-shots spec seeds a replica, and the `updates` topic has no
  screenshots, so no captured pixel changes.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | The `manual`/`failed` copy pointed at **Check for updates** "to install the main till's version". That control installs the *latest* release (`/api/update/check` → `/api/update/apply`). That can run a replica ahead of its main till, which #2726/#2738 forbid. On an install that can't replace itself, the control installs nothing. | **Fixed.** `manual` now says "install that version on this till by hand". `failed` now says to press **Update now** on the main till's Tills page "while the two are connected". Changed in all 7 locales and 5 help files. |
| 2 | should-fix | The regenerated `web/help/img/manifest.json` was not committed, so docs-shots would go red in CI. | **Fixed.** Committed separately with a `Docs-Shots-Unchanged: true` trailer. |
| 3 | nit | A `failed:<code>` attempt is also retried automatically after a restart. | Accepted. The line stays consistent with the chip's `followCanInstall`. "Update now" is the one retry that also covers the no-effect case. |
| 4 | nit | The Tills-page "Update now" only renders while the replica is linked (`CanUpdateNow = Linked && older`). | **Fixed** by the same copy change ("while the two are connected"). |
| 5 | nit | ar `checks_off`: "إعادة تشغيله" reads as "restart it". | **Fixed** → "إعادة تفعيله". |

The reviewer confirmed that the `followLine` precedence and gating match
`followDecision`/`followCanInstall`, and that `manual` is gated on
`followBehind` (as the `Supported` probe in `followInputsOf` is). It found
no SQL, network, CSS, showModal or file writes in the change.

## Verified

- TDD: I removed the `checks_off` branch, and `TestFollowLine` plus
  `TestSettingsPage_AdditionalTillSaysWhyItDoesNotFollow` both failed with
  the expected state. Restored, they pass.
- Driven run: a throwaway till (`UT_AUTH=off`, `UT_UPDATE_CHECK=0`,
  `sync.primary_url` set) renders `data-follow-line="checks_off"`. I took
  and looked at screenshots of the Software update card in en (LTR) and
  fa (RTL) at 1024×600 and 360×800. The text wraps inside the card, and
  `scrollWidth == innerWidth` at both widths. Not driven live: `manual` and
  `failed`, because a dev build is never "behind" (`releaseVersion("dev")`
  is false). The httptest covers them through the real mux.
- Gate: gofmt clean; `go vet ./...` and golangci-lint report 0 issues.
  `go test ./...` passes in every package except `internal/plugins`, which
  hit the 600 s default test timeout while compiling WASM in this
  container. This change does not touch that package; CI runs it. The guards
  in ci.yml's build job pass, except for local-tooling gaps:
  `guard-deadcode-baseline*` (the local deadcode tool was built with
  go1.26, and the module needs go1.27) and `guard-shellcheck-version`
  (shellcheck is not installed). Packs: `check-key-drift.sh` (3203/3203)
  and `validate.sh` pass.

## Verdict

Safe to merge once CI is green. Merge core first, then the three pack PRs
in the same cycle (new keys).

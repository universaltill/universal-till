# Review: manager-PIN prompt — wrong-PIN error announced to screen readers (ut-docs#5299)

Branch `fix/5299-pin-error-role-alert`. A Sonnet Dev agent built it. A separate Sonnet Tester agent ran a mutation and a driven run. An independent Opus reviewer reviewed it. The orchestrator applied the two optional review items.

## What shipped

- `web/ui/partials/elevation_prompt.html`: the wrong-PIN `.login-error` div now has `role="alert"`. It is still rendered only when `ErrKey` is set. This is the one shared partial. It covers the htmx OOB swap and `utPostWithElevation` (app.js parses the server HTML, `replaceWith`s `#elevation-modal`, and builds no markup of its own).
- `internal/pages/elevation_test.go`: `TestElevationPrompt_WrongPINErrorHasRoleAlert`. A wrong PIN must render `.login-error[role=alert]`, and a first prompt (no PIN tried) must render no alert.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| L1 | low | The negative case (no error → no alert) was untested. | **Fixed**: first-prompt assertion added. |
| L2 | low | The class check used exact equality, so a second class would make the test miss the element. | **Fixed**: `slices.Contains(strings.Fields(...))`. |
| I1 | info | About 20 other `.login-error` banners (login, setup, page `errKey` banners) lack `role="alert"`. | Out of scope → Backlog card (filed at close-out). |

## Verification

- TDD: with the attribute reverted, the test fails with `.login-error must carry role="alert"`. Restored, it passes. Seen by the Dev agent, the Tester, the Reviewer and the orchestrator.
- Driven run (Tester): a temporary Playwright spec ran against a real till as a cashier. Before submit there was no `.login-error`. After wrong PIN `000000`, there was one `#elevation-modal .login-error[role="alert"]` with text "Invalid PIN". The temporary spec was removed.
- `gofmt`, `go build ./...`, `go vet ./internal/pages`, `go test ./internal/pages -run Elevation`, `guard-i18n.sh`, `guard-no-showmodal.sh`.

Pre-flight: `web/help/` n/a (screen-reader semantics only, nothing visible changes, screenshots unaffected); `reference/` n/a; README n/a; en.json keys n/a (none added); review record: this file; follow-up → Backlog card for the `.login-error` role sweep.

Verdict: approve.

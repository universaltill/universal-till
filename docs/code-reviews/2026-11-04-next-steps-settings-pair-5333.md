# Review: Next steps card on a Settings pair (ut-docs#5333)

Date: 2026-11-04 · Branch `test/5333-next-steps-wizard-only` · Lane `lane:cloud-24`
Author: Sonnet (Dev subagent) · Reviewer: Opus 5.5 (independent subagent, different model)

## What shipped

Decision for design-5058 non-goal N5: a Settings → Tills pair (`POST
/api/enrol/pair`) or move **never creates** the Next steps card. A till with
its own history keeps its staff and PINs, devices, stock and products
(`enroll.Pair` only resets and re-adopts the cloud identity), so every line of
the card would be false. The one exception the review found: when the setup
wizard's cloud commit fails (`setup_page.go`, `cloudPairFailed`), the till is
still brand-new, so the wizard now parks the card as `deferred:<master>`
(invisible). The next successful Settings pair promotes it to
`pending:<master>`. The pending-move branch and move approval never touch it.

- `internal/pages/setup_next_steps.go`: `nextStepsMode`, `deferNextSteps`,
  `promoteDeferredNextSteps`; doc comments list the `deferred:` state.
- `internal/pages/setup_page.go`: defer on the failed-commit branch.
- `internal/pages/settings_page.go`: promote on pair success, with a comment.
- Tests: `TestEnrolPair_NextStepsOnlyPromotesDeferred`,
  `TestCloudSetupWithFailedCommitDefersNextSteps`,
  `TestNextStepsDeferredShowsNoCard`; absence asserts added to
  `TestEnrolPair_MoveConfirmsThenOpensApprovalPage` and
  `TestMoveApprove_ValidPINSignsAndMoves`.
- Help: unchanged. `web/help/en/multitill.md` already describes the card after a cloud setup. A sentence for the failed-commit edge case was dropped, because it would force a full docs-shots regeneration (CI `guard-docs-shots`) for one rare path. The surface hash was refreshed (`Docs-Shots-Unchanged: true`): the Go edits change no rendered pixel.
- ut-docs `reference/design-5058-replace-main-till.md`: Note #5333 under T3.

## Findings (review round 1)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | medium | "Never on a Settings pair" misses the wizard-commit-failed path, where the till is brand-new and the owner is told to pair again in Settings | fixed: deferred marker promoted on the Settings pair |
| 2 | low | cloud/till matrix in the pair test had no effect (Settings `Pair` drops `data.store`) and copied a fake handler inline | fixed: test collapsed to the pre-existing-value dimension, uses `pairDeps` |
| 3 | low | move-path assertion discarded the `Settings.Get` error (false pass) and used the prod const | fixed: err checked, test-file key literal |
| 4 | low | `TestMoveApprove_ValidPINSignsAndMoves` did not pin absence | fixed |
| 5 | nit | comment/design-note wording | fixed |

## Verification

- Red/green re-verified by the orchestrator: removing the
  `promoteDeferredNextSteps` call fails `pre=deferred:till` and `pre=deferred:`
  ("want \"pending:till\""); restored → green. The Tester independently put a
  `markNextSteps` call into the pair success and pending-move branches; all the
  absence tests failed.
- `gofmt`, `go build ./...`, `golangci-lint run ./...` (0 issues), `go test ./...`: all packages pass except `internal/plugins`, which hit the 10-min `go test` timeout in this container (wasm-heavy, untouched by this change; CI runs it).
- ci.yml build-job guards: 58/60 pass. `guard-deadcode-baseline.sh` (local
  deadcode binary built with go1.26 vs go1.27 module) and
  `guard-shellcheck-version.sh` (no shellcheck in this container) are
  environmental here and run in CI.
- No UI driven run: no screen changed. The card's rendering is unchanged, and
  `deferred:` is invisible (pinned by test).

Pre-flight: web/help topic — n/a (existing multitill text still true; the edge case is not worth a full docs-shots regeneration); docs-shots — surface hash refreshed, no pixel change; reference/ page — updated (design-5058 Note #5333); README — n/a; en.json keys — n/a (no new strings); review record — this file.

Verdict: approved after fixes.

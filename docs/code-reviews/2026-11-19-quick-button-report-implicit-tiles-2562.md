# Review — quick-button report includes implicit tiles, gated (ut-docs#2562)

PR universal-till#2610. Author model Sonnet 5.5; reviewer Fable 5.2 (independent, fresh context, batch review 2026-11-19); tester Sonnet 5.5.
Pre-flight: review record — this file · help/docs-shots: no screen changes, `web/help/img/manifest.json` surface hash regenerated after merging main · reference n/a · en.json/i18n n/a (no strings) · ADR n/a (no new decision).

## What shipped
The device report's `quick_buttons` / `quick_buttons_total` now lists every tile the sell screen shows, implicit (catalogue-derived) tiles as well as explicit `shortcut_buttons` rows. `set_quick_button_layout` materialises implicit tiles when it reorders. A gate (`quickButtonsReportGate`, `internal/pages/cloudsync_wire.go`) sends the report only when the sell-screen report version moved or after 30 minutes, and records it as sent only once a POST carrying it got through. A degraded sale-screen read sends nothing and the layout directive is refused.

## Findings
- APPROVE (Fable 5.2 batch review), with:
- M1 (medium, FIXED here): a failed same-tick follow-up device-report POST (#2609) ends the tick contacted=true err=nil, so `commit` recorded the version the follow-up rebuilt as sent and later check-ins omitted `quick_buttons` until the 30-minute refresh. Fix: new `cloudsync.Hooks.ReportPostFailed`, called when the follow-up POST fails; the quick-button gate and, being a small change, `rolesReportGate` mark it and skip recording in `commit`. Tests: `TestQuickButtonsReportGate_FailedFollowUpPostDoesNotCommit` (pages) and `TestTick_DeviceReportFollowUpFailureIsReportedToHooks` (cloudsync). Red first: both failed to compile (no hook), then with the hook added but the gates not wired the pages test panicked on a nil hook; green after wiring. No roles-gate-specific test was added.
- L2 (low, ACCEPTED): a degraded thumbnail/icon/price lookup also suppresses the quick-button report, although only SellScreenStates and barcode fallbacks matter for it. Not trivial: `degraded` is merged across several lookups inside `internal/ui/buttons.go`. Follow-up.
- L3 (low, accepted, follow-up): ut-cloud does not consume `quick_buttons_total` yet, and ignores an empty list.
- L4 (low, accepted, follow-up): barcode whitespace trimming differs between the report and the layout directive.
- L5 (accepted): "WIP" commit subjects on the branch reach main through the merge.

## Verification
Reviewer and tester TDD re-checks: a report-only-explicit-rows mutation turns 7+ tests red; gate-commit-without-success and degraded-read mutations turn `TestQuickButtonsReportGate_SendsOnChangeCommitsOnSuccess` and `TestCloudSetQuickButtonLayout_DegradedGridRefused` red. Plus the M1 test above.
After merging main (#2609): `go build ./...`, `go vet ./...`, gofmt, golangci-lint (0 issues); `go test ./internal/pages/ -run 'QuickButton|Gate|Cloud|Hooks'`, `./internal/cloudsync/`, `./internal/ui/` pass; `scripts/ci/guard-docs-shots.sh` passes. Heavy `-race` is left to CI.
Verdict: APPROVE.

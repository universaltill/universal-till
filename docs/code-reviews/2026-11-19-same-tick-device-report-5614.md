# Review — same-tick device report after applied directives (ut-docs#5614)

PR universal-till#2609. Author model Sonnet 5.5; reviewer Fable 5.2 (independent, fresh context, batch review 2026-11-19); tester Sonnet 5.5.
Pre-flight: review record — this file · help/docs-shots n/a (no screen or flow) · reference n/a · en.json/i18n n/a (no strings) · ADR n/a (no new decision) · ut-docs#6677 carries the doc note.

## What shipped
After a check-in applies a directive that changes what the cloud shows (quick-button layout, catalogue, settings), the tick rebuilds the device report and POSTs it once more in the same tick, gated by a hash so an unchanged report adds no request. The follow-up's answer directives are dropped (the cloud re-serves them until a result is posted).

## Findings
- APPROVE. TDD re-verified by the reviewer: with `applied.deviceReport` never set, six `TestTick_*` in `device_report_5614_test.go` go red ("sync POSTs = 1, want 2"); with the hash gate disabled `TestTick_NoExtraPostWithoutReportChange/applied_but_value_unchanged` goes red; restored, green.
- M1 (medium, cross-PR with #2610): a failed same-tick follow-up still lets the quick-button report gate record the new list as sent, so the cloud keeps the old order for up to 30 min (self-heals). Fixed on the #2610 branch after this PR merges (see that record).
- L1 (low, accepted, follow-up): `configReportGate.add` records "sent" on add, not on POST success; same exposure already existed for a failed main POST.
- L5 (accepted): a placeholder "WIP (unreviewed)" commit subject reaches main through the merge commit; not matched by `guard-wip-commits.sh`.

## Verification
`go test ./internal/cloudsync/` full pass; `-race -run '5614|Directive|CheckIn|Tick'` pass; vet, gofmt, golangci-lint clean. Full-package `-race` and `internal/plugins` are left to CI (`test-heavy`). CI green on the head at merge.
Verdict: APPROVE.

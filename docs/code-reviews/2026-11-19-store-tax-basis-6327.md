# Review: till reports its default tax rate and basis (ut-docs#6327)

Date 2026-11-19 (round 1 2026-11-16). Full record, findings and both review rounds:
ut-cloud `docs/code-reviews/2026-11-19-store-tax-basis-6327.md`.

## What shipped here
`internal/pages/cloudsync_wire.go`: `DeviceExtra` adds `tax_rate_bp` (int basis points) and `tax_inclusive`
from one `d.CurrentState()` snapshot (the runtime state, not `Cfg`). Additive and `omitempty`; the cloud is
tolerant of tills that omit them and ships first.

## Findings for this repo
Round 1 (Fable): sound; nit — two `CurrentState()` calls could mix snapshots → fixed (one call). Round 2:
no findings on the till side.

## Verification
TDD re-verified by the reviewer (keys removed → `TestDeviceExtra_ReportsDefaultTaxRateAndBasis`,
`…MarshalsAsSnakeCaseNumbers` fail). Tester: a real settings upsert → engine config → real `cloudsync.Start`
POST body carries `tax_rate_bp=810 tax_inclusive=false`; the cloud accepted a replay of that exact body.
`go test -race -run 'TaxReport|DeviceExtra|CloudSync' ./internal/pages/` ok; `go build ./...` ok.

Pre-flight: review record updated; help/README/i18n/en.meta n/a (no user-visible change, no strings);
mirrors n/a (no manifest/ABI change; a cross-repo pin of the two wire names is deferred → #4939);
migrations n/a.

## Verdict
SHIP.

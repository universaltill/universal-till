# Review: guest SDK channel.HTTPRequest.Throttle (ut-docs#4892)

**Date:** 2026-10-24 · **Lane:** lane:cloud-41 · **Built by:** Sonnet 5.5 ·
**Reviewed by:** Opus 5.5 (independent subagent).

## What shipped
- **`sdk/plugin/channel` (`types.go`, `hostfns.go`, `fakehost.go`):** an
  optional `HTTPRequest.Throttle` (`throttle`) field with `{available,
  maximum, restore_rate}`.
  - It is the channel's cost bucket as the guest last saw it.
  - The ut-cloud channel host now paces `cost_hint` against it (ut-cloud,
    same card).
  - It is additive within sales-channel contract v1 (§7).
  - The fake host passes it through.
- **The `Throttle` doc comment** says to send a report once, right after the
  response it was read from, never to replay it, and to round Shopify's
  floats to whole points.
- **`channel_test.go`:** the round-trip test asserts the report survives the
  wire. `TestHTTP_NoThrottleStaysAbsentOnTheWire` checks that `omitempty`
  keeps old guests' requests unchanged.

## Findings
- No defect on this side.
- The review's minor finding about stale reports is answered by the doc
  comment above.

## Verified
- Red phase: `channel_test.go:128: throttle report did not survive: <nil>`.
- In `sdk/plugin`: `gofmt` is clean, `go vet` is clean, and `go test ./...`
  passes.
- `golangci-lint` reports 0 issues.
- `GOOS=wasip1 GOARCH=wasm go vet ./channel` is clean.
- The root `go build ./...` passes.
- The ABI mirror (ut-cloud `abicontract`) checks import names and parameter
  counts only. No import changed, and it passes.

## Verdict
Safe to merge.

# Review: flaky TestStockUploadLoopDebouncesOnTheRealtimeTier (ut-docs#4995)

Scope: test-only change in `internal/cloudsync/stock_upload_4763_test.go`. No product code changed.

## Root cause
The test commits four quantity updates (8, 7, 6, 5) separately. Each commit fires the outbox trigger. A 40 ms
loop tick between two commits uploads a mid-burst quantity, and `waitFor(len(requests) >= 2)` returned on that
partial upload, so the assertion saw 8000 (or 7000) instead of 5000. The loop was correct: a later tick uploads 5000.

## Reproduction (before the fix)
Test binary, `-test.cpu 1 -test.count=300`, 8 busy-loop CPU burners: 3 failures, e.g.
`last row = {... on_hand_milli:8000 seq:2}, want 5000` (the card's signature) and `7000` variants.

## Fix
Keep the four separate commits (real debounce coverage). Wait until a request carries the latest value (5000)
instead of waiting for "a second request". No longer sleeps, no timeout change. Same load run after: 300/300 pass.

## Independent review (Opus subagent, different model)
Root cause confirmed; loop and Ack semantics sound (Ack deletes only sent seqs; re-marks get new seqs).
It flagged that my first attempt (one transaction for the burst) made coalescing automatic and weakened the test;
adopted its alternative above. Remaining nit (tx rollback) no longer applies.

## Verification
`gofmt`, `go vet ./internal/cloudsync`, `golangci-lint` 0 issues, `go test ./internal/cloudsync` ok.

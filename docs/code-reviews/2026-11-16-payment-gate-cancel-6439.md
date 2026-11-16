# Review: payment gate cancel reserve + outcome_unknown toast (ut-docs#6439)

Reviewer: Opus-class inline review of HEAD (b9ba7923a), diff HEAD~1..HEAD.

## What shipped
- `gateRequestTimeout` / `gateCancelReserve` (5s): each `http_request` inside a payment authorize/refund event is bounded by the gate deadline minus 5s, so a stalled call cannot eat the time the plugin needs to cancel the reader; inside the reserve, the call (the cancel) gets all time left.
- `PaymentDeclineError` carries the plugin's `decline_code` from stdout on non-zero exit; unwraps to the exit error.
- `completeTender` maps `outcome_unknown` to `paymentDeclinedError.OutcomeUnknown`; the tender handler returns 402 with the new `pos.toast.payment_outcome_unknown` copy.
- en.json + en.meta.json only (ADR-0160).

## Verification
- `go build ./internal/...`, `go vet ./internal/plugins` clean; plugins tests for the new code pass; pages tender test passed earlier (full ./internal/pages suite deliberately not run, >10 min).
- Error chain checked: handler error -> `blocking event ... %w` in ipc.go keeps `errors.As` working.
- Non-gate events never get a deadline (zero time -> unbounded as before). Non-net plugins (2s timeout) fall in the reserve branch: calls get all time left, no regression.

## Findings
1. Low, FIXED: `declineCodeFrom` accepted any string up to 64 chars and the code is echoed in `Error()` (logged), allowing newline/control-char log injection from a plugin. Now restricted to `[A-Za-z0-9_-]`; test case added.
2. Info, accepted: gate deadline is taken just after the context is created (microseconds skew); immaterial against a 5s reserve.
3. Info, accepted: toast promises "retry is safe"; relies on the same-basket idempotency key behaviour (ut-docs#1762), covered by existing tests.

No help-topic change: toast copy only, no new page. No client names or secrets in test data.

Pre-flight: build, vet, gofmt, targeted tests run; full pages suite skipped by instruction.

## Verdict
Approve with fix applied. No deferred items.

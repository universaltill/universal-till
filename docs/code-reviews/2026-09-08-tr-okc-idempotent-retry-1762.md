# ut-docs#1762 — TR ÖKC retried tender no longer double-charges/double-prints

Date: 2026-09-08
Branch: `fix/1762-tr-okc-idempotent-retry`
Review: one independent subagent, fresh context, Opus (`complexity:medium`).

## The bug

The device-publish call for a Turkish ÖKC fiscal-device tender generated a
**fresh random UUID as its idempotency key on every call**, including when
the operator manually re-taps Pay after a decline/timeout on the same
basket. The device itself already de-dupes correctly by that key (see
`plugins/tax-tr/okc/bridge_test.go`'s pre-existing
`TestBridgeSale_IdempotentOnRequestID`) — the retry just never sent the
same key, so a genuinely slow-but-successful chip-and-PIN authorization
could get charged and printed (mali fiş) twice. Separately, the WASM
plugin runtime gave every net/TCP-permitted plugin a blanket 10s deadline
for *any* blocking event, which is short for a real chip-and-PIN
authorization and part of why the till gave up in the first place.

## The fix

1. `internal/pos.Service` gains `TenderAttemptID()` — a UUID minted lazily
   per basket, stable across repeated calls, cleared by `resetLocked`
   (`Reset()`/`Restore`) so a genuinely new basket never inherits it.
2. `internal/plugins.EventBus` gains `publishWithID`/`PublishAuthorizeWithID`
   so a caller can supply its own event id instead of always getting a
   fresh one. Every existing caller (`Publish`, `PublishAuthorize`) is
   unchanged.
3. `internal/pages/refund_page.go`'s `blockingPaymentEventWithResponse` now
   delegates to `blockingPaymentEventWithResponseAndID(..., requestID, ...)`
   with `requestID=""` preserving the old fresh-id-per-call behaviour — the
   refund call site is deliberately untouched; refund idempotency is a
   separate, out-of-scope concern.
4. `internal/pages/pos_api.go`'s `completeTender` derives, per payment in
   the tender loop, `requestID = "<TenderAttemptID>:<payment index>:<sha256
   of the authorize payload>[:8]"` and passes it through. The payload hash
   (amount/method/reference/fiscal-device line+tax extras) is the load-
   bearing part of the fix beyond the first draft — see review finding #1.
5. `internal/plugins/wasm_runtime.go` adds `paymentGateTimeout` (30s), a
   floor applied only to `.authorize`/`.refund`-suffixed events on a
   net/TCP-permitted plugin, replacing the generic 10s `netTimeout` for
   that event class specifically. Comment cites published EMV chip-and-PIN
   timing (typical dip ~7-10s, ~15s average end-to-end authorization wait).
6. `plugins/tax-tr/okc/protocol.go` / `plugin.json` / `README.md`: the
   plugin's own `okc.read_timeout_ms` default raised 8000ms → 25000ms (see
   review finding #2 — the outer WASM ceiling was moot while the plugin's
   own transport gave up first).

## Independent review — findings

One Opus subagent reviewed the diff in an isolated worktree, ran the full
gate itself, and independently re-verified the TDD claim (reverted the
`completeTender` call site, confirmed
`TestTenderHandler_RetriedTenderOnSameBasketReusesIdempotencyKey` failed
with two distinct UUIDs, restored, confirmed it passed again).

**Finding 1 (HIGH, fixed).** The first draft's key was
`attemptID + ":" + paymentIndex` — stable for a genuine retry, but nothing
invalidated it if the basket's *contents* changed between attempts
(nothing clears `tenderAttemptID` except `Reset()`). A declined tender
followed by the operator editing the basket before retrying — or, on the
shared kiosk engine, a second customer picking up after a first
customer's timed-out tender — would resend the same key for a now-
different payment. Since the device dedupes on the key alone, it would
hand back the *first* attempt's memoized approval for the *new* (wrong)
amount, and nothing would notice: a silent money/fiscal mismatch, strictly
worse than the bug being fixed. **Fixed** by folding a SHA-256 digest of
the actual authorize payload into the key (see point 4 above) — identical
content still dedupes correctly, changed content now mints a distinct key.
Regression test: `TestTenderHandler_ChangedPaymentOnSameBasketGetsDifferentIdempotencyKey`
— independently re-verified the same way (reverted to the position-only
key, confirmed the test fails with the exact "device would replay the
first attempt's stale approval" message, restored, confirmed it passes).

**Finding 2 (MEDIUM, fixed).** The new 30s WASM-level ceiling was inert at
shipped defaults: `okc.read_timeout_ms` defaulted to 8000ms, so the
plugin's own transport gave up at ~8s regardless of the wider outer
deadline — the card's own stated rationale ("~15s average authorization
wait") wasn't actually covered. **Fixed** by raising the setting's default
to 25000ms (comfortably under the 30s outer ceiling, comfortably over the
cited ~15s average). `TestConfigNormalize` updated for the new default.

**Finding 3 (LOW, accepted as-is).** `TestBridgeSale_SlowFirstAttemptThenRetryPrintsOnce`
touches no production code under `plugins/tax-tr/okc/` — it re-proves the
device simulator's own pre-existing dedup contract under timing pressure,
not a regression guard for this diff. Kept as documentation of the
device-level half of the fix; the load-bearing regression coverage is
`TestTenderHandler_*` (real HTTP handler, real `completeTender` path) and
`TestTenderAttemptID_StableUntilResetThenFresh`.

**Finding 4 (LOW, resolved by Finding 1's fix).** Keying on payment
position alone was fragile if a retried request reordered the payments
array. Folding payload content into the key (Finding 1) means a
reordered-but-otherwise-identical split-tender leg still gets a
content-appropriate key; a genuinely different leg at the same position
now mints a different key regardless of position fragility.

## Verified

- `go build ./...`, `go vet ./...` clean.
- `go test ./...` — full repo, zero failures (includes `-race` on
  `internal/pos` specifically for the new locking-sensitive method;
  `internal/pages` under `-race` was not run to completion — package is
  slow under race and exceeded the review's time budget, but the
  non-race full suite is green and zero `DATA RACE` reports surfaced
  before the reviewer's own race run was cut off).
- `scripts/ci/guard-data-access.sh`, `scripts/ci/guard-i18n.sh` — both
  green (no inline SQL added, no new user-facing strings — this is a
  payment-logic/backend-only change, no UI surface touched).
- TDD verified twice, independently, for both Finding 1's regression test
  and the original idempotency-key test (see above).
- No real client/shop name, no secret-shaped literal introduced.

## Explicitly out of scope / deferred

- Refund-path idempotency (`blockingPaymentEventWithResponse`'s
  `requestID=""` default) — same class of risk, deliberately not touched
  here; worth its own card if it hasn't got one already.
- Real ÖKC hardware verification — unavailable to any cold cloud/cron
  session; assessed via the project's own device simulator throughout,
  which is its designated stand-in per existing precedent
  (`bridge_test.go`).

## Verdict

Safe to merge. One review round; the round found one blocker-class issue
(Finding 1, a money/fiscal-record correctness gap), which is fixed and
independently re-verified, plus one non-blocking-but-fixed timeout-default
gap (Finding 2). No second review round — both fixes are small, scoped,
and directly targeted at what the first round found, per this pipeline's
standing "second round only for a blocker, scoped to the fix" rule.

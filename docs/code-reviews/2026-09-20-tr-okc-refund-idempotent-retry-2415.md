# ut-docs#2415 — TR ÖKC retried refund no longer prints a second iade fişi

Date: 2026-09-20
Branch: `fix/2415-tr-okc-refund-idempotent-retry`
Review: one independent subagent, fresh context, Opus (`complexity:medium`).

## The bug

`payment.<key>.authorize` (the sale/tender path) carries a stable
per-attempt event id (`attemptID`, ut-docs#1762) so a device-side retry
after a timeout never prints twice. `payment.<key>.refund` did not:
`blockingPaymentEventWithResponse` (`internal/pages/refund_page.go`) minted
a fresh event id on every call, and `plugins/tax-tr/main.go` used that id
as the ÖKC `request_id`. An operator re-tapping a timed-out refund
therefore sent a *new* key, the device/bridge idempotency map couldn't
match it, and a second **iade fişi** could print for one return. Deferred
explicitly out of scope by ut-docs#1762's own review.

## The fix

1. `GET /refund/{receipt}` (`refund_page.go`) mints a fresh UUID per page
   render (`RefundAttemptID`), rendered into a new hidden
   `refund_attempt_id` field on `#refund-form` (`web/ui/pages/refund.html`).
2. `POST /api/refund` reads `refund_attempt_id` (bounded to 128 chars —
   unauthenticated form input used verbatim as part of a device-facing id)
   and, when present, computes
   `requestID = refundAttemptID + ":" + sha256({payload, guard.returnedQtyByLine})[:8]`
   and calls `blockingPaymentEventWithResponseAndID` with it instead of
   always minting a fresh id. `plugins/tax-tr` and the device
   simulator (`plugins/tax-tr/okc/sim/sim.go`'s `seen` map) needed **no**
   change — both already forward/dedupe on whatever event id they're
   given, for `"sale"` and `"refund"` alike.
3. The now-unused thin wrapper `blockingPaymentEventWithResponse`
   (requestID="" always) was deleted, mirroring ut-docs#1566's precedent
   for the same class of zero-production-caller wrapper; its four test
   call sites (`payment_event_test.go`) now call
   `blockingPaymentEventWithResponseAndID(..., "", ...)` directly.

## Independent review — findings

One Opus subagent reviewed the diff in an isolated worktree, ran the full
gate itself, and independently re-verified the TDD claims (reverted the
fix, re-ran the new tests, confirmed the claimed pre-fix failures,
restored, confirmed green again).

**Finding 1 (BLOCKER, fiscal/money — fixed).** The first draft mirrored
`completeTender`'s `attemptID + payload-hash` shape but missed the
asymmetry: on the sale side, `attemptID` is *server-side* state
(`pos.Service.TenderAttemptID()`) that `resetLocked` clears the instant a
sale commits, so a completed tender can never leak its key into the next
one. On the refund side the attempt identity lived entirely in a
**client-held hidden field**, with nothing server-side invalidating it
once a refund committed. The refund page carries no `Cache-Control:
no-store`, so a bfcache'd back-navigation can resubmit the exact same
stale form. The reviewer reproduced this live against the real handler:
two separate, sequential, genuinely-intended partial refunds of the same
sale (1 unit each, of 2 sold), same `refund_attempt_id`, both approved and
**committed** — the plugin received the identical `request_id` both
times, meaning the device would have replayed the first refund's *iade
fişi* evidence for the second: two refunds paid out, one legal refund
receipt on file, no error and no audit trail of the collision (no
`UNIQUE` constraint on `fiscal_device_receipts.receipt_no`). The payload
hash alone can't catch this because the device-facing payload
(`method`/`amount`/`currency`/`original_sale_id`/`original_receipt`) is
genuinely byte-identical between the two refunds — routine sequential
partial refunds are not a corner case. **Fixed** by folding
`guard.returnedQtyByLine` (loaded per original sale line, already in
scope) into the id's hash alongside the payload: it changes the instant
*any* refund against this sale commits, restoring exactly the
invalidation `Reset()` gives the tender side. A genuine retry (nothing
committed in between) keeps the same guard snapshot and so the same key;
a refund that has since committed shifts the snapshot and mints a fresh
one. Regression test:
`TestPostRefund_TwoCommittedRefundsOnSameAttemptGetDifferentIdempotencyKeys`
— independently re-verified the same way (reverted to the
guard-state-less key, confirmed the test fails exactly as the review
described, restored, confirmed it passes).

**Finding 2 (nit, fixed).** `blockingPaymentEventWithResponse` had zero
production callers left once the refund gate started computing its own
requestID — the same condition ut-docs#1566 deleted the prior `err`-only
wrapper under. Deleted; its four test callers now call
`blockingPaymentEventWithResponseAndID(..., "", ...)` directly.

**Finding 3 (nit, fixed).** A doc comment referred to "the hidden
`#refund_attempt_id` field" as if it carried an `id` attribute (mirroring
`#offline-flag`'s own naming); the actual input has only a `name`
attribute. Comment wording corrected; the field itself needs no `id`
(nothing reads it by CSS/JS selector).

**Finding 4 (nit, fixed as hardening).** `refund_attempt_id` is
unauthenticated, unbounded form input reaching the device-facing
`request_id` verbatim. Not independently exploitable beyond Finding 1 (the
payload+guard-state hash already confines any collision to
same-sale/same-line/same-amount), but bounded to 128 chars (a genuine
value is always a 36-char UUID) while fixing Finding 1 anyway, so
malformed/oversized client input can't reach the bridge or the device's
own `seen` map at all.

## Verified

- `go build ./...`, `go vet ./...` clean.
- `go test ./...` — full repo, zero failures.
- `go test ./internal/pages/... -race -run 'Refund|BlockingPaymentEventGate'`
  — clean, no `DATA RACE` reports.
- `scripts/ci/guard-data-access.sh`, `scripts/ci/guard-i18n.sh`,
  `scripts/ci/guard-page-http-error.sh`, `scripts/ci/guard-htmx-loaded.sh`
  — all green (backend/payment-logic change plus one new hidden form
  field; no inline SQL, no new user-facing strings, no bare
  `http.Error`, htmx already loaded on this page).
- TDD verified twice, independently, for the core retry/changed-amount
  behavior and again for Finding 1's blocker regression test (see above).
- No real client/shop name, no secret-shaped literal introduced (test
  fixtures use `com.universaltill.payment-demo` / `example.test` /
  `'deadbeef'`, matching this package's existing convention).
- No operator-facing surface: a hidden form field carries no visible or
  translatable text, same class as the existing `#offline-flag` field
  (ut-docs#1493) — no UX/help-doc sign-off needed.

## Explicitly out of scope / deferred

- Persisting consumed attempt ids server-side (an alternative,
  more-robust fix the reviewer floated) — the guard-state fold achieves
  the same safety property with no schema change; worth reconsidering
  only if a future case needs invalidation guard-state alone can't
  express.
- Real ÖKC hardware verification — unavailable to any cold cloud/cron
  session; assessed via the project's own device simulator, its
  designated stand-in (`sim.go`, `bridge_test.go`).

## Verdict

Safe to merge. One review round; the round found one blocker-class issue
(Finding 1, a money/fiscal correctness gap), which is fixed and
independently re-verified, plus three nits (two mechanical, one
hardening), all fixed in the same round. No second review round needed —
the fix is small, scoped, and directly targeted at what the first round
found, per this pipeline's standing "second round only for a blocker,
scoped to the fix" rule.

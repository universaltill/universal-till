# Review: tap reversal and settle survive a client disconnect (ut-docs#6908)

Date: 2026-11-22. Lane: `lane:cloud-41`. Author model: Sonnet (Dev). Independent
reviewer: Opus 5.5, fresh context, in its own worktree. Verdict: **ship**.

## What shipped

The manager's and the cashier's tap decisions ran their money and state calls on
`r.Context()`. A closed tab or a LAN drop mid-call cancelled the reversal of a
captured card leg, but `tapUnlock` still cleared the tender lock, dropped the
captured legs and rotated the attempt id. The money stayed out and nothing was
left to retry.

- `internal/pages/tap_pending.go`, `internal/pages/tap_attention.go`: each of the
  three POST handlers (`/pay/tap/check`, the manager resolve, the attention
  resolve) derives `ctx := context.WithoutCancel(r.Context())` once, after its
  sign-in, step-up and form checks and before the first `tapAssess`. Every later
  call uses it: `tapAssess`, `TapBrokerCancel`, `tapCancelThenAssess`,
  `ConfirmTap`, `tapNote`, `tapUnlock` (the reversal), `settleOverrideApproved`,
  `repo.Mark`, and the audit row via `settingsAudit(r.WithContext(ctx), …)`.
  This follows the #6784 and #6805 precedent.
- `tellNativeResolver` is now bounded by `nativeResolveTimeout` (15 s).
  `devicepay.Resolve` otherwise waits only on `ctx.Done()`, which a detached
  context never closes. A timeout is logged and ignored: core's record is
  already settled, so the manager's choice never changes.
- Every other detached call already has a bound: broker calls are capped by
  `paybroker.Timeout` (15 s), reversal dispatch by the plugin bus timeouts
  (30 s, or 120 s for tap), and the rest are local SQLite writes.
- `r.Context()` stays only where nothing is written: the sign-in and step-up
  checks, `auth.FromContext`, the two GET views, the attention list, and the
  attention `repo.Get` before the decision (it fails closed).

## Tests (TDD)

New file `internal/pages/tap_disconnect_test.go`; the rig in `tap_pending_test.go`
gains `tapEnv.doCtx` and a `fakeBroker.onCall` hook.

- `TestTapResolve_ClientDisconnectDuringReversalStillReverses`: a split tender
  (demopay 60 + tapstripe 60), with the request context cancelled inside the
  reversal, on all three `tapUnlock` paths. On main: "a dropped connection
  failed the reversal 1 time(s)" (3 rows).
- `TestTapCancelThenAssess_ClientDisconnectDuringSessionCancelStillSettles`:
  cashier check, manager resolve and attention resolve. On main: "attempt state
  = unknown, want cancelled", and 422 in both resolve rows.
- `TestTellNativeResolver_BoundedOnDetachedCtx`: on main, "unbounded on a
  detached ctx".

The fail-on-main result was re-verified three times: by the Tester, by the
Reviewer in its worktree, and for the review fix below by the orchestrator. The
Tester also mutated six call sites one at a time (each moved back to
`r.Context()`, or the timeout removed), and each mutation turned a test red.

## Findings

1. **minor, fixed.** `TestTellNativeResolver_BoundedOnDetachedCtx` checked only
   that the call returned. An early return from `devicepay.Resolve` (forbidden,
   no resolver, unsupported provider) would have let it pass with the timeout
   removed. The blocking resolver now closes a `called` channel and the test
   asserts it was reached. Mutation re-checked: replacing the `WithTimeout` with
   a plain `WithCancel` turns the test red.
2. **minor, accepted risk.** Detached work can now outlive the request. A
   `/pay/tap/check` with `cancel=1` can make about 5 broker calls of up to 15 s
   each, and a cashier retry after 2 s then runs next to it on the same leg.
   This could already happen with a slow request and a double tap. Money stays
   safe: the reversal request id and refund key are fixed per leg, so the
   provider treats a repeat as the same refund; attempt writes are guarded by
   state (`ErrAttemptSettled`); and `RotateTenderAttemptIDIf` is compare-and-set.
   A per-leg in-flight guard is deferred to ut-docs#6909, which adds the same
   kind of guard on `/api/pos/tender`.
3. **nit, not taken.** The same detach comment appears three times.

Out of scope, deferred to ut-docs#6653: when a reversal genuinely fails (the
provider refuses it, not a cancel), `tapUnlock` still drops the lock and the
captured legs. Only a Problems-ring warning is left, which matches ADR-0136 and
`completeTender`. Keeping it retryable is a product and UX decision, and
belongs with #6653's durable tender journal.

## Verification

- `gofmt -l .`: no output. `go build ./...`, `go vet ./internal/pages/`: ok.
  `golangci-lint run ./internal/pages/...`: 0 issues.
- `go test -race ./internal/pages/ -run 'Tap|Attention|Refund|Tender|Native'`:
  ok, no races.
- `go test -timeout 40m ./...`: 124 packages ok.
- Guards in `ci.yml`'s `build` job: 68 of 70 pass locally. The other two fail
  only because of the environment and touch nothing this change does:
  `guard-catimport-version` (the clone has no `catimport/v*` tags) and
  `guard-shellcheck-version` (no shellcheck installed).
- No driven browser run: no template, CSS, JS, locale or rendered output
  changed, so there is nothing to see. The disconnect is exercised through the
  real mux with a cancellable request, which a browser can't trigger reliably.

Pre-flight: review record updated; `web/help/` n/a (no operator-visible change; a
disconnect no longer loses the reversal); `reference/` n/a (no page describes
these handlers' contexts; `docs/data-model.md:124` only names the route);
`en.json` n/a; README n/a; ADR n/a (follows the #6784/#6805 precedent);
follow-ups deferred to ut-docs#6909 (per-leg in-flight guard) and ut-docs#6653
(a reversal that genuinely failed stays retryable).

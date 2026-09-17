# Code review — payment-provider blocking gate fails open on a ListPaymentEntries DB error (ut-docs#2278)

- **Date:** 2026-09-16
- **Ticket:** ut-docs#2278 (`complexity:medium`)
- **Branch:** `fix/2278-payment-gate-fail-open`
- **Reviewer:** independent pass, Opus subagent working in an isolated
  worktree (per this card's `complexity:medium` routing — different model
  from the Sonnet implementation, never saw the dev reasoning).
- **Verdict: SAFE TO MERGE AS-IS.** No blocking findings; four should-
  fix/nit findings, all folded into this same diff before merge.

## The bug

`blockingPaymentEventWithResponseAndID` (`internal/pages/refund_page.go`)
is the shared blocking leg of the payment-provider contract, used by both
the refund gate (`refund_page.go:809`) and the tender-authorize gate
(`pos_api.go:344`). It started:

```go
entries, err := data.NewPluginRepo(d.Db).ListPaymentEntries(ctx)
if err != nil || len(entries) == 0 {
    return nil, nil
}
```

A genuine DB error on `ListPaymentEntries` was folded into the same
`(nil, nil)` return as "no payment entry configured" — both callers read
that as "nothing to check, proceed." So a real DB error silently let a
refund/sale proceed with **zero** chance for a payment-provider plugin to
veto it, even though both callers already have correct fail-closed
handling (`if blocked != nil` / `if err != nil`) for when this function
actually returns an error — the bug was purely that the DB-error case
never reached it.

## What shipped

- `internal/pages/refund_page.go`: `blockingPaymentEventWithResponseAndID`
  now distinguishes the two cases — a `ListPaymentEntries` error is
  returned (wrapped, naming the method) instead of being folded into
  `(nil, nil)`; `len(entries) == 0` with no error is unchanged. Both
  callers' existing fail-closed handling now engages automatically, no
  caller-side change needed. Doc comments on `blockingPaymentEvent`/
  `blockingPaymentEventWithResponse`/`...WithResponseAndID` updated to
  describe the three-way return contract explicitly.
- `internal/pages/pos_api.go`: the tender-authorize call site now logs the
  underlying error before returning the opaque `paymentDeclinedError` —
  review finding #1 below; without it a DB-error refusal was
  indistinguishable from an ordinary plugin decline in the logs.
- `internal/pages/refund_page_test.go` /
  `internal/pages/pos_api_test.go`: new
  `TestPostRefund_ListPaymentEntriesDBError_FailsClosed` /
  `TestTenderHandler_ListPaymentEntriesDBError_FailsClosed` — drop the
  `plugin_entries` table to force a genuine DB error, submit a **cash**
  refund/tender (a method with no plugin entry at all, chosen specifically
  to prove the fix fails closed on the error itself rather than on some
  plugin lookup succeeding), assert 402 + the correct localized copy + no
  sale/return recorded.

Explicitly **out of scope, not touched**: `pos_api.go:571`'s own
`ListPaymentEntries` call. It runs strictly after the sale is already
committed (`engine.Reset()` at :566) and is an explicitly best-effort,
non-blocking post-completion notification publish, not a blocking gate —
confirmed independently by the reviewer, who also noted that with this fix
in place, :571 is now structurally unreachable with this specific error on
a normal tender (any payment leg aborts earlier, at :344/:346).

## What the independent review found

Ran the full gate itself rather than trusting Dev/Tester's report:
`go build ./...`, `go vet ./...`, `gofmt -l`, the two new tests, the full
`internal/pages` package (all 5 sub-packages), `guard-data-access.sh`,
`guard-i18n.sh`, `guard-page-http-error.sh` — all green.

**TDD re-verification, done for real**: reverted only the `refund_page.go`
fix hunk (kept both new tests) — both failed with the predicted symptom
(200 instead of 402), and the tender case was worse than expected: it
didn't just return 200, it **committed a real sale** with the payment
gate unable to consult any plugin. Restored the fix — both passed again.

Four findings, all folded in before merge:

1. **should-fix** — `pos_api.go:344`: the tender path discarded the
   underlying error entirely (`paymentDeclinedError` carries no detail by
   design), so a DB-error refusal left zero server-side trace — a shop
   with a broken `plugin_entries` table would see every sale refused as a
   generic decline with nothing in the logs to distinguish it from an
   actual card decline. **Fixed**: log the wrapped error before returning
   the opaque error.
2. **should-fix** — `blockingPaymentEvent`'s top-of-chain doc comment
   (refund_page.go) still said "returns nil when ... or the plugin
   approves; returns the plugin's error when it declines" — stale now
   that a DB-error path also returns non-nil. **Fixed**: comment now
   states the error can mean either a decline or a lookup failure.
3. **nit** — `blockingPaymentEventWithResponse`'s comment asserted "a
   non-nil error means the payment-entries lookup itself failed" as if
   exclusive, when a plugin decline (the original, far more common case)
   also returns non-nil. **Fixed**: reworded to "may also mean."
4. **nit** — the wrapped error message double-said "list payment
   entries" (`ListPaymentEntries` already wraps its own error with that
   phrase), so the log read `list payment entries: list payment entries:
   SQL logic error: ...`. **Fixed**: re-wrapped as
   `payment gate: payment-entries lookup for %q: %w`, naming the method
   and de-duplicating the phrase.

After folding all four in: re-ran `go build`, `go vet`, the two targeted
tests (confirmed the new log line appears and the message is no longer
doubled), the full `internal/pages` package, and both guards — all green
again.

## Specific checks the review ran

- **Both call sites closed**: confirmed — refund (`blocked != nil` → 402 +
  `refund.error.provider_declined`) and tender-authorize (`err != nil` →
  `paymentDeclinedError` → 402 + `pos.toast.payment_declined`). A third
  surface neither the card nor the brief named — the self-order kiosk
  (`self_order_shop.go`, also routes through `completeTender`) — gets the
  fix for free, same call path.
- **Missed callers**: `blockingPaymentEvent` itself has no production
  caller (only a test). `HasSubscribers` is an in-memory map read, not a
  DB call, so the "no subscriber" branch hides no error of this class.
- **Is failing closed on ANY DB error (even for an ungated method like
  cash) too conservative?** Reviewed explicitly and held: local SQLite
  runs WAL + a 5s busy timeout, so transient BUSY isn't a realistic false-
  decline source; `EnsurePaymentMethod` already runs earlier on both paths
  against the same DB, so only a `plugin_entries`/`plugins`-specific
  failure (bad migration, corrupt page) reaches this branch; this blocks
  on a local-store failure, not the network, so it doesn't conflict with
  the offline-first rule. Blast radius when it does fire is total, which
  is exactly why finding #1 (logging) matters.
- **Recurring bug classes** (missing `os.MkdirAll`, cwd-relative path vs.
  `paths.Data(...)`): N/A — zero file I/O in this diff, confirmed by grep.
- **Test data / secrets**: clean — fixtures are `Apple`, `Plain Item`,
  `ABC`, `cash`, `R-REFUND-1`; no real client/shop name; no secret-shaped
  literals.
- **Backend-only, confirmed**: `git diff --name-only` touches exactly
  three `.go` files, no template/`web/`/`internal/ui/`/locale file. Both
  asserted i18n keys (`refund.error.provider_declined`,
  `pos.toast.payment_declined`) already exist in every locale — no new
  key added. UX-guidelines checklist and help-topic-update requirement
  don't apply.
- **Error-leak safety verified, not just asserted**: the refund path
  routes through `common.LogAndLocalizedError` (logs the raw error
  server-side, writes only the translated key to the response); the
  tender path never puts the error in the response at all. Neither
  response can carry the raw driver string.

## Non-blocker — not fixed here, filed as a follow-up (ut-docs#2280)

- **`internal/plugins/wasm_runtime.go:266`**: the plugin-load loop has the
  identical `err != nil || len(events) == 0` shape one layer earlier, on
  `ListPluginHookEvents`, and doesn't even log the error. A DB error there
  leaves an active, loaded payment plugin with zero registered
  subscriptions, so `bus.HasSubscribers(...)` correctly (from its own
  narrow view) reports false and this fix's own gate returns `(nil, nil)`
  — the identical fail-open reopened through a different door, invisible
  to either new test (they drop `plugin_entries`, not `plugin_hooks`).
  Real, but a different function in a different package, one layer
  earlier in the load path — deserves its own review rather than being
  folded into an already-scoped PR. Filed as ut-docs#2280.

## Verified beyond automated tests

- Grepped the diff and both touched files for `os.`/`filepath`/`ioutil`/
  `exec`/`MkdirAll`/`paths.Data`/`WriteFile`/`Create` — no hits; neither
  of this pipeline's two recurring bug classes applies.
- No money values reinterpreted, no new i18n keys, no plugin-loading path
  touched (aside from the documented, deliberately-out-of-scope
  `wasm_runtime.go` follow-up above), no UI surface.
- `guard-commit-attribution.sh` fails locally only because it reads
  base/head SHAs from CI environment variables that aren't set outside
  Actions — not a real finding; the commit author is a real, correctly
  configured identity.

## Explicitly deferred (not this card)

- The `wasm_runtime.go` follow-up above, filed as ut-docs#2280.

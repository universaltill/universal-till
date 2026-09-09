# Code review: OKC empty-receipt guard moved to one choke point (ut-docs#1780)

## What shipped

`plugins/tax-tr/okc`'s "no usable receipt_no → refuse" invariant
(ut-docs#1763) previously lived only inside `BridgeDriver.Sale` and
`BridgeDriver.Refund`, duplicated between the two methods. Every
`okc.Driver` implementation now gets the check for free instead:

- `NewDriver` (`drivers.go`) wraps whatever driver it constructs with the
  new `NewValidatingDriver`, before returning it.
- `validating_driver.go` adds `validatingDriver`, a `Driver` decorator
  whose `Sale`/`Refund` convert a nil-error answer with a blank
  `ReceiptNo` into `ErrNoReceipt` via a shared `requireReceipt` helper.
  `Status` is forwarded unchanged through interface embedding.
- `bridge.go`'s own internal checks were deliberately left in place
  (the card's acceptance criteria explicitly allow this) — for the
  `bridge` driver the invariant is now checked twice, which is
  intentional defense in depth, not an oversight.

## Why this design

`main.go` (the plugin's wasip1 entrypoint, which calls `okc.NewDriver`
then `approve()`) carries a `//go:build wasip1` tag and cannot be unit-
tested with plain `go test` on the host — the package doc in
`protocol.go` says as much. Enforcing the invariant inside `NewDriver`
instead keeps the fix in the testable, build-tag-free `okc` package while
still covering the real production call site: `main.go:174` is the sole
caller of `NewDriver`, so every driver constructed there — today's
`bridge` and the four still-unimplemented maker scaffolds alike — passes
through the same seam. A future maker driver that forgets to check for a
blank receipt itself is still caught here.

## Independent review

Reviewed by a fresh-context Sonnet subagent (complexity:easy →
Sonnet-reviews-Sonnet per the model-routing table), in an isolated git
worktree, with no visibility into the implementer's own reasoning.

**Verdict: safe to merge.**

The reviewer did its own TDD sanity check: temporarily reverted
`validating_driver.go` to a no-op pass-through and confirmed
`TestValidatingDriver_RefusesBlankReceiptEvenIfDriverDidnt` and
`TestValidatingDriver_RefusesWhitespaceOnlyReceipt` fail clearly
(`err = <nil>, want ErrNoReceipt`) against the broken version, then
restored the fix and confirmed the full suite (22/22) passes again. It
also confirmed `TestNewDriver_Bridge_RefusesBlankReceipt` — the
end-to-end test through the real `NewDriver("bridge", ...)` call site —
still passes even with the wrapper broken, because `BridgeDriver`'s own
pre-existing check independently catches it for that one driver; this
confirms the fakeDriver-based unit tests are the ones actually proving
the new choke point exists, and that they are not tautological.

Findings, all non-blocking:

1. **[nitpick, fixed]** `Sale`/`Refund` on `validatingDriver` were
   structurally identical. Collapsed into a shared `requireReceipt(ev,
   err)` helper.
2. **[nitpick, accepted as-is]** `NewValidatingDriver` has no nil-driver
   guard — not reachable from `NewDriver` today (every switch branch
   assigns a non-nil concrete driver before wrapping) and low risk since
   the constructor's only other caller is the test package.
3. **[informational, accepted as-is]** `TestNewDriver_WrapsEveryDriverWithValidation`
   overlaps existing coverage in `bridge_test.go`'s `TestNewDriver`, but
   adds a genuine integration-level check (the real `NewDriver` wiring,
   not the decorator in isolation) — kept.

Also confirmed: no real client/shop name or secret-shaped literal
anywhere in the diff; `NewDriver`'s unknown-driver error path is
unchanged (verified by the pre-existing `TestNewDriver` test); no data
races (`go test -race`).

## Verified beyond automated tests

- `go build ./...` and `go vet ./...` for the whole repo: clean.
- `go test ./plugins/tax-tr/... -race -v`: 22/22 pass.
- `bash scripts/ci/guard-data-access.sh` and `bash scripts/ci/guard-i18n.sh`:
  both pass (neither guard is actually implicated — no SQL, no
  user-facing strings — but run anyway per the gate).
- No UI surface touched (backend fiscal-device driver logic only), so no
  screenshot/visual check applies.

## Explicitly deferred / out of scope

- Implementing any real maker driver (`gmp3`, `hugin-pclink`, `pavo-rest`,
  `token-x`) — separate, already-tracked work
  (`docs/arch/turkey-launch-playbook.md`).
- No change to `main.go` itself; it inherits the fix automatically
  through `NewDriver`.

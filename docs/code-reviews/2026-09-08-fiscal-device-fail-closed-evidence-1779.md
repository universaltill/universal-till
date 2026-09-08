# 2026-09-08 — Core fail-closed check for missing fiscal-device evidence (ut-docs#1779)

## What shipped

`internal/pages/pos_api.go`'s `completeTender` gets an independent,
core-side backstop for Turkey's fiscal-device payment method
(`fiscal.MethodKeyOKC`, manifest key `"okc"`): if a plugin's blocking
`payment.okc.authorize` answer is "approved" but carries no valid
`fiscal_device` receipt object, the sale is now refused (no sale row
created) instead of completing with zero fiscal evidence.

Before this change, `pickDeviceEvidence` only ever *recorded* evidence
when a plugin provided it — nothing enforced that an OKC leg actually
produced any. `fiscal.MethodKeyOKC`'s own doc comment claims "fail-closed
by construction, no override path", but that guarantee lived entirely in
plugin code (ut-docs#1763 hardened the reference `bridge` driver against
exactly this); any other/future OKC plugin had no core-side backstop.

- New error type `fiscalDeviceNoReceiptError` (`internal/pages/pos_api.go`),
  distinct from the existing `paymentDeclinedError`: the device plugin may
  already have taken the customer's money before answering, so the
  operator is told to check the device rather than "declined, try another
  method" (which risks a double charge).
- Both tender surfaces that route through `completeTender` handle it:
  the cashier tender handler (`pos_api.go`, new
  `pos.toast.fiscal_device_no_receipt` key, HTTP 402, no sale row) and
  the self-order kiosk (`self_order_shop.go`, new
  `selforder.checkout.fiscal_device_no_receipt` key, HTTP 409 — points an
  anonymous customer to the counter rather than inviting a self-service
  retry).
- New locale keys added to all four shipped locales
  (`web/locales/{en,ar,fa,tr}.json`).
- `web/help/{en,ar,fa,tr}/fiscal-device.md` updated with a new "Good to
  know" bullet describing the refusal and warning against a double charge
  — no screenshot regeneration needed, no page layout changed.
- Regression tests in `internal/pages/pos_api_test.go`:
  `TestTenderHandler_OKCPluginApprovesWithNoEvidence_SaleRefused` (single
  leg), `TestTenderHandler_SplitTender_EarlierLegEvidenceCannotCoverMissingOKCReceipt`,
  `TestTenderHandler_SplitTender_SecondOKCLegWithNoEvidenceRefused`.

## Independent review

Opus, fresh context, isolated worktree (`Agent(isolation: "worktree")`).
First pass verdict: **NOT SAFE TO MERGE AS-IS** — found a real BLOCKER.

**Finding (BLOCKER, fixed):** the original check gated on the sale-wide
`deviceEvidence` accumulator (`!deviceEvidence.Valid()`), not on the
current leg's own parsed response. `pickDeviceEvidence` keeps first-wins
evidence from **any** payment method's response (plugin-controlled JSON,
parsed unconditionally, no `MethodID` check of its own) — so the gate
could be satisfied by evidence the OKC leg itself never produced. The
reviewer empirically confirmed two live bypasses against the first draft:
1. A non-OKC leg (e.g. `demopay`) preceding an OKC leg, with the non-OKC
   plugin's answer carrying a bogus `fiscal_device` object — the OKC leg's
   own empty response was "covered" by the earlier leg's evidence.
2. Two OKC legs in a split tender, first valid, second empty —
   `pickDeviceEvidence`'s first-wins rule let the second leg's missing
   receipt slide through on the first leg's evidence.

Both bypasses defeat this card's entire point (core must not depend on a
well-behaved plugin), and both are now closed: the gate parses `resp`
itself, per leg, independent of the accumulator
(`fiscal.ParseDeviceEvidence(resp)` checked before `pickDeviceEvidence`
updates the accumulator). Both scenarios are now permanent regression
tests (see above) — re-verified via revert-then-restore against the
*fixed* code, both genuinely red without the leg-level fix (HTTP 200, one
sale row) and green with it.

**Finding (MEDIUM, fixed):** the original draft reused the generic
`paymentDeclinedError` → "Payment declined — try again or choose another
method" toast. The reviewer flagged this as actively misleading: an OKC
plugin answering "approved" with no receipt may already have taken the
money, and "choose another method" invites a double charge that ut-docs#1762's
idempotency key does not protect against (it only dedupes an identical
retry of the *same* leg). Fixed with the new `fiscalDeviceNoReceiptError`
type and distinct copy on both tender surfaces (see above).

**Finding (LOW, accepted as-is):** the log line's `%q` formatting of
`p.MethodID` is redundant on this branch (always `"okc"`) and the caller
at the HTTP-handler level double-logs the same rejection. Not fixed —
cosmetic, doesn't affect correctness, and duplicate logging of a refusal
is consistent with how `fiscalNeverConfiguredError`/`fiscalTSEFailingError`
are already handled in the same file.

**Finding (INFORMATIONAL, deferred, agreed in scope):** the refund path
(`internal/pages/refund_page.go:912`,
`pickDeviceEvidence(nil, refundResp)`) has the identical gap — an OKC
plugin approving a refund with no *iade fişi* evidence completes the
return with zero device evidence. Agreed with the reviewer this is
legitimately out of scope for ut-docs#1779 (its acceptance criteria are
scoped to the TR sale/tender path; the refund flow has its own error
surface and fail-closed semantics to reason about separately). **Filed as
a new Backlog card** rather than widening this PR.

## Verified beyond automated tests

- TDD genuinely red→green, independently re-verified by both Dev and the
  reviewer: reverted the fix, confirmed the exact pre-fix failure (HTTP
  200, a real sale row created), restored, confirmed green — done twice,
  once for the single-leg case and once for the leg-level-gating fix
  itself (both split-tender bypass tests reproduce red against the
  accumulator-gated version, green against the fixed version).
- `go build ./...`, `go vet ./...`, `gofmt -l` (both changed files, clean).
- `golangci-lint run ./internal/pages/...` — 0 issues.
- Full `internal/pages` package test suite (183 tests-worth of coverage,
  no regressions) — green, twice (before and after the blocker fix).
- Full repo test suite (`go test ./... -race`, all packages except
  `internal/pages`) — green; `internal/pages` itself confirmed green
  without `-race` (see note below) and the specific new/changed tests
  individually confirmed green under `-race`.
- CI-blocking guards run directly: `guard-data-access.sh`,
  `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
  `guard-page-http-error.sh`, `guard-i18n.sh`, `guard-compliance-claims.sh`,
  `guard-help-topics.sh` — all green.
- All 4 shipped locale JSON files validated as parseable
  (`python3 -m json.tool`).

**Known pre-existing environment characteristic, not caused by this
diff:** the full `internal/pages` package under `go test -race` exceeds
even a 30-minute timeout in this sandbox — traced to wazero's WASM JIT
compilation (used by several unrelated fiscal-sign-hook tests) being
dramatically slower under race instrumentation when many such tests run
in the same package invocation. Confirmed pre-existing and unrelated to
this diff: (a) the specific test named in the timeout's goroutine dump
(`TestRefundFiscalSignAsk_UnreachableDeclaredProceedsAndDeclares`) passes
in 14s under `-race` in isolation, on a checkout with this diff stashed
out; (b) this diff touches no WASM/plugin-runtime code; (c) the full
package passes cleanly without `-race` in ~184s, including every new
test. Not a regression introduced here.

## Safe-to-merge verdict

**Safe to merge**, after the blocker fix above. Reviewer's first-pass
"NOT SAFE" verdict was correct and load-bearing — the original draft
would have shipped a compliance control with two working bypasses.

## Explicitly deferred

- Refund-path mirror of this same gap (`refund_page.go:912`) — new
  Backlog card, not part of this PR.
- Log-line redundancy (LOW) — cosmetic, not fixed.

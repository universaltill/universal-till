# Review: refuse `cannot-sign` on a refund/return when no money has moved (ut-docs#3408, ADR-0146)

- **Date:** 2026-10-03
- **Lane:** `lane:cloud-54` (built on Opus 5.5; reviewed independently on Fable, fresh context)
- **Branch:** `fix/3408-refund-cannot-sign-refused`

## What shipped

- `CreateReturn` (`internal/pages/inventory_api.go`): if `fiscal.sign.ask` answers
  `cannot-sign`, the handler returns 409 with `refund.error.fiscal_cannot_sign`. No
  `CompleteSale` runs, so there is no return row, no restock and no audit marker. These
  returns are always paid in cash with no provider webhook, so no money has moved yet.
- `POST /api/refund` (`internal/pages/refund_page.go`): the provider call switches to
  `blockingPaymentEventDispatch` to get `delivered`. A `cannot-sign` answer with
  `delivered=false` (cash or a hookless method) is refused the same way. A refund whose
  `payment.<key>.refund` hook already ran keeps proceed-and-declare (ADR-0146 Decision 3; the
  decision is on ut-docs#3556).
- New key `refund.error.fiscal_cannot_sign` in en/ar/fa/tr. The de/es/pt language packs
  follow in their own PRs.
- Manual: the "When a sale can't be signed at all" section in `web/help/{en,de,tr,fa,ar}/sell.md`
  now describes the refund/return rule.
- Tests: new `refund_cannot_sign_test.go` covers four cases: a cash refund is refused, a
  hookless refund is refused, an unreachable signer on a cash refund still declares, and an
  inventory return is refused with stock unchanged. The old cash-refund declare test is
  repointed to a provider-backed `demopay` refund
  (`TestFiscalSignAsk_CannotSignOnProviderRefundStillDeclaresWithDifferentWording`) and
  asserts that the provider refund ran once.

## Review findings (Fable)

Verdict: **safe to merge**. No blockers or majors.

| Severity | Finding | Outcome |
|---|---|---|
| minor | Three doc comments in `fiscal_sign_hook.go` still said refund/return always proceed-and-declare `cannot-sign`. | Fixed |
| minor | The de/es/pt packs lack the new key. | Follow-up pack PRs, same cycle |
| nit | The `fiscal.sign.start` comment in `refund_page.go` called the webhook the only later step that can refuse. | Fixed |
| nit | The body of ADR-0136 Decision 0 still read as current. | An inline amended-by note was added in ut-docs |
| nit | The tests match an English copy fragment. | Accepted: same pattern as the existing tests |

The reviewer checked that `delivered=false` is a sound proxy for "no money moved". Every
path that does not publish returns false. A publish error becomes a 402 before the sign-ask.
A `ListPaymentEntries` error fails closed. It also confirmed there are no side effects before
the refusal beyond the pre-existing `EnsurePaymentMethod`, the PIN elevation and
`fiscal.sign.start`; ADR-0146 D1 accepts the orphaned start. Outage outcomes are distinct
constants and still declare. A Turkish ÖKC refund is `delivered=true`, so it is unchanged.

## Verified

- Red first: before the handler change, all three refusal tests failed with a 200. The
  reviewer repeated this with the handlers reverted, and the same three failed.
- `gofmt -l .` is clean, `go build ./...` passes, `go test ./...` passes, and
  `golangci-lint run ./...` reports 0 issues.
- Guards pass: i18n, help-drift, help-topics, compliance-claims, competitor-naming,
  core-neutral, data-access, page-http-error, kiosk-engine, no-showmodal and
  no-inline-handlers.
- `guard-deadcode-baseline` flags `logging.Stderr` locally. Its only callers are in
  `cmd/unitill-desktop`, which the local run skips because there are no GTK headers. CI
  analyses that root, and this change doesn't touch that code.
- `go test -race ./internal/pages` hit the 600s default timeout in this container. CI runs
  this package without `-race` under `-timeout 20m`.
- Both UI surfaces already render a 409 body: `refund.html` uses `htmx:responseError`, the
  same path as the ADR-0048 gate 409s, and `inventory.html` renders into `#return-result`.
  No layout change.

## Deferred

- ut-docs#3556 (Admin Review): what to do with a provider-backed refund that the signer
  answers `cannot-sign`.

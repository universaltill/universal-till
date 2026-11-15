# Review: SDK channel order record matches the contract (ut-docs#4767)

**Change:** `sdk/plugin/channel/types.go`, `channel_test.go`.

## What shipped

- `channel.Order` / `channel.OrderLine` now carry exactly the submitted
  `order` record of sales-channel-v1 §2 (`order_number`, `payment_state`,
  `shipping_tax_bp`, `shipping_tax_included`, `order_discount_minor`,
  `tax_added_minor`, `refund_total_minor`; lines `name`, `tax_included`,
  `discount_line_minor` — was `discount_minor` — `refunded_quantity`,
  `restocked_quantity`). Tags mirror ut-cloud `channelorder` `wireOrder` /
  `wireLine` and the demo/Shopify adapters' own records.
- Required numbers/bools lost `omitempty`: before, a 0 tax rate, 0 discount,
  0 shipping or `tax_included:false` was dropped and the cloud intake
  rejected the order. Basis points stay `int64`.
- `OrderUpdate` and `Refund` are aliases of `Order` (§9.1: one full-order
  shape for all three kinds); the old delta shapes were refused by the cloud.
- The personal-data tag test exempts only `name` on `Order.Lines` (mirrors the
  host gate, §9.3) and now covers the host's `mobile` and `postal_code` too.

## Tests

`TestOrderZeroValuesKeepEveryRequiredKey`, `TestOrderContractExampleRoundTrips`
(contract §2 example, `DisallowUnknownFields`),
`TestOrderUpdateAndRefundAreTheOrderShape`. TDD re-verified by the reviewer in
a separate worktree: with the old `types.go` all three fail on assertions
(14 missing keys; `unknown field "order_number"`; type identity), pass with
the new one.

## Review (independent, different model from the author)

| sev | finding | outcome |
|---|---|---|
| low | personal-key list in the test narrower than the host's (`mobile`, `postal_code`) — pre-existing | fixed |
| nit | comment said a dropped zero is always `invalid_order`; also `bad_amount`/`bad_tax_rate`; `CloudID` is optional too | fixed |
| nit | adapters' "the SDK's channel.Order is thinner" comments now stale | follow-up card (adapter repos) |

Checked and accepted: `Identity.ExternalVersion` keeps `omitempty` (the cloud
treats missing and `""` alike); breaking field changes have no users in
universal-till, FakeHost or either adapter; 250-byte title guidance satisfies
both the intake (250) and the host gate (256).

## Gates

`sdk/plugin`: gofmt, vet, `go test -count=1 ./...`, wasip1 vet,
golangci-lint 0 issues. Root: gofmt, `go build ./...`; CI `build` guards all
pass except two environment-only failures in this container
(`guard-deadcode-baseline` — deadcode built with go1.26 vs go1.27 module;
`guard-shellcheck-version` — no shellcheck binary); unrelated to this diff,
CI runs them.

**Verdict:** safe to merge.

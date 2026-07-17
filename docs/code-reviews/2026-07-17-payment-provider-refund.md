# Code review — payment-provider refund leg (2026-07-17)

Branch `feat/payment-provider-refund`. Task 9 (payments B1–B3). Investigation
first: B1/B3 largely EXISTED — payment plugins' entries already sync into
payment_methods (tender buttons per provider) and get a blocking
`payment.<key>.authorize` + async `.requested`. The real gaps: the REFUND leg
(a card refund recorded a return but never moved money back at the provider),
and no documented contract.

## What changed (till)
- `blockingPaymentEvent` helper (refund_page.go): publishes
  `payment.<key>.<suffix>` for the method's owning plugin, blocking; nil when
  no entry/subscriber (cash unaffected).
- Refund flow gates on `payment.<key>.refund` BEFORE recording the return,
  payload incl. amount (minor), currency, original_sale_id/receipt. Provider
  decline → 402 "provider refund failed", return not recorded.
- `SharedBus` now rebinds its db per call (`EventBus.SetDB`, guarded reads
  via `dbHandle()`): the process singleton kept the FIRST db — closed test
  dbs broke later hook checks; prod (one db) unchanged, and the data race
  on eb.db is fixed.

## Cross-repo (same task)
- **Stripe plugin 1.2.0**: event-type dispatch (was authorize-only);
  `.requested` handler links sale_id→PaymentIntent in plugin storage;
  `.refund` posts /v1/refunds (partial via amount) and declines on unknown
  charge. PROVEN E2E against the REAL Stripe test API via the wasm harness
  (authorize pi_…, settle-link, partial refund re_… succeeded, unknown-sale
  declined); throwaway E2E removed (network+secret), offline gate test kept.
- **Marketplace**: ingest summary root-cause fix (separate review doc).
- **Docs**: `reference/payment-provider-contract.md` = the B1 contract
  (entries→buttons, authorize/requested/refund, storage conventions).

## Tests
- `TestBlockingPaymentEventGate` (entry+hook seeded; approve passes, decline
  blocks, no-subscriber and cash pass through).
- Full pages+plugins suites + both CI guards green.

## Known limits (documented in the contract)
- sale→charge linking via `last_txn` assumes authorize/settle of one sale
  aren't interleaved with another stripe sale on the same till — true for a
  single lane; revisit if split-tender with two stripe payments lands.
- Refunds for sales made BEFORE 1.2.0 have no stored PI → plugin declines
  (falls back: refund by another method, e.g. cash).

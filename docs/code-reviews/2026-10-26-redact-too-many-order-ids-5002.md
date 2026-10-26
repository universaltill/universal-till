# Review: customer_redact over 10,000 orders is applied and recorded, not lost (ut-docs#5002)

Date: 2026-10-26 · Lane: lane:cloud-24 · Built by: Sonnet 5.5 subagents · Reviewed by: Fable 5.1 (independent, fresh context), orchestrated by Opus 5.5

## What shipped (one change across five repos)
- **universal-till** `sdk/plugin/channel`: `WebhookResult.OrderIDsTruncated` (`order_ids_truncated,omitempty`).
- **ut-plugin-channel-shopify** / **ut-plugin-channel-demo**: a `customers/redact` naming more than 10,000 distinct orders is no longer refused. The adapter still validates every id (a malformed id anywhere still refuses the webhook), keeps the first 10,000 distinct ids in payload order and sets the flag. Before this, the host 503'd on every retry until the channel gave up, and the customer-id half of the erase was lost.
- **ut-cloud**:
  - `channelhost.ParseWebhookAnswer` accepts the flag on `customer_redact` only and drops it with `order_ids` on every other event. The host still refuses more than 10,000 ids.
  - `ChannelInboundEvent.order_ids_truncated` is a new bool (default false, kept after processing, holds no personal data).
  - A truncated redact erases what it names, ends `processed` with `too_many_order_ids`, writes one Error log line (event and connection ids only) and an audit row with `orders_truncated: true`.
- **ut-docs** `reference/contracts/sales-channel-v1.md` §4 and §11: additive within v1. ut-cloud `docs/api-reference.md` is updated to match.

## Findings
1. Minor: the requirement that the flag survives a failed attempt had no test. **Fixed** with `TestTruncatedCustomerRedactKeepsTheShortfallAcrossARetry`: the erase fails on the first attempt and the retry still ends `too_many_order_ids`.
2. Nit: "logged once" is at-least-once if saving the state fails after the work ran. This is the same semantics every event already has, the erase is idempotent, and the doc's "once" holds on the normal path. **Accepted.**
3. Nit: the Shopify adapter validates ids past the cap without adding them to `seen`. Harmless because the flag is already set. **Accepted**; noted so nobody "fixes" it by skipping validation.

The reviewer also checked these and found them correct:
- dedupe order, and a duplicate past the cap does not set the flag;
- a bad id past the cap is still refused;
- the flag never survives a non-redact event;
- the dedupe-row interaction;
- ent auto-migrate (`ADD COLUMN … NOT NULL DEFAULT false`);
- no transaction or txguard impact;
- no PII in logs or audit;
- the docs match the code.

## Verified beyond the automated tests
- I re-ran the TDD claims myself. With each fix reverted, the new tests failed: the Shopify and demo `TestCustomerRedactOrderLimit` failed with "names more orders than the channel allows", the ut-cloud tests failed to build because `reasonTooManyOrderIDs` did not exist, and the SDK round-trip test failed because the field did not exist. With the fixes restored, all passed.
- Gates: ut-cloud `scripts/ci/verify.sh` passed. In the adapters, these passed: gofmt, `go vet` (native and wasip1), build, `go test ./...`, `validate.sh`, `package.sh` and the version-bump self-test. In universal-till, `sdk/plugin` gofmt, vet and tests passed.
- Rollout order: SDK → ut-cloud deploy → plugin releases, which go through marketplace approval. An older host refuses only truncated redacts, and those were already being lost, so there is no new regression window.

## Verdict
Safe to merge.

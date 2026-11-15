# Review: customers/redact flags the orders it names (ut-docs#4929)

**Date:** 2026-10-25 · **Lane:** lane:cloud-41 · **Built by:** Sonnet 5.5 (dev subagents), Opus 5.5 (architect brief, SDK field, review fixes) · **Reviewed by:** Fable 5.1 (independent, fresh context; hard card touching GDPR erasure)

## What shipped

Since Shopify adapter v0.2.2 dropped `read_customers`, channel orders carry no customer id. So a `customers/redact` matched nothing, and my. still offered "Show contact" (a live fetch) for an erased customer's order. The Shopify adapter did not read the GDPR body at all, so the redact even arrived with no ids.

- **universal-till SDK** `sdk/plugin/channel`: `WebhookResult.OrderIDs` (`order_ids`, customer_redact only), with a round-trip test.
- **ut-cloud:**
  - `channelhost.ParseWebhookAnswer` accepts `order_ids`: dropped unread on any other event, at most 10,000, each 1-256 bytes.
  - `ChannelInboundEvent.order_ids` is stored only for a received customer_redact and cleared on every terminal path.
  - `channelorder.EraseCustomers(customerIDs, orderIDs)` marks the connection's orders redacted by customer id and by `external_order_id`. It works in chunks of 500, in one transaction on the tx client, scoped to tenant and connection.
  - The audit holds counts only (`customers`, `orders`).
  - `docs/api-reference.md` is updated.
- **ut-plugin-channel-shopify 0.4.0:**
  - `customers/redact` decodes only `customer.id` and `orders_to_redact`, matched on raw JSON text against `^[1-9][0-9]{0,19}$`, so big ids stay exact.
  - They become `gid://shopify/Customer/N` and `gid://shopify/Order/N`, deduplicated.
  - More than 10,000 orders is an error, never a truncation.
  - Email and phone are never decoded. The other GDPR bodies are still not read.
- **ut-plugin-channel-demo 0.2.0:** the same for its string ids (`okID`), plus a `fakeshop.RedactBody` helper.
- **ut-docs:** sales-channel contract v1 §4 (webhook output) and §11 (`customer_redact` row). The field is additive within v1 (§7).

## Findings (Fable)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major (process) | The plugin branches pin an SDK without `OrderIDs`. The cloud's `DisallowUnknownFields` makes a new plugin on an old cloud 503 every redact, which is lost after Shopify's ~48 h of retries. | Enforced order: SDK merge → ut-cloud merge and deploy → plugin SDK bump → plugin merge. The marketplace approval of the plugin releases waits for ut-cloud to be live (noted on the card for the owner). |
| 2 | minor | More than 10,000 orders: the adapter refuses → a silent 503 loop, with nothing recorded or audited. | Deferred: ut-docs#5002. |
| 3 | minor | The host validated `order_ids` before dropping them on other events, so one malformed id on `order_changed` would 503 every order webhook (the contract says "dropped"). | **Fixed:** they are dropped before validation. Regression test `TestParseWebhookAnswerOrderIDs` ("stray malformed order id") fails on the old order. |
| 4 | minor | An order stored after its customer's redact is unflagged (reachable: Shopify redacts customers with no order for 6 months). | Deferred to the existing tombstone card ut-docs#4895, with a comment adding order ids. |
| 5 | nit | Shopify `"customer":{"id":null}` was refused (503 loop) instead of treated as absent. | **Fixed**, and the test fails without the fix. |
| 6 | nit | `record` re-checks the event after the parse already dropped the field. | Kept (defence in depth). |
| 7 | nit | Retention of order ids on the event row. | Fine: cleared at the terminal update; the 400-day dedupe row keeps only the body hash. |

The reviewer also confirmed:
- tenant scoping (with a cross-tenant test);
- that the txguard window ends at commit, and the post-commit read is outside it;
- that order_ids never leak on duplicate or skipped events;
- that the Shopify order `ExternalID` is the GID, so the ids match;
- that 10,000 GIDs (~400 KB) fit the 2 MiB body and 4 MiB stdout caps;
- that the tests are not vacuous.

## Verified beyond the automated tests

- **TDD claims re-verified by the orchestrator in a separate worktree:**
  - With the order-id loop disabled, `TestEraseCustomersFlagsOrdersByExternalOrderIDEvenWithoutACustomerId` fails ("a named order without a customer id was not flagged") and `TestEraseCustomersFlagsOrderIDsInChunks` fails ("flagged 0 of 1202").
  - Both pass with the loop restored.
- Fix #3: the test fails with `bad order id` on the old order. Fix #5: the test fails with "body is not a customers/redact webhook".
- ut-cloud `scripts/ci/verify.sh` is green. The plugins pass `go test`, `golangci-lint`, `validate.sh`, `package.sh` and `check-version-bump.test.sh`. Until the SDK is merged, the plugins were tested against the SDK working tree with a temporary `replace`. The replace was dropped, and the real SDK bump follows the merge.
- No UI surface changed. No help topic is affected (the my. contact view already honours `customer_redacted`).

## Verdict

Safe to merge in the order above.

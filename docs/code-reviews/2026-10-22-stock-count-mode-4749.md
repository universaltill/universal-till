# Review: adjust_stock `count` mode (ut-docs#4749, ADR-0168 §4)

Scope: `internal/data/stock_count_repo.go`, `internal/pages/cloudsync_wire.go`
(`resolveStockSlot`, `cloudCountStock`), `internal/cloudsync` (`applyCountStock`,
`CountStockRequest`, `server_time` on the sync response). Companion: ut-cloud PR
(payload rules, `server_time`, version-gate helper).

## What changed
- `adjust_stock` with `mode: "count"` (`target`, `counted_at`) sets an item or
  variant to the counted quantity and keeps every movement recorded since:
  movement = `target + net_movement_since(counted_at) - current`, read and
  written in one transaction (the till's connection is `_txlock=immediate`).
- The movement type is `count`; movements of that type are left out of the
  net, so the same count applied again moves nothing, also after later sales.
- Weighed items take up to three decimals, every other item a whole number;
  a refusal writes nothing. Variant rules, location rules and the
  ADR-0043 refusal are shared with the delta mode (`resolveStockSlot`).
- `counted_at` is the cloud's clock. The sync response carries `server_time`;
  the offset to the till's clock is measured when the response arrives and
  applied to `counted_at`; an offset above five minutes is named in the result.
- Reason defaults to `import`.
- A payload with no `mode` (or `delta`) behaves exactly as before; an unknown
  or non-text mode is refused. A till without the hook refuses a count.

## Finding vs the ADR text
ADR-0168 §4 and the card write the movement as `target - net_movement_since -
current`. With a sale of 2 after a count of 10 (current 8, movement -2) that
gives +4, which re-adds the sale. The behaviour the ADR and the acceptance
criteria ask for ("a sale made after counted_at is preserved") is
`target + net_movement_since - current` (= 0 here). Implemented that; the
test pins it. The ADR sentence should be corrected in a follow-up.

## Independent review (opus subagent) and triage
- Fixed: the clock offset was measured at apply time, so a directive that
  waited behind others looked skewed and a retry could flip a boundary; it is
  now measured at receipt (`directive.ReceivedAt`).
- Fixed: the payload `reason` was dropped; a non-text `mode` was read as a
  delta on the till; the plugin event carried the "adjustment" reason.
- Kept as is: cloud `StockCountTillVersionError` has no caller yet (card G, the
  stock path, queues the directive); `CountStockMinTillVersion` is the next
  patch release and must be confirmed when the till is tagged.
- Known limit: a movement is stamped when it is recorded, so a sale ingested
  late (offline, replicated) whose real time was before the count is counted
  as after it.
- Not done here: the `opening` mode of #4530 (blocked on its own cards); this
  card only adds `count`. Both modes share the `mode` field.

## Verified personally
Mutations: dropping the clock conversion fails
`TestCloudCountStock_VariantAndClockConversion`; dropping the `count`-type
exclusion fails the keep-sales and idempotency tests.

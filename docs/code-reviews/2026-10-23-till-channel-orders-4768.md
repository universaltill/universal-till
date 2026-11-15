# Review record: till channel orders (ut-docs#4768, slice C)

Branch `feat/4768-till-channel-orders`. ADR-0169 section 9 (accepted in principle by the owner
2026-10-22; text acceptance pending ut-docs#4758). Contract:
`ut-docs/reference/contracts/channel-order-v1.json` and `sales-channel-v1.md` section 5.1
(ut-docs#4851, round 3).

## What this adds

- Migration `090_channel_orders.sql`: `channel_orders` (key `(connection_id, external_order_id)`,
  `revision`), `channel_order_lines` (`qty_applied`, `restocked_applied` = units already moved
  into stock). No customer columns; only `customer_country`. Listed as not-synced in
  `sync_admin_repo.go`. Checksum pinned in `shipped_migrations_test.go`.
- `internal/data/channel_orders_repo.go`: `Apply` (one transaction), `List`, `Get`.
- `internal/cloudsync/channel_order_directive.go`: strict decoder for `channel_order` and
  `channel_order_update`; both are in `mainTillOnlyTypes`; `Hooks.ApplyChannelOrder`.
- `internal/pages/cloud_channel_order.go`: the hook (default location, currency check, Problems).
- `internal/pages/channel_orders_page.go` + two templates: read-only "Online orders" list and
  detail, gated on the `reports` action, launcher tile (not the rail), `?` link, help topic in
  en/de/tr/ar/fa, strings in en/tr/ar/fa, `en.meta.json` metadata, logical CSS.

## Field mapping (payload -> till)

| payload | stored / used |
|---|---|
| envelope `id` | identity and dedupe (`directive_results`, generic path); `last_directive_id` |
| `schema_version` (const 1) | required; any other value fails the directive visibly |
| `connection_id`, `external_order_id` | order key |
| `revision` | only ordering key: higher applied, equal/lower ignored (`Duplicate`) |
| `external_version`, `order_number`, `plugin_key` | display / support |
| `placed_at` (Z only), `currency`, `status`, `payment_state` | columns; status mirrors the cloud but never regresses (`nextChannelStatus`) |
| `shipping_minor`, `shipping_tax_bp`, `shipping_tax_included`, `order_discount_minor`, `tax_added_minor`, `refund_total_minor`, `total_minor` | `money.Money` at the boundary, integer minor columns |
| `customer_country` | the only customer datum |
| line `external_id`, `sku`, `name`, `qty`, `unit_price_minor`, `tax_bp`, `tax_included`, `discount_line_minor` | line columns |
| line `cloud_item_id`, `cloud_variant_id` | slot match (variant, then item, then SKU) |
| line `refunded_quantity`, `restocked_quantity` | stock: `-(qty - qty_applied) + (restocked - restocked_applied)` |

Stock: `channel_sale` (out) and `channel_return` (back) movements through
`POSRepo.RecordStockMovement` in the order's transaction. The slot is sold from the location
holding most of it, else the shop's first location. Negative stock is applied and raised as a
Problem, never refused. An unmatched line (no item, or an item with variants and no variant) is
stored `unmatched`, moves nothing, raises a Problem and is matched again on the next revision.

## Security and data

- Payload is untrusted: unknown fields refused at both levels (so no name, e-mail, phone or
  address can ride in), 64 KiB / 500 line caps, whole non-negative integers only, tax at most
  10000 bp, refusal text never echoes a value. A schema test compares the decoder's field sets
  with the contract schema.
- No network grant, no new credential, no raw SQL outside `internal/data` / migrations.
- Exposes: a manager-only read page of order totals and country. Cashier gets 403 and no tile.

## Not a till sale (owner question 6)

`TestCloudApplyChannelOrder_LeavesZReportDrawerAndFiscalJournalUnchanged` snapshots the Z report
(`EndOfDayInstant`), the sales journal and the row count of every table; only the channel tables,
`stock_movements`, `inventory` and `audit_log` may move.

## Known limits / decisions to confirm

- Directive status transitions initiated from the till (accept/pick/pack/ship): payload not
  defined, so the page is read-only; marked TODO in `channel_order_detail.html`.
- The default location for a slot with no stock row is the first stock location.
- `total_minor` that disagrees with the lines is recorded and raised as a Problem, not refused
  (stock really left at the channel).
- The "Online channels" report section is part 2.
- Stock-level snapshot to the cloud follows the existing hash-gated push after the next tick.
- Problems are plain English log lines like every other Problem (they are not translated), so the
  designed keys `problem.channel_sale_negative_stock`, `problem.channel_order_unmatched_sku` and
  `stock.movement.channel_sale` were not created (no surface renders them).

## Reviews and outcomes (tier 1)

Two passes: the Opus code review and the Tester/UX pass. Every tier-1 finding
was fixed test-first (each test was run and seen to fail before the fix).

| ID | Finding | Outcome |
|---|---|---|
| H1 | A transient apply error (db busy, no stock location, cancelled ctx) was stored as a final `failed` result | Fixed. `apply` returns `statusRetry` for any hook error; `handleGenericDirective` stores and posts nothing, so the cloud re-delivers. Only decode refusals are final. Tests: `TestTick_ChannelOrderTransientFailureIsRetriedNotStoredFailed`, `TestTick_ChannelOrderDecodeRefusalIsFinal`. Residual: a deterministic hook error would retry every tick (logged each time) |
| H2 | A line that had moved stock was re-matched with an `is_active` check, so a deactivated variant lost its restock and the stored ids could be blanked | Fixed. A line with `qty_applied > 0` keeps its stored item/variant (tracked read from `items`), and `UPDATE` uses `COALESCE` so ids are never overwritten with NULL. Test: `TestChannelOrders_DeactivatedVariantStillGetsItsRestock` |
| M3 | Duplicate line `external_id` accepted | Fixed in the decoder (`duplicate line external_id`). Test: `TestDecodeChannelOrder_DuplicateLineIDRefused` |
| M4 | The contract-schema test skipped unless an env var or a sibling checkout existed | Fixed. `testdata/channel-order-v1.json` is vendored and the decoder-vs-schema test always runs; `TestChannelOrderVendoredSchemaIsCurrent` diffs the copy against ut-docs when a checkout/`CHANNEL_ORDER_SCHEMA` is present (CI has no ut-docs checkout, so there it skips; drift is caught on every local pipeline session) |
| M5 | Status transitions not exactly per contract; fulfilment could regress after a partial refund | Fixed. `partially_refunded` goes to picked/packed/shipped/refunded/cancelled only (not accepted/new) and never to a step at or below the furthest reached; `fulfilment_rank` stored (migration 090, still unmerged so edited in place, checksum re-pinned). Test: `TestChannelOrders_StatusTransitionsFollowTheContract`, `TestNextChannelStatus` |
| L1 | `bestLocation` could pick an inactive location | Fixed (join on `stock_locations.is_active`). `TestChannelOrders_InactiveLocationIsNeverChosen` |
| L2 | Negative-stock Problem printed an empty SKU | Fixed: names `name (item id)` when the SKU is empty. `TestCloudApplyChannelOrder_ProblemNamesTheItemWhenTheLineHasNoSKU` |
| L3 | No `stock.adjusted` plugin event | Fixed: one event per booked movement, published after commit (reason `sale`/`refund`). `TestCloudApplyChannelOrder_PublishesStockAdjusted` |
| L4 | Catalog snapshot not re-pushed after a channel order | Fixed: `channel_order(_update)` added to `catalogTypes`. `TestChannelOrderTriggersCatalogSnapshot` |
| L5 | `total_mismatch` not persisted | Fixed: column, repo field, shown on the detail page (`channel_orders.detail.total_mismatch`, en/fa/tr/ar + meta). `TestChannelOrders_TotalMismatchPersistedAndNegativeNamesTheItem` |
| L6 | Page test condition/comment unclear | Tidied |
| D1 | Amounts shown in the till's currency, not the order's (JPY, KWD wrong) | Fixed. New `{{ moneyIn amount code }}` template func; `FormatMoneyIn` uses the ISO 4217 exponent for a code the Settings registry lacks (KWD/BHD/OMR 3 decimals, JPY/KRW 0), leaving `CurrencyByCode` untouched. Tests: `TestFormatMoneyIn_UsesTheCodesMinorUnit`, `TestMoneyInTemplateFunc`, `TestChannelOrdersPage_AmountsFollowTheOrdersOwnCurrency` |
| D2 | Detail page overflowed at 360px | Fixed: plugin key wraps (`.channel-order-key`), both tables sit in `.channel-order-scroll`. Hook test only; see "Not checked" |
| D3 | Order-number link under 44px | Fixed (`.channel-order-link`, 44px). The shared `?` help link (27px) is page chrome: filed as a note, not changed here |
| D4 | `currency_mismatch` only set on insert and based on the cloud's cached shop currency | Fixed. Recomputed on every revision from the till's own currency setting (`httpx.ActiveCurrency()`, what sales and receipts use). `TestCloudApplyChannelOrder_CurrencyFlagFollowsTheTillsCurrencySettingOnEveryRevision`, `TestChannelOrders_CurrencyMismatchFollowsEveryRevision` |

## Flags for other cards

- **DE fiscal flag for legal card ut-docs#4775:** the till stores one row per
  order and overwrites it on each revision; there is no immutable revision
  history. Whether a German shop must keep each revision of an online order
  (retention, change traceability) is a legal question for #4775, not decided here.
- **Follow-up:** a Reports "Online channels" section (channel orders are
  deliberately in no Z report, drawer or fiscal journal).
- **Follow-up note:** the shared `?` help link is 27px on every page; a touch
  target fix belongs in the shared chrome, not this card.
- **Lang packs:** the new `channel_orders.detail.total_mismatch` key needs
  the pack PRs (de and the others) after core merges.

## Not checked

- `make docs-shots` not run (Linux job later); the 360px and 1024x600 layout
  was asserted by markup/CSS hooks only, not looked at in a browser, and not on
  real touch hardware.
- `golangci-lint` cannot run locally (go 1.26 vs 1.27.1). On this macOS host
  four CI guard scripts fail for environment reasons only (BSD `sed`/`awk`,
  shellcheck 0.11 vs pinned 0.9): shellcheck-version, store-private (+test),
  pipefail-grep-q_test, competitor-naming_test.

# Review: online order page shows line discounts (ut-docs#4945)

Dev: Sonnet. Reviewer: Opus 5.5 (independent, one round).

- `ChannelOrder.LineDiscountTotal` and `ItemsSubtotal` (money.Money, pure Go).
- `channel_order_detail.html`: "Line discount" column; totals gain "Items"
  (first) and "Line discounts", shown only when a line or order discount exists.
- en.json + en.meta.json: 3 keys. English only. No CSS change.
- Help topics `channel-orders` and `reports` updated; `make docs-shots` run,
  manifest regenerated (3 hashes), PNGs unchanged.
- Tests: `TestChannelOrder_LineDiscountAndItemsTotals`,
  `TestChannelOrderDetail_{ShowsLineDiscounts,OrderDiscountOnly,NoDiscounts}`.
- `guard-deadcode-baseline.sh` passes with `GOTOOLCHAIN=go1.27.1` (Tester).

## Reviewer findings

- Sums checked: `Qty` is whole units (`int64`, contract `qty` integer, `Qty <= 0`
  refused), so `UnitPrice.Scale(Qty)` is right; `discount_line_minor` is for the
  whole line, matching the Shopify adapter (sum of `discountAllocations`, which
  include order-level code shares; it never sets `order_discount_minor`).
  Overflow: the values are bounded by the stored total the contract formula
  checks; display only. No finding.
- Minor, fixed: the "Line discount" column rendered empty on every order with
  no line discount (Tester obs. 2). It now shows only when a line has a
  discount (`$ld` hoisted to the top of the page); `NoDiscounts` and
  `OrderDiscountOnly` assert the column is absent. Help step 2 updated.
- Accepted: at 360px the shared `t-cards` pattern puts the line discount behind
  "More" (Tester obs. 1). Existing component behaviour; the totals block still
  shows "Line discounts", so the order adds up without expanding.
- i18n keys/meta: descriptions, `review: standard`, no placeholders. OK.
- Help prose matches the template and the Shopify adapter. Manifest: the topic
  hash for `en/channel-orders` and the surface hash refreshed by hand
  (`update-docs-shots-surface-hash.sh`); its screenshot is the list page, which
  this change does not touch. Commit needs the `Docs-Shots-Unchanged: true` trailer.

Pre-flight: gofmt -l . (empty), go build ./... ok, go vet ./... ok,
golangci-lint run ./... 0 issues, go test ./... ok (pages/plugins with
-timeout 80m), race -run ChannelOrder ok, all 193 ci.yml build-job guards rc=0
(catimport-version env-only/not applicable), shellcheck 0.11.0 ok (Tester);
re-run after the fix: go test ./internal/data/ -run ChannelOrder ok,
go test ./internal/pages/ -run ChannelOrderDetail ok (3 pass),
guard-docs-shots ok, guard-help-topics ok, guard-i18n ok (Reviewer).

Verdict: approve.

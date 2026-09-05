# Strengthen the zero-marginal-net refund test with a stock assertion (ut-docs#1580)

**Card:** universaltill/ut-docs#1580 — "Strengthen ut-docs#1561's
zero-marginal-net refund regression test with an inventory/stock_movements
assertion" (found during independent review of #1561, filed separately per
that review's recommendation).
**Repo/branch:** universal-till, `fix/1580-refund-inventory-assertion`
**Complexity:** easy (Dev inline at Sonnet, Review via a fresh-context
Sonnet subagent — one round, no defects found).

## The gap

`TestPostRefund_ZeroMarginalNetPartialRefundSucceeds` (ut-docs#1561) drives
3 sequential 1-of-3 partial-refund POSTs against a 3-unit line and asserts
that the summed refunded `total` across `sales` rows where
`sale_type = 'return'` equals exactly the original line's net. It did
**not** assert that the underlying units/stock actually came back — the
actual harm the original card (#1561) described was "only 1 of the 3
units could ever actually come back." A future regression where requests 2
and 3 return HTTP 200 but silently persist no return sale_line and no
stock movement would still have passed this test, since the money-only
check happens to still balance in that failure shape too (confirmed below,
not assumed).

## What shipped

`internal/pages/refund_page_test.go` — extended the test with three
assertions after the existing loop:

- Exactly 3 `sale_lines` rows exist joined through `sales` where
  `sale_type = 'return'` for `item_id = 'itm-1561'` (one per refund
  request).
- `stock_movements` summed `quantity` for `item_id = 'itm-1561' AND type =
  'return'` equals 3.
- `inventory.quantity` for `item_id = 'itm-1561'` equals 3.

No production code changed — this is test-only, hardening an existing
regression guard.

## Independent review — one round, fresh-context Sonnet, no defects

Spawned as a `general-purpose` subagent (`model: sonnet`, fresh context),
reviewing the live diff on the branch. It:

- Traced `item_id` propagation from the original `sale_lines` row through
  `refund_page.go` and `pos.CompleteSale` into the persisted return line
  and stock keys, confirming the `item_id = 'itm-1561'` filter is actually
  correct (not a guess) for this non-variant fixture.
- Independently re-ran the same TDD verification I had already done
  myself — temporarily reducing the test's own loop from 3 iterations to
  1 and confirming the new assertions fail for real ("got 1" instead of
  "got 3"), while the pre-existing money-only assertion still passed even
  with only 1 of 3 real refunds applied. This is the concrete confirmation
  that the new checks catch a regression class the old test would have
  missed — restored afterward, confirmed the diff was back to exactly the
  intended 37 lines.
- Checked `EnsureStockLocation` for idempotency across the 3 sequential
  requests (confirmed: first call creates the default location row,
  subsequent calls find and reuse it — no risk of the inventory row
  fragmenting across multiple locations, which would have broken the
  un-scoped `SELECT quantity FROM inventory WHERE item_id = ...` query).
  Also confirmed `itm-1561` appears nowhere else in the shared test
  fixture set, so the assertion is safe for this specific test.
- Confirmed CLAUDE.md compliance: test-only change, no repository-pattern/
  i18n/money/offline-first/kiosk-isolation surface engaged.
- Re-ran the gate itself: `gofmt -l`, `go build ./...`, `go test
  ./internal/pages/...`, `golangci-lint run ./internal/pages/...` — all
  clean.

No findings. One incidental, non-blocking observation: the shared working
tree had unrelated `web/help/img/**/*.png` modifications at the time of
its review, from this session's separate, concurrent work on a different
card (ut-docs#1592) in the same checkout — noted as working-tree noise to
be aware of, not a defect in this diff (confirmed not part of this
branch's own commit).

## Verified beyond automated tests

- `gofmt -l internal/pages/refund_page_test.go`: clean.
- `go build ./...`: clean.
- `go test ./internal/pages/...`: all pass, including the extended test.
- `go test ./...` (whole repo): all pass.
- `golangci-lint run ./...` (whole repo): 0 issues.
- `bash scripts/ci/guard-data-access.sh`: passes (raw SQL stays inside a
  `_test.go` file, the same convention every other assertion in this file
  already uses).
- TDD re-verified personally (loop-count experiment described above),
  independently re-confirmed by the reviewer via the same experiment.
- No migration, money, i18n, offline-first, or plugin-taxonomy surface
  touched. No real client/shop name anywhere in the diff.

## Safe to merge

Yes. No blocker survived review — in fact, no finding at all. The test
now guards the actual physical-restock harm the original #1561 card
described, not just its money-side symptom.

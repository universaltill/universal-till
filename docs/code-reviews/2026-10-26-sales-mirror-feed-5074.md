# Review: the main till's sales-copy feed, shipped dark (ut-docs#5074 M2)

Part of ut-docs#5074 (design §4–§5.1, §11; contract ut-docs
`reference/contracts/pos-lan-sales-mirror.md`). Builds on M1 (#2169).

## What shipped

- `GET /api/sync/sales-mirror` (feed) and `GET /api/sync/sales-mirror/state`
  (state re-sync) on the main till: bearer (`syncTill`), pinned TLS only
  (`r.TLS == nil` → `403 tls_required` before the bearer is used),
  satellites `403`, copy off / killed / not the main till → `409`, two in
  flight then `503 Retry-After: 5`, `410 horizon_passed` for an empty or
  other epoch, `after < horizon` or `after > head`.
- Pages: exact `(from_seq, to_seq]`; the caller's own records left out but
  covered; entries for one record collapse to one record with the later
  seq; `gone` for a sale no longer in `sales`; ≤ 500 entries and ≤ 1 MiB of
  records; `to_seq ≤ head_seq`; a cap prune racing the read answers `410`.
- One log read + one batched read per table (8) per page, never per sale;
  `EXPLAIN QUERY PLAN` pinned (no table scan; the state read takes its
  order from `idx_sales_local_date`).
- Acks: a feed request acknowledges `after` (written when moved ≥ 50 or
  after 5 s, never when unmoved); the first state page acknowledges
  `resume_seq`, holding the log for that till.
- `POST /api/sync/sales-mirror/enable|disable` (manager, main till only):
  enable sets the switch and a fresh epoch in one write (sales made while
  off were never logged, so every copy re-syncs); disable keeps copies and
  epoch. Audit rows.
- `StartSalesMirrorMaintenance`: change-log prune every 10 min (MIN ack of
  registered tills; caps 30 days / 250,000 rows, halved below the low-disk
  floor, WARN per till a cap overrides; an empty epoch is refused).
- `GET /api/sync/sales-mirror/status` (manager, local): log head, horizon,
  rows, each till's ack and lag.
- A backup restore drops `sales_mirror.epoch` (`db.RestoreDropKeys`); a new
  one is minted on the next request or tick.
- Routes classified for demo mode (denied) and the two peer routes made
  session-exempt like every other bearer route.

## Off = inert

- Off (default): the feed answers `409` after the bearer check; the
  maintenance tick is at most three settings reads (it never probes the
  disk or prunes, `TestSalesMirrorMaintenance_OffIdleAndLowDiskHalvesCaps`);
  nothing touches the sale path. Serving a page writes nothing but the
  caller's ack (`TestSalesMirrorFeed_ServingWritesOnlyTheAck`, every table
  counted).
- Full `go test ./...` on this branch: green; CI guards: no new failure
  against `main`.

## Independent review (Fable, strict)

1. BLOCKER — feed routes not session-exempt (`auth.middleware.exempt`): a
   real till would get `401` before the bearer check. **Fixed**; pinned in
   `TestSyncPullPathsAreExempt`, and `TestSalesMirrorManagerRoutesNeedASession`
   keeps status/enable/disable behind the session. TDD-verified: removing
   the two paths fails the exempt test.
2. MAJOR — `GetOrCreate` (BEGIN IMMEDIATE) on every served page: **fixed**,
   a plain read first.
3. MAJOR — a cap prune between the horizon check and the read could yield a
   page that silently skips entries: **fixed** (horizon re-checked after the
   read → `410`), `TestSalesMirrorFeed_PruneDuringReadAnswers410`.
4. MINOR — the state read sorted the rest of the window on every page:
   **fixed** (keyset on `(local_date, rowid)` over `idx_sales_local_date`,
   no new index on `sales`, plan pinned).
5. MINOR — contract promised ≥ 1 record per page: **contract fixed** (the
   range advances; records may be empty).
6. MINOR — disable answered an empty epoch, replica refusal not in the
   envelope: **fixed**.
7. MINOR — `to_seq` could pass `head_seq`: **fixed** (entries truncated to
   the head read).
8. MINOR — the working set is ≤ 500 complete sales, not ≤ 1 MiB, before the
   byte cut (a run of 20 KB notes ≈ 10 MB): **accepted** for now (bounded);
   follow-up: size headers before loading children.
9. MINOR — tests: dedupe seq, at-head, own-only page, writes-only-ack and
   the exact byte bound now asserted.
10. NIT — comment on the off tick's reads corrected; the ack map is bounded
    by enrolled tills.

Contract mismatches the reviewer listed are fixed in the D1 contract PR
(zero-record pages, the `main`/`copy` status blocks, `not_main`, re-mint on
the tick).

## Deferred

- Finding 8 (load headers first when notes are large).
- `golangci-lint` could not run locally (binary built with an older Go);
  CI runs it.

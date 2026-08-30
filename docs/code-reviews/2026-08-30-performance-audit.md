# Performance audit — universal-till (2026-08-30)

**Requested by:** product owner, live — "go through all our code as a
principal engineer... find the performance issues... find the places we
need to add cache maybe, finding the queries or db actions that make it
slow."
**Scope:** whole `universal-till` repo, three parallel audits: the
repository/data-access layer (`internal/data`, 42K lines + all 75
migrations), the in-memory checkout engine (`internal/pos`, 10K lines),
and the HTTP handler layer (`internal/pages`, 101K lines — the handlers
agent itself fanned out two further sub-passes for inventory/catalog and
journal/reports/EOD to cover the volume).
**Method:** the new `performance` SDLC skill (`ut-docs/.claude/skills/
performance/SKILL.md`) — every finding requires an exact file:line, a
concrete failure scenario at this product's real scale (hundreds to
low-thousands of SKUs, thousands of sales/month, multi-year history, a
kiosk Pi or budget tablet — not a server), and a fix direction, not an
implementation. This was an **audit pass only** — nothing in this report
has been fixed yet; see the board cards it produced (linked at the
bottom) for follow-through.

## The one finding that matters most

**`datetime(created_at) >= datetime(?)`-style predicates defeat
`idx_sales_created` across roughly twenty report/inventory queries in
`internal/data/pos_repo.go`, independently confirmed by two separate
audit passes** (the data-layer audit and the handlers audit both found
this on their own, from different angles — the handlers audit verified
it live with `EXPLAIN QUERY PLAN` against a synthetic 100k-row `sales`
table). Wrapping the indexed column in a function makes the predicate
non-sargable in SQLite, so every one of these queries full-scans the
sales table (falling back to the much less selective `idx_sales_status`
index) regardless of how narrow the requested date window is. Cost grows
with the shop's *entire lifetime* history, never with the window asked
for.

This hits two of the most-visited pages in the admin surface on every
single load: `GET /reports` (the default landing tab fires ~4 of these
at once) and `GET /inventory` (`ItemDailySellRates` fires on every page
view **and** after every single stock receive/adjust/override/return via
`HX-Trigger: stock-updated`). At a plausible 2-3 year old shop (~100k+
sale rows), that's a full-history scan standing in for what should be a
14-day answer, repeatedly, all day.

Fix direction: either normalize `created_at` to one canonical stored
format at write time so a raw comparison is sargable, or add expression
indexes matching the actual predicate shapes used (`datetime(created_at)`
and the `date(created_at,'localtime')` family used elsewhere). Tracked as
its own high-priority card — see below.

## Full findings, grouped by theme (29 total across the three audits)

### A. Checkout hot-path (the code a cashier waits on for every single tap)
*Highest weighted priority — this is the only code in the product where
milliseconds are directly cashier-perceptible.*

1. **Every basket mutation recomputes the entire basket from scratch**
   (`internal/pos/service.go:478-580`, `recomputeTotals`) — called from
   every mutator and even read-only getters, does a full O(n) walk twice
   plus three fresh slice allocations per call. A 100-line basket built
   one scan at a time does ~5,050 line-recomputes instead of ~100 to get
   there. Untested by the existing perf benchmarks, which never exceed a
   3-line basket.
2. **A ~100ms-capable plugin call happens while holding the
   service-wide mutex** (`service.go:531,559-566`, inside
   `recomputeTotals`) — directly contradicting the locking pattern the
   same file documents and uses correctly on the sibling `ChargePolicy()`
   method (read state under the lock, ask outside it, re-acquire to
   apply). On a cache miss (new item/tax-code/order-type combination, or
   right after a plugin install), every other concurrent request on that
   till — another tap, a status poll — stalls behind the lock.
3. **`mergeResolved` re-scans and re-hashes every existing line's
   modifier signature on every add, including repeat scans of an
   already-cached item** (`internal/pos/types.go:427-461`, `service.go:
   312-321`). A till with 40 modifier-bearing lines already rung up
   re-does 40 fresh signature computations on every subsequent "+1" tap.
4. **Scan-line resolution is 4-5 sequential, unbatched DB round trips
   per item** (`internal/data/pos_repo.go:5603-5644,5705-5759`,
   `ResolveScanLine`) — settings read, then barcode lookup across
   fallback tiers, then price resolution, each blocking the next. A
   20-30 line basket does 60-150 sequential round trips just resolving
   lines.
5. **A manager-toggled setting that changes essentially never is
   re-fetched and re-JSON-parsed on every single scan**
   (`internal/data/barcode_settings.go:66`, `EnabledBarcodeSymbologies`)
   — safe to cache in-process (not a money/stock/tax value), invalidated
   on its own write path.

### B. `CompleteSale`'s N+1 writes (the tender transaction itself)

6. **Up to 5 separate statement executions per basket line inside the
   tender transaction** (`internal/pos/sales.go:619-735`) — stock check,
   line insert, modifier insert, discount insert, stock-movement record,
   none batched. A 60-line basket issues ~300 individual statements
   inside one transaction. Existing `BenchmarkCompleteSale*` benchmarks
   only exercise 1-3 lines, so this never gets scale-tested against the
   product's own documented SC-001 (<5s sale completion) target.

### C. Missing or defeated indexes (cheap, contained migration work)

7. **`sales(created_at)` effectively unusable** for report/inventory
   queries — see "the one finding that matters most" above.
8. **`audit_log` has no index on `created_at`**, only
   `(entity_type, entity_id)` — hits every `/audit` page open and every
   AI "Ask your till" summary once the log reaches six figures of rows
   after a year of voids/logins/overrides.
9. **`variant_barcodes.variant_id` has no index anywhere in 75
   migrations** — a correlated subquery in `ItemVariants`/
   `GetVariantLabel` runs once per active variant on every catalog page
   load and every catalog mutation's re-render.
10. **`sales.register_id` and `stock_movements.location_id` are
    unindexed** — `RegisterInUse`/`StockLocationInUse` full-scan years of
    history to answer a boolean, on register/location deactivation.
11. **`sale_lines.item_id`/`variant_id` (and their `_archive` twins) are
    unindexed** — `ListObsoleteItems`/`CleanupObsoleteItems` run 8
    correlated `NOT IN` subqueries against them; weigh against
    `sale_lines`' write-heavy checkout insert path before adding.
12. **`worker_allocation_repo.go`'s `date(allocated_at,'localtime')`
    predicate defeats its own index**, same non-sargable-function shape
    as the sales-table finding, plus `payments.paid_at` is unindexed and
    `ListWorkerAllocations` has no `LIMIT`.
13. **`sale_links(sale_id)`/`sale_links(original_sale_id)` unindexed** —
    every `/journal/{receipt}` detail view does two full scans to find
    linked return/original receipts.

### D. Template re-parsing on every request

14. **No handler caches a parsed `*template.Template`** — `internal/
    httpx.Render`/`RenderPartial`/`RenderWith` and `internal/ui`'s view
    constructors all `template.New(...).ParseFS(...)` fresh, inside the
    handler, every single call. The sale screen alone triggers ≥4
    independent full template parses per load (`/`, `/ui/basket`,
    `/ui/buttons`, `/ui/held`+`/ui/suggestions`), and 2-3 more per
    subsequent tap during checkout (9 separate `ui.NewBasketView(funcs)`
    call sites in `pos_api.go` alone). This is a fixed CPU cost paid on
    every tap, independent of data volume — the kind of thing that's
    invisible on a developer's laptop and very visible on a kiosk Pi.
    Straightforward, high-value, low-risk fix: parse once (package-level
    `sync.Once`, or once at `common.Deps` construction), reuse.

### E. Unbounded queries / missing pagination (admin surfaces)

15. **`POST /api/reports/eod/range` has no maximum date-range bound**,
    unlike its sibling export endpoint which already enforces one.
16. **`GET /invoices` fetches every invoice ever issued** on a bare page
    load (no default date range), and sums totals in a Go loop instead
    of SQL `SUM`.
17. **Catalog admin refetches the *entire* unbounded item/barcode/
    variant/tax-code list after every single mutation** — attaching one
    barcode triggers 4 full-table queries plus a full-page re-render.
18. **`ListLiveTrackedOrders` has no time bound**, pulled and marshaled
    in full on every cloud-sync tick, with liveness filtering happening
    in Go instead of SQL.

### F. Sync and import batching

19. **`DumpAdmin` does a full multi-table scan + JSON marshal + SHA256
    on every 30s replica poll**, whether or not anything changed —
    competes with checkout traffic for CPU/IO on every till.
20. **`ApplyAdmin`'s `upsertRow` issues one statement per row of the
    entire bundle**, not a diff — one price edit anywhere re-applies
    thousands of byte-identical rows as individual executions on every
    replica.
21. **CSV/Excel catalog import does N+1 category/tax-code lookups per
    row** instead of per distinct value — a 2-3k row import across ~30
    categories issues thousands of redundant queries instead of ~30-60,
    slowing the onboarding flow a shop owner is actively watching.

### G. Small, low-effort N+1 cleanups

22. One settings query per active payment method on every sale-screen
    load (`index_page.go:83-87`) — small N, but the highest-traffic page
    in the product.
23. One plugin-version lookup per receipt-template plugin on every
    completed sale (`pos_api.go:1308-1337`).
24. Report-reprint handler loads up to 100 full report blobs to find one
    row that a direct indexed lookup would find in one query
    (`eod_api.go:683-693`) — `report_archive` already has the needed
    `UNIQUE(kind, period)` index, just isn't queried directly.
25. Four plugin-manifest conflict-check methods loop one query per
    candidate key instead of a batched `IN (...)` — install-time only,
    not hot-path, but a textbook N+1 (`plugin_repo.go:1394-1556`).

## Checked and found clean (worth recording, so it isn't re-audited)

- `internal/money.Money` arithmetic — clean integer minor-units
  throughout, no correctness-costume caching risk.
- `VATBandsForSale`/`ApportionServiceChargeTax` group by distinct tax
  rate (1-3 bands typically), not by line count — not the quadratic risk
  the per-line loops feeding them are.
- `SetCustomerID`/`SetCustomer` deliberately skip `recomputeTotals` since
  customer attachment doesn't affect totals — a correct example of the
  targeted-skip pattern finding A.1 should follow.
- `export_repo.go` (already fixed a prior N+1, ut-docs#229),
  `reset_archive_repo.go`, `fiscal_repo.go`, `kitchen_stations_repo.go`,
  `modifier_repo.go` (both already batch via `IN (...)`),
  `translation_repo.go` (loaded once at startup).
- Button/catalog search endpoints are already paginated SQL, not
  in-memory filtering.
- `d.CurrentState()`/`d.MenuSnapshot()` are already in-process cached.
- The checkout money math itself in the tender path is pure in-memory
  computation, no DB calls per line.
- `CreateItem`/`CreateItemTx`'s non-transactional inventory-row creation
  is a documented, deliberate stockless-deployment tradeoff, not a
  defect.

## Board cards filed (Ready, this session)

Grouped by theme rather than one card per finding (29 individual cards
would flood the board for related fixes that share root causes and
review surface):

- **A + item 5** — checkout hot-path (findings 1-5): `complexity:hard`,
  `p1`. Highest real cashier-perceptible impact; needs careful review
  since it touches the sale/money path.
- **B** — `CompleteSale` N+1 writes (finding 6): `complexity:hard`, `p1`
  — separate from A because it's a persistence-batching change inside a
  money-writing transaction, different review lens than in-memory
  recompute logic.
- **C** — missing/defeated indexes (findings 7-13): `complexity:medium`,
  `p1` — cheap, contained migration additions; grouped because they're
  the same class of fix reviewed the same way, weighing write-path cost
  on each.
- **D** — template re-parsing (finding 14): `complexity:medium`, `p1` —
  standalone, clean, high-value, low-risk.
- **E** — unbounded queries / pagination (findings 15-18):
  `complexity:medium`, `p2`.
- **F** — sync/import batching (findings 19-21): `complexity:hard`, `p2`
  — touches the multi-till sync protocol, needs its own careful design.
- **G** — small N+1 cleanups (findings 22-25): `complexity:easy`, `p2`.

(Card links added by the pipeline step that filed them — see the PR/issue
list in the session's own report to the product owner.)

# Review — cloud-link sale frames for refunds and replica sales (ut-docs#2894)

**Change:** one `publishCloudLinkSale` helper (non-blocking, nil-safe) used by
the tender path, refunds (`refund_page.go`), returns (`inventory_api.go`
CreateReturn) and the main till's ingest of a replica's journaled sale
(`sync_sales.go` applyJournal, attributed to the replica's till id from its
authenticated bearer). Refunds/returns carry `refund:true` and a negative
total. Follow-up of #2824 for the my. live view (#2826).
Author: Sonnet. Reviewer: Opus 5.5.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Replica frame built from the replica's own figures, not the row the main stored (recomputed totals; replica-chosen time) | fixed: re-read the stored row by id (`publishJournaledSale`) |
| 2 | minor | A replica replaying its backlog after an outage would show old sales as live and use the frame allowance (20 burst, 5/s) ahead of the main's live sales | fixed: only sales from the last 10 min (not > 2 min in the future) publish; test `TestApplyJournal_BacklogSaleNotPublished` (failed first) |
| 3 | minor | Comment said the client is nil on a replica | fixed (it exists but never dials) |
| 4 | minor | Extra `GetSaleDetailByID` per refund/return even when nobody watches | accepted (cheap, rare) |
| 5 | minor | Hash-only manifest change needs the trailer | done |

**Checked, no issue (reviewer):** every publish runs after `CompleteSale`
committed; a failed re-read only skips the frame; till id from the hashed
bearer, not payload text; dedupe via `SaleExists` (duplicate-ingest test: one
frame); harness cleanup order and bounded waits; surface-hash-only manifest
valid (no rendered change).

**Verification:** `go build ./...`, gofmt, `go test -race -run
'TestApplyJournal|Refund|CreateReturn' ./internal/pages/` green; guards
data-access, core-neutral, i18n, kiosk-engine, docs-shots.

**Verdict:** safe to merge.

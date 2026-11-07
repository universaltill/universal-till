# Review: promoted till's own late sales reach the next Z (ut-docs#5777)

**Date:** 2026-11-07 · **Branch:** `fix/5777-promote-own-late-sales` ·
**Docs:** ut-docs `docs/5777-adr0066-promote-own-late` (ADR-0066 amendment
sentence, design-5074 §9.2/apply, LAN sales-mirror contract 1.1.0)
**Author:** Sonnet Dev subagent · **Reviewer:** Opus, independent subagent (different model)

## What shipped
After a promote, the promoted till's own sales that the old main till
recorded at or after its last close (made before it) were counted in no Z.
- Migration `099_promote_own_late.sql`: `sales.recorded_at` (+ archive twin),
  `mirror_sale_arrivals`, Z-window index on
  `COALESCE(recorded_at, completed_at, created_at)`.
- The sales-mirror feed sends the caller its own sales header-only (contract
  1.1.0; a 1.0.0 receiver already skips own-origin records). `ApplyMirror`
  keeps only their arrival time; pruned with the copy.
- `MarkOwnLateArrivals` at promote stamps `recorded_at` (NULL-only, bounded by
  the floor and this till's own last close; idempotent), then the arrivals
  are cleared. A failure logs and never blocks the promote (offline-first).
- Z window, late note (own rows under this till's own id/name) and
  `SalesOutsideAnyZ` (excludes marked rows) follow; `LateBeforeFloor`
  removed; `own_late_counted` in the `sales_adopted` audit row.
- Origin `created_at`/`completed_at`, receipts and closed Zs unchanged
  (ADR-0066 amendment rules 2 and 4).

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| — | design | Feed never delivered own sales, so the design's arrival source was empty (found by Dev's end-to-end test) | Fixed: header-only own records, contract 1.1.0 |
| M1 | medium | Late note fell back to this till's name for *other* tills missing from the roster (write-once Z) | Fixed; `TestLateNote_OtherTillNotLabelledWithOwnName_5777` (red before, verified by orchestrator) |
| L1 | low | `mirror_sale_arrivals` never emptied; join snapshot handed it on | Fixed: cleared after mark and in `resetSalesMirrorBookkeeping`; tests |
| L2 | low | Contract said "no amounts"; record carries zeroed amounts + `origin_till_name` | Fixed in contract wording |
| L3 | low | State re-sync / pre-1.1.0 main keep no own arrival → still no Z after promote | Contract note + Backlog ut-docs#5807 |
| L4 | low | Second promote before a Z loses `recorded_at` | Backlog ut-docs#5808 |
| L5 | low | Stale comments (contract 1.0.0, pos_repo doc) | Fixed |
| L6 | low | Test's "later Z" step is a preview, not archived | Commented |
| — | residual | Voucher Z query windows on `voucher_transactions.created_at`; an own-late sale's voucher rows stay in no Z | Accepted, release note |

## Verification
- TDD re-verified by the reviewer in a separate worktree: reverting
  `saleArrivalCol` → `TestPromote_OwnLateSale_ExactlyOneZ_5777` fails
  ("B's first Z after promote = 0/0"); no-op `MarkOwnLateArrivals` → fails
  ("promote did not mark S"); restored → passes. M1 test re-verified by the
  orchestrator (revert → "labelled \"Back counter\"" → restore → pass).
- Reviewer checked the 30 s backdating in the pages test hides no boundary
  bug (half-open windows match to the second), the index swap (query plan
  uses the new index; only the Z window used the old one), migration cost
  (nullable ADD COLUMN, no rewrite), no positional `sales` column reads.
- Full gate after the last edit: see PR (gofmt, build, vet, `go test ./...`,
  golangci-lint, every `ci.yml` build-job guard).

Pre-flight: ADR-0066 amendment sentence + Consequences updated (existing ADR, index unchanged); reference/contracts/pos-lan-sales-mirror.md → 1.1.0 updated; reference/design-5074 §9.2 + apply updated; en.json/i18n-meta n/a (no new key; on-screen late note is ut-docs#5776); web/help n/a (sales mirror ships dark, promote panel unchanged); README n/a; ut-cloud mirrors n/a (LAN-only contract); follow-ups → ut-docs#5807, ut-docs#5808.

**Verdict:** approve after fixes.

# Review: state re-sync keeps the till's own sale arrivals (ut-docs#5807)

**Date:** 2026-11-07 · **Branch:** `fix/5807-state-own-arrivals` ·
**Docs:** ut-docs `docs/5807-state-own-arrivals` (LAN sales-mirror contract
1.2.0, design-5074 §4.4 and apply rules)
**Author:** Sonnet Dev subagent · **Reviewer:** Opus, independent subagent (different model)

## What shipped
A till whose sales copy was rebuilt by a state re-sync kept no recording
time for its own sales (the state pages left them out), so after a promote
its own late sales reached no Z (L3 of the ut-docs#5777 review).
- `StateSaleIDs` no longer excludes the caller's sales; it returns an `own`
  flag per id (`till_id <> '' AND till_id = caller`). The main till's own
  sales (`till_id = ''`) are never flagged.
- `LoadMirrorSaleHeaders` split out of `LoadMirrorSales` (same query, same
  origin mapping; children empty and non-nil); `LoadMirrorSales` now calls
  it and adds children.
- The state handler loads flagged ids header-only and sends them through
  `ownSaleArrival` (the feed's 1.1.0 shape). They count toward `limit` and
  the byte budget like any record. The cursor advances past them.
- Receiver unchanged: `applyMirrorRecords` is shared by feed and state
  pages and already keeps the arrival (window-checked) and skips the copy.
- Contract 1.1.0 → 1.2.0, `feed_version` stays 1; CHANGELOG entry (combined
  with #5777, which had none).

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| L1 | low | `SalesMirrorFeedVersion` comment still said only "1.1.0 adds header-only own sales" | Fixed: mentions 1.2.0 on state pages |
| L2 | low | design-5074 §4.4 described the own header as "(id, times, local_date)"; it also carries receipt, status, currency, origin name and zeroed amounts | Fixed wording in ut-docs |
| — | checked | Paging: own ids now share `limit`; `len(ids) < limit` and the byte-budget cut still advance the cursor only over records processed; a flagged id whose header vanished between reads just advances the cursor (same as a vanished full sale) | No issue; `TestSalesMirrorState_OwnSalesHeaderOnly_5807` pages with `limit=2` |
| — | checked | Own flag for `till_id = ''` (main till's own) vs the caller; `sales.till_id` is `NOT NULL DEFAULT ''` (001_init), so the flag expression never scans NULL | No issue |
| — | checked | Header fields equal `LoadMirrorSales`' (same function now; `TestLoadMirrorSaleHeaders_NoChildren_5807` DeepEquals them) | No issue |
| — | checked | 1.0.0 receivers: own-origin skip predates #5777 (verified at `7c8b9c4^1`), shared by feed and state apply; 1.1.0 receivers keep the arrival | No issue |
| — | checked | Window: server sends `local_date >= from_date`; receiver skips arrivals older than its window — same rule as the feed | No issue |
| — | checked | Resume: own records carry `seq = resume_seq` (inside `seqOK`); arrivals ignore seq, last apply wins, so a later feed record overrides | No issue |

No file writes, no path handling, no SQL outside `internal/data`, no
migration change in the diff (the two recurring-bug checks are n/a).

## Verification
- TDD re-verified by the reviewer in a separate worktree (`git worktree add`
  at HEAD, `internal/data/sales_mirror_repo.go` and
  `internal/pages/sync_sales_mirror.go` reverted to `origin/main`):
  `TestPromote_OwnLateSale_StateResync_ExactlyOneZ_5807` fails ("the pull
  kept 0 arrival rows for S, want 1"), `TestSalesMirrorState_OwnSalesHeaderOnly_5807`
  ("own headers = 0, want 2"), `TestSalesMirrorResync_RebuildsOwnArrivals_5807`
  ("S1 arrival = \"\" (0 rows)"), `TestSalesMirrorState_PagesSalesThenCloses`
  ("B was not sent its own sale as a header"); the data package's header
  test does not compile without `LoadMirrorSaleHeaders`. Restored → all pass.
  Worktree removed. `TestApplyStatePage_OwnHeaderKeepsArrivalNotCopy_5807`
  is a characterisation test of the unchanged receiver (passes on main by
  design; it is the old-receiver proof).
- `go build ./...`, `go vet ./internal/data/... ./internal/pages/...`,
  `gofmt -l internal/` (clean), `golangci-lint run ./internal/...` (0 issues),
  `go test ./internal/data/... -count=1` (ok), `go test ./internal/pages/...
  -count=1` (all ok, pages 326 s), guards `guard-sales-mirror-isolation.sh`,
  `guard-data-access.sh`, `guard-fixed-dates.sh` (pass).

Pre-flight: reference/contracts/pos-lan-sales-mirror.md → 1.2.0 updated; reference/design-5074 §4.4 + apply rules updated; CHANGELOG updated; ADR n/a (ADR-0066 amendment unchanged, this closes its documented gap); en.json/i18n-meta n/a (no key); web/help n/a (sales mirror ships dark, no screen changed); README n/a; ut-cloud mirrors n/a (LAN-only contract); follow-ups below.

**Verdict:** approve after fixes (L1, L2 fixed; no blocking findings).

## Deferred
- design-5074 §4.4 still describes the state endpoint with `after_id` and
  `(local_date, id)` order; the shipped contract pages by an opaque cursor
  in `(local_date, rowid)` order — pre-existing doc drift, Backlog card.
- A till re-syncing from a main till older than 1.2.0 still keeps no own
  arrivals (release note: upgrade the main till first) — accepted.
- Second promote before a Z loses `recorded_at` — already ut-docs#5808.

# Review: the additional till's sales-copy pull, shipped dark (ut-docs#5074 M3)

Part of ut-docs#5074 (design §4.4, §5.3, §6, §7, §12, §13; contract ut-docs
`reference/contracts/pos-lan-sales-mirror.md`). Stacked on M2.

## What shipped

- `StartSalesMirrorPull`: every 30 s, 5 min while linked, at once on a kick
  of `Deps.SalesMirrorPullNow` (the link nudge, M4). One tick pages the feed
  from this till's cursor until the head (≤ 400 pages, 250 ms apart, 100
  records per page = one apply transaction); a `410` starts a state re-sync
  (paged, resumable, never a database replace) and the feed continues from
  `resume_seq`.
- `SalesMirrorRepo` (receiving side, `sales_mirror_repo_copy.go`): a page or
  frame applied in one transaction with its cursor; refused unless it
  continues the cursor in the same epoch; newer `(epoch, seq)` wins and the
  children are replaced; own-origin, gone, out-of-window and malformed
  records skipped (malformed = missing fields, unknown status, bad
  `local_date`, duplicate child ids, a seq outside the page, or anything the
  schema refuses — per-record savepoint), never wedging the cursor.
- Bounds (hourly and after 1,000 applied records): window
  (`sales_mirror.window_days`, 31…3660, default 400), closes kept 3,660
  days, disk budget `min(max_mb, 20 % × (free + copy))` dropping the oldest
  whole days first, never below 31 days, then `paused:budget`; below the
  low-disk floor `paused:low_disk` and a trim of sales and closes to 31 days.
- Gates: no main till / not joined → nothing; shop setting off, this till's
  `sync.sales_mirror_off`, `UT_SALES_MIRROR=off` → no request at all; no pin
  for the main till → `needs_tls`, the bearer is never sent; main till off
  → `main_off`; satellite → quiet `satellite`; newer feed → 
  `paused:version_wait`, cleared by the first readable page.
- Status for a manager on `GET /api/sync/sales-mirror/status` (`copy` block).

## Off = inert

`TestSalesMirrorScenario_KillSwitchesKeepTheCopy` counts requests on the
main till: with the shop setting off, this till's kill switch or the
process kill switch, the tick sends **zero** copy requests and writes
nothing (status is in memory). Full `go test ./...` green.

## The three-till harness (proof)

Real tills in temp dirs, the main till's real mux over TLS, the real
journal push and the real pull and apply:

| Scenario (design §13) | Test |
|---|---|
| 1 fan-out, C briefly offline | `FanOutWithOneTillBrieflyOffline` (A→M→B; C back after 20 sales; A, B, C equal M, none holds its own) |
| 2 offline through 150 changes incl. a void and a close (AC2) | `OfflineThroughManyChanges` |
| 3/4 different lags, prune never passes the lower ack | `DifferentLagsAndPruneSafety` |
| 5 horizon overflow → state re-sync, no DB replace, own sales kept | `HorizonOverflowResyncsByState` |
| 6 cut mid-catch-up ×3, and a failure inside the apply transaction | `CutMidCatchUpResumes`, `FailureMidTransactionLeavesNothing` |
| 8 clock skew +3 h / −2 h (AC6) | `ClockSkew` |
| 9 kill switches and epoch change (AC7), satellite (AC8) | `KillSwitchesKeepTheCopy`, `SatelliteIsRefusedQuietly` |
| AC3 the copy never counts on the additional till | `CopyNeverCountsOnTheAdditionalTill` (Z figures, next receipt, push queue, stock, push cursor, sales rows identical after copying 400 sales) |
| AC11 low disk / budget | `TestSalesMirrorBounds_LowDiskPausesAndTrims` |
| duplicates / reorder / gaps (AC5) | repo tests (`RefusesDuplicateAndGap`, `NewerStateWinsOlderIgnored`) |

`assertCopyEqualsMain` compares, per sale, status, receipt, origin, total
and the line and payment counts, and fails on any missing, extra or own
row. Not covered here: link frames (M4 sends nudges only), promote (M6).

## Measurements (Apple M2 Max; Pi 5 still to measure on the device)

- Apply of one 100-sale page (2 lines, a modifier, a payment each):
  11.6–11.8 ms (`BenchmarkSalesMirrorApplyFeedPage100`). The design's
  target is p95 ≤ 50 ms on a Pi 5; a Pi is roughly 4–6× slower, so this is
  near the target and must be measured on the device before the setting is
  turned on (halve the page if over).

## Independent review (Fable, strict)

Verdict: safe to merge dark after findings 1 and 2. All fixed except where
noted:

1. MAJOR — AC3 harness test flaky (`EODReport.GeneratedAt` is the wall
   clock): **fixed** (also in M1's AC3 test).
2. MAJOR — `version_wait` never cleared: **fixed**
   (`VersionWaitPausesThenClears`).
3. MAJOR — 200 sales per apply transaction vs design ≤ 100: **fixed** (pages
   of 100) and measured (above).
4. MINOR — `local_date` not validated: **fixed** (rejected unless
   `YYYY-MM-DD`).
5. MINOR — `max_mb << 20` overflow: **fixed** (clamped to 1 TiB).
6. MINOR — a satellite logged a refusal every tick: **fixed** (quiet
   `satellite` state).
7. MINOR — low-disk trim left closes: **fixed**.
8. MINOR — kill-switch test did not prove "no request": **fixed** (request
   counter on the main till).
9. MINOR — no fault inside the apply transaction: **added**
   (`FailureMidTransactionLeavesNothing`: a non-constraint failure mid-page
   leaves no row and the old cursor; a constraint failure rejects only that
   record, by design).
10–13. NITs (lock reads, channel made before the link client starts,
    comments, lag during a re-sync, status vocabulary): **fixed**.

## Deliberate deviations (recorded)

- No purge on revoke yet: the only revoke signal an additional till gets is
  a link `401`, which the design says must never purge (contract states it).
- No "yield while a sale is in progress": pages of 100 spaced 250 ms apart.
- Status on a manager-only local route, not yet in the diagnostics bundle.
- No `sales_mirror_applied` audit rows (less writing).
- In-process tills: a transport failure and an in-transaction failure
  stand in for SIGKILL; what a SIGKILL leaves is exactly the committed pages.

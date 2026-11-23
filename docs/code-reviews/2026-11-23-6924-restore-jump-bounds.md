# Review: restore number jump stays inside the bound; ArchiveReport ignores out-of-bound Z (ut-docs#6924)

Branch `fix/6924-restore-jump-bounds`. Built by Sonnet (Dev). Tester: TODO. Reviewer: TODO.

## What shipped

- New leaf package `internal/restorenum` with `Max` (10^15) and `ParseFloor` (base-10 int64 in `[0, Max]`). `data.RestoreNumberMax`, `db.restoreNumberMax` and `db.parseRestoreFloor` now delegate to it, and `data.restoreFloor` and the jump read floors through it, so there is one parser. `fmt.Sscan` and the SQL `CAST(value AS INTEGER)` are gone.
- `JumpNumbersAfterRestore` believes only inputs in `[0, restoreInputMax]` (`Max` minus the 10^14 jump cap minus the 1000 rounding): the receipt scan (`maxReceiptSeqUpTo`), the stored counter, the receipt floor, the per-kind Z max (`BETWEEN 0 AND`) and the Z floor. A skipped value logs a warning. `days` and `restoreJumpSize` are capped at `restoreJumpMax` (10^14) and cannot overflow. The jump's output therefore never exceeds `Max`, so every reader accepts it.
- `ArchiveReport` (both variants) ignores `z_number` outside `[0, RestoreNumberMax]` through `CASE` inside the aggregate and a bounded `prev_closed_at` subquery. It reads the floor in Go through `ParseFloor`, so a huge, garbage (`12abc`, `1e18`) or negative floor counts as absent. No error is returned and the close is never blocked.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| - | - | TODO (reviewer) | TODO |

## Verification

- TDD: each new test was run red first (failure lines in the dev note), then green after the fix.
- `gofmt`, `go build ./...`, `go vet`, `golangci-lint run ./internal/...`, `guard-data-access`, `guard-fixed-dates` and `guard-core-neutral` pass; `go test ./internal/data/... ./internal/db/... ./internal/restorenum/...` passes.

Pre-flight: help topic n/a (internal numbering safety, no screen or step a shop owner sees); reference docs n/a (no reference page describes the bound); README n/a; en.json n/a (no user-facing strings, the warning is an operator log line); migrations n/a; ADR n/a (hardens ADR-less #5068/#6906 mechanism, no new decision).

**Verdict:** TODO (reviewer)

## Round 1 review (Opus, independent; author Sonnet) — 2026-11-23

Verdict: **ship after fixes** — not merged. The card hit its budget before the fix round.

# Review: fix/6924-restore-jump-bounds (ut-docs#6924), reviewer Opus 5.5

Scope: `git diff main...HEAD` (8 files). Ran: `go build ./...`, `go vet` (data, db, restorenum), `go test ./internal/restorenum/ ./internal/db/` and `go test ./internal/data/ -run 'Restore|ArchiveReport|NumberSeries|Receipt'`. All green. I reproduced H1 against the exact SQL using Python sqlite3 with the real `ux_report_archive_kind_znumber` unique index.

| id | sev | file:line | failure scenario | suggested fix |
|---|---|---|---|---|
| H1 | high | internal/data/pos_repo.go:3778-3783 (and 3797-3801) | Z numbers have no stored counter, and `ux_report_archive_kind_znumber` (001_init.sql:1085) is UNIQUE on (kind, z_number). Take a corrupt in-bound row `z=999999999999999`. Close 1 gives 1e15. Close 2 gives 1e15+1. Close 3 then ignores 1e15+1 (it is > Max), finds 1e15 again, computes 1e15+1, and gets `UNIQUE constraint failed`. The 3 retries fail the same way, so the cashier's day close is blocked from then on. I reproduced this. The same thing happens after the first close if a row sits at exactly `Max`, or if the floor is `Max` (the floor is accepted up to Max inclusive, and floor+1 goes past it). On main the same data just keeps counting, so this is a regression and it breaks card item 2 ("never block a day close"). | ArchiveReport should only guard against overflow and REAL: believe `typeof(z_number)='integer' AND z_number >= 0 AND z_number < 9223372036854775807`. Leave the 1e15 believability bound to the jump. If you keep a bound, the result must stay readable at the next close: emit nothing above the bound, i.e. cap the floor at `< bound` and ignore rows at `>= bound`. Even then a row at bound-1 can still collide, so overflow-only is the clean fix. Add a test with 3+ consecutive closes after a near-bound row and after a floor = Max. |
| M1 | medium | internal/data/restore_numbers.go:177,184,259 vs number_series_repo.go:79 / pos_repo.go:3778 | The jump ignores values in (restoreInputMax, Max], but the allocators don't. The receipt seed uses `maxReceiptSeq` with Max (or the stored counter) and ArchiveReport uses Max. In `NearBoundReceipt` the marker says next receipt `000011001`, but the counter seeds from 999999999999999, so the real next receipt is 1e15. In `NearBoundZ` the marker says NextZ 7, but ArchiveReport issues 1e15. The restore banner/marker and the jump result describe numbers the till won't use, and the new tests pin the jump's view without checking what is actually issued next. | Use one believability rule across the jump, `maxReceiptSeq` seed and ArchiveReport. Or, when the jump sees a value it doesn't believe but the allocator would, don't record a marker/floor that claims a different "next" (log it instead). Extend both tests to allocate the next receipt / archive the next Z and assert it matches the marker. |
| M2 | medium | internal/data/pos_repo.go:3790 | The floor is now read in a separate statement and the error is discarded (`_ =`). Any error other than no-rows (ctx cancel, SQLITE_BUSY during a checkpoint, I/O) makes floor = 0. The insert then proceeds with `MAX(z)+1`, which right after a restore is a Z number the dead till already issued. That duplicate Z is exactly what the floor exists to prevent. Before this branch the floor was read in the same INSERT, so it was atomic and an error was retried. | Treat only `sql.ErrNoRows` as absent and return or retry any other error (move the read inside the retry loop). Or keep the read in SQL with a validating expression: `CASE WHEN value NOT GLOB '*[^0-9]*' AND value <> '' AND length(value) <= 16 THEN CAST(value AS INTEGER) END`, bounded. |
| L1 | low | internal/data/restore_numbers.go:214-215 | `now.Sub(la)` saturates at maxDuration when `created_at` is a corrupt but parseable year before about 1734 (datetime() accepts 0000-9999). `d+24h-1` then overflows to a negative value, and `days` becomes about -106750. The cap at line 217 only catches the high side. `zFloor = base + days` then drops below the last Z (or goes negative, which ParseFloor later rejects), so the Z protection is silently lost. Note also that `days` can never exceed about 106752, so the `restoreJumpMax` cap on it is dead code. | Compute `days = int64(d/(24*time.Hour)) + 2` (round-up without the add), or clamp `d`. Also clamp `days` to `>= 1`. Add a test with `created_at = '0100-01-01 00:00:00'`. |
| L2 | low | internal/data/restore_numbers.go:84-85 | `2*peak` overflows for `peak > MaxInt64/2`. `TestRestoreJumpSize_Capped{1<<62,…}` passes only by luck (the divisor goes negative). `peak` is a row count, so this can't happen in practice. | Use `if peak > 0 && (peak > restoreJumpMax/2 \|\| days > restoreJumpMax/(2*peak))`. |
| L3 | low | internal/data/restore_numbers_test.go (new ArchiveReport tests) | `HugeZNumberIgnored` only seeds MaxInt64 and does one close. Nothing pins behaviour for values inside the bound band or for consecutive closes, which is why H1 got through. `HugeOrGarbageZFloorIgnored` doesn't cover a floor of exactly `Max`. | Add the tests described in H1. |
| N1 | nit | internal/restorenum/restorenum.go:16 | `strconv.ParseInt` accepts `"+5"`, but the doc says "base-10 integer". The jump never writes it, so harmless. | Reject a leading `+`, or document it. Add a test case. |
| N2 | nit | internal/data/restore_numbers.go:133 | A value above `Max` fails `ParseFloor` silently, while values in (inputMax, Max] are logged. The bigger corruption is the one that isn't logged. | Log when `ParseFloor` fails on a non-empty value. |
| N3 | nit | internal/db/restore_floors_test.go:537 | `TestRestoreFloorKeysMirrorData`'s Max check is now trivially true (both sides alias `restorenum.Max`). | Drop the `restoreNumberMax` alias and use `restorenum.Max` directly, or keep it and note it in a comment. |

Checked and fine:
- The placeholder/arg order in both ArchiveReport variants matches, including the HAVING double-close guard (which is unchanged).
- `prev_z_number` and `prev_closed_at` correctly come from the highest in-bound row.
- Arithmetic bound: zFloor ≤ inputMax + jumpMax < Max, and receiptFloor ≤ roundUp(inputMax + jumpMax, 1000) = Max. The jump output stays within the bound.
- `internal/restorenum` is a stdlib-only leaf: no import cycle, and the name is fine.
- There is no raw SQL outside `internal/data`/`internal/db`, and no user-facing strings.
- A valid till (numbers far below 1e14, digit-only floors) gets the same numbering as on main.

**Verdict: ship after fixes.** H1 must be fixed (it is a new way to block the day close, the exact thing item 2 forbids). M1 and M2 should be fixed too. L1 is cheap and fits under card item 1.

# Review — EOD: journaled rows count in the Z of their arrival (ut-docs#5769, slice A)

**Shipped:** the main till's close-to-close Z now counts each row journaled from
another till by the time the main till recorded it. This follows the ADR-0066
amendment of 2026-11-07, merged in ut-docs#5778. Before, it counted by the row's
origin `created_at`, so a row made before a close and applied after it landed in
no Z.
- Sales window on `COALESCE(completed_at, created_at)`.
- Migration **098** adds `received_at` to `no_sale_events`, `cleared_baskets`
  and their `_archive` twins, plus an expression index
  `idx_sales_status_arrival_dt`. Its checksum is pinned.
- The journal apply paths stamp `received_at` from the SQL clock.
- `LateEntriesForInstantWindow` fills `EODReport.LateEntries`, which is stored in
  the archived `content_json`. Rendering it is slice B, #5776.
- `generateEOD` takes `to` and runs every window read while it holds the write
  lock (`HoldWriteLock`), bounded by a 4 s ctx deadline. On expiry nothing is
  archived and the next tick retries.
- The promote warning counts only own sales outside any Z.

**Phases:** BA, Architect and the ADR on Opus; Dev on Sonnet; Tester on Opus;
independent code review on Fable (different model from the author; fiscal code,
so high-risk).

## Findings

| # | Sev | Source | Finding | Outcome |
|---|---|---|---|---|
| T1 | high | Tester | No index covered the new sale window expression. At 300k sales the window reads went from 28 ms to 2.76 s, all while holding the write lock, so a checkout could fail with SQLITE_BUSY. | Fixed. Expression index in 098; EXPLAIN QUERY PLAN uses it. The tester measured 14 ms with it. |
| T2 | medium | Tester | The atomic-close test passed with the lock disabled. The pages tests used a 1-connection pool (lock no-op), and their second same-day close was never archived. | Fixed. Tester tests added: an in-flight sale committing during the close (fails without the lock), a full HTTP close on a real multi-connection WAL database checking archived `late_entries`, three closes (AC2), and the promote floor (AC8). |
| M1 | medium | Review | The held lock stalls every writer for up to the 5 s busy_timeout. | Fixed. 4 s ctx deadline on the held section, held-duration log (Warn above 1 s), and a writer-stall contract comment on `HoldWriteLock`. |
| M2 | medium | Review | No test proved the lock. | Fixed with T2. |
| M3 | medium | Review | The promote "may be in no Z" count still included late copied sales, which the next Z now counts. | Fixed. The count is `OwnOutsideZ` only; `LateOutsideZ` stays in the audit payload. |
| L1 | low | Review | AC8 promote test missing. | Fixed (tester test). |
| L2 | low | Review | A no-sale late-entries query error was swallowed. | **Declined.** #5753 decided that a no-sale read failure never fails the close. The note's no-sale part follows the same rule, and the sale and cleared parts stay hard errors. |
| L3 | low | Review | `ReceivedAt` could be set from a payload one day. | Fixed: field comment. |
| L4 | low | Review | Stale `created_at` window comments. | Fixed. |
| L5 | low | Review | `HoldWriteLock` turns into a no-op by checking whether the pool is capped at one connection. | Accepted. Production `db.Open` sets no cap. The tester's multi-connection pages test exercises the real path. |

The reviewer re-verified the TDD claims: with the arrival hunks reverted, all
six core tests failed for the right reason (the late row stayed in Z N or in no
Z, and a late voucher sale's GUTSCHEINE figure disagreed with its sale). The
lock was verified with a probe: a writer blocks until release, and a reader is
not blocked.

The tester also ran the real binary against a temp data dir: a sale, then a
close via the API; the archived Z was correct.

## Gate
- gofmt clean, `go build ./...`, `go vet ./internal/...`, golangci-lint 0
  issues.
- Guards: data-access, fixed-dates, card-data-schema, core-neutral and i18n all
  pass.
- `go test` for data, pages and db passes.
- Unrelated, also on `origin/main` or sandbox-only:
  - `TestStockUploadPendingAndHasSendable_FastWithRandomCloudIDsAndUnlinkedMarks`
    fails under `-race` on `origin/main` too.
  - pages under `-race` times out in this sandbox (it passes without `-race`).
  - internal/plugins `TestHostTCPInvalidPtrDoesNotConsumeSocketBytes` passes on
    its own.
- CI runs the full set.

Pre-flight: help topic and print/Reports UI are deferred to #5776 (slice B),
which renders the stored note. The `reference/` sync contract 1.13.3 and the
design-5074 N9 pointer were updated in ut-docs#5778. The ADR-0066 amendment was
merged first (ut-docs#5778). No `en.json` keys changed. README is n/a. The
promote gap (own late sales) is #5777. Cutover residuals go in the release notes
per the ADR.

**Verdict:** accept.

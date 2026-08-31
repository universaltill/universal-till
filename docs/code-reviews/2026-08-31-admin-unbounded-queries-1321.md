# Code review — perf: admin unbounded queries (ut-docs#1321)

- **Date:** 2026-08-31
- **Branch:** `fix/1321-admin-unbounded-queries`
- **Reviewer:** independent reviewer, fresh-context Opus subagent, isolated
  worktree (`complexity:medium` → Opus review, per `scrum-master`'s model
  routing table)
- **Verdict: SAFE TO MERGE after fixing one blocking finding.** Initial
  review found it NOT safe to merge; the blocking finding and two of the
  non-blocking ones were fixed in this same branch and re-verified before
  merge.

## What shipped

Three of the four "unbounded queries / missing pagination" findings from
the 2026-08-30 principal-engineer performance audit
(`docs/code-reviews/2026-08-30-performance-audit.md`, section E, findings
15/16/18). Finding 17 (catalog admin's whole-table refetch on every
mutation) was judged too large/risky to bundle with these three contained
fixes and split off as its own follow-up card, **ut-docs#1363**.

1. **`POST /api/reports/eod/range`** (`internal/pages/eod_api.go`) had no
   maximum date-range bound, unlike its sibling
   `/api/reports/archive/export`. Reuses `data_api.go`'s existing
   `maxExportRange`/`maxExportRangeDays` (366 days, same package) rather
   than a new constant — both endpoints load the same heavy
   per-sale/sale_line/payment-row shape for the whole span.
2. **`GET /invoices`** (`internal/pages/invoice_page.go`,
   `internal/data/invoice_repo.go`) fetched every invoice ever issued on a
   bare page load, and summed totals in a Go loop. Now: (a) a bare load
   defaults `from` to the start of the current calendar month — `to`
   stays **open** (see the blocking finding below for why); (b) added
   `InvoiceRepo.Totals`, summing net/tax/gross in SQL (credit note
   negated, same sign convention as the Go loop it replaced) instead of
   marshalling every row into Go just to add them up.
3. **`ListLiveTrackedOrders`** (`internal/data/order_tracking_repo.go`,
   called from `internal/cloudsync/cloudsync.go`'s ~2min sync tick)
   fetched every sale that ever held a tracking token, filtering almost
   all of it out in Go via a `visible` callback. Added
   `terminalStatuses []string, terminalCutoff time.Time` parameters: a
   **terminal** row (collected/cancelled) is SQL-pruned unless its last
   status write (or `created_at`, if never updated) is within the cutoff;
   a **non-terminal** row (new/preparing/ready) is never bounded by the
   cutoff, matching `pos.OrderTrackingVisible`'s own tested rule
   ("preparing is live regardless of timestamp") exactly.
   `internal/data` cannot import `internal/pos` (pre-existing cycle,
   documented in the file), so the terminal-status list is passed in by
   the caller — now via the new `pos.OrderTrackingTerminalStatuses()`
   (added during review triage, below) rather than a second hardcoded
   copy.

New/changed tests: `internal/data/order_tracking_repo_test.go` (signature
update + `TestListLiveTrackedOrders_PrunesOldTerminalRowsInSQL`),
`internal/data/small_repos_test.go` (`TestInvoiceRepo_Totals`),
`internal/pages/eod_api_test.go` (`TestPostEODRange_DateRangeTooLarge` +
two regex-shaped-but-invalid-date cases), `internal/pages/invoice_page_test.go`
(`TestGetInvoices_DefaultsToCurrentMonthWhenNoFilterGiven` +
`TestGetInvoices_DefaultDoesNotHideTodaysInvoiceAcrossTimezones`).

## Independent review — what was found, and the fix-back

The review ran in an isolated worktree with no visibility into the
implementation's own reasoning, traced the diff against the pre-fix code,
and independently re-verified every TDD claim (revert/mutate → red →
restore → green) rather than trusting the implementer's word.

### BLOCKING (fixed) — `/invoices` default dropped invoices issued *today* on any non-UTC host

The first draft defaulted **both** `from` and `to` to local calendar
dates. `InvoiceRow.IssuedAt` is stored as UTC RFC3339 and
`InvoiceRepo.List`/`Totals` compare it **lexicographically**
(`invoiceRangeBound`) — whenever the local and UTC calendar dates
disagree at the request instant (any timezone west of UTC in the
evening; the first ~2h of a month in a timezone east of UTC, e.g. the
Germany pilot), a `to=today` bound silently excluded an invoice issued
minutes earlier. The reviewer proved this both directions with live
probes (`TZ=Pacific/Honolulu`, and a repo-level `List`/`Totals` call
against a `2026-08-31T22:30:00Z` invoice) before the fix landed. This
was a **newly introduced** regression — the pre-diff bare load was
open-ended, so it could never happen.

**Fix:** default only `from`; leave `to` open. `List`/`Totals` still get
the indexed range scan from `from` (the actual perf win — a shop's
entire history vs. "this month onward"), and a just-issued invoice can
never fall outside an open upper bound. Added
`TestGetInvoices_DefaultDoesNotHideTodaysInvoiceAcrossTimezones`, which
sets `TZ=Pacific/Honolulu` (UTC-10) and seeds an invoice issued at the
real current UTC instant — reproduces the reviewer's own scenario and
would fail if `to` were ever re-defaulted. Also asserts the `to` date
picker renders empty (`TestGetInvoices_DefaultsToCurrentMonthWhenNoFilterGiven`).

### Non-blocking, fixed

- **`eod/range`'s cap was bypassable via a regex-valid, unparseable
  date** (`eodDateRe` only checks the digit shape, not calendar
  validity — `to=9999-99-99` matched the regex, failed `time.Parse`, and
  the discarded error left both dates at the zero value, making
  `toDate.Sub(fromDate)==0` and silently skipping the cap). Not
  exploitable as shipped (SQLite's `date('9999-99-99')` is NULL, so the
  query matched zero rows regardless) but the guard's correctness rested
  on an unrelated SQLite quirk, not the code. Fixed to handle both parse
  errors explicitly with a 400, mirroring `data_api.go`'s identical
  from/to parse. Added two cases to `TestPostEODRange_ValidatesFromTo`.
- **Terminal-status list duplicated** — `cloudsync.go` hardcoded
  `{OrderStatusCollected, OrderStatusCancelled}` independently of
  `pos.OrderTrackingTerminal`'s own hardcoded pair; a third terminal
  status added later would silently stop being pruned (fails open, safe
  but ineffective) with nothing to catch the drift. Added
  `pos.OrderTrackingTerminalStatuses()` as the single source both
  `OrderTrackingTerminal` and `cloudsync.go`'s call site now share.
- **String-compared cutoff's format invariant was unstated** — the SQL
  bound compares `terminalCutoff` as TEXT, sound only because every
  writer of `order_status_updated_at`/`created_at` formats with
  `time.RFC3339` via `time.Now().UTC()` (no fractional seconds, always a
  literal `Z`). A future `RFC3339Nano` or offset-bearing writer would
  compare incorrectly at the boundary. Documented the invariant inline
  in `order_tracking_repo.go`.

### Non-blocking, deferred (not blocking, tracked separately)

- **Misleading comment about the CSV export** — an early draft's comment
  claimed the accountant CSV export "widens" the request past the
  default; in fact `web/ui/pages/invoices.html`'s export link
  interpolates the *same* defaulted `From`/`To` the page resolved, so a
  bare-load export is now narrowed from all-time to "this month onward"
  too (not "this month" — `to` is open, per the blocking fix above). The
  misleading comment is gone (rewritten as part of the blocking fix); the
  behavior itself — the accountant's default CSV export now starts at
  the current month rather than the shop's full history — is a
  deliberate, in-scope consequence of the perf fix, not a bug: the
  picker is visibly narrowed before the button is clicked, and widening
  `from` via the picker still reaches full history. Worth a plain-English
  mention in the manual's invoices topic (see below) but not a merge
  blocker.
- **`sales(tracking_token)`/`sales(order_status)` have no index** — the
  `ListLiveTrackedOrders` query still full-scans `sales`; the win shipped
  here is in rows *returned*, Go marshalling and push payload size, not
  the scan itself. A schema change is out of scope for this card.
- **Non-terminal tracked orders accumulate unbounded forever** — forced
  by `OrderTrackingVisible`'s own tested "stays live no matter how old"
  rule; a shop that abandons orders in `new`/`preparing` grows this set
  without bound. Product/schema question, not a bug in this fix.
- **Manual not updated for the `/invoices` default-window change** — no
  existing manual prose is now *false*, but CLAUDE.md's rule (a shop
  owner-visible behavior change gets the affected manual topic updated in
  the same branch) points at a one-line addition to
  `web/help/en/invoices.md`: the list (and its CSV export) defaults to
  the current month onward; widen the date pickers for older invoices.
  Added below, in this same branch.
- Finding 17 (catalog admin's whole-table refetch/re-render on every
  mutation) — confirmed `internal/pages/catalog/handlers.go` untouched by
  this diff, correctly split into **ut-docs#1363**.

### `ListLiveTrackedOrders` correctness trace (reviewer's own, recorded here for the permanent record)

Traced the new SQL against `pos.OrderTrackingVisible` by hand:

- **Non-terminal**: `order_status NOT IN (terminal)` is true, the `OR`
  short-circuits, the row is kept unconditionally — the cutoff never
  applies. Matches `OrderTrackingVisible`'s unconditional `true` for a
  non-terminal status exactly; nothing non-terminal can ever be pruned.
- **Terminal, parseable timestamp**: SQL keeps iff `ts >= cutoff`; Go
  keeps iff `now.Sub(at) <= 2h` ⇔ `at >= now-2h` = cutoff. Identical
  predicate, identical **inclusive** boundary (`pos/order_tracking_test.go`'s
  "collected at exactly the expiry visible" case). A currently-visible
  terminal order can never be pruned.
- **Terminal, NULL/empty timestamp**: SQL falls back to `created_at`
  (may keep or prune); Go's `time.Parse("")` errors → `false` → the
  callback drops it either way. No divergence, at worst a wasted fetch.

## Verification performed (implementer, post-fix)

| Check | Result |
|---|---|
| `gofmt -l .` | empty |
| `go build ./...` | pass |
| `go vet ./...` | pass |
| `go test ./internal/data/...` (full package) | pass, 86.9s |
| `go test ./internal/pages/...` (full package) | pass, 105.3s |
| `go test ./internal/cloudsync/...` (full package) | pass, 4.5s |
| `go test ./internal/pos/...` (full package) | pass, 5.7s |
| `go test ./...` (whole repo, `-count=1`, foreground, nothing concurrent) | pass, all packages |
| `bash scripts/ci/guard-data-access.sh` | pass |
| `bash scripts/ci/guard-i18n.sh` | pass |
| `bash scripts/ci/guard-compliance-claims.sh` | pass |
| `bash scripts/ci/guard-help-topics.sh` | pass |

**TDD re-verification (implementer, revert/mutate → red → restore →
green), independent of the reviewer's own):**
- `TestPostEODRange_DateRangeTooLarge` / the two new regex-shaped-invalid
  cases in `TestPostEODRange_ValidatesFromTo` — confirmed genuinely red
  against the pre-fix handler (full unbounded report body, 200 instead
  of 400).
- `TestGetInvoices_DefaultsToCurrentMonthWhenNoFilterGiven` /
  `TestGetInvoices_DefaultDoesNotHideTodaysInvoiceAcrossTimezones` —
  confirmed the timezone case is genuinely red against the *first draft*
  (both-ends-defaulted) version before the blocking fix.
- `TestInvoiceRepo_Totals` — reverting `Totals` is a compile error (the
  method wouldn't exist), which the reviewer already treated as
  insufficient proof and mutation-tested instead (un-negate the
  credit-note sign → red; drop the `COALESCE` → red on the NULL-sum
  case) — re-run here and confirmed identical results.
- `TestListLiveTrackedOrders_PrunesOldTerminalRowsInSQL` — same
  reasoning; reviewer's mutation tests (disable the prune entirely →
  red, `got 3 want 2`; flip the `OR` to `AND` → red, `got 0 want 2`)
  re-run and confirmed.

**A process note on the full-suite background runs during this cycle:**
two earlier `go test ./...` runs launched via `run_in_background` — one
deliberately overlapping this session's own git-stash-based TDD-revert
check, one launched around the same time as the independent review
subagent's own worktree setup — both reported the same two spurious
failures (`TestPostEODRange_DateRangeTooLarge`,
`TestGetInvoices_DefaultsToCurrentMonthWhenNoFilterGiven`) that could not
be reproduced by any isolated, `-count=1`, or foreground run with nothing
else touching the repo concurrently (confirmed clean repeatedly, before
and after `go clean -cache`). Recorded here rather than silently
discarded: root cause not fully confirmed, but every foreground/isolated
run — including the final whole-repo gate above — was unambiguously
green, and the reviewer's own independent run (in a separate, isolated
worktree) also reported all-green. Treated as an artifact of running
`go test ./...` concurrently with a deliberate mid-flight file
revert/build-cache access, not a defect in the shipped code.

No real client/shop name or secret-shaped literal in any new test data
("Jane Doe", "Old Customer", "Just Now Customer", "u-alice"). No file I/O
in this diff, so the file-write/`os.MkdirAll`/cwd-relative-path bug
classes don't apply (read-path / query-shape changes only).

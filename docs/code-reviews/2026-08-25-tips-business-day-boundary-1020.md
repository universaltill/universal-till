# 2026-08-25 — Tips report: business-day boundary bug + CSV formula injection (ut-docs#1020, items 2 & 6)

## What shipped

Two targeted fixes from the non-blocker follow-up backlog card ut-docs#1020
(filed by the independent review of the just-merged UK tip-allocation
feature, PR #521), addressing real, live bugs already on `main`:

1. **`workerAllocationDateRange` (item 6)** — converted a `reportWindow`'s
   `[From, To)` instant range into inclusive date strings by formatting
   `window.To.Add(-time.Second)` directly. Only correct when
   `reports.business_day_start` is midnight; with any other start (e.g.
   06:00) a `?period=day` report's range spanned two calendar days and a
   `?period=month` report spilled into the next month — every payout on the
   boundary day counted in two consecutive reports. **Fix**: map both ends
   through the existing `businessDateFor(t, hour, minute)` helper — the same
   boundary `parseReportWindow` itself already resolves its anchor date
   through.
2. **CSV export (item 2)** — `worker` (display name/username) and `note`
   (manager free text) were written to the export unescaped; a field
   starting with `=`, `+`, `-`, or `@` becomes a live formula when opened in
   Excel/Sheets, and this export's own help text frames it as for exactly
   that use. **Fix**: both fields now go through the existing `csvSafe()`
   helper already used by the catalog CSV export.

Not in scope here (still open on #1020): item 1 (export date-range cap),
item 3 (tips tab swallows query errors as "no data"), item 4 (hard 403
instead of a manager-PIN elevation path), item 5 (`GetUser` DB error
reported as bad input), item 7 (pre-existing UTC-hardcoded repo tests). Left
for a separate pass — this PR is scoped to the two reachable-by-anyone-
holding-`worker_allocation`-today items #1020 itself flagged as worth
prioritizing.

## Independent review (fresh-context Sonnet — this is a small, mechanical,
`complexity:easy`-shaped fix, not the medium/hard tier)

**Verdict: safe to merge, no defects found.**

- Confirmed `businessDateFor` is the correct, already-battle-tested helper
  (same one `parseReportWindow` uses for its own anchor) and traced all
  four period types × both midnight/06:00 boundaries by hand, including
  month→next-month and year→next-year rollovers.
- Explicitly checked the `?days=N` rolling-window path (doesn't go through
  the `?period=` switch) — also covered, since `workerAllocationDateRange`
  is called unconditionally.
- Grepped for other call sites of the old buggy pattern — none found.
- Confirmed the CSV export's own `from`/`to` link params are populated from
  this same corrected `From`/`To` (the template `reports_tab_tips.html`),
  so the fix is correct end-to-end, not just for the tab's own display.
- Verified `csvSafe()` is applied to exactly the two operator-controlled
  fields, and confirmed the other two text-shaped fields (`date`/
  `AllocatedAt`, `source_type`) are genuinely not attacker-controlled
  (server-derived / validated-enum at the one write path) and correctly
  left unwrapped.
- **Independently re-verified TDD**, not taken on trust: reverted just the
  two fix lines (test files kept), re-ran both new tests, confirmed they
  fail with the exact on-topic errors claimed; restored the fix and
  confirmed the diff was unchanged.

## Verified beyond automated tests

- `gofmt -l .`, `go build ./...`, `go vet ./...`: clean.
- `go test ./...` (full suite, not just this package): green.
- `guard-data-access.sh`, `guard-i18n.sh`, `guard-docs-shots.sh` (after
  `make docs-shots` — this diff touches `internal/pages/reports_page.go`,
  which the guard treats as app-surface regardless of visual relevance):
  all green.
- No UI-visible change (pure logic + CSV-writer fix), so no new
  screenshot/manual verification needed beyond the regenerated
  screenshot-freshness check above.

## Safe to merge

Yes. Both fixes are proven against the actual bugs they claim to fix (not
just asserted), independently re-verified by a fresh-context review that
reverted and re-ran the regression tests itself, and the full gate is
green.

## Explicitly deferred

ut-docs#1020 items 1, 3, 4, 5, 7 remain open — this PR does not close that
card, only its two most reachable items. Left a comment on #1020 noting
which items this PR addresses.

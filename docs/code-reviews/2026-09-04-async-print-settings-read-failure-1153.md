# Code review — async print/kitchen silently no-ops on settings-read failure (ut-docs#1153)

- **Date:** 2026-09-04
- **Branch:** `fix/1153-async-print-settings-read-failure`
- **Reviewer:** independent read via an Opus subagent (complexity:medium →
  reviewer runs at Opus, per the `reviewer`/`scrum-master` skills'
  model-routing table), no shared context with the implementation, run in
  an isolated worktree.
- **Verdict: SAFE TO MERGE.** No blocking findings. One cheap nit (extra
  doomed queries once a read fails) applied before merge; one residual
  same-class gap filed as a follow-up card (ut-docs#1533), out of this
  card's scope.

## What shipped

`printReceiptAsync`/`printKitchenAsync` (`internal/pages/print_api.go`,
`internal/pages/kitchen_print.go`) each read printer settings before
attempting a print, and discarded the read's error entirely — a genuine
DB fault (SQLite busy, disk error, expired context) was indistinguishable
from "printing switched off". That took the early "no attempt" return,
which skips the failure-recording code entirely: no audit row, no
`/orders` ⚠ warning. A shop's paid order could silently lose its
receipt/kitchen ticket with zero trace.

- Added `printerConfigChecked` (`print_api.go`) and
  `kitchenPrintingEnabledChecked` (`kitchen_print.go`) — checked variants
  that surface the first genuine read error.
  `data.SettingsRepo.Get` already distinguishes "key not set"
  (`ok=false, err=nil`) from a real read failure (`err!=nil`); this
  surfaces that distinction instead of discarding it.
- `printerConfig`/`kitchenPrintingEnabled` (used by their other 12/1
  call sites respectively) become thin wrappers that discard the error —
  unchanged signature, unchanged behavior for every other caller.
- `printReceiptAsync`/`printKitchenAsync` now route a genuine read
  failure through the existing `recordPrintFailureCtx()` + `InsertAudit`
  + `Set{Receipt,Kitchen}PrintFailed` path (the ut-docs#517a mechanism),
  instead of the silent no-op.
- New tests: `TestPrinterConfigChecked_SurfacesSettingsReadError`,
  `TestAsyncPrintFailureIsRecordedWhenSettingsReadFails` (forces a genuine
  error by dropping the `settings` table, not just leaving it empty —
  distinct from "unconfigured"), and
  `TestAsyncPrintNoFailureFlagWhenPrinterGenuinelyOff` (regression guard:
  a genuinely unconfigured printer must still take the silent path).

## Review findings

No correctness, concurrency, money, repository-pattern, or i18n issues.
The reviewer independently enumerated all 12 callers of `printerConfig`
(not just the 5 originally listed) and the 1 remaining caller of
`kitchenPrintingEnabled`, confirming all are unaffected. Confirmed no
double-audit risk (each error branch returns immediately) and that
`recordPrintFailureCtx()`'s fresh 5s context is the right choice here too
(an expired async ctx would otherwise drop the write, same reasoning as
the existing ut-docs#517a fix).

One non-blocking nit fixed before merge: `printerConfigChecked` was
still firing all 7 `Settings.Get` calls once the first one failed
(7x the DB errors logged per failed print, for no benefit — the async
callers only care whether an error occurred at all once `cfgErr != nil`).
Now short-circuits to the default on every key once `firstErr` is set.

One residual gap **filed as a new card, not fixed here**
(ut-docs#1533): `buildKitchenTargets` and `receiptDesignFromSettings`
still discard settings-read errors the same way, but narrower in
consequence (already gated behind `kitchenPrintingEnabledChecked`, or
purely cosmetic receipt formatting) — out of this card's stated scope.

**Operational note for the release/ops summary**: under heavy SQLite
contention, a shop with printing genuinely off can now surface a ⚠ on
`/orders` if a settings read happens to fault at exactly the wrong
moment. This is the deliberate "couldn't tell, so don't claim off"
semantics this card asks for, not a regression — worth one line in
release notes so it isn't mistaken for a new bug.

## Verification beyond automated tests

- `go build ./...`, `gofmt -l internal/pages/print_api.go
  internal/pages/kitchen_print.go internal/pages/print_api_test.go`,
  `go vet ./...` — clean.
- `go test ./...` (full repo, all packages) — green, ~113s.
- `go test ./internal/pages/...` — green, ~91s (both before and after
  the post-review short-circuit fix).
- Targeted `-race` run on print/kitchen/async tests specifically — clean,
  no data races.
- Relevant CI guards run locally and pass: `guard-data-access`,
  `guard-i18n`, `guard-page-http-error`.
- **A full `-race` run of the whole `internal/pages` package hits the
  default 10-minute `go test` timeout in this sandbox — reproduced
  identically on unmodified `main` (stashed this diff, re-ran, same
  hang in unrelated code paths).** Pre-existing, already tracked as
  ut-docs#1394; not this card's regression, not chased further here.
- **TDD claim independently re-verified by the reviewer, not just
  asserted:** hand-reverted only the two async early-return blocks to
  their pre-fix shape (keeping the new tests), confirmed
  `TestAsyncPrintFailureIsRecordedWhenSettingsReadFails` fails with the
  exact predicted symptom (`"...must flag the sale's receipt print, not
  silently no-op, got empty"`), confirmed the regression-guard test
  still passes both before and after, then restored the real fix
  byte-identical (`git diff --stat` unchanged) and confirmed all tests
  pass again.

Refs: ut-docs#1153, ut-docs#517a, ut-docs#1394, ut-docs#1533 (follow-up).

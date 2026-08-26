# Code review: hold cash tips out of CashReconciliation.CashSales (ut-docs#1046)

**Branch:** `fix/1046-cash-recon-hold-out-tips`
**Author:** autonomous SDLC pipeline (Dev: inline, Sonnet — `complexity:easy`;
Review: fresh-context Sonnet subagent, independent of the Dev reasoning,
per the `complexity:easy` review tier)

## The bug

`CashReconciliation.CashSales` (ut-docs#1006, the day-close cash-drawer
reconciliation) was taken straight from `EODMethod{Method:"cash"}.In`. Per
`EODTip`'s own doc comment (`internal/data/pos_repo.go`), `EODMethod.In` is
the **full tendered amount** — `payments.amount` already folds in any
`tip_amount` (migration 019) — so a cash tip would inflate `CashSales` and
therefore the drawer's `Calculated`/expected-cash figure by the tip amount.
That is exactly the revenue/tip commingling ut-docs#1007 already prevents
on the reporting side, just reproduced on the cash-drawer side instead.

The issue offered two ways to close this: fix the figure, or confirm (with
a test) that a cash tip is structurally impossible today and document that
as the invariant `CashReconciliation` relies on. **Checked before picking
one**: `pos.validatePayments` (`internal/pos/sales.go`) rejects a negative
`TipAmount` and rejects a tip on a `voucher` redemption, but nothing ties
`TipAmount` to a specific `MethodID` otherwise — a caller can send
`PaymentInput{MethodID: "cash", TipAmount: 50}` today and it validates and
persists. The "structurally impossible" premise doesn't hold at the API
layer; only the till's own UI declining to offer cash tipping keeps this
dormant. So the fix, not the invariant-and-test option, is the one that
survives contact with the actual code — a client-side-only gate is not a
guarantee.

## What shipped

- **`internal/data/pos_repo.go`** — `CashReconciliation` gains
  `TipsHeldOut int64`. `dateRangeSummary`'s single-day block now looks up
  the `"cash"` entry in `rep.Tips` (the same tip_amount-by-method query
  ut-docs#1007 already runs, computed earlier in the same function) and
  subtracts it from `CashSales`, storing the subtracted amount in
  `TipsHeldOut`. No new query — this reuses data already fetched for the
  report, so the two figures (Tips section, cash-reconciliation block)
  can't disagree with each other.
- **`internal/pages/eod_api.go`** — the printed Z-report's CASH
  RECONCILIATION block gains a "Tips held out" line, positioned right
  after "Cash sales", printed only when `TipsHeldOut != 0` — same
  omit-when-zero convention already used for GUTSCHEINE/STORNOS elsewhere
  in the same function. Never appears on a real report until cash tipping
  is actually turned on.
- **`web/help/{en,fa,tr,ar}/reports.md`** — the "Cash reconciliation on the
  day-end report" topic now mentions that cash sales excludes any cash tip
  and that a "Tips held out" line appears when one exists. Edited
  identically across all four locale files, matching this section's
  pre-existing state (it was already untranslated, verbatim English, in
  every locale before this change — not a gap introduced here).
  `web/help/img/manifest.json` regenerated via `make docs-shots` — only
  the `reports` topic's four locale hashes changed; no screenshot pixels
  changed (this is a printed-document change, not an operator-UI screen),
  confirmed by `guard-docs-shots.sh` passing before and after.
- No schema migration: `payments.tip_amount` already exists (migration
  019); this is a read-side fix only.

## Tests

- `internal/data/pos_repo_cash_recon_test.go`:
  - `TestEndOfDay_CashReconciliation_ExcludesCashTips` — seeds a cash
    payment (amount 420.00, `tip_amount` 20.00) and asserts `CashSales`
    comes out to 400.00 and `TipsHeldOut` to 20.00, and that the report's
    own `Tips` breakdown agrees.
  - `TestEndOfDay_CashReconciliation_ZeroTipsHeldOutWhenNoCashTip` — the
    ordinary no-cash-tip day: `TipsHeldOut` stays at its zero value and
    `CashSales` is untouched. Kept as a separate test from the tip-bearing
    one specifically so a regression that always subtracts something
    can't hide behind the happy path's totals matching by coincidence.
- `internal/pages/eod_test.go`:
  - `TestBuildEODDoc_CashReconciliation` extended to assert the "Tips held
    out" line is **absent** when `TipsHeldOut` is zero.
  - `TestBuildEODDoc_CashReconciliation_TipsHeldOut` (new) — asserts the
    line renders with the correct signed amount and sits inside the CASH
    RECONCILIATION block (not confused with the separate top-level TIPS
    BY PAYMENT METHOD section from #1007, which reports the same
    underlying figure independently).

**Re-verified personally, not taken on faith.** Temporarily removed just the
new tip-holding-out block from `dateRangeSummary` (leaving the struct field
and the rest of the fix in place) and re-ran
`go test ./internal/data/... -run TestEndOfDay_CashReconciliation`:

```
--- FAIL: TestEndOfDay_CashReconciliation_ExcludesCashTips (0.18s)
    pos_repo_cash_recon_test.go:329: CashSales: want 40000 (tip held out), got 42000
    pos_repo_cash_recon_test.go:332: TipsHeldOut: want 2000, got 0
--- PASS: TestEndOfDay_CashReconciliation_ZeroTipsHeldOutWhenNoCashTip (0.18s)
```

The tip-bearing test fails with exactly the expected/got mismatch (real
failure, not a tautology); the zero-tip test — which would catch the
inverse mistake, a bug that subtracts unconditionally — correctly keeps
passing since it doesn't depend on the removed block. Restored via `git
checkout -- internal/data/pos_repo.go` and re-ran: both pass again, and
`git status`/`git diff` confirm the working tree came back byte-identical
to the commit before this revert-and-restore.

## Gate run

- `gofmt -l .` — clean.
- `go build ./...` — clean.
- `go test ./internal/data/... ./internal/pages/...` (targeted, including
  every new/changed test above) — clean.
- Full `go test ./... -race` — every package passed except
  `internal/plugins`, which panicked with `test timed out after 10m0s`
  mid-`TestHostHTTPRepeatSameRequestBothReachServer` (a wazero WASM
  compile under `-race`, unrelated to this diff — this branch never
  touches `internal/plugins`). Same class of pre-existing sandbox
  resource-contention issue already tracked as ut-docs#1119
  (`internal/pages` hitting the identical 10-minute `-race` timeout on
  its own WASM fiscal-sign tests when the full suite runs concurrently).
  Verified it is a timeout artifact, not a real failure or anything this
  diff caused: re-ran just that test in isolation —
  `go test ./internal/plugins/... -run TestHostHTTPRepeatSameRequestBothReachServer -race -timeout 20m -v`
  — and it passed cleanly in 19.64s.
- All CI-blocking guards under `scripts/ci/` relevant to this diff:
  `guard-data-access.sh` (no SQL added outside `internal/data`),
  `guard-i18n.sh` (no new user-facing template strings — the Z-report
  footer lines are fixed-vocabulary printed-document text, same
  established convention as GUTSCHEINE/STORNOS, not routed through `T()`),
  `guard-help-topics.sh`, `guard-docs-shots.sh` — all pass.

## Non-goals confirmed unchanged

No new cash-tipping feature or toggle was added — this is purely the
reconciliation math staying correct if/when cash tipping is ever turned
on, per the issue's own non-goals section.

## Scope not touched

`Calculated`/`Counted`/`Variance` are unaffected — those come from each
shift's own `expected_cash`/`closing_cash` counts (`CashReconciliationForLocalDay`),
which already reflect whatever cash — tip included — physically sat in the
drawer; only the reporting figure `CashSales` needed the tip held out.

## Independent review

Spawned a fresh-context Sonnet subagent, `isolation: "worktree"`, briefed
with the diff scope, the relevant `CLAUDE.md` rules, and an explicit
instruction to run things rather than just read — including its own
independent revert-then-restore TDD re-verification, not trusting this
record's claim on the implementer's word.

**Verdict: SAFE TO MERGE.** It independently confirmed: `gofmt`/`build`/
`vet` clean; the targeted test suite green; its own revert-then-restore of
`dateRangeSummary`'s tip-subtraction block reproduced the exact
`CashSales`/`TipsHeldOut` mismatch above, then passed again after
restoring, tree clean; all four guards pass; `rep.Tips` is populated
(`pos_repo.go:1949-1968`) before the cash-reconciliation block that reads
it (`1990-2025`) — no ordering bug; `TipsHeldOut int64` matches the file's
established plain-`int64`-minor-units convention (this is the DB/DTO
boundary the money-type rule already carves out, not a violation); the
`tips_held_out` JSON tag is correctly snake_case; the footer line is
correctly gated and positioned; no real client/shop name or secret-shaped
literal; this record's own claims all checked out against its independent
re-run.

**One real, correctly-scoped-out finding, not blocking this diff**: once
cash tipping is ever turned on, the printed CASH RECONCILIATION section's
own arithmetic won't visibly close — `Calculated`/`Counted` (from
`SumCashPaymentsForShift`) still include the physically-counted tip cash
by design (correct — it really is in the drawer), while `CashSales` now
deliberately excludes it, leaving a `TipsHeldOut`-sized gap between "Cash
sales + Pay-ins + Pay-outs + Skim" and "Calculated" with nothing on the
receipt or in the help topic explaining why. Not reachable today (no UI
path enables cash tips) and out of scope for this card's own non-goals
(no new cash-tipping feature here) — filed as a follow-up Backlog card,
ut-docs#1124, for whoever turns cash tipping on.

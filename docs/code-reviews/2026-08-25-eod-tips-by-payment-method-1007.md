# Code review: Day-close tips by payment method (ut-docs#1007)

**Date:** 2026-08-25
**Author:** Dev subagent (Sonnet), orchestrated by the autonomous pipeline
**Reviewer:** independent subagent, Opus (different model from the author,
per the pipeline's `complexity:medium` routing)

## What shipped

`EODReport` gains a `Tips []EODTip` field (`EODTip{Method, Count, Amount}`,
raw minor units at this DTO/data-layer boundary, matching the existing
all-`int64` convention on `EODReport`/`EODMethod`). A new aggregation query
in `data.POSRepo.dateRangeSummary` — the single function backing both
`EndOfDay` and `EndOfDayRange` — sums `payments.tip_amount` grouped by
`method_id`, using the exact same join/date-range predicate shape as the
sibling `EODMethod` query (same `payments p JOIN sales s`, same
`s.status = 'completed'`, same `date(s.created_at, 'localtime') BETWEEN`).
Only methods with at least one tipped payment produce a row, mirroring
`EODMethod`'s own convention.

The printed Z-report (`buildEODDoc`, `internal/pages/eod_api.go`) grows a
"TIPS (held out of revenue)" footer section, printed only when
`len(rep.Tips) > 0`. Help manual topics (`web/help/{en,tr,fa,ar}/reports.md`)
and `README.md` were updated in the same change.

No schema migration was needed — `payments.tip_amount` (migration 019) and
`payments.tip_recipient` (migration 061, ADR-0061) already existed; this
card only adds reporting on top of data already captured.

## What was verified beyond automated tests

- **TDD confirmed independently, twice** — once by Tester (this session)
  and once by the independent Reviewer subagent, each in isolation: the
  new SQL aggregation was reverted, `TestEndOfDay_TipsByMethod` was
  re-run and failed with a real assertion mismatch (not a compile error),
  then the fix was restored and the test passed again. The Reviewer did
  this a second time, independently, inside its own isolated git worktree
  (`isolation: "worktree"`, per ut-docs#386's shared-checkout mitigation)
  so its revert never touched the orchestrator's live checkout.
- **Mutation testing on the query's WHERE clause**: `tip_amount > 0` →
  `tip_amount >= 0` was confirmed to break the test (a spurious zero-tip
  CASH row appears); the guard rendering condition in `buildEODDoc`
  (`len(rep.Tips) > 0`) was also mutation-tested. Both proved the tests
  are real, not tautologies.
- **Predicate consistency** between the new Tips query and the sibling
  EODMethod query was checked character-for-character (Reviewer) — they
  cannot silently disagree about which payments/sales they count.
- Full `internal/data` and `internal/pages` package test suites run
  clean (not just `-run` filtered), `gofmt`, `go build ./...`,
  `go vet ./...`, and the CI-blocking guards relevant to this diff
  (`guard-data-access.sh`, `guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-compliance-claims.sh`) all pass.
- A rendered Z-report was inspected by the Reviewer for a realistic
  scenario (5 sales, £819.30 revenue, £3.20 tips) to confirm the printed
  output is internally consistent with the issue's own golden numbers
  (card 814.30 + tip 3.20 = terminal total 817.50).

## Independent review findings (Opus, single round — no blocker-class
issue was found, so no second round was earned per the pipeline's
process-depth rule)

All **should-fix** findings below were fixed in this same round before
merge:

1. **Doc-comment inaccuracy** — the original `EODTip` comment implied tips
   are absent from `EODMethod.In`; in fact `EODMethod.In` is the full
   tendered amount (sale + tip) and already includes tips — `Tips` is an
   *additional* breakdown, not a carve-out of `Methods`. What tips are
   genuinely excluded from is *revenue* (`Gross`/`Net`/`TaxNet`, which
   come from `sale_lines`, never from `payments`). Fixed: the doc comments
   on `EODTip` and the query itself now state this precisely, so a future
   DATEV/ledger-posting change doesn't double-count a tip by posting both
   `Methods.In` to revenue and `Tips` to the 1363 account.
2. **Help-doc overclaim** (all four locales) — the original wording
   implied a card tip is captured "even when the shop's terminal has
   tipping prompts turned off," worded as if capture were automatic
   regardless of the terminal. In reality `tip_amount` is only populated
   when a payment actually carries one (via a payment plugin's authorize
   response, a direct `/api/pos/tender` POST, or LAN sync replay) — there
   is no separate operator-facing tip capture path. Reworded in all four
   locales to describe what's actually true: the line appears for any day
   with at least one payment that recorded a tip.
3. **Reconciliation note added** — the new Z-report tips line and the
   existing "Tips tab" → "Received" figure (`WorkerAllocationsSummary`)
   can legitimately disagree: the Z-report line counts every tipped
   payment regardless of recipient, while "Received" only counts tips
   recorded for the employee (the default per ADR-0061). Both bullets now
   say so explicitly, in all four locales, rather than reading as two
   views of the same number.
4. **Migration-number nitpick** — comments citing "migration 061" for
   `tip_amount` were corrected to migration 019 (`tip_recipient` is 061).

**Accepted, not fixed — noted here rather than silently dropped:**

- **No recipient dimension on `EODTip`.** ADR-0061 persists
  `tip_recipient` specifically so a later report can read it as captured;
  this report doesn't yet split by recipient. Not a blocker: the AC's
  operative clause ("core must not hardcode one [tip policy] behaviour")
  is satisfied by hardcoding none, and the DATEV/ledger-posting AC item
  is out of scope for this repo entirely (see below). Flagged because
  `EndOfDay`'s archive table (`ArchiveReport`, write-once,
  `ON CONFLICT DO NOTHING`) means a recipient split added later won't
  backfill already-archived days — recoverable from raw `payments`, but
  worth remembering rather than discovering.
- **`EndOfDayRange` (multi-day) has no dedicated test exercising `Tips`.**
  Real but low-materiality gap — Tips rides the same `dateRangeSummary`
  function and `BETWEEN` predicate already covered by range tests for
  `EODMethod`/`Gross`.
- **Latent, currently unreachable**: the tips query has no `sale_type`
  filter (unlike `EODMethod`, which splits In/Out on it). Today every
  tipped payment is a `completeTender` `'sale'` row — the refund path
  never sets `TipAmount` — so this is equivalent to `EODMethod`'s split in
  practice. If a returned sale ever carries its own tipped payment, this
  query would add it rather than net it; the code comment now states this
  explicitly as a "revisit then, not speculatively now" note instead of
  an unqualified claim.

**Explicitly out of scope for this repo (per the issue's own AC, and
independently confirmed by the Reviewer via a fresh codebase search):**

- **DATEV export / posting to ledger account 1363.** No DATEV/
  Buchungsstapel/SKR03/SKR04 implementation exists anywhere in
  `universal-till` — it lives in the separate `ut-plugin-tax-de` repo
  (`ut-docs/reference/feature-catalogue.md`). `README.md`'s "Confirmed not
  built yet" section names the gap, the account, and the owning repo.
- **A new "cash tips off by default" policy toggle.** The issue's
  "correction" paragraph is pilot context, not a request for a new core
  policy field — `ChargePolicy.TipDefaultRecipient` (ADR-0061 Decision 1/3)
  already owns this policy surface, and the AC's prohibition ("core must
  not hardcode one behaviour") argues *against* adding a core-side toggle,
  not for it.

## Safe-to-merge verdict

**Yes**, after the should-fix items above were applied and the full gate
(build/vet/test/guards) re-run clean. No blocker-class (money/tax/data
loss/security) finding was raised, so per the pipeline's process-depth
rule this stays a single review round, scoped to the fixes above — not a
full re-review of the diff.

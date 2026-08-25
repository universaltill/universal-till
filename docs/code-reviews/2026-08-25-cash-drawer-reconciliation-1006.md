# Code review: Day-close cash-drawer reconciliation (ut-docs#1006)

- **Branch**: `feat/1006-cash-drawer-reconciliation`
- **Dev**: Fable subagent (complexity:hard)
- **Independent review**: Opus subagent, isolated worktree
- **Verdict**: safe to merge after fixes below (initial review verdict was
  **no** — one blocker; re-verified green after fixes)

## What shipped

German-pilot day-close cash-drawer reconciliation, built on the existing
Shifts feature rather than parallel machinery:

- `internal/db/migrations/067_shift_cash_reconciliation.sql` — `shifts.new_float`,
  `shifts.count_protocol` (nullable, additive), mirrored onto `shifts_archive`
  with `reset_archive_repo.go`'s column list and `rewindShiftCashRecon067`
  extended in lockstep.
- `internal/pos/shifts.go` — `CloseShift` accepts `Skim`/`SkimReason`/
  `CountProtocol`/`SkimApproverID`; computes `expected_cash` **before** any
  skim effect (variance is checked against the count before the skim, per
  the reference block); persists `new_float = counted − skim`; writes the
  skim as a second `cash_adjustment` audit row inside the same transaction
  as the close (bypassing `RecordCashAdjustment`, which requires an open
  shift). `RecordCashAdjustment` accepts type `skim`.
- `internal/data/pos_repo.go` — `CashReconciliation` (day-scoped,
  `date(closed_at,'localtime')` per ADR-0057) wired into `EndOfDay` only.
  `LastClosedShiftCarryForward`/`LastClosedShiftNewFloat` for the opening-
  float carry-forward.
- `internal/pages/shifts_api.go` — `ShiftOpenRequest.OpeningCash` is
  `*int64` so an omitted field (⇒ carry forward) is distinguishable from an
  explicit `0` (⇒ respected). Close handler wires skim/reason/count
  protocol.
- `internal/pages/eod_api.go` — CASH RECONCILIATION section on the printed
  Z-report, `!!` flag on non-zero variance.
- UI: `web/ui/pages/shifts.html` (skim + reason + optional denomination
  grid on close, carry-forward prefill on open), `web/ui/partials/
  reports_tab_eod.html` + `reports_page.go` (⚠ tag on report rows with
  variance).
- Golden reference-block test (`internal/pages/eod_test.go`,
  `internal/data/pos_repo_cash_recon_test.go`) reproduces the de-identified
  epic figures (ut-docs#1002) exactly: opening £100.00, cash sales
  £411.10, calculated/counted £511.10, variance £0.00, skim −£411.10, new
  float £100.00.
- **Deliberately deferred**: no TSE/fiscal gating added to the skim path —
  ut-docs#998 is a separate, still-open `status:admin-review` question
  about whether `RecordCashAdjustment` should get the ADR-0048 hard gate;
  this card correctly does not decide that question either way.
- **Known, already-flagged gap**: the ar/fa/tr locale/help strings this
  card adds are English fallback, not machine-translated — the self-hosted
  Ollama endpoint was unreachable from this cloud pipeline's environment.
  Key parity holds (`guard-i18n.sh` passes); a follow-up Backlog card
  (`blocked:env`) is filed for re-verification against the NAS Ollama
  pipeline, the same pattern already used for #996/#991/#982/#978/#960/#957.

## Independent review findings and resolution

An Opus subagent reviewed the diff in an isolated git worktree (build/
vet/full test suite/all relevant guards re-run independently, plus its own
mutation testing and a live revert→run→restore TDD re-verification). It
found one blocker and several should-fix items; none were dismissed —
every finding below was fixed and re-verified by the orchestrator.

1. **BLOCKER — skim bypassed the manager-PIN gate.** The close-flow skim
   path wrote a negative `cash_adjustment` audit row (cash leaving the
   drawer — exactly the class ut-docs#266's sign-based gate exists for)
   with no PIN check at all, and attributed it to the shift's cashier, not
   an approving manager. Proven live by the reviewer: identical cash
   movement went through `/api/shifts/adjustment` (403, PIN required) and
   `/api/shifts/close` (200, no PIN).
   **Fix**: `ShiftCloseInput` gained `SkimApproverID` (required whenever
   `Skim > 0`, enforced in `CloseShift` itself, not just the handler); the
   HTTP handler runs the same `AuthorizeManager` sign-based gate
   `RecordCashAdjustment` uses, only when `req.Skim > 0`, and the
   *approving manager* — not the cashier — becomes the skim audit row's
   actor. New tests: `TestCloseShift_SkimRequiresManagerPIN`,
   `TestCloseShift_SkimWithManagerPINRecordsManagerAsActor`, plus a
   domain-layer `"skim with no approver"` case in
   `TestCloseShift_SkimValidation`.
2. **SHOULD-FIX — the shift-close Note field was dropped from the UI**
   (collateral damage, not a decision — `note` is still read/persisted
   server-side but nothing submitted it). **Fix**: restored the input in
   `shifts.html`.
3. **SHOULD-FIX — printed "New float" was wrong on any day with 2+ shifts
   on the same register.** It was summed across every close that day, but
   a register's drawer only ever holds its LAST close's new float — two
   closes at £60 and £5 printed £65 while the drawer held £5, contradicting
   the till's own carry-forward behavior. **Fix**: `CashReconciliationForLocalDay`
   now sums, per register, only the row with the latest `closed_at` that
   day (a `ROW_NUMBER() OVER (PARTITION BY register_id …)` window query —
   this codebase already uses window functions, see
   `related_items_repo.go`). Opening/Counted/Calculated/adjustments stay
   additive across every close, as they should. Test rewritten
   (`TestCashReconciliationForLocalDay_MultipleShiftsAndAdjustments`) to
   cover both a register with two same-day closes (only the latest counts
   for NewFloat) and a second register (additive across registers).
4. **SHOULD-FIX — the ADR-0042 archive-twin invariant for the two new
   columns was untested.** Reverting `reset_archive_repo.go`'s extended
   `shifts` column list left the full suite green — a real, silent
   data-loss bug on a go-live reset/restore would have shipped undetected.
   **Fix**: added `TestResetThenRestoreRoundTrip_ShiftNewFloatCountProtocol`
   (`internal/data/reset_test.go`), mirrored on the existing
   `…_SaleTrackingToken` test's shape. Mutation-tested: reverting the
   column list makes this new test fail with a NULL-scan error; restored,
   it passes.
5. **SHOULD-FIX — the ADR-0057 `localtime` day boundary was not pinned in
   the new tests.** The original `cashReconDay` helper hardcoded both the
   seeded timestamps and the expected day string as fixed UTC literals
   (`"2026-03-10"`) — exactly the mistake ut-docs#869/#559 already found
   and fixed elsewhere in this package (removing `'localtime'` from the
   query left the tests green). **Fix**: rewrote the test file to anchor
   on host-local noon (`cashReconAnchor()`, not a hardcoded date) and
   derive every "day" argument via `b8ExpectedDay`'s
   `date(?,'localtime')` control query against the real DB, the exact
   pattern `eod_zreport_local_day_869_test.go` documents and uses.
6. **SHOULD-FIX — user-input errors returned HTTP 500, and
   `count_protocol` validation was too weak.** `skim > closing_cash` and a
   malformed `count_protocol` fell through to the handler's generic 500
   mapping; `json.Valid` alone accepted any JSON value (a bare string, a
   number, an array), not just the documented flat denomination:count
   object, with no size bound. **Fix**: handler pre-validates both and
   returns 400; `pos.ValidCountProtocol` now requires a flat JSON object of
   non-negative integer counts, capped at 4096 bytes. New test:
   `TestCloseShift_SkimAndCountProtocolValidationAre400`.
7. **NIT — denomination grid hardcodes GBP** on a German-pilot feature.
   Deferred: filed as a follow-up Backlog card (currency-aware
   denomination sets are a bigger, genuinely separate task, and the count
   protocol is explicitly optional in the acceptance criteria).
8. **NIT — `count_protocol` is write-only** (never displayed back to an
   operator). Deferred to the same follow-up card as #7 — thin but not a
   correctness issue, and out of this card's bounded scope.
9. **NIT — the close form's HTML response dropped skim/new-float**
   (the JSON path had them, the HTML path an operator actually sees did
   not). **Fix**: `respondCloseSuccess` now includes both when a skim was
   recorded.

## Verified beyond automated tests

- Tester drove the real running app end to end (built `bin/unitill-pos`,
  seeded a shift, closed with a skim, ran the EOD report) and reproduced
  the exact reference figures live, before the review found finding 1 —
  re-verified after the fix that a skim now genuinely requires (and
  records) manager authorization via the same handler test harness.
- Independent reviewer's own mutation testing: sign-flip on variance,
  dropped skim-subtraction, dropped the `skim > closing_cash` bound, all
  caught by the test suite.
- Orchestrator's own mutation testing (post-fix): reverted the
  `reset_archive_repo.go` column-list fix — new archive round-trip test
  fails with a real NULL-scan error; restored, passes. Reverted the
  `new_float` window-function fix conceptually (confirmed via the
  rewritten multi-shift test asserting 1100, not the old buggy 6500).
- Full `go test ./...` green, `gofmt -l .` empty, `go vet ./...` clean, all
  16 CI-blocking guards pass (`guard-data-access`, `guard-i18n`,
  `guard-help-topics`, `guard-docs-shots` — regenerated via `make
  docs-shots` after the `shifts.html` note-field restoration —
  `guard-compliance-claims`, `guard-kiosk-engine`,
  `guard-plugin-menu-read`, `guard-webkit-version`,
  `guard-kiosk-launch-flags`, `guard-android-status-address`,
  `guard-android-i18n`, `guard-emoji-font`, `guard-htmx-loaded`,
  `guard-autofill-suppression`, `check-brand-assets`,
  `guard-makefile-version`).

## Explicitly deferred (filed as separate Backlog cards)

- Re-verify the ar/fa/tr strings this card adds against the NAS Ollama
  pipeline (`blocked:env`, matching #996/#991/#982/#978/#960/#957).
- Currency-aware denomination grid + surfacing the stored count protocol
  back to an operator (nits 7/8 above).
- Browser-level visual check (dark theme, RTL/fa layout, longest-string
  overflow) for the new skim/denomination-count form fields — Tester
  inspected the raw rendered HTML for structural correctness but did not
  drive a real browser session for this specific surface; lower risk than
  the ut-docs#300 precedent (a small addition to an existing card form,
  not a new complex layout) but genuinely unverified.

# Code review: worker_allocations ledger primitive (ADR-0063 step 1/2, ut-docs#987)

**Branch:** `feat/987-worker-allocations-ledger`
**Reviewer:** independent Opus subagent, fresh context (complexity:medium →
Opus review, per scrum-master's model routing) · **Author:** Sonnet (this
pipeline cycle)

## What shipped

The shared record-keeping primitive behind two independent statutory
obligations — UK Employment (Allocation of Tips) Act 2023 (ut-docs#964) and
Turkey İş Kanunu 4857 art. 51 "yüzde usulü" (ut-docs#965) — per ADR-0063.
Neither obligation is about how much is collected or taxed
(`payments.tip_amount`/`sale_charges` already cover that); this is about
what happens to the money afterward: it must be paid out to named workers,
and the payout must be provable later.

- New migration `064_worker_allocations.sql`: `worker_allocations` +
  `worker_allocations_archive` twin (ADR-0042 §1 pattern — no FK, no
  PK/UNIQUE on the archive twin, NOT NULL kept), indexed on
  `cashier_id`/`(source_type, source_id)`/`allocated_at`.
- `data.WorkerAllocation` type + `InsertWorkerAllocation`, a narrow repo
  method (not a wider `InsertSale`/`InsertPayment` signature per #976's
  existing risk).
- `worker_allocations` added to `reset_archive_repo.go`'s ordered table
  list (no FK to sales/payments, so its position is not load-bearing).
- `WorkerAllocationsSummary`: one shared received-vs-allocated query,
  filterable by date range/`cashier_id`/`source_type`, that both #964's and
  #965's step-2 consumers each apply their own `source_type` filter to.
  `service_charge` received deliberately returns 0 pending `sale_charges`
  (ADR-0062, ut-docs#984, not yet merged) — documented in the code.

## Independent review — first round: needs fixes

Full independent pass (different model, fresh context): schema verified
column-for-column against ADR-0063 Decision 1; archive-twin relaxations
verified against ADR-0042 §1 and `040_reset_archive.sql`'s own header rule;
`reset_archive_repo.go` integration verified order-independent (no FK to
any live table, so neither the archive loop nor the reverse restore loop
can trip); all SQL confirmed parameterized; money convention confirmed
matching `InsertPayment`/`InsertSaleDiscount` (raw `int64` minor units at
the data-layer boundary, not `money.Money` — that type is for the domain
layer). `go build`/`go vet`/`gofmt -l` clean, `guard-data-access.sh` clean,
`go test ./internal/data/... -count=1` green.

**TDD re-verified independently, not taken on trust**: the reviewer
mutated `SUM(p.tip_amount)` → `SUM(p.amount)` and confirmed
`TestWorkerAllocationsSummary_Tip` failed; removed the `worker_allocations`
entry from `resetArchiveTables` and confirmed
`TestWorkerAllocationsArchiveRoundTrip` failed. Both reverted, suite
green again.

But the read side, `WorkerAllocationsSummary`, is where the ADR itself
calls the received-vs-allocated comparison "the compliance check" — and
the first round found that number was wrong in three independent ways,
plus untested in ways that let all three ship unnoticed.

## Findings — all fixed in this same round

- **F1 (real bug)**: the "tip" received-sum ignored `payments.tip_recipient`
  (`'employee'` | `'business'`, ADR-0061 Decision 3, set per shop from
  `ChargePolicy.TipDefaultRecipient`). A `'business'`-retained tip was
  never owed to a worker, so counting it as "received" manufactured a
  permanent, monotonically growing phantom shortfall against Allocated on
  any shop configured that way. The test fixture hardcoded
  `tip_recipient='employee'`, so nothing could have caught this. **Fixed**:
  added `AND COALESCE(p.tip_recipient, 'employee') = 'employee'`.
  Regression test seeds one `'business'` tip and asserts it's excluded.
- **F2 (real bug)**: bare `date(...)` instead of `date(..., 'localtime')`
  on all three range comparisons, while the doc comment claimed to match
  `dateRangeSummary`'s convention — it didn't. `dateRangeSummary`'s
  `'localtime'` wrapping is the documented fix for ut-docs#869: a bare UTC
  `date()` match "silently aggregates the wrong calendar day" on a
  non-UTC host, and both this ledger's actual markets (Turkey UTC+3, UK
  UTC+0/+1) are non-UTC. **Fixed**: `date(allocated_at, 'localtime')` /
  `date(p.paid_at, 'localtime')` at all three sites; doc comment corrected
  to state the convention truthfully.
- **F3 (real bug)**: no `s.status = 'completed'` filter on the tip received
  query. `pos.UpdateSaleStatus` flips a sale to `voided`/`refunded` without
  deleting its `payments` rows, so a voided sale's tip stayed in the sum
  forever — another false shortfall. **Fixed**: added the status filter.
  Regression test seeds a voided sale's tip and asserts exclusion.
- **F4 (real bug, semantic)**: the type doc stated "comparing the two
  fields IS the compliance check." It isn't, as written: Received and
  Allocated are measured on different clocks (`paid_at` vs `allocated_at`,
  and ADR-0063 Decision 1 explicitly allows `allocated_at` to postdate the
  payment — a shift-end payout the next day) and, when `cashierID` is set,
  different people (who rang the sale vs who received the payout). A
  routine same-day payout lag would have rendered as a false violation in
  a step-2 consumer. **Fixed**: doc comment rewritten to state the clock/
  identity mismatch plainly and warn that the `yuzde_usulu_pool` equality
  is a tautology by construction, not a passed check.
- **F5 (test gap)**: the reviewer neutralized the `source_type` filter
  (`WHERE ? IS NOT NULL`) and widened the date range by 198 days — all 5
  original tests still passed, because every test seeded exactly one
  `source_type` inside the range. This directly undercut the card's own
  stated test requirement ("proving the same query path serves both
  without special-casing"). **Fixed**: both summary tests now seed an
  extra same-day row of the *other* source_type and an extra out-of-range
  row, and assert both are excluded.
- **F6 (nit, fixed anyway — migration still editable)**: the write side
  accepted any `source_type` string; only the read side rejected unknown
  values, so a caller typo wrote a statutory record silently invisible to
  every report forever. **Fixed**: `CHECK (source_type IN ('tip',
  'service_charge', 'yuzde_usulu_pool'))` on both the live table and the
  archive twin (single-table CHECKs are a relaxation ADR-0042 §1 keeps).
  Regression test confirms the CHECK rejects an unknown value at insert.
- **F7 (nit, fixed anyway)**: `restoreEmptyCheckTables` (the four live
  tables that must be empty before `RestoreResetBatch` runs, per ADR-0042
  §2 "never a merge") omitted `worker_allocations`. A `yuzde_usulu_pool`
  payout needs no sale/shift/held-sale row of its own, so a post-reset
  pool distribution could exist while all four other checks pass, and
  restore would then merge the archived batch in alongside it. **Fixed**:
  added `worker_allocations` to the list.
- **F8 (doc-only)**: three code/migration comments repeated ADR-0063's own
  garbled phrasing ("outlives a reset ... the same way sale_charges does
  not [outlive a reset]") — the actual behavior is the opposite
  (`worker_allocations` IS archived and cleared on reset, same as every
  other table). **Fixed**: reworded to state the real reason for no FK
  (a soft reference can't break/block on an archived/removed row).
- **F9 (traceability note, not a defect)**: the implementation
  deliberately does not join Received to Allocated by `source_id`, unlike
  ADR-0063 Decision 2's literal wording — a join would only count a
  tip/pool that already has an allocation row, making Received ≤
  Allocated by construction and hiding the exact shortfall this report
  exists to detect. Reviewer confirmed this is the better choice; recorded
  in the doc comment rather than left as a silent departure from the ADR's
  text.

## Real gate failure found independently (not a review finding — caught by
## running the actual CI command, not just `go test ./...`)

Running the exact CI-equivalent command (`go test $(go list ./... | grep
-v '/internal/plugins$')`, as opposed to a plain `go test ./... -race`)
surfaced `internal/pages` test failures: `TestResetTransactions_*` and
`TestResetArchives_*` failed with `no such table: worker_allocations_archive`.
Root cause: `internal/pages/ui_smoke_test.go`'s `seedForPages` fixture is a
hand-rolled schema (not real migrations, for test speed), and this diff's
addition to `reset_archive_repo.go`'s unconditional `resetArchiveTables`
list broke every handler test using that fixture — a real drift-detection
gap the fixture's own file header already documents as a known risk class
("a drifted fixture here would let a test pass against a schema that
doesn't match production" — here it was the reverse: the fixture didn't
match the new production schema at all). **Fixed**: added both
`worker_allocations` and `worker_allocations_archive` (CHECK constraint
included) to the fixture, column/constraint-identical to the migration.

Also ruled out as unrelated: a plain `go test ./... -race` run showed
`internal/plugins` failing at its default 600s per-package timeout mid
WASM-module-compile. This is pre-existing and already documented
(`ci.yml`'s own comment cites ut-docs#643/#753/#776): CI runs that package
separately, without `-race`, with a 20-minute timeout, specifically
because of this. Re-ran with `go test -timeout 20m ./internal/plugins`
(CI's actual command) — green.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l` on every touched file: clean.
- Full `go test $(go list ./... | grep -v '/internal/plugins$')`: green.
- `go test -timeout 20m ./internal/plugins` (CI's own command for that
  package): green.
- All 16 CI-blocking guards run locally: green (`guard-data-access`,
  `guard-kiosk-engine`, `guard-plugin-menu-read`, `guard-i18n`,
  `guard-compliance-claims`, `guard-docs-shots`, `guard-help-topics`,
  `guard-webkit-version`, `guard-kiosk-launch-flags`,
  `guard-android-status-address`, `guard-android-i18n`,
  `guard-emoji-font`, `guard-htmx-loaded`, `guard-autofill-suppression`,
  `check-brand-assets`, `guard-makefile-version`).
- No SQL outside `internal/data`/`internal/db` (repository-pattern rule) —
  confirmed both by `guard-data-access.sh` and manual read of every
  touched file.
- This card is explicitly non-UI (data-layer + report query only, per its
  own stated non-goals) — no i18n keys, no help-topic, no README change
  needed.

## Safe-to-merge verdict

**Yes**, after the fixes above. Second review round not warranted: the
first round's findings (three real bugs in the received-side query, one
test gap, two nits, two doc-only) were all fixed directly in this same
pass, scoped exactly to what was flagged — not a re-review of the whole
diff, per this pipeline's "earn a second round" rule (none of these are
individually a money/tax/data-loss/security blocker on their own once
fixed and tested; collectively they were exactly the kind of first-round
catch that class of rule exists for).

# Refund: per-original-line discount clamp for non-uniform keys (ut-docs#1560)

**Card:** universaltill/ut-docs#1560 — "Refund: non-uniform-discount
sibling lines sharing a refund-line key still under-refund the discount
slightly." Follow-up to ut-docs#1531 (PR #773, merged).
**Repo/branch:** universal-till, `fix/1560-non-uniform-refund-discount-clamp`
**Complexity:** medium (Dev at Sonnet, Review at Opus — one round, found
one blocker; fixed and re-verified in-session, no second review round
needed since the fix was mechanical and scoped exactly to what the finding
named).

## What shipped

`#1531`'s own per-key discount clamp is exact only when every original
line sharing a `RefundLineKey` (item/variant/price/mode) applies the SAME
discount rate (`keyUniform`). When sibling lines share a key but carry
DIFFERENT `LineDiscount` amounts, #1531 deliberately fell back to a
non-uniform branch that computed `lineDiscount = floor(l.LineDiscount *
qty/l.Qty)` fresh on every request — a one-shot floor with no
cross-request cumulative tracking. That reproduces the identical
flooring-accumulation defect #1531 fixed for the uniform case, but for the
non-uniform case: refunding the SAME original line across more than one
sequential partial request under-refunds its discount by a minor unit. A
round-2 review of #1531 found this via a 400-case fuzz sweep: ~17% of
non-uniform-key cases under-refunded.

Fix, in four layers:

1. **Schema.** New migration `002_refund_of_line_id.sql` — the first
   migration file added since the ADR-0074 baseline squash — adds a
   nullable, indexed `refund_of_line_id TEXT REFERENCES sale_lines(id)` to
   `sale_lines` (and a matching, FK-less `refund_of_line_id TEXT` on
   `sale_lines_archive`, same precedent as `invoices_archive
   .original_invoice_id`): for a return line, the specific original
   `sale_lines.id` it was refunded against.
2. **Plumbing.** `data.SaleDetailLine` gained an internal-only `ID` field
   (`json:"-"`, wire contract unchanged); `data.SaleLineRow` and
   `pos.SaleLineInput` both gained `RefundOfLineID`, threaded through
   `pos.CompleteSale` into `InsertSaleLinesBatch`'s INSERT. Two new
   `POSRepo` methods mirror the existing per-key ones but group by
   `refund_of_line_id`: `ReturnedLineDiscountsByOriginalLine` and
   `ReturnedQuantitiesByOriginalLine`.
3. **Core clamp.** `refund_page.go`'s non-uniform branch now runs the SAME
   running-cumulative-target clamp shape #1531 already gave the uniform
   branch (`target = floor(l.LineDiscount * cumulativeQtyForLine /
   l.Qty)`, snapped to `l.LineDiscount` exactly once
   `cumulativeQtyForLine >= l.Qty - 1e-9`, same epsilon #1531's own F4 fix
   uses), scoped to `l.ID` instead of the shared key pool.
   `RefundOfLineID: l.ID` is now set unconditionally on every constructed
   return line (uniform or not), so the new per-line ledger stays complete
   regardless of which branch a future request takes.
4. **Regression test.** `TestPostRefund_NonUniformKeySequentialPartialsNeverUnderRefundDiscount`:
   two sibling lines share a key (line A qty 3 discount 10 — doesn't
   divide evenly by 3; line B qty 1 discount 0), line A refunded one unit
   at a time across 3 sequential POSTs. Asserts the cumulative
   `line_discount` recorded against `refund_of_line_id` is exactly 10, not
   9 (the pre-fix `floor(10/3)*3` result).

## Independent review — one round, Opus, found one blocker

Spawned as a worktree-isolated `general-purpose` subagent (`model: opus`)
against a WIP commit on the feature branch. Ran the full gate itself
(`go build`/`vet`/`test ./...`/`golangci-lint`/`gofmt`/
`guard-data-access.sh`, plus several migration/help/i18n/kiosk-engine
guards) and independently re-verified the TDD claim by reverting just the
non-uniform branch, confirming the new test failed with the exact
predicted "got 9, want exactly 10" message, then restoring the fix and
confirming it passed again — plus ran the whole `internal/pages` suite
with the fix reverted and confirmed exactly one test failed (the new one),
so the existing #1531-era cross-attribution test wasn't accidentally
covering the same ground.

**Blocker (fixed).** The archive/restore round-trip silently dropped the
new column: `sale_lines_archive` had no `refund_of_line_id` column, and
`reset_archive_repo.go`'s `resetArchiveTables` column list for
`sale_lines` didn't carry it either. The reviewer proved it with a
driven probe (seed a return line with `refund_of_line_id`, run
`ResetTransactionHistory` then `RestoreResetBatch`, observe the restored
row's `refund_of_line_id` come back NULL). Consequence: a shop that ever
used "clear transaction history" / restore would silently lose the new
per-line ledger for every already-returned non-uniform line, re-opening
the exact class of bug this card exists to close (the next partial
refund of that line would recompute its cumulative target from zero and
re-give discount already paid) — and it directly contradicts ADR-0042's
"restore returns to exactly the pre-reset state, never a merge."

Fix: added `refund_of_line_id` to `sale_lines_archive` (via the new
migration) and to `resetArchiveTables`'s column list, plus the same
two-phase archive/restore ordering `invoices.original_invoice_id` already
needed for its own self-FK (`reset_archive_repo.go`'s restore loop now
special-cases `sale_lines` the same way it special-cases `invoices`:
re-insert original-line rows — `refund_of_line_id IS NULL` — before
return-line rows on restore, so row-by-row FK enforcement can't trip on
the archive SELECT's row order). Pinned with a new test,
`TestResetThenRestoreRoundTrip_RefundOfLineID` in `reset_test.go`, which
passes with the fix. (Note on TDD honesty: unlike #1531's `invoices` case,
reverting just the two-phase split here did NOT reproduce a failure in
this specific two-row test — `sale_lines`' own live FK already forces the
original line to exist, hence be archived, before its return line, so a
plain unordered SELECT happens to come back in the right order for this
shape. The two-phase split is still the correct, precedent-matching
defensive fix — SQL gives no ordering guarantee without `ORDER BY` — and
costs nothing; this paragraph records honestly that the specific test
added does not itself force that failure, rather than overclaiming a
red-then-green result that didn't actually occur for this finding.)

**Non-blocking, addressed anyway.** Editing `001_init.sql` directly (as
the fix's first draft did, since ADR-0074 Decision 1 permits it
pre-first-paying-shop) changes its checksum, and `verifyAppliedMigrations`
hard-fails an already-migrated database on any drift — forcing a full
data-directory wipe on every existing install (dev machines, the TECLAST
tablet, both Pis, the shadow-café pilot) with nothing in the diff saying
so. Switched to the additive `002_refund_of_line_id.sql` migration
instead (an `ALTER TABLE ... ADD COLUMN`, which SQLite allows with a NULL
default), needing no wipe. This also fixed the reviewer's separate
performance finding for free: the new migration adds
`idx_sale_lines_refund_of_line_id` up front, so the new FK's child column
was never left unindexed (with `foreign_keys=ON`, an unindexed child
column makes every `sales`-cascaded delete and `ResetTransactionHistory`'s
own `DELETE FROM sale_lines` an unindexed scan per row).

**Non-blocking, deferred as a new card (not this diff's to fix).** The
`POST /api/refund` handler validates a requested `qty_<i>` against the
shared per-**key** pool only, never against that specific original line's
own sold quantity — reproduced identically against `origin/main` (i.e.
pre-#1560, not a regression), but this diff's new `refund_of_line_id`
ledger can now record a return against a line for more units than it ever
sold. Filed as **ut-docs#1583**, `complexity:easy`.

**Nitpick, not actioned.** A return line refunded before this fix ships
has `refund_of_line_id = NULL`, invisible to the two new by-line queries,
so the per-line clamp restarts from zero for that line going forward. The
residual error is bounded by the line's own discount and lands
shop-unfavourable (the shop pays out slightly more than owed on the first
post-fix partial refund of an already-partially-refunded pre-fix line) —
same magnitude class as the bug being fixed, already documented honestly
in the new repo methods' doc comments. Not worth a migration-time backfill
given the product has no real trading history yet (ADR-0074's own
premise).

## Verified beyond automated tests

- Full `go test ./...` green (48 packages) after every fix, not just the
  package the finding named — including the two `internal/db` migration-
  count tests (`TestFreshBaselineRecordsNameAndChecksum`,
  `TestOpenPostResetDatabaseReopensClean`) whose hardcoded "exactly one
  embedded migration" assertion was itself now stale the moment a second,
  legitimate migration landed on top of the ADR-0074 baseline — both
  updated to assert the real invariant (migration 1 is always the
  baseline; a reopen applies nothing new) rather than a count frozen at
  the moment of the squash.
- `golangci-lint run ./...`: 0 issues. `gofmt -l .`: clean. `go vet ./...`:
  clean.
- `guard-data-access.sh`, `guard-migration-version-collision.sh`,
  `guard-i18n.sh`, `guard-compliance-claims.sh`, `guard-help-topics.sh`,
  `guard-kiosk-engine.sh`, `guard-page-http-error.sh`,
  `guard-plugin-menu-read.sh`: all pass.
- No UI/template/route change — no e2e run needed (none of the existing
  refund e2e specs target discount-proration math; they cover the OSK
  layer, orthogonal to this diff).
- No real client/shop name in any new test data.

## Safe to merge

Yes. One blocker found and fixed (archive/restore round-trip), one design
improvement adopted (additive migration instead of an edit forcing a data
wipe, which also closed the missing-index finding for free), one
pre-existing-but-unrelated gap filed as its own card rather than folded in
here.

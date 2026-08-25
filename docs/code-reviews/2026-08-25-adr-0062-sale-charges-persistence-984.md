# Code review: ADR-0062 step 1/3 — sale_charges persistence + ChargePolicy hook

- **Card:** universaltill/ut-docs#984 (split from #963, step 1 of 3 —
  #985/#986 follow)
- **Design:** ADR-0062 (universaltill/ut-docs#983)
- **Repo:** `universal-till`
- **Reviewer:** independent Opus subagent, fresh context, isolated git
  worktree (`isolation: "worktree"`), no shared checkout with the
  implementer session (Sonnet)

## What shipped

Foundation layer for ADR-0062's general additive statutory charge list —
persistence and the plugin-policy hook only, nothing wired into the real
checkout flow yet (that's #985/#986):

- Migration `064_sale_charges.sql`: `sale_charges` live table (child of
  `sales`, no `ON DELETE CASCADE`) + `sale_charges_archive` twin, matching
  the `sale_discounts`/`sale_discounts_archive` archive-twin conventions
  from `040_reset_archive.sql` (no FK on the archive table, no PK/UNIQUE).
- `data.SaleCharge` + `POSRepo.InsertSaleCharges` — a new sibling method to
  `InsertSale`, not a widened signature (ut-docs#976).
- `data.SaleDetail.Charges []SaleCharge`; `GetSaleDetail` now reads
  `sale_charges` (`ORDER BY seq`), empty/nil for a sale with none.
- `internal/data/reset_archive_repo.go`'s `resetArchiveTables` gets
  `sale_charges` inserted before `sales` (child-before-parent — required,
  not cosmetic, see Findings).
- `internal/pos/charge_policy.go`: `ChargeItem` type,
  `ChargePolicy.Charges []ChargeItem`.
- `internal/pages/charge_hook.go`'s `validateChargePolicy` extended to
  parse/validate a plugin's `charges` JSON array: rate clamp `[0,10000]`bp,
  drop the reserved key `service_charge`, drop duplicate keys, default an
  unrecognized `base` to `net_lines`.

## Independent review — findings (all addressed)

1. **BLOCKER — `guard-docs-shots.sh` (CI-blocking) was red.**
   `charge_hook.go` registers zero mux routes but is still (deliberately,
   per that guard's own header) included in the app-surface hash, so any
   edit to it invalidates the manual-screenshot manifest even though no
   pixel changed. **Fixed:** ran `make docs-shots` (regenerates all 92
   screenshots via the pre-installed Chromium + rewrites
   `web/help/img/manifest.json`); confirmed the diff is the
   `surface_sha256` line only, no PNG changed. Guard now green.
2. **SHOULD-FIX — false-pass test.** The original "base defaults to
   net_lines" assertion in `pos_repo_sale_charges_test.go` read
   `Charges[0]`, which explicitly sets `Base: "net_lines"` in the fixture —
   so it would pass even if `coalesceChargeBase` were a no-op. Reviewer
   verified by mutation (neutering the function left the test green).
   **Fixed:** re-pointed the assertion at `Charges[1]`
   (`municipality_tax`, which leaves `Base` unset); re-verified the
   mutation now fails it correctly.
3. **SHOULD-FIX — `TaxBasisBP` had no upper clamp.** `DefaultRateBP` was
   clamped to `[0,10000]`bp (ADR-0062 Decision 3's stated hazard: a
   plugin-declared, merchant-invisible, applied-verbatim rate), but
   `TaxBasisBP` — equally plugin-supplied and equally applied verbatim in
   step 2's math — was only negative-clamped. **Fixed:** added the same
   `[0,10000]` upper clamp, logged both directions; covered by
   `TestAskChargePolicy_ChargesKeyHygieneAndTaxBasisClamp`.
4. **NIT — the negative-`TaxBasisBP` clamp was silent**, contradicting the
   function's own "every drop/clamp is logged" doc comment. **Fixed** as
   part of #3's change (both directions now log).
5. **NIT — reserved/duplicate-key checks were exact-match, untrimmed, and
   accepted an empty key.** `{"key":"Service_Charge"}` or
   `{"key":" service_charge"}` would have walked past the reserved-key
   drop; a whitespace-only key would have persisted as `key = ''`.
   **Fixed:** trim + case-fold the comparison for both the reserved-key
   check and dedup; reject an empty (post-trim) key outright. Covered by
   the same new test.
6. **NIT — `ChargeItem.Label`'s doc comment described a fallback that can
   never fire** (it referenced core's `service_charge` default copy, but a
   plugin item can never carry that reserved key). **Fixed:** rewrote the
   comment to say plainly that an empty plugin-declared `Label` has no
   core fallback yet, and that settling it (e.g. defaulting to `Key`) is
   #986's job.
7. **NIT — a stale forward-reference to `ChargeInput`** in
   `charge_policy.go`'s doc comment didn't say where/when that type
   actually lands. **Fixed:** now explicitly says "ut-docs#985, not yet
   present as of this step."
8. **NIT (cosmetic) — `reset_archive_repo.go`'s comment overstated the
   ordering risk** ("fail outright *or, worse,* silently orphan/lose rows
   depending on FK enforcement") when this project always runs with
   `foreign_keys=ON`, so it's deterministically a hard error, not a
   silent-loss risk. **Fixed:** narrowed the comment to state the actual,
   verified behavior and point at the regression test that pins it.
9. **Informational, no fix needed** — two tests (`NoChargesIsNil`,
   `NoChargesRowsIsEmptyNotError`) assert intentional negative properties
   that would also hold with the feature entirely absent. Not false
   positives (one does catch a missing-table regression), just noted as
   weak coverage on their own; the mutation-verified tests elsewhere in
   the diff carry the real weight.

## Verified beyond automated tests (by the independent reviewer, via mutation)

- **Reset ordering is genuinely required**, not just "matches the ADR's
  wording": moving `sale_charges` to *after* `sales` in
  `resetArchiveTables` made 7 tests fail with a real SQLite `FOREIGN KEY
  constraint failed` error (this project runs with `foreign_keys=ON`,
  `internal/db/db.go`). The new seed row in `reset_test.go`'s
  `seedFullSale` is what pins this — without it, the ordering bug would
  have shipped silently.
- **The `internal/pos/sales_test.go` schema fixture addition is
  load-bearing**: removing the new `CREATE TABLE sale_charges` there fails
  `TestCompleteSale_ClampsUnknownOrderTypeToDineIn` with "no such table:
  sale_charges" — confirming `GetSaleDetail`'s new query is exercised by
  existing tests, not just the new ones.
- Full `go test ./...` and every CI-blocking guard from `ci.yml`'s `build`
  job pass on the final diff (re-run after all fixes above, not just the
  first pass).

## Deferred to #985/#986 (explicit non-goals of this step, not gaps)

- Nothing in `SaleInput`/`computeSaleTotals`/`recomputeTotals`/the tender
  handler consumes `Charges` yet.
- No LAN-sync journal wiring, no fiscal/receipt/invoice/UI changes.
- `ChargeItem.Label`'s empty-string rendering (finding 6) is explicitly
  punted to #986.

## Safe-to-merge verdict

**Safe to merge.** All blocker/should-fix findings resolved and
re-verified; full test suite and all CI-blocking guards green on the
final diff.

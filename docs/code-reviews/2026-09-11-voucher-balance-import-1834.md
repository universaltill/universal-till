# Import existing voucher (Gutschein) opening balances (ut-docs#1834)

**Date:** 2026-09-11
**Card:** universaltill/ut-docs#1834 — "Import existing Gutschein balances
from the merchant's current system (migration blocker)"
**Complexity:** medium (Dev: Sonnet, Review: Opus, per `scrum-master`'s
model-routing table)
**PR:** universaltill/universal-till#(opened this cycle) —
`pipeline/1834-voucher-balance-import`

## What the card asked for

A pilot merchant migrating from another system has customers holding
physical voucher cards with real outstanding balances. Those balances need
to land in this till's voucher-liability system (ut-docs#1008) as an
**opening liability**, not a sale — never sent to the fiscal device — and
the import must be idempotent (no double-credit on re-import) with rejected
rows reported, never silently merged or dropped. The card's own "open
question" (what format can the merchant's system actually export?) was
explicitly flagged as non-blocking for design.

## BA/Architect: resolving the open question without escalating

Per the standing research-before-escalating rule, CSV was chosen as the
supported format — it's the universal common denominator every POS export
format supports, and every other import path in this codebase
(`internal/catimport`) already works this way. If the merchant's system
turns out not to export CSV, that's a follow-up card (manual entry), not a
blocker here.

Verified current state first: vouchers are fully implemented as a liability
(`internal/data/voucher_repo.go`, ut-docs#1008) but only ever created
through `pos.CompleteSale` — no import/opening-balance path existed, and no
settings UI for vouchers at all. The gap was real.

## What shipped

- `internal/catimport/voucher_balances.go` — `ParseVoucherBalances`, a
  small header-detected CSV parser (code/balance/label columns), reusing
  the existing `catimport.ParsePrice` money-parsing helper. Pure parsing,
  no DB.
- `internal/pages/import_vouchers_page.go` — `GET /settings/vouchers/import`
  (manager-gated) + `POST /api/vouchers/import` (preview/commit). Reuses
  the *existing* `POSRepo.CreateVoucher`/`RecordVoucherTransaction` exactly
  as `pos.CompleteSale` does, with an empty `issued_sale_id`/`sale_id` (no
  sale) — no schema/migration change needed. Deliberately skips this
  codebase's full cross-page-load staged-upload wizard (`import_stage.go`)
  in favour of a small embedded-base64-bytes confirm step, capped at
  256KB raw upload — documented as a scoped simplification for a
  two-column CSV format, not a gap.
- **Real correctness issue this design surfaces and fixes**:
  `VouchersIssuedRedeemedForRange`/`ForInstantWindow` (the Z-report's
  GUTSCHEINE section) previously counted every `type='issue'` transaction
  toward that day's "Issued" flow. An imported opening balance has
  `sale_id = NULL` by construction, so without a fix it would silently
  inflate the day-of-import's Z-report as if N vouchers were sold that
  day. Fixed by bucketing `type='issue' AND sale_id IS NULL` into a new,
  separate `Imported` figure (see "Independent review" below for how this
  evolved from a plain exclusion to a proper bucket).
- i18n across all 4 locales (`en`/`ar`/`fa`/`tr`), a new manual topic
  (`web/help/{en,de,fa,tr,ar}/voucher-import.md`, `routes:
  [/settings/vouchers/import]`), and regenerated screenshots
  (`make docs-shots`) for all 4 shipped locales.
- Tests at every layer: parser (8 cases), a day-close regression test
  proving the Imported/Issued separation, and 9 HTTP-level page tests
  (manager gating, preview-writes-nothing, commit correctness, idempotency,
  bad-row handling, a mixed collision+clean-row-in-one-batch case, and the
  preview existence pre-check).

## Independent review (Opus, complexity:medium → Opus per model-routing)

Spawned in an isolated worktree (`isolation: "worktree"`) against the
pushed `pipeline/1834-voucher-balance-import` branch. Ran the full gate
(build/vet/gofmt/`go test ./...`/golangci-lint) plus every relevant CI
guard, independently re-derived the EOD-exclusion safety claim by grepping
every `RecordVoucherTransaction(type: "issue", ...)` call site rather than
trusting the Dev report, and personally reverted-then-restored the two
production fixes under TDD to confirm their regression tests actually fail
for the right reason.

**Verdict: NOT safe to merge as-is** — one CI-blocking failure, plus
should-fix findings:

1. **BLOCKER** — `guard-docs-shots.sh` failed: the new routed help topic
   had no screenshot. **Fixed**: ran `make docs-shots` for real (this
   session's pre-installed Chromium at `/opt/pw-browsers` made this
   possible, unlike the Dev subagent's sandbox) — 116 screenshots
   regenerated, guard now green.
2. **Should-fix** — the audit record on commit carried row counts only,
   never the imported *amount* — a real gap for a change that creates
   monetary liability. **Fixed**: `total_minor` added to the audit
   payload, with a test asserting the exact value in `data_json`.
3. **Should-fix** — excluding the import from "Issued" was correct, but
   the consequence was the imported liability appeared **nowhere** on any
   day-close report at all, so a later redemption of an imported voucher
   would show up on some day's Z-report with no matching "issue" ever
   reported on any Z-report of this till — summing every day's report no
   longer ties out to outstanding voucher liability. **Fixed**: gave
   imports their own `Imported`/`GUTSCHEINE` line (distinct from Issued),
   so every voucher_transactions row this till ever writes lands in
   exactly one of Issued/Redeemed/Imported on some day's report.
4. **Should-fix** — preview didn't check the database, so it could
   promise "N ready to import" and then a commit of the identical file
   import 0 (every code already existed) — directly contradicting this
   page's own manual topic, which tells the operator the preview is
   trustworthy. **Fixed**: `splitExistingVoucherCodes` pre-checks each
   code via the existing `GetVoucherBalance` read path (nothing written)
   before rendering the preview; a genuine DB error during that check gets
   its own honest `check_failed` reason rather than being misreported as
   an existing code.
5. **Nice-to-have** — `VoucherBalanceIssueZeroOrNegBalance` is only ever
   reachable for a balance of exactly zero (a negative balance is already
   caught by `catimport.ParsePrice`'s own rejection as `bad_balance`) — the
   doc comment overclaimed. **Fixed**: comment corrected, no behaviour
   change.
6. **Nice-to-have** — a preview test's assertion used `&&` where an `||`
   was meant, and neither substring was even present in the preview's real
   output — it passed on almost any body. **Fixed**: replaced with a real
   assertion on the rendered count/total (pinned to `?lang=en` so it
   doesn't depend on process-global locale state).
7. **Nice-to-have** — no test exercised "a collision earlier in a commit
   batch, followed by a clean row in the SAME transaction" (the existing
   recommit test collides on every row). **Fixed**: added
   `TestVoucherImportPage_CollisionThenCleanRowInSameBatch`, which failed
   for the expected reason when I temporarily made the collision path
   silently `continue` without reporting, confirming the claim in
   `commitVoucherBalanceImport`'s own doc comment (SQLite's default ABORT
   conflict resolution leaves the shared transaction usable after one
   statement fails) actually holds.

### Things the review checked and found clean (not findings)

- EOD-exclusion safety re-derived independently (not trusted from the Dev
  report): `RecordVoucherTransaction(type: "issue", ...)` has exactly one
  other caller (`internal/pos/sales.go`), which always passes a real,
  non-empty `SaleID` generated at `sales.go:774-776`.
- Money handled as `int64` minor units end to end, `catimport.ParsePrice`
  reused rather than reinvented — no floats anywhere in the new path.
- Manager gating genuinely tested against the real mux, not the
  `UT_AUTH=off` bypass.
- No `MkdirAll`/cwd-relative-path bug class (this feature writes nothing to
  disk).
- Offline-first: no network dependency; bypasses `pos.CompleteSale`
  entirely, so no fiscal-signing hook ever fires for an import.
- i18n: all 22 new keys present in all four locales with matching key sets
  and verb signatures.
- UI: reuses existing design tokens, logical CSS properties only (no
  literal left/right), real empty/error states.
- No real merchant/shop name anywhere in code, tests, or docs.

## TDD re-verification (by the Reviewer, personally)

- EOD exclusion (now: Imported bucket) — removing the bucketing logic and
  re-running `TestVoucherRepo_IssuedRedeemedForRange_SeparatesImportedOpeningBalance`
  failed with `issued = 2/10500, want 1/3000` (both functions independently
  guarded); restoring the fix passed cleanly.
- Idempotency — silently `continue`-ing past a collision (no report) made
  `TestVoucherImportPage_RecommitSameFileDoesNotDoubleCredit` fail with
  `second commit response must report BOTH colliding codes, got: "0
  voucher(s) imported..."`; restoring the fix passed.
- Collision-then-clean-row (new test, added during fix-up) — silently
  `continue`-ing past the collision (no `collisions = append(...)`) still
  left `GS-NEW` created, confirming the transaction-survives-one-failed-
  statement claim independently of the reporting behaviour.

## What was verified beyond automated tests

- Real screenshots generated via `make docs-shots` (Playwright, this
  session's pre-installed Chromium) and visually inspected for both `en`
  and `ar` (RTL): clean layout, no overlapping/cut-off text, the native
  `<input type=file>` control's internal LTR ordering inside an
  otherwise-mirrored RTL card is a browser-native behaviour, not a defect
  this page introduced. Not separately inspected at the 10-inch kiosk
  viewport or in dark theme — this is a manager-only settings page, not a
  cashier/kiosk-facing surface, so that risk is low, but noting the gap
  explicitly per the Tester skill's own rule rather than implying full
  coverage.
- `go build ./...`, `go vet ./...`, `gofmt -l .` (empty), full
  `go test ./...` (0 failures, all packages), `golangci-lint run ./...`
  (0 issues) — re-run after every fix above, not just once.
- Guards: `guard-data-access.sh`, `guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-help-drift.sh` (this topic's own drift is zero; all other output
  is pre-existing baselined drift, ut-docs#1962/#1973, unrelated to this
  change), `guard-page-http-error.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-compliance-claims.sh`,
  `guard-docs-shots.sh` — all green.

## Explicitly deferred (non-goals, per BA/Architect scoping)

- Matching the full catalog-import wizard's cross-page-load staged-upload
  UX — the embedded-bytes approach is a deliberate, documented MVP shape
  for this much smaller file format.
- Auto-detecting the merchant's exact source-system export format beyond
  CSV — a follow-up if the pilot merchant's system turns out not to
  support CSV export.
- A manual single-voucher entry UI as a CSV alternative.
- Any accounting-export/balance-sheet reporting beyond the day-close
  Imported line added here.

## Safe-to-merge verdict

**Yes**, after the fixes above — full gate green, guards green, both TDD
claims independently re-verified against real failing-then-passing
evidence, manual topic + screenshots present and current.

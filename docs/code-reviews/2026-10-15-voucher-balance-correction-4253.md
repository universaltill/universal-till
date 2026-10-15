# Review — gift voucher balance correction by a manager (ut-docs#4253)

## What shipped
- **Migration 074** rebuilds `voucher_transactions` (018's create/copy/drop/rename pattern):
  - The CHECK admits `'adjustment'`, and there are two new nullable columns, `actor_user_id` and `reason`.
  - Both indexes are recreated, including 012's partial `ux_voucher_tx_redemption_once`.
  - This is safe because the table is a leaf: nothing references it, and 036 refused the parent `vouchers` for exactly that reason. The checksum is pinned.
- **`POSRepo.AdjustVoucherBalance`** runs in one immediate transaction. It writes a signed-delta `adjustment` row, sets the balance, sets status (0 → redeemed, >0 → active) and writes an `audit_log` row (`voucher`/`adjust`).
  - It refuses: void, single-purpose (taxed at issue), negative or above-ceiling balance, empty reason or one over 200 runes, no change, and a moved balance (guarded UPDATE).
  - It is idempotent on the adjustment id, but only for the same request.
- **Manager form** "Correct balance" on `/vouchers/{id}`: new balance, reason, save.
  - `POST /vouchers/{id}/adjust` is gated on `settings` server-side, and refused when auth is on with no session user. Demo mode denies it.
  - The history shows "Adjustment", the signed amount, the reason and who made it.
- **Additional till**: forwards to the primary's new bearer-authed `POST /api/sync/vouchers/{id}/adjust` (auth-exempt path, bearer checked in the handler). There are three outcomes:
  - The request never left the till: refused with "can't reach the main till".
  - Maybe applied (timeout, 5xx, malformed 200): an honest "didn't confirm — reload and check the history".
  - Success: `MirrorVoucherAdjustment` writes the primary's result into the local ledger without re-validating.
- **Day-end**: a new Adjusted bucket (count plus signed net) in both window functions and the EOD JSON, plus an `Adjusted (n)` line under GUTSCHEINE on the printed Z. It never enters Gross/Net/VAT bands.
- **i18n and docs**: 19 keys in en/ar/fa/tr; the help topic `vouchers` in en/de/ar/fa/tr; language-pack PRs for de/es/pt; and ut-docs LAN sync contract 1.11.0.

## Review
The independent review ran on Sonnet. The planned Fable review was refused (usage credits exhausted), so Sonnet stands in per MODEL-ROUTING.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | A replayed adjustment id with a different amount reported success. | **Fixed.** A replay must match the stored delta and reason; otherwise `ErrVoucherAdjustmentInvalid`. A matching replay reports its own before/after. Two tests added. |
| 2 | major | A lost or timed-out response from the primary was shown as "nothing changed" when it may have applied. | **Fixed.** The not-sent and maybe-applied cases are split, with a new `primary_unknown` message and help wording. Three tests added. |
| 3 | minor | The replica mirror re-validated and could fail silently, leaving the local ledger stale. | **Fixed.** `MirrorVoucherAdjustment` applies the primary's result directly, and failures are logged at Warn (the Problems panel). |
| 4 | minor | 074 would abort on a pre-FK-era orphaned ledger row. | **Accepted.** Vouchers are never deleted and the parent row is written first. 018 already carries the same theoretical risk. |
| 5 | minor | The detail page on a replica waits up to 2 s for the primary's live balance when the primary is black-holed. | **Accepted.** It's a back-office page; checkout is unaffected. |
| 6 | minor | The `?adjusted=1` banner shows again on reload. | **Accepted** (cosmetic). |
| 7 | minor | Empty actor when auth is on; the primary trusts the forwarded actor. | **Fixed** (403 without a session user). The primary-side trust is the same as `/redeem`, since a bearer-authed till is trusted. |
| 8 | nit | The reset-classification text didn't mention adjustments. | **Fixed.** |
| 9 | nit | A bad hidden `expected_balance` showed "balance changed". | **Fixed.** It now shows the reload message, with tests. |
| 10 | nit | A zero-net day prints `Adjusted (2) +0.00`. | **Accepted** (truthful). |

TDD re-verified by the reviewer in an isolated worktree, by reverting the production code and seeing each test fail with the claimed error:
- `TestAdjustVoucherBalance_Refusals/balance_changed`
- `TestVoucherRangeSummary_AdjustmentBucket`
- `TestVoucherAdjust_ReplicaPrimaryUnreachableRefuses`
- `TestMigration074_RebuildKeepsLedgerAndIndexes` (removed index; narrowed CHECK)

## Verified beyond unit tests
I drove the real binary on a fresh data dir with Chromium against a seeded voucher (£32.50 → £20.00):
- Saving gave "Balance corrected." and a history row "Adjustment −£12.50", with the reason and author.
- A stale form (expected 3250 after the change) gave a 409 with the "balance changed" message, inline, no modal.
- The EOD range JSON showed `vouchers_adjusted_count 1`, `vouchers_adjusted -1250`, with gross and net still 0.

I looked at the screenshots at 1024×600 (light), at 360 px (stacked card history, form wraps cleanly) and in fa RTL (mirrored layout, Persian digits). Not looked at: dark theme, a real two-till LAN pair (covered by httptest primary/replica tests only), and touch hardware. The help screenshots are unchanged, because the topic's shot is the `/vouchers` list.

## Gates
- `gofmt` clean; `go build`/`vet` clean; `golangci-lint` 0 issues.
- `go test` passes for db/data/pages/auth plus the full `./internal/...` run.
- Guards all pass: i18n, help-topics, help-drift, data-access, no-showmodal, compliance-claims, core-neutral, card-data-schema, kiosk-engine, migration-version-collision and the others in the build job.
- `guard-deadcode-baseline` can't load packages with this container's go1.26-built tool. That is environmental, and the same was noted on #4251.

**Verdict:** safe to merge. The language-pack PRs (de/es/pt) land right after core.

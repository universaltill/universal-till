# 2026-11-04 — line quantity reduction needs the void gate (ut-docs#5411)

## What shipped
- `/api/pos/line` (`internal/pages/pos_api.go`) now looks the addressed line
  up first (`cur`), keeps the current qty when `qty` is blank/absent, refuses
  an unparseable/negative/NaN/Inf `qty` with 400 (it used to read as 0 and
  silently void), and gates ANY reduction of a priced, non-gift-voucher
  line (− stepper, qty box, qty=0, key- or code-addressed) behind
  `checkOrElevate("void_comp_waste")`, writing a `shrinkage_events` row and
  an `audit_log` row (`InsertAuditElevated` when a PIN approved it) with the
  same actor/approver semantics as `/api/pos/remove`.
- Elevation prompt replays the absolute target qty + discount + key/code;
  response is retargeted to `#shrinkage-hint` and `HX-Trigger:
  ut-basket-resync` makes `app.js` re-fetch `/ui/basket` so the qty box
  shows the real quantity while the PIN prompt is up.
- New key `elevation.summary.reduce_line_qty` (en/ar/fa/tr + meta), help
  `web/help/en/sell.md` step 3, new test file
  `internal/pages/pos_api_line_reduce_gate_test.go`, three existing qty
  tests pinned to `UT_AUTH=off`, `qty=Inf` test flipped from "voids" to 400.

## Process
Security-focused independent review (Fable, different model from the
author). Read-only: no source edits, no commit. Diff: uncommitted worktree
`5411-line-reduce-pin` vs `origin/main` + the untracked test file.
Reference: ADR-0052, `/api/pos/remove` (ut-docs#1465) as the precedent,
`Service.UpdateLine`/`UpdateLineByKey`, `money.MulQty`.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | **high** | **Rounding bypass.** `pos_api.go:1626` gates on `cur.PriceCents.MulQty(reducedBy).IsZero()`. Any reduction worth under half a minor unit passes ungated and unrecorded, and they accumulate. Probe (scratch copy, real handler): cashier posts qty 2 → 1.996 → … → 1.98 on a 100-minor line: five 200s, no prompt, `shrinkage_events` empty, line total 200 → 198. Scriptable from any cashier session against the API; from the UI only for a weighed line priced under 0.50/unit (2-dp pattern). This is exactly the class of bypass the card closes. Fix: gate on `!cur.PriceCents.IsZero() && qty < cur.Qty` (basket.html already picks the reason sheet on the UNIT price, line 366), and record `lost` as the line-total delta `AmountForQuantity(price, cur.Qty) − AmountForQuantity(price, qty)` so micro-steps sum to the real loss. Add a sub-half-cent reduction test. | **fixed** (unit-price gate, line-total-delta loss; `TestLineHandler_SubCentReductionStillGated`) |
| 2 | **high** | **Shrinkage/audit written before the engine accepts the change.** `:1665-1676` insert the shrinkage event and audit row, then `:1688/:1694` call `UpdateLineByKey`/`UpdateLine`, which can refuse with `LineDiscountTooLargeError` (`service.go:1848`) and leave the line untouched. Probe: manager posts qty 3 → 1 with `discount=999999`: 200, line still qty 3, one shrinkage row for 2 units that are still in the basket. Reachable from the UI: the qty box `hx-include`s the row's discount (basket.html:297), so a line discounted near its total mis-records on any qty reduction. Fix: pre-check `pos.LineDiscountExceeds(cur.PriceCents, qty, money.FromMinor(discount))` before the gate and return the same refusal render, or record only after a successful update (the remove precedent records first only because `RemoveLine` cannot fail). Add a test. | **fixed** (gate skipped when the engine will refuse the discount; `TestLineHandler_RefusedDiscountRecordsNoShrinkage`) |
| 3 | medium | **Code-addressed qty=0 with several lines sharing a SKU.** `cur` is the first SKU/ItemID/VariantID match (`:1581`, same predicate and order as `UpdateLine` `:1786`, so the gate and the update agree on WHICH line), but `UpdateLine` at qty 0 calls `removeLocked(SKU)` (`:1792`, `:1617-1625`), which drops EVERY line with that SKU; the gate valued and recorded only the first. `/api/pos/remove` snapshots all `matched` lines and records each (`:1413-1465`). API-only (shipped UI always sends `key`). Fix: on the code path collect all matches like remove, gate on the sum, record each; or refuse `code`+reduction when more than one line matches. | **fixed** (code shared by several lines is a 400, key required; `TestLineHandler_AmbiguousCodeRefused`) |
| 4 | medium | **TOCTOU between `Basket()` snapshot (`:1580`) and the update (`:1688`).** The gate decision uses a snapshot; a concurrent ungated raise (`delta=+N`) landing in between turns an ungated "same qty" post into an ungated reduction. Window is microseconds on the ungated path (scripted race only; `hx-sync="#basket:replace"` serialises the UI). Fix: a compare-and-set in the service (expected prior qty, sentinel error) or a handler-level mutex around snapshot+update. | card |
| 5 | low | **Replayed elevation form carries attacker-choosable hidden `qty`/`discount`/`key`** (`:1640-1649`); the approver sees the summary rendered from the first request, the replay executes whatever the form holds, nothing binds the two. Inherent to ADR-0052 §1 (no stored state) and identical at every site; consequence here is bounded because the shrinkage/audit rows record the ACTUAL from/to qty (`:1665-1669`), so the journal stays truthful even if the approver was shown something else. | accepted (ADR-level; note for a future HMAC-over-hidden-fields ADR) |
| 6 | low | **Weighed lines: phantom reduction on a discount edit.** Steppers and the discount box `hx-include` the row's qty input (basket.html:297/336/356), rendered `%.2f`. A line whose Qty carries more precision (scale-label weight, API-set qty) is "reduced" to the 2-dp value on every discount edit — previously a silent truncation, now a PIN prompt and a shrinkage row (0.125 kg → "0.12" at 1000/kg = 5 minor). Fix: discount/stepper posts omit `qty`, or the box renders the line's real precision. | card |
| 7 | low | `InsertShrinkageEvent`/`InsertAudit` failure is logged and the reduction proceeds (`:1665-1676`): value leaves the sale unrecorded. Same as `/api/pos/remove` (`:1461-1475`). Fail-closed would be a decision for both sites. | accepted (precedent) |
| 8 | low | `HX-Trigger: ut-basket-resync` (`:1655`) fires before the swap (htmx semantics); the resulting `GET /ui/basket` outerHTML-replaces `#basket`, which contains `#shrinkage-hint`, so the prompt's hint line is wiped a moment after it lands. The OOB dialog survives. Cosmetic; `HX-Trigger-After-Settle` or a narrower resync would keep the hint. | nit |
| 9 | low | **Help/i18n follow-through.** Only `web/help/en/sell.md` updated; de/ar/fa/tr step 3 predate even #1465's PIN sentence (pre-existing drift, structure guard green, prose stale). New `en.json` key needs the `ut-plugin-language-de/es/pt` catch-up PRs (CLAUDE.md i18n rule; de is the priority pack). | card / same cycle |
| 10 | nit | Tests: `TestLineHandler_BadQtyNeverVoids` (fixed: dead code removed) has an unreachable `continue` (`:168-170`) and a `_ = strings.TrimSpace` import hack (`:180`); no test for the rounding floor (1), the discount-refusal ordering (2), the multi-SKU code path (3), or the audit `detail` from/to values. Add with the fixes. | fix with 1-3 |

Checked and found correct: actor vs approver (`allowed` → actor = session,
approver ""; `elevated` → `InsertAuditElevated(approver, blockedActor)`),
matching remove and ADR-0052 §4; gift-voucher exemption (`lockedGiftVoucherQty`
forces qty 1 unless 0, so qty=0 drops an unissued voucher ungated — #4214,
same as remove); `qty`+`delta` both present (delta wins on the key path, qty
ignored; code path ignores delta; replay carries absolute qty and no delta, so
no double-apply); invalid qty → 400, blank → current qty; `reason` validated
and defaulted to `void` before the PIN lookup (no lockout burn on a bad
reason); summary goes through html/template (item name escaped); meta
placeholders (4) match every locale's `%s` count and order.

## Verification
- TDD re-verified in a scratch copy (`origin/main`'s `pos_api.go`, this
  branch's tests): `TestLineHandler_CashierReductionNeedsManagerPIN` (all 6
  subcases), `_CashierWrongPINRefused`, `_ManagerPINApprovesReductionAndAudits`,
  `_ManagerReductionNeedsNoPINButIsAudited`, `_BadQtyNeverVoids` and
  `_AbsoluteQtyRejectsPositiveInfinity` fail with the claimed errors; with
  the fix all pass. `_CashierNonReductionsStayFree` and
  `_CashierMayReduceZeroValueLine` pass either way (negative cases, expected).
- Probes for findings 1 and 2 run against the fixed handler in the scratch
  copy (not committed): see the Findings column for the observed numbers.
- `gofmt -l` clean, `go build ./...`, `go vet ./internal/pages/`,
  `golangci-lint run ./internal/pages/` 0 issues, `go test ./internal/pages/
  -run 'TestLineHandler|TestRemove|Shrinkage|Elevation'` green; guards
  i18n, i18n-meta, help-drift, help-topics, compliance-claims green.

Pre-flight: help en updated (other locales stale, #9); en.json/meta done,
packs pending (#9); README n/a; ADR n/a (applies ADR-0052 as written, no
new mechanism); docs-shots not regenerated (prose-only change to an
existing step).

## Verdict
**Not safe to merge as-is.** Findings 1 and 2 are both small, both proven
by a probe, and both leave the gate the card exists to close either
bypassable (1) or mis-recording (2). Fix those with tests, decide 3 (fix or
card), card 4/6/9, then re-run the gate and re-review the delta.

## Fix pass (author)
Findings 1-3 and 10 fixed with failing-first tests (new tests added
alongside the fix; all `TestLineHandler*`, `TestRemoveHandler*` green).
Findings 4 (TOCTOU), 6 (weighed phantom reduction), 8 (hint wiped) and 9
(stale translated help; language-pack catch-up PRs) are carried as
follow-up cards; 5 and 7 accepted per ADR-0052 / the `/api/pos/remove`
precedent. The Tester found three further ungated value-loss paths outside
this card's scope (line discount to 100%, `/api/pos/discount`,
`/api/pos/reset` New sale, the last recorded in cleared_baskets): filed as
cards for an owner policy decision.

# Review: New sale and held-order cancel take the bound single-use PIN approval (ut-docs#7096)

Date: 2026-11-27. Lane `lane:cloud-41`. Built by Sonnet 5.5. Reviewed by an Opus 5.5 subagent in a separate worktree, at the WIP snapshot.

## What shipped

- **`/api/pos/reset` (New sale)** now goes through `checkOrElevateBound`, the same path as #7085.
  - The approval is bound to the posted line count and total, plus every line (LineKey, SKU, qty, price, line discount), the add-by-hand lines, the sale discount and the table.
  - It is used once, belongs to the session user and expires after 5 minutes.
  - The posted-value 409 and the `ResetAndSnapshotIf` compare-and-set stay as they were.
- **`/api/pos/held/cancel`** for staff without void/comp/waste: the held row is read first.
  - An unknown order answers "not found" with no PIN prompt.
  - The approval is bound to the id, view and tab, a normalised `offline` flag (with `offline_override` folded in), and the order as saved: UpdatedAt, total, line count, label, table and a sha256 of the payload.
  - Managers' path is unchanged.
  - Side fix: an `offline_override` cancel used to lose the flag on the PIN retry and send the fiscal cancel as if online. That no longer happens.
- **F1:** `/api/pos/line` also binds `LineKey` and `SKU`.
- **F2:** `lineTargetDetail` writes `code` into elevated audit detail only when `key` is empty, at all four sites.
- **Tests:** 17 new `TestElevationBind7096_*` tests.
  - `TestHeldCancel_CashierWithManagerPINRecordsApprover` now prompts first and then retries.
  - The e2e `resetBasketWithPIN` helper is used at 5 call sites.
- **Docs:** help sentence in `web/help/en/open-orders.md` (manifest topic hash updated). ut-docs `reference/roles-and-permissions.md` (ut-docs branch `docs/7096-bound-pin-gates`).

Out of scope, on ut-docs#7134: the refund page, the `/api/pos/sale/status` void, and the no-sale and shift gates.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| M1 | Medium | `HeldCancel_ReplayRefused` was a false pass: the order was gone after the first cancel, so the replay failed whether or not the approval was single-use. | **Fixed.** The test now re-seeds a byte-identical row (same payload and `updated_at`) before replaying. Verified by the orchestrator: with `consume` made non-consuming the test FAILS; restored, it passes. |
| L1 | Low | The unknown-order test only logged the popup body. | **Fixed.** It now asserts "not found". |
| L2 | Low | The F2 tests never posted an unbound `code` on the applying request. | **Fixed.** `retry.Set("code","junk")` in both subtests. |
| L3 | Low | e2e `resetBasketWithPIN` ignored 4xx/5xx replies. | **Fixed.** It throws on `!ok()` for both posts. |
| L4 | Low | The help prose said a reopened order re-asks; it actually answers "not found" while open. The 5-minute expiry wasn't mentioned. | **Fixed.** Prose corrected, manifest hash recomputed, `guard-docs-shots.sh` green. |
| L5 | Low | A PIN is spent on a cancel that is then refused (the order is open in this basket). | Deferred: ut-docs#7134 checklist. |
| L6 | Low | The cashier path reads the order from the main till twice without the short cross-till timeout. | Deferred: ut-docs#7134 checklist. |
| — | Accepted | Bound read before the claim in held cancel. The window is at most ~800 ms, and another till would have to resume and re-park the order inside it. The same applies to reset's count/total-only compare-and-set, as in #7085. | Accepted. "Claim only if unchanged" is on ut-docs#7134. |

## Extra verification

- **Tester:**
  - Mutations: each binding removed in turn turns its tests red.
  - Full gate: `go test ./... -count=1` passes (`internal/pages` 773 s); `golangci-lint` 0 issues; shellcheck 0; every ci.yml `build` guard passes, except the Windows signing step, which needs sudo apt and is unrelated.
  - e2e: specs 5416, 5417, 2534, open-orders-cancel-3582, till-confirm-labels-6117 and parked-move-toggle-3630 pass on a fresh till.
  - Driven run at 1024×600: a basket swapped for one with the same count and total re-asks and is not cleared. Cancelling an unknown held order answers "not found" with no PIN prompt.
- **Reviewer:** re-verified the reset, held-cancel and F2 mutations; build, vet and the targeted 54 tests pass.
- **After the fixes:** targeted tests, `go vet`, gofmt, and the help-topics and docs-shots guards pass.

Pre-flight:
- Till user-visible behaviour: `web/help/en/open-orders.md` updated. `make docs-shots` is n/a: no screenshot changes, and the topic hash was recomputed, which is what docs-shots writes.
- `reference/` page: updated (`roles-and-permissions.md`).
- README: n/a, its text is unaffected.
- `en.json` keys: n/a, no new strings.
- Follow-ups: deferred to ut-docs#7134.

Verdict: **ship** (after the fixes above).

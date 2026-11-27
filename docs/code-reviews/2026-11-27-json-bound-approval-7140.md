# Review: JSON bound approval for sale-status void and no-sale (ut-docs#7140)

Date: 2026-11-27. Lane `lane:cloud-41`. Built by Sonnet 5.5 (Dev + fix round). Architect brief and review by Opus 5.5 subagents (different model from the author). Tested by a separate Tester agent with a real driven run.

Pre-flight: Till user-visible behaviour n/a (both routes are API-only, no caller in `web/`, `mobile/` or `android/`, so no help topic or docs-shots); `reference/` updated in ut-docs (`roles-and-permissions.md`, `contracts/till-drawer-no-sale.md`, `architecture/pos-auth.md`, `architecture/receipt-printing.md`); en.json keys n/a (existing keys only); amended ADR updated (ADR-0052 amendment 2026-11-27; index line unchanged, nothing superseded); README n/a; follow-ups filed as a Backlog card (below). Supersession check: no commit on `main` since the branch point touches `internal/pages/elevation.go`, `pos_api.go`, `no_sale_api.go` or `internal/pos/sales.go`; the card was open and unclaimed.

## What shipped

- **One gate, two readers.** `checkOrElevateApproval(dp, r, binding, pin, tok, actions...)` in `internal/pages/elevation.go` holds the bound single-use logic from #7085; `checkOrElevateBound` (forms) is now a one-line wrapper that reads `r.Form`. Shared JSON helpers `elevationJSONCode` and `writeElevationRefusalJSON` answer `{"data":null,"error":{"code","message","approval"}}` with codes `manager_pin_required`, `approval_changed`, `invalid_pin`, `locked_out`, `insufficient_role`, `elevation_unavailable` (#7141 reuses them).
- **`POST /api/pos/sale/status` void:** the reusable `override_pin` no longer approves on its own. The 403 carries `error.approval`, bound to endpoint, session user, `refund`, `saleId`, `status`, `reason` and the sale's current status. Lockout stays 403. An impossible transition (already voided/refunded) answers the existing 409 before any prompt, PIN check or token spend (`pos.SaleStatusTransitionAllowed`).
- **Race closed:** `pos.UpdateSaleStatusFrom(..., expectedFrom, ...)` refuses with `ErrSaleStatusTransition` inside the existing transaction if the status moved since the approval was read; `UpdateSaleStatus` delegates with `""`, and the handler passes `expectedFrom` only on an elevated approval. No new SQL.
- **`POST /api/pos/no-sale`:** the reusable `manager_pin` no longer approves on its own; the token binds endpoint, session user, `cash_adjustment`, the trimmed reason and the register id (read-only `tillRegisterIDBestEffort` before the gate; the `EnsureRegister` self-heal stays after the kick). Lockout stays 429.
- Tests: `elevation_json_7140_test.go`, `pos_sale_status_bind_7140_test.go`, `no_sale_bind_7140_test.go`, two cases in `internal/pos/sale_status_transition_test.go`; `TestSaleStatus_VoidNeedsRefundPermission`, `TestNoSale_CashierWithManagerPINElevatesAndRecordsBoth`, `TestNoSale_CashierWithoutGrantNeedsPIN403RecordsNothing` and `TestNoSale_ElevatedEventCarriesApprover` rewritten to the two-step flow with names and assertions kept.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| R-L1 | Low | Nothing proved the no-sale token is bound to the register id (removing it from the binding left every test green). | **Fixed:** `TestNoSaleBind7140_RegisterChangeRefused`; fails with the register id dropped from the binding (`register changed = 200 ... want 403 approval_changed`). |
| R-L2 | Low | Voiding an already-voided/refunded sale still prompted, checked the PIN (a wrong one counted toward lockout) and spent the token before the 409. | **Fixed:** 409 before the gate; `TestSaleStatusVoid7140_ImpossibleTransition409BeforeGate` (no PIN, wrong PIN, right PIN → 409; the same token still approves once the sale is completed again). |
| T-G1 | Low | No automated test for a token reused on another `saleId`. | **Fixed:** `TestSaleStatusVoid7140_TokenForOtherSaleRefused`. |
| T-G2 | Low | The no-sale replay test did not assert "no new rows". | **Fixed:** exactly one event and one audit row after the replay. |
| T-G3 | Low | The void replay test still passes with consume disabled (the bound current status already refuses it). | **Accepted:** single use is covered by the gate unit test and the no-sale replay tests. |
| T-G4 | Low | The gate→write race is tested at the pos level only. | **Accepted:** the handler passes `expectedFrom` straight through; covered by `TestUpdateSaleStatusFrom_StaleExpectedFromRefusedNothingWritten`. |
| R-N | Nit | Unknown sale id now answers 404 before the permission check (was 403); an open sale's lines are not bound; one pre-existing hardcoded English message. | **Accepted.** |
| T-O1 | Nit | JSON `approval_changed` reuses the HTML prompt text ("Check what is shown above"). | **Deferred** → follow-up card. |
| T-O2 | Low | Every refused attempt mints a token into the shared 256-slot store, so one session can evict another's pending approval (since #7085; fails closed). | **Deferred** → follow-up card. |

## Verification beyond unit tests

- TDD re-verified by the reviewer in its own worktree: removing the `expectedFrom` check fails `TestUpdateSaleStatusFrom_StaleExpectedFromRefusedNothingWritten` ("err = <nil>, want ErrSaleStatusTransition"); restoring the one-step PIN fails five `NoSaleBind7140` tests ("one-step = 200"); dropping the sale status from the void binding fails `TestSaleStatusVoid7140_StatusChangedSincePromptRefused`.
- Tester mutations: consume off, `reason` dropped from the void binding, `expectedFrom` off — each caught.
- Driven run (real binary, temp data dir, seeded manager + cashier, drawer to a temp file, curl): void without PIN → 403 `manager_pin_required` + `error.approval`; edited reason / other saleId → `approval_changed`, nothing written; one-step PIN → 403; cashier PIN → `invalid_pin`, same token; token + manager PIN → 204 with one audit row (actor manager, blocked actor cashier); replay refused; token minted while parked refused after reopen. No-sale: padded reason → 200, one kick, event actor cashier / approver manager; replay refused, no kick; 5 wrong PINs `invalid_pin`, the 6th 429 `locked_out`.
- Gate: `gofmt`, `go build ./...`, `go vet`, full `go test ./internal/pages/ ./internal/pos/` (before the fix round; targeted pages tests + `-race` after), `golangci-lint` 0 issues, `guard-i18n`, `guard-data-access`, `guard-fixed-dates`, `guard-no-native-confirm`.

## Verdict

Ship. Deferred: `/api/pos/sale/status` POST-only with a body cap; per-actor cap on pending approval tokens; JSON-specific `approval_changed` wording — one Backlog card.

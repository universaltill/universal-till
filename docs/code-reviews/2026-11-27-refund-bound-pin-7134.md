# Review: the refund page takes the bound single-use PIN approval (ut-docs#7134)

Date: 2026-11-27. Lane `lane:cloud-41`. Built by Sonnet 5.5 (Dev + fix round). Reviewed by an Opus 5.5 subagent (different model from the author). Tested by a separate Sonnet Tester agent with a real driven run.

Pre-flight: card #7134 sliced by BA to the refund page only; the void/no-sale, shift and held-cancel items were split to ut-docs#7140, #7141 and #7142. Supersession check: no commit on `main` since the branch point touches `internal/pages/refund_page.go` or `refund.html`; draft universal-till#2768 (ut-docs#7104, Admin Review) edits the same handler and is unmerged.

## What shipped

- **`POST /api/refund`** no longer accepts a reusable `manager_pin` in the form body. A cashier without `refund` gets the standard elevation prompt (`X-UT-Response: elevation-prompt`, `HX-Retarget: #refund-msg`), and the retry carries a single-use approval from `checkOrElevateBound` (#7085), bound to the session user and:
  - the posted fields, in fixed order: receipt, normalised method, `refund_leg`, `offline`, `refund_attempt_id`, `qty_0…qty_{n-1}` (normalised as the line parser reads them);
  - server state: sale id, currency, total in minor units, returned qty per line (sorted keys), each payment's id and remaining amount, and the per-payment allocation.
- Order: sale read → non-HTMX caller without `refund` gets plain-text 403 "manager PIN required" before any PIN lookup → fiscal gate → `refundSaleLocks` → guard reads → line/qty validation → total → leg allocation → **bound check** → `EnsurePaymentMethod` (moved below the check) → fiscal sign start → provider refund. Every input error shows without a prompt and without spending a PIN.
- `refund.html`: inline PIN field and `NeedsManagerPIN` removed. New key `elevation.summary.refund` ("Refund %s from receipt %s, paid back by %s."), with the on-screen method name and the same amount string as the page's "Refund total" (UX M1/M2); `refund.manager_pin` and its meta-baseline line removed; `refund.help` English text updated.
- Help: `web/help/en/sell.md` step 6; manifest topic/surface hashes. ut-docs: `reference/roles-and-permissions.md`, `architecture/pos-auth.md`.
- Tests: `internal/pages/refund_elevation_bind_7134_test.go` (prompt, every bound field edited → refused with a valid PIN, returned-state change, replay/missing/forged/other-user/expired refused before PIN lookup, wrong PIN, lockout, manager unchanged, non-HTMX 403, second-line qty, spent approval after a provider decline); the two #3134 refund tests rewritten for the prompt flow.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| T1 | High (Tester) | `hx-disabled-elt` on `#refund-form` (added for UX N3) was inherited by `#refund-total` and the qty inputs; `/api/refund/preview` never fired and the total stayed £0.00. | **Fixed:** attribute removed; the double-tap guard dropped (each approval is single-use, so a double tap only mints a spare). The Tester proved this exact state restores the preview (route interception stripping only that attribute: £1.20, then £3.40 after an edit). |
| M1 | Medium | No test sale had more than one line, so `qty_1` binding was never exercised. | **Fixed:** `TestRefundBind7134_SecondLineQtyBound`. |
| M2 | Medium | The replay test could not tell "approval spent" from "state changed". | **Fixed:** `TestRefundBind7134_SpentApprovalRefusedAfterProviderDecline` — decline after a correct PIN, then the identical replay is refused before PIN lookup, no return recorded, no second provider call. |
| L1 | Low | The approval is spent when the PIN verifies, not when the refund succeeds; a provider decline means the manager types the PIN again. | **Accepted:** fails closed; same as #7085/#7096. |
| L2 | Low | Dead line in the non-HTMX test (correct PIN never sent). | **Fixed.** |
| L3 | Low | "NothingRecorded" test did not assert it. | **Fixed:** `noSideEffects` asserted. |
| L4 | Low | Quantities edited while the prompt is open are ignored: the refund uses what the prompt showed. | **Accepted:** correct for the binding; the scrim blocks editing anyway (UX). |
| L5 | Low | ut-docs said refund answers "the same 403" JSON body. | **Fixed:** plain-text 403 wording. |
| L6 | Low | `architecture/pos-auth.md:15` and `refund.help` still mentioned the inline PIN. | **Fixed** (English only). |
| L7 | Low | An edited `refund.html` comment said the opposite of what it meant. | **Fixed.** |

Design points recorded: the PIN check runs while the per-sale refund lock is held (in-process, same sale, same till; the lock already spans the provider call). Lockout shows inside the prompt at HTTP 200, like every #7085/#7096 site (the old 429 had no remaining reader). UX N1/N2 (which payment / item count in the summary) and N4 (retry reuses the first post's offline flag) deferred; N5 (raw method ids in the page's own dropdown, lockout duration) out of scope.

## Verification

- TDD: tests written first and failed (compile, then 403 instead of a prompt). Mutation checks: dropping the returned-qty loop or all extras fails `ReturnedStateChangedRefused`; reverting to unbound `checkOrElevate` fails 5 test groups (re-verified independently by the Tester, diff byte-identical after restore).
- `go test ./...` 131 packages ok (Tester, before the fix round); after the fix round `go test ./internal/pages/ -run 'Refund|3134|7134|Elevat'` ok; gofmt, vet, golangci-lint 0 issues; i18n, i18n-meta, help-topics, docs-shots, no-showmodal, no-native-confirm, rtl, fixed-dates, data-access guards green.
- Driven run (Chromium, DSF 1, real binary with auth on, seeded split-card sale, cashier user): no inline PIN; prompt "Refund £1.20 from receipt 000000001, paid back by Card."; fits 1024×600 and 360×740; no showModal (status bar not inert); focus/Tab/Escape/Cancel correct, form state survives Cancel; wrong PIN keeps the dialog; right PIN → `/journal/000000002`; 6th wrong PIN shows the lockout inside the dialog; manager goes straight through. Touch was emulated (`hasTouch`), not real hardware; kiosk mode, cash-sale prompt, dark theme and RTL at 1024×600 not driven.

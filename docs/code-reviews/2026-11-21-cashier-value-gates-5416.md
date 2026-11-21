# Review: cashier value gates — manager PIN for discounts and New sale (ut-docs#5416)

Delegated owner decision (admin-review-decisions.md entry 24): a cashier
needs a manager PIN to raise a line discount, raise the sale discount, or
clear a priced basket with New sale; every such change is audited. Built
over three `lane:cloud-41` runs (2026-11-20/21) by Sonnet Dev subagents;
tested by separate Sonnet Tester subagents; reviewed twice by independent
Opus 5.5 subagents (a different model from the author).

## Change

- `price_override` (shown as "Discounts" in the Sales group of the
  Permissions page) gates any raise of a line discount (`/api/pos/line`),
  the sale discount (`/api/pos/discount`) and a tender `discount` above the
  basket's own (`/api/pos/tender`, JSON 403 before any payment). Admin,
  manager and super_admin already hold it, so no migration.
- `void_comp_waste` gates New sale (`/api/pos/reset`) on a priced basket.
  Priced = any line has a non-zero unit price (`PriceCents`), so a basket a
  promo or discount brought to 0 still asks; an empty or all-free basket and
  a resumed held order (re-parked) never prompt.
- A post that lowers a qty and raises a discount checks the PIN once
  (`checkOrElevateAll`); the approver must hold both permissions.
- Retries replay the `from_*` values; a basket that changed while the
  prompt was open gets 409 and nothing is applied
  (`pos.ResetAndSnapshotIf` under the lock for New sale).
- `/api/pos/discount` answers a non-HTMX client without `price_override`
  with 403 `{data:null,error:{code:"forbidden"}}` instead of a 200 HTML
  prompt it cannot answer.
- Client: the approved New sale retry closes `#payment-overlay` (#1386
  regression found by Tester + UX); help topics `sell`, `elevation`,
  `users`, `reports`, `promotions`; English text of existing keys only
  (placeholders unchanged) plus the `elevation.summary.*` prompt texts.

## Findings

Round 1 (Opus 5.5, 2026-11-20, verdict *ship after fixes*):

| # | Sev | Finding | Outcome |
|---|-----|---------|---------|
| 1 | minor | New sale's `priced` was `lines > 0 && total != 0`: a promo to €0, then New sale, cleared with no PIN and no audit | Fixed: any non-zero `PriceCents`; `TestPOSReset_PricedBasketDiscountedToZero_CashierPrompted` (red before) + `TestPOSReset_OnlyFreeLines_ClearsWithoutPrompt` |
| 2 | minor | `/api/pos/discount` reads then sets the sale discount without CAS; a promo landing between is overwritten | Follow-up card (below) |
| 3 | minor | `/api/pos/discount` gave a non-HTMX client a 200 HTML prompt | Fixed: 403 JSON; `TestPOSDiscount_RaiseNonHtmx_Cashier403JSON` (red before) |
| 4 | minor | Approval is bound to the *from* value, not the *to* value (same as #6113's qty pattern, not a regression) | Follow-up card (below) |
| 5 | nit | An equal-value post turns a percent promo into a fixed amount; no value impact | Accepted |
| 6 | nit | Changing the English text of existing keys leaves translations stale until Weblate | Accepted; release note |
| 7 | risk | ~18 e2e specs POST `/api/pos/reset` for setup | Cashier specs run by the Tester (below) |

Round 2 (Opus 5.5, 2026-11-21, fixes 1 and 3, verdict *ship*): no bypass.
Every line kind carries its value in `PriceCents` (modifier deltas, weighed
per-kg price, gift voucher face value); add-by-hand lines exist only on a
resumed held order, which is re-parked before the check.

| # | Sev | Finding | Outcome |
|---|-----|---------|---------|
| 1 | minor | A non-HTMX client with a valid manager `override_pin` is refused before the PIN is checked | Intended, same as tender: an API client raises a discount only with a session holding `price_override` |
| 2 | minor | `/api/pos/reset` and the line gates still send a non-HTMX cashier a 200 HTML prompt (nothing cleared) | Follow-up card (below) |
| 3 | nit | The 403 still carries `HX-Retarget`/`HX-Reswap` | Accepted (ignored by non-HTMX clients) |
| 4 | nit | Comment said "positive" where the code tests non-zero | Fixed |

## Verification

- Round 1 Tester: 14 of 16 mutations red; driven run with auth on as a
  cashier (prompts, manager PIN applies, empty New sale free, tender 403,
  audit actor = approver, `blocked_actor_id` = cashier); PIN dialog above
  the payment overlay at 390px.
- Round 2: both new tests red on the pre-fix code, green after; reverting
  the `priced` loop turns the promo-to-zero test red. e2e (Chromium):
  default project 2534, 4252, 2763, hold-named-tab pass; auth project
  (login, 5416, 5417 ×3, locked-cashier 2534) 26/26 pass. The 5416 spec
  was missing from `AUTH_ONLY_SPECS` and is now registered there.
- `gofmt`, `go vet`, `go test ./internal/pos/...`, `./internal/data`,
  `./internal/pages` green; guards per CI.

Pre-flight: help topics `sell`/`elevation`/`users`/`reports`/`promotions` updated; docs-shots n/a (no layout change, CI `guard-docs-shots.sh`); en.json existing keys' English text + `elevation.summary.*` with meta updated; reference `ut-docs/reference/roles-and-permissions.md` updated (ut-docs PR, part of #5416); README n/a (no feature claim); ADR n/a (extends #557 elevation and ADR-0128 grants); follow-ups → Backlog card.

## Release note

A custom role without `price_override` now needs a manager PIN for every
discount raise; a cashier needs one for New sale on a priced basket.
"Price overrides" is now labelled "Discounts".

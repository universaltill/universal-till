# Till value gates follow-ups (ut-docs#6858, items 1, 3, 4)

Follow-ups from `2026-11-21-cashier-value-gates-5416.md` (round 1 F2, F4;
round 2 F2/F3; round 3). Item 2 (bind the approval to the *to* value) was split
to ut-docs#7085 and is not part of this change. No bypass existed; the gates
and audit held. This makes them exact.

## Changes

- **Item 1.** `pos.Service.SetDiscountIf(from, to)`: a compare-and-set on the
  computed sale discount (what `SaleDiscount()` returns), `ErrLineChanged` on a
  mismatch. `/api/pos/discount` always applies through it; a lost CAS is the
  existing stale 409 (`LineChangedKey = "sale"`), nothing audited. Tender reads
  `SaleDiscount()` once, so the gate and the audit `from` agree.
- **Item 3.** A request without `HX-Request` from a session lacking the gated
  permission gets 403 JSON (`writeForbiddenJSON`, the discount envelope) before
  the PIN is looked at, on `/api/pos/reset` (priced basket), `/api/pos/line`
  reduction, raise, and reduce+raise (both permissions). Keys reused:
  `pos.error.discount_not_permitted`, `common.error.manager_or_admin_required`;
  no new key, one meta description widened.
- **Item 4.** `/api/pos/discount` sends `HX-Retarget: #basket` only on the
  approval retry (`override_pin` or `from_discount`), as `/api/pos/reset`.

## Follow-ups noted, not done

- Tender's third `SaleDiscount()` read (charged amount when `in.Discount == 0`)
  is a money-path change; left alone.
- `/api/pos/remove` has the same non-HTMX 200-prompt shape.
- Round 1 F5 (equal-value post flattens a percent promo) unchanged.

## Tests

`internal/pos/service_discount_if_test.go`;
`internal/pages/pos_value_gates_6858_test.go`. Cashier posts in the 5416, line
CAS and line reduce-gate tests moved to `postFormHtmx` (they model the till's
page). Five e2e `/api/pos/reset` setup posts gained `HX-Request: true`.

## Review (independent, Opus 5.5; code by Sonnet)

Scope: `git diff origin/main...HEAD` at 883f46a8f, plus ut-docs
`docs/6858-pos-gate-errors` (`reference/roles-and-permissions.md`, separate
record).

### Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| R1 | low (test gap) | `/api/pos/line` reduce+raise non-HTMX gate needs *both* permissions, but the test cashier holds neither, so `\|\|` → `&&` still passed. | **Fixed**: `TestPOSLine_NonHtmx_ReduceAndRaise_OnePermissionIsNotEnough` grants the cashier role one permission at a time (real `role_permissions` row). Red with `&&` (both subtests: 200 instead of 403), green with the shipped `\|\|`. |
| R2 | nit | A plain (non-HTMX) `/api/pos/discount` post carrying `override_pin` or `from_discount` still gets `HX-Retarget: #basket` on its 403/409. | **Accepted**: a non-HTMX caller ignores HX-* headers; the header must be set before the `from_discount` 409, which the retry needs. |
| R3 | note | Behaviour change for integrations: a non-HTMX caller without the permission that sent a manager `override_pin` to `/api/pos/reset` or `/api/pos/line` used to be approved; it now gets 403 before the PIN. | **Accepted**: same contract `/api/pos/discount` has had since #5416 (round 2 F1); documented in ut-docs `roles-and-permissions.md`. The five e2e setup posts that relied on it now send `HX-Request: true`; no other e2e raw post runs as a cashier (checked: the remaining `page.request.post('/api/pos/reset')` calls run as the default admin). |

Checked and found sound:
- **Locking.** `SetDiscountIf` holds `s.mu` and calls only `recomputeTotals`
  and `setDiscountLocked` (which calls `recomputeTotals`), the same shape as
  `SaleDiscount`/`SetDiscount`; nothing re-locks `s.mu`. With tax/charge
  plugins `recomputeTotals` drops and retakes the lock internally, but the
  compare and the write both happen after it returns, under the lock.
- **Percent promos.** The CAS compares the recomputed `s.basket.Discount`
  (what `SaleDiscount()` returns), not `discountValue`; the percent test
  fails both ways if it compared the raw value.
- **Refused requests do not mutate or audit.** Lost CAS returns before
  `auditValueGate`; the 403s return before `checkOrElevate`, so no PIN
  attempt, lockout tick, shrinkage, cleared-basket row or audit (tests count
  `audit_log`, `shrinkage_events`, `cleared_baskets`). Only the gate
  decision runs before the CAS; `verifyElevationPIN` writes no audit row.
- **Order.** discount: `from_discount` 409 → 403 → PIN; reset: `from_lines`
  /`from_total` 409 → 403 → PIN; line: reason 400 → stale 409 → 403 → PIN.
  Consistent with the discount precedent.
- **Error shape.** `writeForbiddenJSON` is byte-identical to the former inline
  discount/tender 403 (`{"data":null,"error":{"code":"forbidden","message":…}}`);
  keys exist in `en.json` and have meta; one meta description widened, no
  locale touched.
- **Gate discovery.** Permission checks keep literal `canPerform(d, r,
  "<action>")` arguments.
- Money stays `money.Money` to the boundary. No file writes, no paths: the
  `os.MkdirAll` / `paths.Data` recurring bugs do not apply.

### Extra verification (reviewer, in a detached worktree)

- `go build ./...`, `go vet ./internal/pos/ ./internal/pages/`, `gofmt -l internal/`: clean.
- TDD re-verified by reverting each fix hunk, one at a time:
  - engine CAS made a plain set → `TestSetDiscountIf_RefusesWhenDiscountMoved`
    (`got <nil>`), `TestSetDiscountIf_PercentPromoComparesComputed` and
    `TestPOSDiscount_PromoLandsBeforeApply_409NoAudit` (`want 409, got 200`) red;
  - tender re-read after the gate → `TestPOSTender_AuditFromEqualsGateRead` red
    (no audit row: the second read disagreed with the gate);
  - reset 403 removed → `TestPOSReset_NonHtmx_Cashier403JSON` red (`want 403, got 200`);
  - line 403 block removed → `TestPOSLine_NonHtmx_Cashier403JSON` red in all 3 subtests;
  - unconditional retarget restored → `TestPOSDiscount_PlainRequest_DoesNotRetarget` red.
  Restored, all green.
- `go test ./internal/pos/` and `go test ./internal/pages/` (full packages): green.

### Verdict

**Approve.** One test gap fixed (R1); no production code changed by review.

### Deferred

- Item 2 (approval bound to the *to* value): ut-docs#7085.
- Tender's third `SaleDiscount()` read, `/api/pos/remove` non-HTMX 200 prompt,
  round 1 F5: listed above under "Follow-ups noted"; to be carded on the epic
  checklist by the orchestrator (<1 day each).

Pre-flight: till user-visible behaviour n/a (HTMX till page unchanged; only
non-HTMX callers and a lost race differ) — `web/help/` and docs-shots n/a,
`README.md` n/a; `reference/roles-and-permissions.md` updated (ut-docs
`docs/6858-pos-gate-errors`); `en.json` keys n/a (none new), `en.meta.json`
updated (one description widened); ADR n/a (existing #5416/#6113 patterns);
plugin/manifest/host function n/a; plan/price n/a; review record: this file;
follow-ups: #7085 plus the three items above → epic checklist lines.

# Review: enforce the Refund and Cash adjustment permissions, hide Void and Price override (ut-docs#3134)

Date: 2026-09-29 · Lane: `lane:cloud-24` · Author: Opus 5.5 (dev subagent) · Reviewer: Fable (independent, fresh context)

## What shipped

- **Refund** (`POST /api/refund`, `internal/pages/refund_page.go`) is gated by `checkOrElevate(…, "refund", manager_pin)`, the same shape as `void_comp_waste`.
  - A role holding `refund` refunds with no PIN.
  - A role without it needs a manager PIN. The approver must hold `refund` too. The approver is the audit actor, and the audit row is `InsertAuditElevated`, naming the blocked user.
  - The `UT_AUTH`-off bypass and the 403/429 responses are unchanged.
  - `refund.html` renders the (still `required`) PIN field only when the session lacks the grant.
- **Cash adjustment** (`internal/pages/shifts_api.go`): the skim-at-close gate and the negative-adjustment (payout) gate use `checkOrElevate(…, "cash_adjustment", …)`, with the same outcome mapping.
  - `internal/pos/shifts.go` gains `SkimBlockedActorID` and `BlockedActorID`, so an elevated row names both people. The skim row stays inside the close transaction.
  - Positive adjustments stay ungated. The Pfand payout gate is untouched.
  - `shifts.html` hides the PIN fields and the skim hint when the session holds the grant.
- **Void and Price override** are removed from the permissions grid. The till has no whole-sale void (line voids are `void_comp_waste`) and no price-override feature.
  - They are listed in `permissionHiddenActions` with the reason, and are skipped before grouping, so they don't land in the "Other" group either.
  - This is display only: no migration, and stored grants are untouched (a test pins this).
  - `permissions.action.*` labels stay, because historical audit rows render them.
- **Locales and help:** the `permissions.action_desc.{refund,cash_adjustment}` strings are rewritten in en/ar/fa/tr, and `…{void,price_override}` are removed.
  - `users.md` and `reports.md` are updated in en/de/ar/fa/tr. The English Shrinkage paragraph no longer cites a "price override".
  - docs-shots are regenerated (manifest only; no PNG changed).
- **Language packs:** de/es/pt follow-up PRs are opened in the same cycle (changed and removed keys).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The de/es/pt packs still say "the till doesn't check this box yet" and carry the removed keys. | Fixed: pack PRs in this cycle, after core merges (they fail drift against core `main` until then). |
| 2 | nit | `web/help/en/reports.md` Shrinkage said the cashier's PIN is "the same approval a price override needs", which cites a feature that doesn't exist. | Fixed (English only; the translations don't carry this section yet, per the help-drift baseline). |
| 3 | nit | `registerRefund` kept an unused `_ *auth.Service` parameter. | Fixed: the parameter is removed along with its two callers. |
| 4 | nit | The `skimApproverID == ""` fail-closed branch is unreachable (`auth.UserID` returns "system"). | Accepted: harmless defensive code. |
| 5 | nit | The label/desc key asymmetry for hidden actions isn't documented. | Accepted: `TestPermissionGroups_EveryActionInExactlyOneGroup` pins that a hidden action has no `action_desc` key, and the labels are needed for audit rows. |
| 6 | nit | If a grant is revoked mid-session, a page rendered without the PIN field gets a 403 with no field to type into. | Accepted: htmx senderror shows the message and a reload shows the field. This is the same shape as before for `UT_AUTH` toggles. |

The reviewer found no blocker or major issue. It checked these security paths:
- A blank PIN never reaches `AuthorizeManager`, so no lockout attempt is burned.
- A wrong PIN gives 403, and a lockout gives 429.
- An approver whose role lacks the grant is refused. This is stricter than the old "any manager PIN".
- Shifts decode `ManagerPIN` from both JSON and form.
- No satellite forwarding exists for these routes.

## Verification

- **TDD re-verified by the reviewer in a separate worktree:** with the source reverted, the claimed tests fail:
  - Refund: `ManagerSessionNeedsNoPIN`, `CashierGrantedRefundNeedsNoPIN`, `CashierWithManagerPINElevatesAndRecordsBoth`, `PINFieldOnlyWhenSessionCannotRefund`.
  - Shifts: `CashierWithManagerPINElevatesAndRecordsBoth` ×2, `GrantedRoleNeedsNoPIN` ×4, `PINFieldOnlyWhenSessionCannotAdjustCash`.

  With the source restored, they pass.
- The refused-path tests (no grant and no PIN; grant revoked from manager) pass both before and after the change. They pin the unchanged refusal and that nothing is written.
- `gofmt`, `go build`, `go vet`, full `go test ./...` and `golangci-lint` (0 issues) all pass.
- Every `ci.yml` build-job guard passes, with three exceptions that are local environment only:
  - `guard-deadcode-baseline` fails identically on the unmodified tree, because the GTK headers are missing and `cmd/unitill-desktop` is skipped.
  - shellcheck isn't installed, and apt is blocked. No shell script changed.
- **Playwright:**
  - `default` project, affected specs (refund/shifts/permissions/deposit/OSK): 16/16.
  - Full `auth` project: 30/30.
  - `make docs-shots`: 120/120.
- **Visual:** no new screenshots were taken. The change only removes a field when the role holds the grant; the permissions-matrix layout spec (1024×600 and 360×740, en and fa) passes.

## Verdict

Safe to merge. Deferred: none.

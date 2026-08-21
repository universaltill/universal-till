# Code review: wire settings_page.go's core/consequential mutations onto manager-override elevation, slice 1 (ut-docs#796)

**Date:** 2026-08-21
**Card:** ut-docs#796 (split off ut-docs#781/#557; itself further BA-scoped down
to a single buildable slice — remainder tracked as ut-docs#865 and ut-docs#866)
**Complexity:** hard — build: Fable subagent, review: Opus (fresh-context
subagent, isolated worktree)

## What shipped

8 mutating, previously-unaudited `canPerform(d, r, "settings")`-gated
handlers in `internal/pages/settings_page.go` moved off a flat
`if !canPerform(...) { 403 / error-span }` gate onto the existing generic
manager-override-elevation mechanism (`internal/pages/elevation.go`'s
`checkOrElevate`/`renderElevationPrompt`, dual-attribution audit via
`InsertAuditElevated`/`InsertAudit`), mirroring the 6 already-wired sites
(`permission_settings_page.go`, `backup_api.go`, `sync_api.go`,
`users_page.go`/#795, `eod_api.go`/#794, `reports_page.go`):

`payments-default`, `payments-fee`, `remove-demo-catalogue` (irreversible
demo-data deletion), `shop-type`, `till-name`, `till-register`, `save`,
`upsert`. None wrote an audit entry before this change; each now writes one
(`InsertAudit` on the allowed path, `InsertAuditElevated` on the elevated
path, via two new small helpers `settingsAudit`/`settingsRespondSaved`).

`upsert` is the highest-risk site: it has its OWN interior ADR-0048
`fiscal_tse_override` gates for `fiscal.*` keys. Only the OUTER `"settings"`
gate was touched — the interior fiscal checks are untouched and still read
the session user, not the elevation's approver, so an elevated "settings"
approval can never leak into fiscal authorization (proved, see below).

New i18n: 8 `elevation.summary.*` keys, real translations in all 4 locales
(en/ar/fa/tr). Template changes in `web/ui/pages/settings.html`: new
elevation-retry `<span>` targets on 5 forms, and the till-name/till-register/
save forms' `hx-on::after-request` reload guard updated to skip reload on a
`text/html` (elevation-prompt) response — same pattern `eod_api.go`'s
report-retention handler already established (#794).

**Non-goals, explicitly out of scope and untouched**: the 2
`fiscal_tse_override`-gated sites in this file; `GET /api/enrol/devices`
(read-only); the other 10 `settings_page.go` sites (device/session UX
toggles + enrolment — ut-docs#865); `receipt_designer.go`/`print_api.go`/
`invoice_page.go`/`menu_page.go` (ut-docs#866).

## Independent review (Opus, isolated worktree)

Ran the full gate personally (build, vet, `go test -count=1 ./...`, all 6
guard scripts) and did NOT just trust the implementer's claims — reverted
each of 4 pieces of the fix in turn (the `remove-demo-catalogue` gate, the
`upsert` gate, `settingsAudit`'s elevated branch, and its actor/blocked
assignment) and confirmed the corresponding new test genuinely fails against
the reverted code, then restored and confirmed green again. Also wrote a
throwaway probe against a real migrated DB to test the single highest-risk
property by hand: an **admin** approver (who genuinely holds
`fiscal_tse_override` themselves) elevating a **cashier** session still gets
403 on a `fiscal.*` write — nothing written. Proved, not just reasoned
about: `canPerform` reads the session off `r`'s context, and `checkOrElevate`
never mutates `r`.

Found no blockers. 3 should-fix findings, judged real and fixed on this
branch before merge (all small, mechanical, re-verified with the full gate
afterward — no second review round: none of these are blocker-class
(money/tax/data-loss/security) findings, and the one property that *is*
security-adjacent — the fiscal-gate composition — was independently proved
safe, not just re-argued):

1. **Validation-before-elevation violated in `upsert` for its two
   always-rejected fiscal keys.** `fiscal.tse_failing_since` (always 400, for
   anyone) and a non-empty `fiscal.override_*` write (always 400, for
   anyone) were checked AFTER `checkOrElevate`, so a manager got walked
   through a live PIN entry for a request that was always going to fail —
   exactly the cost the established validate-before-elevate convention
   (`permission_settings_page.go`) exists to avoid. **Fix**: split the
   `fiscal.*` switch into a pure-validation pass (hoisted above the gate)
   and the real `canPerform("fiscal_tse_override")` authorization checks
   (left exactly where they were, after the gate, reading the session user).
2. **`remove-demo-catalogue`'s live-basket refusal ran after the gate.** An
   approver could PIN in only to be told a demo item is in the current
   basket — a read-only precondition, not authorization. **Fix**: moved
   `demoDataInLiveBasket` above `checkOrElevate`, same class of fix as
   `payments-fee`'s range check (already correctly ordered by the
   implementer).
3. **Elevated success on `shop-type`/`till-name`/`till-register`/`save`/
   `upsert` left a stale page.** `settingsRespondSaved`'s elevated branch
   answered 200 without `X-UT-Response: ok`, and `elevation_prompt.html`'s
   own retry-form handler keys its post-close `window.location.reload()`
   off exactly that header — without it the dialog closed but the form kept
   showing the pre-change value. **Fix**: added the header, matching
   `users_page.go`'s `usersRespondOK` precedent (#795 review finding 1).

Also folded in while the branch was open (cheap, flagged as nits but worth
fixing before merge):

4. `settings_elevation_test.go`'s `assertElevatedAudit` scanned
   `blocked_actor_id` into a bare `string`; a real dual-attribution bug
   would have misdiagnosed as "no audit row" (a NULL→string Scan error)
   instead of "wrong value". Switched to `sql.NullString`.
5. Added a permanent regression test for the fiscal-composition property
   the review proved by hand
   (`TestSettingsUpsert_ElevatedSettingsApprovalDoesNotGrantFiscalOverride`)
   — an admin approver elevating a cashier's `"settings"` denial still gets
   403 on a `fiscal.*` write, against a real migrated DB so migration 046's
   actual admin/super_admin-only grant is in effect.

**Deferred, not fixed here** (real finding, but inherited from the existing
6-site rollout, not introduced by this diff, and fixing it means revisiting
the visibility model across all 14 sites at once): every one of the 8 wired
forms sits inside `web/ui/pages/settings.html`'s `{{ if .isManager }}`
block, so a denied session never renders the form at all and the dialog can
only be triggered by a hand-crafted POST — same gap already present on
`/api/backup/now` (an already-live #557 site) in the same template. Filed as
ut-docs#867, a cross-cutting card against all elevation-wired settings UI,
not scoped to this slice.

## Verified beyond automated tests

- Full `go test -count=1 ./...` green (zero failures) after the review
  fixes, not just the targeted `internal/pages` package.
- All 6 guard scripts green: `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-i18n.sh` (1116 keys, all locales match), `guard-help-topics.sh`,
  `guard-plugin-menu-read.sh`, `guard-compliance-claims.sh`.
- No real client/shop name in any fixture (`"Elevation Manager"`,
  `"Blocked Cashier"`, `"Front Till"`, generic register names). No
  secret-shaped literal — test PINs match the existing `backup_api_test.go`
  fixture convention.
- `/api/settings/*` is not in `auth.exempt()` — an anonymous request 401s
  before reaching any of these handlers in production; the test harness's
  "no session" case bypasses middleware intentionally to test the handler's
  own gate in isolation.

## Safe-to-merge verdict

Safe to merge. All should-fix findings addressed on this branch; the one
deferred finding is real but pre-existing and cross-cutting, filed as a
separate follow-up (ut-docs#867) rather than scope-creeping this slice.

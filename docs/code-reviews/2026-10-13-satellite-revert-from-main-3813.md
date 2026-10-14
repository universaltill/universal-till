# Review: a self-made satellite is revertible from the main till in one step (ut-docs#3813)

**Card:** ut-docs#3813, split from the #2781 review. It said a joined
*additional* till that confirms "Make this a satellite?" locally sets only
its own `sync.till_role`, never the main till's authoritative `tills.role`,
so the main till's Tills page can't revert it (Save role with the same
value is a no-op; a double flip was needed). It also asked to replace a
vacuous `TestReconcileOwnTillRole_NoOpOnAMainTill` and re-verify a
reworded `tills.role.error.satellite_mode` string.

## Finding: superseded in code

The #2781 implementation that merged took the card's option 1. In
`POST /api/settings/display-mode` (`settings_page.go`) the confirmed
make-satellite branch calls `reportOwnRoleToMain` **before** changing
anything locally; the main till's `POST /api/sync/till-role`
(`till_role.go`) runs `changeTillRole` → `tills.role = satellite` plus a
`role_changed` audit row, and an unreachable main till refuses the switch.
So the roster already says Satellite, and choosing Additional there is a
real change. Neither the vacuous test nor the `tills.role.error.satellite_mode`
key exists anywhere in the repo; they belonged to an implementation that
never merged. No product code changes.

## What shipped

- `TestSelfMadeSatellite_RevertedFromMainTillInOneStep`
  (`internal/pages/till_role_test.go`) pins the card's scenario end to end:
  self-confirm → main roster `satellite` → one manager change to
  `additional` on the main till (audited `satellite→additional`) → the
  admin pull bundle moves and reports `additional` → the till accepts
  `register` again.
- `auditRows` test helper orders by `created_at, rowid` (two rows in the
  same second were ordered only by SQLite's scan order).

## Review

Author: Opus 5.5. Reviewer: Fable (different model), one round.
Verdict: **safe to merge**.

- **M1 (medium, out of scope → ut-docs#4263):** `POST /api/settings/upsert`
  only gates `satellite → register/backoffice`; `display.mode=self_order`
  on an additional till goes through with no confirm and no write-through,
  so a kiosk can run while `tills.role` says additional. API-only today
  (the Settings page only reads the key). Not the card's scenario; filed.
- **L1 (low, fixed):** audit tie ordering, as above.
- **N1 (nit, fixed):** comment that the test's bare main mux (UT_AUTH=off)
  pins the revert, not the manager gate (covered by
  `TestTillRoleChange_ManagerOnlyAuditedAndShownOnRoster`).
- Checked sound: the bundle `Version` comparison is meaningful (migrations
  023/067 triggers bump the generation; `before` is taken after the
  self-report); `rememberOwnTillRole(after.Role)` mirrors the real replica
  apply in `sync_admin.go`; only placeholder names and secrets.

## Verified beyond the test passing

- Mutation 1: `/api/sync/till-role` not writing the role → the test fails
  at "main till's roster after the self-confirm = additional".
- Mutation 2: `changeTillRole` treating every change as a no-op → fails at
  the audit assertion.
- Reviewer ran the role/display-mode tests `-count=3`: no flakes.
- No UI, help or locale change, so no UX/manual step.

## Deferred

- ut-docs#4263 (M1 above).
- The card's ar/fa/tr native-speaker re-verify concerned a string that
  never merged; nothing to verify.

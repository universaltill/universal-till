# 2026-10-07 — Till role (additional / satellite), pairing + gating

- **Issue:** ut-docs#2781
- **Repo:** `universal-till`
- **Built by:** Opus 5.5 (dev subagent). **Reviewed by:** Fable (independent, different model).

## What shipped

A persisted, manager-assigned `tills.role` column (`additional` default |
`satellite`), migration `067_tills_role.sql` (with its own
`AFTER UPDATE OF role` trigger — migration 023's existing `tills` trigger
only fires on name/enrolled_at, confirmed by reading it). Both join paths
("Find on this network" and "Paste a pairing code") get a role picker that
threads through to `POST /api/sync/enroll` → `InsertTill`; the replica
persists the role it was given (`sync.till_role` / `sync.till_role_main`
settings) and sets `display.mode=self_order` immediately if satellite. A
new manager-only `POST /api/sync/tills/{id}/role` (modeled on the existing
Revoke handler: same gate, same primary-only refusal, same audit/trigger
convention) lets a manager change a joined till's role later from the Tills
page. `display.mode` is gated so a satellite till can only run
`self_order` (kiosk/order-capture) — `register`/`backoffice` are refused
with a 409, both via the dedicated display-mode endpoint and the generic
settings-upsert path, checked before PIN elevation. An additional till
picking "Self-order kiosk" gets an `hx-confirm` ("Make this a satellite?")
that sets its own role locally. After every admin pull, `reconcileOwnTillRole`
adopts a role change from the main till and forces a satellite caught on
register/backoffice back to kiosk, audited. Fleetlink's hello now reports
`Role: "satellite"` instead of always `"replica"`, resolving a TODO at
`hub.go:250`. 7 new `tills.role.*` i18n keys added to all 4 locale files.
25+ test files touched; new `internal/pages/till_role_test.go` plus a new
real end-to-end fleetlink-link test.

Scope deliberately excludes ut-docs#1154 (actual satellite payment/fiscal
architecture, ADR-0086) — a satellite here means "kiosk/order-capture
only," nothing about payment terminals or fiscal signing.

## Verification beyond automated tests

- `go build ./...`, `go vet ./...` clean.
- `go test ./internal/data/... ./internal/db/... ./internal/pages/... ./internal/fleetlink/...` — all green, run independently by both the reviewer and this orchestrator (not taken on the Dev subagent's word).
- `bash scripts/ci/guard-i18n.sh` green (2059 keys resolve, all 4 locales match, no duplicates/orphans) — re-run after the review-driven text fix below, still green.
- TDD re-verification (reviewer + orchestrator, without disabling any auth/permission gate — a live session classifier here refuses edits that look like weakening a security check, even transiently for a revert-run-restore cycle, so that technique was limited to pure validation/data-shape logic):
  - Orchestrator: reverted `internal/pages/settings_page.go`'s satellite gate via `git stash` → `TestDisplayMode_SatelliteRefusesRegisterAndBackoffice` failed with the expected 204-vs-409 mismatch → restored, test green again.
  - Orchestrator: cross-read the new `POST /api/sync/tills/{id}/role` primary-only gate against the other three existing `SyncPrimaryURL(r.Context()) != ""` call sites in `sync_api.go` — identical shape, not inverted.
  - Reviewer: flipped `ValidTillRole` to `return true` → `TestValidTillRole` and `TestTillRoleChange_OnPrimaryPersistsAuditsAndRefreshesRoster` failed as expected (SQLite's own `CHECK` constraint caught it too — defence in depth) → restored.
  - Reviewer independently traced the main-till lockout question (can `reconcileOwnTillRole` ever run on/against the main till itself?) by reading `syncPullTick`'s `primary_url == ""` early return and `till_role.go`'s own `sync.till_id == ""` guard, rather than trusting the test named for it (see finding F2 — that test turned out to be vacuous).

## Findings

| # | Severity | Summary | Outcome |
|---|---|---|---|
| F1 | should-fix | A till that confirms "Make this a satellite?" locally never writes that back to the main till's `tills.role`, which stays `additional`. The Tills-page roster then shows "Additional" and **Save role** is a no-op at that value, so the documented recovery ("change its role on the main till's Tills page first") doesn't work in the common case — only an undocumented double-flip (set Satellite, wait for a pull, set Additional) does. | **Fixed the misleading symptom this session**: reworded `tills.role.error.satellite_mode` (en/ar/tr/fa) to describe the actual double-flip instead of promising a single step. **Filed ut-docs#3813** for the real fix (write-through to the main till, or a local "make this additional again" path) and for F2 below — not fixed here, per the reviewer's own stated bar for an acceptable minimal remedy. |
| F2 | should-fix (test quality) | `TestReconcileOwnTillRole_NoOpOnAMainTill` is vacuous — passes even with the real guard deleted; doesn't prove the main-till-safety property it's cited for (the property itself is real, confirmed by reading `syncPullTick`/`till_role.go`, just not by this test). | Deferred to ut-docs#3813 alongside F1. |
| F3 | nitpick | A till that becomes a satellite stops receiving `Hub.RelayCloudCheckin` (its own 2-minute check-in still covers it; existing replica-only behaviour, now reached by a new role). | Accepted, no action. |
| F4 | nitpick | Main till's roster can show two disagreeing role signals (live hello-reported role vs. the `tills.role` picker) during the F1 divergence window. | Accepted as a natural fix-F1 UI hook; no separate action. |
| F5 | nitpick | The two role-pickers validate the role before vs. after their respective auth gate (asymmetric, both safe — a 400 before a 403 leaks nothing). | Accepted, no action. |

No blocker-class finding (money/tax/data-loss/security). Nothing in the
diff touches payment, fiscal signing, correlation IDs, or sales — confirmed
by reading `internal/pages/till_role.go` end to end.

## Verdict

**Safe to merge** with F1's minimal remedy applied (done) and follow-up
filed (ut-docs#3813) for the real fix and the vacuous test. F3–F5 accepted
as-is.

## Deferred

- ut-docs#3813 (F1 write-through / local-undo path, F2 test replacement, native-speaker check on the reworded ar/tr/fa error string).
- The usual `ut-plugin-language-*` catch-up PRs for the 7 new `en.json` keys (advisory on this PR, blocking on `main` per `lang-pack-drift`).

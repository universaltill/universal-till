# Review: automatic till registration + change history (card ut-docs#6559, ADR-0200)

Reviewer: combined (one pass for the batch of one card). Uncommitted diffs of
`universal-till` (feat/6559-auto-registration-till), `ut-cloud`
(feat/6559-auto-registration-cloud), `ut-admin-app` (feat/6559-auto-registration-admin),
`ut-docs` (docs/6559-auto-registration-adr). No reset/checkout/stash done.

## Shipped
Background registration loop (notice first, stop switch, guards: dev build, demo,
UT_NEVER_REGISTER, IR, replica, held), anonymous register body (no shop/till name),
change-event outbox (migration 105, cap 200, batch 50, ack on 2xx/402), cloud
validation + 200-entry/12-month history ring, admin active-tills + TillHistory,
Settings "Cloud record" card, ADR-0200 + contract + legal record.

## Verification run
- till: `go build ./...`, `go vet ./internal/...` clean; guards i18n, i18n-meta, help-topics,
  never-register, data-access pass; `go test ./internal/db ./internal/enroll ./internal/cloudsync` pass.
  **`go test ./internal/data` FAILS (two tests, findings 1 and 2)**; the Tester's "all green" did not cover this.
- cloud: build, vet, tests for handlers/adminshops/api/claims/config pass (Postgres tests skip locally).
- admin: vitest (excluding .worktrees) passes. ut-docs `guard-adr-index` ok.
- Migration 105: pinned checksum present in `shipped_migrations_test.go`, test passes; append-only (105 is the new tail).

## Findings
| # | Sev | Finding | Status |
|---|---|---|---|
| 1 | BLOCKER | `registration.*` settings (notice_shown_at, notice_version, stopped, last_reported_version, last_checkin_at) are in neither `PerTillSettingPrefixes` nor `ShopWideSettingPrefixes` (`internal/data/sync_admin_repo.go` ~793). `TestSettingScope_EveryUsedKeyIsClassified` fails. Worse, unclassified = admin-synced: `last_checkin_at` is rewritten every cloud tick, moving the admin fingerprint so every replica re-pulls (the #2792 failure), and a main till's "stopped"/"notice shown" leaks onto replicas. Fix: add `"registration."` to `PerTillSettingPrefixes` (a failing test was drafted: `/tmp/tabletfix/rev/registration_per_till_6559_test.go.txt`). Also the const `changeEventsKey = "events"` (`internal/cloudsync/change_events.go:21`) is read by that scan as a settings key: rename/retype it (e.g. inline literal) so the scan ignores it. | NOT fixed: the repo hook refused Edit for the reviewer (needs the Dev role in this agent); apply as Dev |
| 2 | BLOCKER | `till_change_events` is in neither `adminTables` nor `nonAdminTables` (`sync_admin_repo.go` ~402): `TestSchemaTablesAreClassified` fails. Add to `nonAdminTables` with reason "this till's own outbox of change events to the cloud, never synced (ADR-0200)". | NOT fixed (same hook) |
| 3 | BLOCKER (e2e) | The five specs using `ensureRegisteredAutomatically` (cloud-catalog-bootstrap-4494, diagnostic-mode-indicator-2169, pair-confirm-escape-5275, registration-card-paid-4285, subscription-panel-4286; `e2e/tests/helpers.ts:~298`) can never pass: every e2e launcher sets `UT_NEVER_REGISTER=1` (`worker-till.ts:225` forces it), and e2e tills are source builds (`Version=dev`), so the loop is never started and the card never says registered; the helper times out after 30 s. They previously registered through the removed button. Needs a design choice: a test-only seam (not in release builds) or have these specs register via claim/pair against the fake cloud. Not runnable here (no Playwright). | NOT fixed; back to Dev |
| 4 | HIGH | The stop switch does not halt everything: `EnsureRegistered` (plugin download/install fallback) and the main-till device-registration loop (`enroll.Init`, `needDevice`) do not read `registration.stopped`, so a stopped till still registers (WITH shop name and till name, non-anonymous) on its next plugin download. ADR-0200 s6 and the help say "registers nothing". Either gate them on `ReportingAllowed`-minus-notice or document the exception in ADR, notice and help. | NOT fixed; owner/Dev decision |
| 5 | MEDIUM | Anonymous register is undone one tick later: the sync/check-in device report still carries `name` (till name, `cloudsync.go:1532`) and, on a main till, `store_name` (`pages/cloudsync_wire.go:1578`). The cloud does not store them for an unpaid (402) till (`recordRefusedDevice`), but they are on the wire, and a paid till's name is adopted. The notice text ("the shop name you chose") says they are sent, so notice and the "no names" owner decision disagree. Owner decision needed: drop both from unpaid check-ins, or keep and say so in ADR-0200. | NOT fixed; owner Q |
| 6 | MEDIUM | `takeStartupCheckin` is gated only by Stopped, not by the notice/dev/demo/never guards, so an already-registered till sends its start-up check-in before the notice is shown (those tills accepted the terms when registering). Confirm this is intended; otherwise use `ReportingAllowed`. | NOT fixed; confirm |
| 7 | LOW | Stale references: `settings.enrol.retry` ("Register now") still in en.json/meta; comments mentioning Register now (settings.html 179/202, sync_gate.go, pair.go, enroll.go 169/534/656/893, settings_page.go, enrol_terms.go, operator_checkin.go); help claim.md "Name your shop first ... the till then registers itself" is stale because the anonymous register no longer waits for a name (the ErrStoreUnnamed branch of the loop is dead for the auto path). Orphan `setup.auto_register.*`, `settings.enrol.auto_register.*` keys. | NOT fixed (cleanup) |
| 8 | INFO | Tester said `go test ./...` passed on till; `./internal/data` fails (1, 2). Re-run after fixes. |  |

## Checked and fine
- Notice before first POST: `reportingAllowedWith` requires notice shown + current version; loop, outbox writes (`ReportingPolicy`) and event attach all use it. Notice is stored on wizard Finish or first Settings render by a manager.
- Events carry only id/kind/plugin_id/action/from/to/at; outbox cap 200 (newest kept), batch 50, ack only on 2xx/402, ids stable + deduped; 304 does not swallow events; system plugins and rolled-back installs record none.
- Offline-first: loop is a goroutine, 30 s attempt timeout, capped backoff with jitter; setup Finish only marks the notice and kicks.
- Cloud: events accepted only for a device-credential caller reporting exactly itself; invalid events dropped individually; history deduped, 12-month prune, cap 200; admin reads under the existing admin gates; `active_days` validated 1-365; old till/old cloud compatible (additive keys, blank store_name accepted). Deploy order cloud first, then till, admin after cloud.
- IR: till `DenyCountries` = cloud default; guarded in loop and events.
- Migration 105 pinned and last; guard-never-register wired for Makefile, ci.yml and run-till*.sh.

## Pre-flight
- Till user-visible behaviour: help claim.md/users.md updated; `make docs-shots` not run (deferred; Tester gap).
- en.json keys: en.meta.json updated, baseline updated, guard passes; English only.
- ADR: adr/README.md index updated, 0015/0026/0071 status lines updated, contract `reference/contracts/till-change-events.md` added.
- my./admin UI: admin-app TillHistory and active tiles added (i18n via `src/i18n/i18n.ts`); ut-cloud 10 locales n/a (admin-app only).
- Plan/price, platform claims, plugin taxonomy: n/a.

## Verdict
NOT READY TO COMMIT. Blockers 1, 2 (go test ./internal/data red; fix is small) and 3 (e2e specs cannot pass). Findings 4-6 need an owner/Dev decision before release; the notice wording and the legal terms gap remain owner-approval items (draft text, per ADR-0200). Re-review after Dev fixes: re-run `go test ./internal/data ./internal/enroll ./internal/cloudsync ./internal/pages`.

## Addendum (orchestrator, after fixes)

Findings 1-7 were fixed by a Dev agent (see /tmp/tabletfix/dev-auto-fix2.md): registration.* per-till, till_change_events classified, e2e seam `UT_E2E_AUTO_REGISTER` (loopback + source builds only), stop switch gates lazy register and device loop, no shop/till name in the automatic register nor the first check-in of an auto-registered unpaid till, check-in honours the honesty guards, stale "Register now" removed. Full `internal/{data,db,enroll,cloudsync,pages,config,httpx}` green. Playwright specs not run locally (CI). Pre-flight: done.
Verdict: ready; CI is the final gate.

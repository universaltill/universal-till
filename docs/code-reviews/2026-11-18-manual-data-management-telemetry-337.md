# Review: manual topics for data management and telemetry (ut-docs#337)

Date: 2026-11-18 · Branch `docs/337-data-management-telemetry` · Lane `lane:cloud-41`
Author: Sonnet (Dev subagent) · Reviewer: Opus 5.5 (independent subagent, separate worktree)

## What shipped

- **New help topics** `web/help/en/data-management.md` (clear transaction
  history, reset archives, erase a customer, catalog cleanup, sample data,
  export) and `web/help/en/telemetry.md` (what plugin telemetry sends, how to
  switch it, what it does not cover). Both are route-less: every surface lives
  on `/settings`, which `display.md` owns.
- `display.md`'s one-line Data management items are now pointers to the new
  topic; `backups.md` and `updates.md` get a generated card screenshot and
  cross-links (restore vs reset archive, erase vs existing backups).
- `settings.html`: the Data management card's `?` opens `data-management`
  (was `display`); the Plugin telemetry card gains a `?` to `telemetry`.
- **Corrected copy:** `settings.telemetry.help` (English) said the till sends
  "anonymous install/update/error status". The code sends merchant, store and
  device ids plus each active plugin's id/version, and no error or update
  events. The English value now says so; the key keeps its name (no
  placeholder change), gains `en.meta.json` metadata, and leaves the
  shrink-only baseline. No other locale touched (ADR-0160).
- **docs-shots harness:** inline extra shots are no longer limited to topics
  declaring `routes:` (`e2e/tests-docs/lib.js` `extraShotsByTopic`,
  `scripts/ci/guard-docs-shots.sh` `extra_shots()`, both changed the same
  way). Four new read-only recipes: `backups-card`, `updates-card`,
  `data-management-card`, `telemetry-card` (visit `/settings`, set the
  `#cat-…` fragment in `interact`, assert the card is visible). Generated
  PNGs for en/fa/ar/tr + `manifest.json`. No ADR: no ADR states the
  routes rule; it was a harness convention.

## TDD

- `guard-docs-shots_test.sh` case (f) flipped from "a route-less extra must
  fail" to "fails only until the PNG + manifest entry exist", plus a pass
  case. **Re-verified by the orchestrator** in a throwaway worktree: with
  `origin/main`'s `guard-docs-shots.sh` + `lib.js` the test fails
  (6 failures, the route-less shots refused); with the fix it passes.
- `internal/manual` `TestInlineShot_RouteLessTopicRenders` pins that the
  renderer already handles route-less inline shots (passes without a code
  change, as designed).

## Findings (Opus review) and triage

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Telemetry setting is shop-wide (main-till lock region), topic implied per till | Fixed: sentence + link to Multiple tills |
| 2 | minor | "Every action here is recorded in the audit log": Run export is not audited | Fixed: lists the audited actions, says export is not |
| 3 | minor | Clear transaction history also archives parked orders, payments, stock movements | Fixed |
| 4 | minor | Erased customer also stays in the restore set-aside copy (kept as long as sales records) | Fixed |
| 5 | minor | Route-less extras' freshness rides on the surface hash, which only covers a routed topic's `routes[0]` | Documented in `guard-docs-shots.sh`, `lib.js` and the spec's how-to. A hard spec check was tried and refused six existing recipes (`/self-order/shop`, `/plugins/store…`) on `main`: a pre-existing gap → follow-up |
| 6 | nit | "came from the marketplace": `source` is hard-coded `"cloud"` | Fixed |
| 7 | nit | Erase notice "reaches each plugin" overstated delivery | Fixed |
| 8 | nit | Missing full stop in `backups.md` | Already present; no change |
| 9 | nit | Cashier also sees fiscal notices in the card | Accepted (harmless) |
| 10 | nit | Over-long guard header lines | Fixed (re-wrapped) |
| 11 | nit | Card shot clips the page heading | Accepted: same as other card shots |

All four new card recipes visit `/settings` = `display`'s `routes[0]`, so
this change's shots are covered by the surface hash.

## Verification

- Guards (orchestrator, after fixes): `guard-docs-shots.sh` (35 routed
  topics, 13 extra shots, fresh), `guard-docs-shots_test.sh`,
  `guard-help-topics.sh`, `guard-compliance-claims.sh` green. Dev and
  Reviewer also ran `guard-i18n.sh`, `guard-i18n-meta.sh`,
  `guard-help-drift.sh`, `guard-mail-addresses.sh`, `go vet`/`go test`
  `./internal/manual/` green.
- Tester (separate agent): driven run of an auth-off till with Playwright,
  both cards render, every bolded label matches `en.json`, telemetry
  tick/save/reload round-trips, both `?` links open their topic with the
  card image; visual check of the four en PNGs. Not driven: PIN prompts and
  the destructive clear/erase/purge flows (labels matched to `en.json`).
  `go test ./internal/manual/... ./internal/pages/... ./internal/httpx/...`
  green; 56/69 `ci.yml` build-job guards + the 11 named ones green; the two
  failures were environmental (no catimport tags, no shellcheck).
- Facts in the topics were checked against code by the reviewer
  (`server.go` 5-min tick, `telemetry_client.go` payload and skip rules,
  `settings_page.go` elevation + audit, `pos_repo.go`/`data_api.go` erase
  path, reset/restore refusals, 366-day export limit).
- Screenshots: generated with the container's Chromium (157, pinned 149
  warned); en shots byte-identical across two runs. CI's
  `docs-shots-determinism` is the authority.
- shellcheck 0.11.0 (installed for this check) on both changed guard
  scripts: clean bar SC1091 info for the sourced sandbox helper (unchanged
  line; CI lints `scripts/ci/*.sh` together).

Pre-flight: help topic + docs-shots updated; `en.meta.json` updated
(English only); `reference/` page n/a (no reference page describes the
settings cards or telemetry payload); README n/a (no telemetry/data
management text); ut-website n/a; follow-ups deferred → Backlog card filed
at close-out.

## Deferred

- Extra-shot recipes on routes outside the surface hash (6 existing) can go
  stale unnoticed: add the surface route check after fixing them.
- Per-customer personal-data export (only erase exists); removing erased
  customers from existing backups; a page describing all outbound cloud
  traffic; audit row for Run export.

## Verdict

Approve after fixes (applied above).

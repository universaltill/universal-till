# Code review: auto-update ON by default, nightly (ut-docs#2726)

**Date:** 2026-09-25
**Scope:** `internal/pages/update_api.go`, `internal/pages/settings_page.go`,
`internal/data/sync_admin_repo.go` (+ their tests), `web/help/{en,de,ar,fa,tr}/updates.md`,
`scripts/ci/i18n-baseline/help-drift-baseline.json`, `web/help/img/manifest.json`
(surface hash only).
**Author:** Opus 5.5 (cloud lane `:24`). **Reviewer:** Fable, independent subagent.

## What shipped

Product-owner decision (2026-09-25): unattended self-updates are on by default,
nightly, so a till nobody touches stops drifting behind (a Pi till was stuck on
v0.18.0 because the switch was off unless someone turned it on).

- `update.auto_enabled` **unset ⇒ on** for a main/standalone till. An explicit
  `"false"` is kept. Any other value fails safe to off.
- **Replicas stay off while unset** (`sync.primary_url` set). No replica
  version cap exists yet, so a replica that updates while its main till can't
  (Windows main till, unwritable .deb) would run ahead of it indefinitely. The
  cap is ut-docs#2732. An explicit `"true"` still enables a replica, as before.
- Saving "on" from a **main** till's Settings stores the default (unset), not
  `"true"`. The setting replicates shop-wide, so `"true"` would switch every
  replica on. "Off" is stored explicitly and applies shop-wide.
- Default time 03:00, plus a stable per-till offset in [0, 30) min: FNV of
  `sync.till_id`, then `marketplace.device_id`, then the hostname. It's computed,
  not stored, because `update.*` settings replicate.
- The window uses minutes-of-day modulo 24h, so a slot crossing midnight
  works. The attempt is recorded against the slot's opening date.
- `update.auto_last_attempt` is now per-till (`data.PerTillSettingPrefixes`).
  It used to sync from the main till, and its 03:0x attempt reached every
  replica as "already attempted today".
- The Settings page shows the effective schedule (ticked, 03:00 on a new till).
  Help topic `updates` gains an "Automatic updates" section in all five
  languages. The fa drift baseline is re-recorded (gap unchanged).

Existing "never mid-sale" guards are unchanged: the cashier basket, kiosk
basket, self-order sessions, the dev-build guard, and the fresh re-check before
`selfupdate.Apply`. Shift close is a single synchronous request with no
mid-close state to guard.

**Split out of #2726:** replica version cap + 'behind' banner → ut-docs#2732.
.deb unwritable-tree banner → ut-docs#2733. Windows silent installer → the
existing ut-docs#160 (linked).

## Tests (TDD: each written first and seen failing)

- `TestAutoUpdateDue_OffsetAndMidnight`, `TestAutoUpdateSchedule_Defaults`,
  `TestAutoUpdateJitterFor`, `TestAutoUpdateJitter_SeedIsPerTill`,
  `TestAutoUpdateTick_UnsetSettingUpdatesNightly`,
  `TestAutoUpdateTick_UnsetSettingOnReplicaDoesNotUpdate`,
  `TestPostSettingsUpdateSchedule_OnIsDefaultOnMainTill`
- `TestSettingsPage_AutoUpdateShowsEffectiveDefault`: failed without the
  settings_page change ("should render On"), passes with it.
- `TestAdminDumpApplyRoundTrip_AutoUpdateLastAttemptNeverSyncs`: failed
  before the `PerTillSettingPrefixes` change. The reviewer re-verified this
  independently in a separate worktree.
- Review-fix tests were re-verified by reverting each fix. The seed-order
  swap gives "two replicas sharing the synced device id got the same offset".
  The handler revert gives `on must be stored as the default (unset), got "true"`.

## Findings (Fable review)

1. **should-fix, fixed.** The jitter seed `marketplace.device_id` is overwritten
   by the main till's value on admin sync, so all tills of one shop got the
   same offset. Now seeded from `sync.till_id` first.
2. **should-fix, fixed.** The form renders ticked by default, so any Save on
   the main till (even just a time change) stored `"true"`. That replicated and
   switched replicas on with no cap. Now "on" on a main till stores the
   default. Help step 4 is reworded in all five locales.
3. **nit, fixed.** Comment on fail-safe handling of unknown values.
4. **nit, fixed.** Comment pointing at the canonical primary/replica rule.
5. **nit, fixed.** Jitter range is now [0, 30), matching the help's "in the 30
   minutes after".
6. **nit, accepted.** DST: a slot in a spring-forward gap is skipped that
   night. On fall-back, the slot-date guard blocks a second attempt.
7. **nit, fixed.** Help step 5 no longer says the chip updates Windows (it
   opens the download page).

## Verified beyond automated tests

- Real app on a fresh DB (`UT_AUTH=off`): `/settings` renders the checkbox
  ticked and the time 03:00. POSTing "off" and reloading renders unticked.
- Screenshots of the Software update card: en at 1280×800, fa (RTL) and de
  at 1024×600. Labels are in place, nothing clipped, and RTL mirrors
  correctly. Dark theme was not checked (this change touches no styles).
- The manual has no screenshot of this card. `make docs-shots` re-rendered
  every PNG with only font-environment noise, so those were discarded and the
  surface hash was refreshed instead (`Docs-Shots-Unchanged`).

## Gate

`gofmt -l .` clean; `go build ./...`, `go vet ./internal/...`, and
`go test ./...` all pass. `golangci-lint run ./...` reports 0 issues. Every
guard in the ci.yml `build` job passes, except three helper scripts that need
arguments and fail identically on `main`.

## Verdict

Safe to merge.

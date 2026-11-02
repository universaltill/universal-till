# Review: `update.fleet_follow`, when additional tills update (ut-docs#2739)

- **Branch:** `feat/2739-fleet-follow-setting`
- **Design:** ADR-0114 §5
- **Built by:** Sonnet 5.5
- **Reviewed by:** Opus 5.5 (independent subagent, fresh context)

## What shipped

- **New setting.** Shop-wide `update.fleet_follow` is set on the main till's Settings → Software update card and replicates like `update.auto_enabled`. It has two values:
  - `next_idle` (default, stored unset): the till installs at the next safe moment, after a 0–120 s jitter drawn per target.
  - `nightly`: the till installs only in the main till's `auto_time` slot, plus this till's own offset, inside the 30-minute window.
- **Update now.** A press on the Tills page skips both waits. It never skips busy, a downgrade or an unsupported install.
- **Follow rule.** `followDecision` gets two new decisions, `nightly_wait` and `jitter_wait`. Both come after `no_effect` and before `busy`. `followInputsOf` never arms the jitter, so Settings renders are side-effect free. `followTick` arms it and then re-evaluates in the same tick.
- **Shift-close guard.** `autoUpdateBusy` also counts a shift close in flight, using an atomic counter around the `CloseShift` handler. This holds both the nightly main-till path and the follow restart (`autoUpdateIdle`). A payment in flight was already covered, because the basket keeps its lines until tender.
- **Save handler.** `POST /api/settings/update-schedule` takes an optional `fleet_follow` field. A bad value is a 400 and writes nothing.
- **Replica Settings.** When the policy is nightly and the till follows, a replica shows a read-only line.
- **Translations and help.** en/ar/fa/tr strings and their metadata; a help step in `web/help/{en,ar,de,fa,tr}/updates.md`.
- **Language packs.** Separate PRs in de/es/pt.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | ar help named the Tills page «صفحة الأجهزة»; the UI calls it «الصناديق» | Fixed |
| 2 | should-fix | fa help named the Update now button differently from the UI | Fixed |
| 3 | nit | fa `fleet_follow_next_idle` was ambiguous («بازی») | Fixed (string + help) |
| 4 | nit | Replica line prints `auto_time` (e.g. 03:00), but the till acts at that time plus its 0–29 min offset | Deferred, see below |
| 5 | nit | Under nightly or jitter, a behind replica with a sale open reports `idle`, not `waiting-safe-moment` | Accepted: intended, pinned by `TestFollowReportState_WaitsReportIdle` |
| 6 | nit | `followTick` re-reads all inputs after arming the jitter (one extra disk probe per new target) | Accepted: harmless |
| 7 | nit | A failed nightly attempt is not retried on later nights until restart (existing once-per-run guard) | Deferred, see below |

## Verified

- **Reviewer's targeted run.** `go build ./...` and `go vet ./internal/pages/` pass. The targeted `-race` run (`Follow|AutoUpdate|CloseShift|UpdateSchedule|FleetFollow|SettingsPage|SettingsWriteThrough`) passes.
- **TDD re-checked by the reviewer.**
  - Disabling the wait block in `followDecision` fails 5 tests.
  - Removing `shiftCloseInFlight.Add(1)` fails `TestCloseShift_HoldsTheBusyCounter`.
- **Dev's full gate.**
  - `gofmt` clean; `golangci-lint` 0 issues.
  - Full `internal/pages` tests pass without `-race`. The full-package `-race` run timed out in this sandbox inside an unrelated wazero test, so CI is the judge there.
  - Guards pass: i18n, i18n-meta, help-*, data-access, kiosk-engine, no-showmodal, rtl, compliance, competitor naming, core-neutral, mail, docs-shots after a surface-hash refresh.
  - After the wording fixes, I re-ran guard-i18n and help-drift/terms/breadcrumbs/topics: all pass.
- **Not done.** No driven browser or device run. The Software update card has no docs-shots screenshot (`Docs-Shots-Unchanged: true`).

## Verdict

Safe to merge once CI is green on a head rebased onto `main`. The card went over its cycle budget before CI and merge. It is parked in Admin Review for the owner to raise the budget, then merge this PR and the three pack PRs.

## Deferred

- Finding 4: replica line wording ("from about %s").
- Finding 7: nightly retry after a failed attempt.

Both are listed on the card for a follow-up.

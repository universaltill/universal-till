# Code review: a post-setup country change queues the new country's base plugins (#1068)

- **Card:** universaltill/ut-docs#1068 (a follow-up from #1055)
- **Design:** BA/Architect notes on the card. It follows the #591/#1110
  base-plugin pattern, so no ADR was needed.
- **Author:** Opus 5.5 (cloud lane `:54`). **Reviewer:** an independent
  Fable subagent that ran the build, vet, affected tests with `-race`
  (`-count=3` for the loop tests), lint and the guards.

## What shipped

- `queueBasePluginsForCountryChange(ctx, d, country)` in
  `internal/pages/setup_base_plugins.go`:
  - Adds the country's `setupBasePlugins` specs to the pending list with a
    merge-safe `addPendingBasePlugins` (#1110).
  - Nudges the background retry.
  - Never touches the network. A failure is logged and never returned.
- `basePluginRetryNudge` is a buffered (1) package-level channel.
  `StartBasePluginRetry` now selects on it both during its 30s initial
  delay and in its 5-minute ticker loop. An online till therefore installs
  the new pack within seconds, while an offline till keeps the spec
  pending, as the wizard does.
- It is wired into all three post-setup writers of `store.country`:
  - `POST /api/settings/save`
  - `POST /api/settings/upsert`
  - the cloud `SetSetting` directive hook (`cloudsync_wire.go`)

  At each site, `countryChanging` is decided **before** the write and the
  queue runs only **after** a successful write. So a case-insensitive
  re-save of the same country queues nothing (a dismissed pack stays
  dismissed). A refused change (403 without fiscal authority, 400 on an
  additional till) returns before the queue call.
- Help: `web/help/{en,de,tr,fa,ar}/display.md` item 11 gets one sentence,
  translated in this change. `web/help/img/manifest.json` got a surface-hash
  refresh; no PNG changed.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The upsert handler evaluated `countryChanging` after `saveShopSettings` had already persisted the country. A cloud-triggered rederive that reloaded state from the DB in that window would have hidden the change, so the pack would never be queued. | Fixed: it is now decided before the write, inside the existing country gate. |
| 2 | nit | A nudge token left in the package-level buffer by an earlier test made `TestStartBasePluginRetryShutsDownOnCtxDone` skip the initial-delay arm it exists to test. | Fixed: `drainBasePluginRetryNudge()` runs at the start of both loop tests. |
| 3 | nit | Nothing tested that a refused change queues nothing. | Fixed: `wantPending(t, …)` was added to the fiscal-authority 403 test (target switched TR→ES so the assertion can bite) and to the additional-till refusal test. |
| 4 | nit | The tr help text said "Tüm Ayarlar", but the shipped label is "Tüm ayarlar". | Fixed. |

The reviewer also judged the de, fa and ar sentences natural and consistent
with the terminology already used next to them.

## Verification

- TDD: the tests were written first and failed against a stub helper
  (upsert/save/cloud assertions, and a 10s nudge timeout). Re-verified after
  review: with the upsert call site removed,
  `TestUpsertCountryChange_QueuesBasePlugins` fails with the missing `de`
  spec, and it passes again once the call site is restored.
- `go test ./...` is green. The affected tests are also green with `-race`.
- `golangci-lint run ./...` reports 0 issues and `gofmt` is clean.
- The guards pass: help-topics, help-drift, i18n, core-neutral,
  data-access, compliance-claims, competitor-naming, kiosk-engine,
  page-http-error and docs-shots (after `make docs-shots`).
- Two guards could not be run in this environment:
  - `guard-deadcode-baseline` fails identically on `main` here.
  - The shellcheck binary is missing locally.

  CI runs both.
- Tester: this is a backend change. The layers are handler tests through the
  real mux with a fully migrated DB, and a real retry goroutine against a
  fake marketplace that installs the pack after a nudge. No visual surface
  changed; the existing pending-plugin chip in Settings → Data renders the
  queued spec, and `TestSettingsShowsPendingBasePluginChipOnlyWhenPending`
  already covers it. The app was not driven by hand in a browser.

## Verdict

Safe to merge.

## Deferred / non-goals

- #1069: a reconciliation UI for a shop already past setup with a missing
  plugin.
- #2879: moving the country table into plugin manifests.

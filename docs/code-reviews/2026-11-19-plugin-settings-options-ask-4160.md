# Review: plugin settings asked options (`ui.settings.options.ask`) — ut-docs#4160

Date: 2026-11-19. Branch `feat/4160-settings-options-ask` (universal-till) +
`docs/4160-settings-options-ask` (ut-docs). Author: Sonnet (Dev, two rounds).
Reviewer: Fable 5.2, fresh context (security-relevant: secrets must never reach
the ask payload). Orchestrator: Opus 5.5.

## What shipped

- Settings page asks a plugin for a choice field's options
  (`ui.settings.options.ask` via `EventBus.AskPlugin`, `events:receive` gate,
  5 s bound via the shared `boundedAskPlugin`), htmx fragment
  `POST /api/plugins/{id}/settings/{key}/options` (`plugin_management`,
  same-origin, demo-denied).
- Payload carries only manifest-declared non-secret values; strict answer
  decoder (16 KiB, 32 options); bounded per-plugin cache (64 entries, 10 min)
  keyed by plugin + version + key + values hash; dropped on save; empty answers
  never cached; a generation counter stops an in-flight ask re-caching a stale
  list after a save.
- Save accepts an asked value (1–64 bytes, no comma/control chars) or the
  stored value; saved value kept when the ask fails.
- "Loading choices…" only after 400 ms (JS timer, works under `fx-light`);
  in-flight asks aborted when the form saves; chained `show_when` reveals ask.
- `pickSettingRow` maps scope `""` to `global` (was "first row").
- Guest SDK types, 5 en.json keys + meta, help topic `plugins.md`, docs-shots
  surface hash; contract in ut-docs `reference/plugin-views.md` "Settings asks".

## Findings (Fable)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | medium | Empty answer cached 10 min: grant / unchanged save / plugin `secret_set` never refresh it | fixed — empty never cached; `TestSettingsOptions_EmptyAnswerIsNotCached` |
| 2 | low/med | Put-after-drop race re-caches the old key's list | fixed — per-plugin generation; `TestSettingsOptions_AskInFlightDuringDropIsNotCached`, `TestSettingsOptionsCache_PutAfterDropIsRefused` |
| 3 | low | Chained `show_when` reveal never asks | fixed — `scan(form)` after `apply(form)` (no e2e: seed plugin has no chain) |
| 4 | low | Options route caps body at 64 KiB, save has no cap | accepted; follow-up line on epic #4023 (matters once list/map #4158/#4159 draw) |
| 5 | low | Stale ask fragment's OOB message can land in the re-rendered form | fixed — abort asks on the form's save `beforeRequest` |
| 6 | low | One `event_dispatch` audit row per uncached ask (denied rows each load) | accepted; follow-up line on epic #4023 (same as `ui.view.ask`) |
| 7 | nit | Demo mode shows "Couldn't load… Try again" | accepted |
| 8 | nit | Unsaved values reach the plugin unvalidated against max_len/options | accepted (plugin's own handler only) |
| 9 | nit | Shared helper logged under `pages.pluginViewAsk` | fixed — label parameter |

Checked clean: secret exclusion (declared + name heuristic, list/map skipped),
cache isolation and bounds, save validation against forged POSTs, auth/CSRF,
timeout + panic recovery, JS timer cleanup on abort/error, escaping, RTL
logical CSS, no showModal/native confirm, manifest/signing unchanged
(`OptionsAsk`/`DependsOn` already on main and mirrored in ut-cloud).

## Verification

- Orchestrator re-ran TDD for fixes 1–2: reverted → both tests fail
  ("asks = 1, want 2"), restored → pass. Tester mutation-checked the 400 ms
  e2e and `TestPickSettingRow_EmptyScopeIsGlobal` (fail without fix).
- `go test ./internal/pages/...` full (Tester), `-race` on the 4160 tests
  (reviewer + Dev), `internal/plugins` Choice/DataClass, `sdk/plugin`, gofmt,
  vet, golangci-lint 0, guards (i18n, i18n-meta, rtl, docs-shots, help-topics,
  data-access, no-showmodal, no-native-confirm, mail-addresses, core-neutral,
  fixed-dates, wip-commits), Playwright `plugin-settings-options-4160` 7/7.
- Visual attestation (Tester): en 1024×600 with pending answer shows the muted
  loading line under the select inside the card; fa 360×740 RTL, no overflow.
- Not verified: real touch hardware, screen reader, WebKit, `fx-light` driven.

Pre-flight: help topic updated (`web/help/en/plugins.md`); docs-shots surface
hash updated, PNGs n/a (no screenshotted page shows an asked dropdown);
reference updated (ut-docs `plugin-views.md`, `plugin-manifest.md`); en.json →
en.meta.json updated; README n/a (no stale claim), `sdk/plugin/README.md`
updated; ADR n/a (implements ADR-0121 S5 amendment); manifest field / host
function / taxonomy / ut-cloud mirrors n/a (unchanged); follow-ups → epic #4023
checklist lines.

Verdict: ship.

## Addendum — PR sweep, 2026-11-19 (lane:cloud-41)

`desktop-shell`'s whole-program deadcode guard (`guard-deadcode-baseline.sh`)
flagged `settingsOptionsCache.put` as unreachable: production writes only
through `putIfGeneration`, and `put` was called from the unit tests alone.
Fix: `put` removed; the tests store through a `putFresh` helper that calls
`putIfGeneration` at the plugin's current generation and panics if refused,
so they exercise the one write path the handler uses. No behaviour change.
[no-subagent: dead-code removal of one test-only method, no logic touched]
Verified: `go test ./internal/pages/ -run 'SettingsOptions|PluginSettings'`
passes; `guard-deadcode-baseline.sh` passes locally (GOTOOLCHAIN=go1.27.1).

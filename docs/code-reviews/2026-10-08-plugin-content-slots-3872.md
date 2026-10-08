# Code review — plugin content slots (ut-docs#3872)

- **Card:** universaltill/ut-docs#3872 — ADR-0121 §7 build 8b, the six
  core content slots. Part of epic ut-docs#2848.
- **Author:** Opus 5.5 dev subagent (lane:cloud-54). **Reviewer:** Fable,
  independent, different model (complexity:medium routing).
- **Docs:** ut-docs `reference/plugin-views.md` "Content slots" (companion
  ut-docs PR), `docs/plugin_guidelines.md`, help `plugins` topic (en/de/tr/fa/ar).

## What shipped

- `internal/pages/plugin_slot.go`: `contentSlotPanels` asks every active
  plugin whose page entry declares the slot and which holds
  `ui:slot:<slot>`, with `ui.view.ask`, in parallel, 2 s budget each
  (`pluginSlotTimeout`), at most 8 entries, stable order (plugin id, entry
  key). Invalid, slow, broken, redirecting or permission-less answers are
  skipped with a warn log, never shown, never block the host page.
- `GET /ui/slot/{slot}`: lazy fragment for the five signed-in slots, gated
  by the host page's permission (`catalog_management`, `reports`,
  `eod_report`, `settings`; `admin.pages` = visible admin destination +
  `reports`, ADR-0149 §6). Fail closed: 403 without panel content; 404 for
  unknown slots and `setup.wizard.steps`. Allowed in demo mode (no plugins
  there, so always empty; `/plugin/` actions stay denied).
- `setup.wizard.steps`: drawn inline and read-only by the auth-exempt wizard;
  no new auth-exempt route.
- Panel actions post to the entry's own `/plugin/` route and answer into
  the panel, recognised by an `HX-Target` matching
  `^plugin-slot-[a-z0-9-]{1,64}$`; anything else falls back to
  `#plugin-view`. Page output is byte-identical to #3160's.
- `data.PageEntryRow.Slot` read from `config_json.content_slot`.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| T1 | blocker (tester) | `contentSlotPanels` goroutine deferred `wg.Done` before `RecoverAndLog`, failing `TestNoUnrecoveredGoroutines` (ut-docs#3304) | Fixed: recover is the first defer |
| T2 | UX | `reports.panels` above the tab strip pushed the tabs below the 600 px fold and made them jump when panels arrived late | Fixed: placeholder moved below `#report-tab-panel` |
| R1 | minor | A failed/busy/job action in a slot replaced the panel with a bare notice — no heading | Fixed: slot bodies always carry the title; heading rendered before the notice branches. Test `TestPluginSlot_FailedActionKeepsPanelHeading_3872` (failed without the fix, passes with it) |
| R2 | minor | Setup wizard asks the setup slot on every render, incl. validation re-renders (≤ 2 s) | Accepted: bounded, documented; first boot has no plugins in practice |
| R3 | minor | A revoked `ui:slot:*` grant writes an audit row + warn per host-page visit | Accepted here; follow-up Backlog card filed |
| R4 | minor | Doc wording: 403 body; setup params carry only `slot` | Fixed in plugin-views.md |
| N1–N3 | nit | Poll id matches the panel regex (harmless); panel ids by answered index; timeout test bound loose | Accepted |
| UX2 | product | Settings/Admin panels show under every section, never on the hub | Follow-up Backlog card |
| UX3 | pre-existing | Plugin literal text lacks `dir="auto"` (mixed-direction punctuation in fa) — from #3160 partials | Follow-up Backlog card |

## Verified

- Reviewer re-verified two TDD claims by breaking the code: disabling the
  slot gate fails `TestPluginSlot_RoleGates_3872`; ignoring the slot
  timeout fails `TestPluginSlot_SlowPluginSkipped_3872` (5.4 s). Both pass
  restored. `-race` clean on the slot tests.
- Reviewer confirmed: no auth bypass (route not auth-exempt, per-slot gate
  matches each host page), no XSS (all plugin values via html/template, only
  request-controlled attribute is the regex-checked HX-Target), no WASM call
  slot held past the 2 s deadline (`WithCloseOnContextDone`), setup panels
  reachable only during first boot (`NeedsFirstBoot`).
- Tester driven run (real app, Chromium): every placeholder fetched its slot
  and swapped to nothing on Reports, EOD tab, Settings, Admin, Catalog item
  dialog (with `item_id`); no console errors, no `#pos-alert`. Live auth-on
  gates: admin 200, no session 401, cashier 403. Filled panels rendered from
  a real server-rendered fragment at 1024×600 and 360 px in en, de and fa
  (RTL): no overlap, clipping or sideways scroll. Playwright: 164/165
  passed; the one failure (`phone-layout-sweep-3297` `/backoffice`, untouched
  here) passed 2/2 re-run alone.
- **Not verified:** a real installed WASM plugin filling a slot (no WASM
  toolchain in the container); panel actions in a browser (Go test only);
  dark theme; real touch hardware. `golangci-lint` and the deadcode
  baseline could not run locally (binaries built with an older Go) — CI
  covers them.

## CI round 1

- `desktop-shell` failed `guard-deadcode-baseline.sh`: the exported
  `plugins.ContentSlots()` was called only from tests. Now used at
  startup by `registerPluginSlots` to log any manifest-declarable slot
  that has no host screen. (The guard can't run locally: older Go
  toolchain in the container.)

## Verdict

Safe to merge.

## Rebase onto main (PR sweep, lane:cloud-54)

`main` gained operator file uploads (ut-docs#3793) while this PR waited,
touching the same lines. Resolved as a union of both, no behaviour
dropped:
- `PageEntryRow` carries both `UploadMaxMB` and `Slot`; `ListPageEntries`
  unpacks `view`, `upload_max_mb` and `content_slot` from one config blob.
- `askPluginUIAs` sets `vctx.Uploads` from the entry and uses the
  per-call `timeout` parameter, so slot asks keep their own bound.
- `pluginview_form` posts to `#{{ .Target }}` and keeps the multipart
  attributes, so a file field works inside a slot panel too.
- Help step 13 keeps the upload sentence; step 14 (panels) follows it.
- `manifest.json`: surface hash via `update-docs-shots-surface-hash.sh`,
  `plugins` topic hashes recomputed from the merged markdown
  (`guard-docs-shots.sh` passes).
Verified: `go build ./...`, `go test ./internal/pages ./internal/data
./internal/plugins` (TCP fixture tests skipped locally: the container's
sandbox hangs them; CI runs them), every `ci.yml` build-job guard except
three that need tool versions this container lacks (deadcode, shellcheck
version pin).

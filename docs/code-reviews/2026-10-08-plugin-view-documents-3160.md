# Code review: plugin view documents on plugin pages (ut-docs#3160, ADR-0121 build 8a)

**Date:** 2026-10-08 · **Lane:** `lane:cloud-54` · **Built by:** Opus 5.5 subagent · **Reviewed by:** Fable subagent (different model), fixes by the orchestrator

## What shipped

Core now draws a plugin's page from a **view document** (ADR-0121 §7):
- A `/plugin/…` page entry with a `view` asks its own plugin `ui.view.ask`.
- An action posts `ui.action.ask`, and the plugin answers with a new document or a redirect to its own route.
- Core renders the answer with its own `html/template` partials (`web/ui/partials/pluginview/`). No plugin HTML, JS or CSS reaches the page.

The parts:
- `internal/pluginview`: a strict decoder and validator with caps, a render model and form decoding.
- v1 components: `heading`, `text`, `notice`, `stat_tiles`, `table`, `list`, `empty_state`, `button`, `form` (text, number, money, select, toggle, secret).
- Text is a key from the plugin's **own** locale bundle (`Manager.OwnLocaleKeys`) or an escaped literal.
- `internal/pages/plugin_view.go`: the GET and POST flow, with a 5 s ask deadline in a recovered goroutine, a bounded form (64 KiB, 100 keys), and bounded params.
- Failure handling: a broken, slow or invalid answer renders a translated "unavailable" notice (`plugin.view.unavailable`, en/ar/fa/tr plus the de/es/pt packs) and blocks nothing.
- `entryConfigJSON` now persists `view` and `content_slot`.
- `isSalePathEvent` excludes `ui.*`, so a view never takes the reserved sale-path slot.
- WASM stdout for `ui.*` asks is capped at 1 MiB.
- Docs: help topic step (`plugins.md` × 5 locales), `docs/plugin_guidelines.md`, and ut-docs `reference/plugin-views.md` plus the manifest and architecture notes.

Split out of the original card 8: #3872 (content slots), #3873 (suggestions + identify seam), #3874 (qr / wizard_step / paging). Still owned elsewhere: #3793 (file uploads), #3699 (role-aware asks), #2913 (full §7 CSP).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | blocker | `web/help/img/manifest.json` topic hashes stale after the `plugins.md` edits, so `guard-docs-shots` fails the build job | **Fixed**: `make docs-shots` regenerated the manifest (no PNG changed). The surface hash was then refreshed for the template edits below; no screenshotted page renders a plugin view. |
| 2 | minor | An unknown currency (e.g. KWD, 3 decimals) was formatted with a guessed 2 decimals, a 10× display error | **Fixed**: the validator accepts only codes in the till's currency registry (`httpx.IsKnownCurrency`). Tests were written first and seen failing. |
| 3 | minor | A negative money pre-fill rendered into an unsigned input the operator could never submit | **Fixed**: refused by the validator (test first). |
| 4 | minor | An htmx action swapped only the body, so the new document's `title` never reached the `<h1>` | **Fixed**: an out-of-band `#plugin-view-title` swap, escaped (`TestPluginView_ActionTitleUpdatesHeading_3160`, seen failing first). |
| 5 | minor | Plugins installed before this change have no persisted `view` until they are updated or reinstalled | **Accepted, documented** in `reference/plugin-views.md`. No plugin uses views yet. |
| 6 | nit | Doc drift: `invalid` is also an ADR payload addition; `null` is allowed in stat tiles; route ownership vs `/plugin/<id>/` | **Fixed** in the doc. |
| 7 | nit | Page entries are listed twice per request; a denied plugin's page writes an audit row per hit | **Accepted**: small lists, same as the existing page handler. |

Tester / UX findings:
- **Fixed in this branch:** the table-card label nowrap at 360 px in German, and the toggle touch target (46 px whole row).
- **Not fixed here, filed as follow-ups:**
  - A failed action replaces the form with the notice and has no retry, so the operator's input is lost.
  - Shared `FormatMoneyIn` puts the sign after the symbol ("£-42.50").
  - Shared `t-cards` labels break mid-word ("GESAMTBETR|AG").
  - Global inputs are 40–42 px tall, below the 46 px floor.

## Verified beyond unit tests

- **Reviewer** (in its own worktree) re-ran three TDD claims: reverting each fix made its test fail with the claimed error, and restoring it made the test pass.
  - `ui.*` excluded from the sale-path slot: `TestIsSalePathEvent`.
  - Own-route-only redirect: `TestDecodeActionAnswer`, `TestPluginView_Redirect_3160`.
  - Stdout cap: `TestWasmHandleEvent_UIAnswerStdoutCapped_3160`.
- **Reviewer security hunt, clean:** no `template.HTML` on plugin data, enum- and regex-validated attributes, no open redirect, `/plugin/` not auth-exempt, bounded bodies, no goroutine leak (buffered channel, recovered panic), secrets never logged.
- **Tester driven run:** real migrated SQLite, real `registerPluginPages`, templates and CSS, with a fake plugin answering every component.
  - Viewports: 1024×600 and 360×740.
  - Themes and locales: light and dark × en / de / fa.
  - Exercised: htmx action and form swap, and the failure notice on load and on action.
  - Screenshots were read for overlap, clipping, RTL mirroring, focus ring, touch targets and two-keyboard risk (`osk.js` sets `inputmode="none"`).
  - **Not looked at:** ar, tr, other themes, real touch hardware, a real signed WASM view plugin.
- **Gate:** `gofmt`, `go build ./...`, `go vet`, `go test ./internal/...` (all green), and `golangci-lint` 2.14 (0 issues). Guards pass: i18n, data-access, no-showmodal, core-neutral, help-topics, help-drift, compliance, competitor-naming, kiosk-engine and docs-shots. Language packs: `check-key-drift.sh` passes against this branch's `en.json`.

## Verdict

Safe to merge once CI is green. A new `en.json` key means `lang-pack-drift` goes red on `main` until the de/es/pt pack PRs merge in the same cycle.

## CI follow-up (same branch)

The first CI `build` hung in `TestWasmHandleEvent_UIAnswerStdoutCapped_3160` until the job was cancelled after 1 h 45 min. The test's uncapped case used a non-`.ask` event type. For those, `wasmResultLogLine` logs the whole answer, so a 2 MiB answer became a single 2,097,291-character `go test -v` line, and the runner spent over an hour streaming it.

**Fix:** the uncapped case now uses `export.requested.ask`, the real large-answer event, whose log line is bounded by `maxAskLogBytes`. The longest output line is now 147 characters. The test still asserts the full 2 MiB answer comes back uncapped.

## Sweep follow-up (lane:cloud-54, same branch)

- **Rebase onto `main`:** the branch conflicted with main, so CI never started. The only conflict was in the generated `web/help/img/manifest.json`. It was resolved by keeping main's topic hashes and taking this branch's `plugins` topic hashes, the only topic it changes. `surface_sha256` was then recomputed with `update-docs-shots-surface-hash.sh`. No screenshot changes: main's inventory/sell pages and this branch's plugin page don't overlap. `guard-docs-shots.sh` passes.
- **`desktop-shell` red:** `guard-deadcode-baseline.sh` flagged `pluginview.ValidName` as unreachable. It had no callers: validation uses `nameRe` directly. It is removed rather than baselined.
- **Re-verified locally after rebase:** `go build ./...`, `go vet`, `go test` for `internal/plugins`, `internal/pluginview`, `internal/pages/...` and `internal/httpx`, plus the i18n, help, data-access, no-showmodal, core-neutral and kiosk-engine guards. All are green. CI on the rebased head was green apart from `desktop-shell`.

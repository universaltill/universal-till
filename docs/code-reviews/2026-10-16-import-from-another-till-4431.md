# Review: import "From another till system" (ut-docs#4431)

- **Date:** 2026-10-16
- **Card:** ut-docs#4431 (epic ut-docs#4426), built on ADR-0161 / universal-till#1978
- **Built by:** Opus 5.5
- **Reviewed by:** Fable 5.1 (independent)

## What shipped

The import page's welcome gets a fourth option, **From another till system**. It is the till-side path for free or till-only shops to bring a catalogue across from another system through an installed API-source import plugin (for example ut-plugin-import-eposnow). Paid shops will import through the cloud manager later (ut-docs#4453).

**The section:**
- Lists the installed `source: "api"` import entries by plugin name. No vendor names are in core.
- With none installed, it links to the plugin store, now opened on its import category (`/plugins/store?type=import`; an unknown type falls back to All).
- Shows each secret setting as set or not set, never the value.
- Links to the existing plugin settings editor, so there is no second secrets form. The settings and store links need plugin_management; without it the card says to ask a plugin manager.

**The run:**
- **Check & preview** posts `mode=preview` and **Import** posts `mode=commit` to the new `POST /api/import/from-system` (import_export-gated; demo mode denies it). It returns an htmx fragment (`partials/import_system_result.html`) with the plugin's message, counts, up to 50 sample rows and their issues.
- A failed or part-way commit offers **Run again**. That is safe, because rows are linked; it isn't offered on an additional till.
- Errors show in an `aria-live` area, never a modal.

**Shared logic:**
- The run logic is shared with the JSON API (`executeAPISourceImport`, `grantedImportEntities`), and the JSON API's behaviour is unchanged.
- `ImportEntryRow.PluginName` names the importers.

**Strings and help:** 43 new keys in en, translated in core's ar, fa and tr. The de/es/pt packs follow. Help is updated in `catalog.md` and `users.md` for en, ar, de, fa and tr, with the help-drift baseline bumped.

**Tests:**
- Handler tests in `import_from_system_test.go`.
- e2e `import-from-system-4431.spec.ts` runs the real WASM importer through a new `e2e/seed_api_importer` seed, at 1024×600, 360 px and in Persian.

## Findings and outcome

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Minor | Set/not-set took the first stored row per key; the settings editor prefers the shop-wide (global) row, so a key with both a per-till and a shop-wide row could show the wrong state (state only, never a value) | **Fixed:** `shopWideSettingRows` applies the editor's rule. Its unit test failed first against the first-row-wins version (`api_key: want the global row, got … Scope:register`). |
| 2 | Minor | The plugin manifest was read twice per importer on page render | **Fixed:** one read serves both the secret test and the labels |
| 3 | Nit | The "four ways" intro still says four if the importer list fails to load (DB error) and the card is hidden | **Accepted:** an error-path edge case |
| 4 | Nit | The e2e seed built `value_json` with Go `%q` quoting | **Fixed:** `json.Marshal` |
| — | Pre-existing | The deadcode guard fails locally on `internal/logging` (untouched) | Not this change |

## Verified beyond automated tests

The reviewer checked:
- **Authorization:** the import_export gate runs before any body read. The cashier test asserts a 403 and that the plugin is never asked; with the gate removed, that test fails (mutation in a scratch copy).
- **The refactor:** the JSON API keeps its gates, the replica 409, the commit lock, the audit entry and its messages byte for byte.
- **Entry type:** a file entry can't be driven through the new endpoint.
- **Escaping:** plugin `message`, `error` and row issues go through `html/template` escaping (test: `&lt;b&gt;`). Concatenating a secret into a label makes the no-echo test fail (mutation).
- **e2e ports:** the new `extraSeeds` offset (+100) can't collide with other specs' ports.

Dev looked at the screenshots: kiosk before, preview and commit; phone; phone in Persian with a preview. Buttons are at least 44 px, nothing scrolls sideways, and RTL is aligned.

**Not checked:** dark theme, the kiosk body class, a real touch device, and an error-state screenshot.

## Gate

- `go build ./...`, `go vet ./...` and `go test ./...` pass.
- These guards pass: i18n, data-access, no-inline-handlers, help-drift, help-topics, core-neutral, competitor-naming, page-http-error, htmx-loaded and demo-env.
- The e2e spec passes (4 tests).
- docs-shots were regenerated in the Playwright Docker image, because they can't run on macOS.

## Deferred

- **Row issue language:** catsync row issues show in English on every locale (ut-docs#4485).
- **Language packs:** the de/es/pt packs need the 43 new keys plus the changed `import.welcome.intro`, as pack PRs right after merge.

## Verdict

Safe to merge.

## Addendum: key metadata (ADR-0160, ut-docs#4362)

- **Why:** while this PR was open, main gained `guard-i18n-meta.sh`, which requires an `en.meta.json` entry for every new key. CI failed on the 43 new keys.
- **What changed:** each key now has a translator description and `review: standard` (no legal text). The four keys with printf verbs carry `placeholders`, and the three buttons plus the welcome action carry `max_length` 40.
- **Review:** Sonnet 5.5 (independent) checked every description against `import.html`, `import_system_result.html` and `import_from_system.go`, plus the placeholder values. Verdict: safe to merge. It raised four wording points, all applied:
  - `connection` is a screen-reader label, not a heading;
  - `ask_manager` also shows in the empty state;
  - `commit_done` also shows above the issues warning;
  - `changes` should stay short and lower case.
- **Gate:** `guard-i18n-meta.sh` and `guard-i18n-meta_test.sh` pass.

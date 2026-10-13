# Review: plugin settings page for a plugin that isn't installed (ut-docs#4135)

- **Card:** universaltill/ut-docs#4135, "AI Assistant 2.4.0 on Android till shows no settings"
- **Branch:** `fix/4135-plugin-settings-not-installed`
- **Author:** Opus 5.5 (lane `cloud-41`). **Reviewer:** Fable (independent subagent).

## What the card turned out to be

- **"Remove background" never appears (AC 2, AC 3):** the device ran v0.31.9. The next release, v0.31.10, includes #4137 (`4946af2`), which added a built-in on-device engine. Since then `aiCutoutOffered` offers the button to every manager whether or not a rembg service is set. The help already says the rembg server is optional and how to set one up (`catalog.md`, `plugins.md` §4). Nothing left to build there.
- **Settings and permissions missing (AC 1):** I could not reproduce this on `main`. The real 2.4.0 manifest and a 2.3.1 → 2.4.0 update both go through `PersistManifest`, which every install path uses, and both store all 8 settings and all 10 permissions.
  - **Likely cause:** the device check opened `/plugins/integration-ai/settings`, but the plugin's id is `com.universaltill.integration-ai`.
  - For an id that isn't installed, the page used to render "This plugin has no settings" and "declares no permissions", which reads as a broken install.

## What shipped

- `GET /plugins/{id}/settings` redirects to `/plugins` (303) when no `plugins` row exists for the id. That is the same response the handler gives without `plugin_management`.
  - It uses `GetInstalledPluginVersion`, so a disabled plugin keeps its page.
  - No new strings, so no language-pack follow-ups.
- `POST /api/plugins/{id}/settings` answers 404 for a plugin that isn't installed (reviewer finding 1). It uses the existing `plugins.install.error.not_found` key. Nothing is written and no `plugin_settings_saved` audit row is added.
- Tests in `internal/pages/plugin_settings_4135_test.go`:
  - GET redirect for a plugin that isn't installed;
  - POST 404 with no audit row;
  - a disabled plugin's page still opens;
  - every declared setting and permission is listed after a fresh install and again after an update that adds settings and permissions (AI-Assistant-shaped manifests).
- Help: one sentence added to `web/help/{en,de,fa,tr,ar}/plugins.md` item 4.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | POST had no installed-guard: a crafted save for an unknown id answered "Saved (0)" and wrote an audit row | **Fixed**: 404 + test (fails without the guard, verified) |
| 2 | nit | Test restored `paths.Init("")` instead of the previous dir | **Fixed**: saves and restores `paths.DataDir()` |
| 3 | nit | The help points at the Settings button, which shows only for enabled plugins; a disabled plugin's page still opens by URL | Accepted: accurate for the common case |
| 4 | note | Orphaned `plugin_settings` rows (no `plugins` row) are no longer reachable on this page | Accepted: that state is itself a bug |

**Caller audit (reviewer):** nothing breaks.
- `plugins.html` ids come from the `plugins` table.
- The `fiscal_device.html` link appears only when the plugin is active.
- The e2e plugin-settings spec uses the seeded FAQ plugin, which has a `plugins` row.
- No docs-shot covers this page.

## Verified beyond automated tests

- **TDD:**
  - The GET test failed on the original handler (200) and the reviewer re-verified it in an isolated worktree.
  - The POST test failed without its guard (200 "Saved (0)").
  - Both pass with the fix.
- **Driven run:** `e2e/run-till.sh` (real binary, seeded data).
  - `/plugins/integration-ai/settings` returns 303 to `/plugins`.
  - `/plugins/com.universaltill.ut-faq/settings` returns 200 with its form.
- **Not looked at visually:** the change only adds a redirect and a 404, with no new markup. The Playwright suite was not run locally: no `node_modules` in this container. CI's `playwright` and `e2e` jobs cover it.
- **Gate:**
  - gofmt clean, `go build ./...`, golangci-lint 0 issues;
  - guards clean: data-access, core-neutral, kiosk-engine, i18n, help-drift, help-topics, compliance-claims, competitor-naming, no-showmodal, page-http-error, plugin-settings-bump, no-inline-handlers, card-data-schema, deadcode-baseline-growth, netaccess;
  - full `go test ./...` result is in the PR's CI.
  - `guard-deadcode-baseline.sh` could not run here: the container's `deadcode` binary is built with go1.26 against a go1.27 module. CI runs it with the right toolchain.

## CI follow-up

- The first CI run failed `guard-docs-shots`: the `plugins` topic's prose hashes (en, fa, ar, tr) were stale.
- That topic's screenshot is the `/plugins` list, which this branch doesn't change. So I refreshed only the four topic hashes in `web/help/img/manifest.json`, with a `Docs-Shots-Unchanged: true` trailer. This follows the precedent of `54c07ce65`.
- `guard-docs-shots.sh` and `guard-docs-shots_test.sh` pass.

## Verdict

Safe to merge.

**Still open for a human:** on the tablet (v0.31.10 or later), open **Plugins → AI Assistant → Settings** from the plugin's card and check that the 8 settings are listed.

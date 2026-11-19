# Review: plugins & marketplace user-manual topic (ut-docs#335)

**Scope:** branch `docs/335-plugins-manual`, commits `91f30aa9f` +
`9853985ec` against `origin/main`: `web/help/en/plugins.md` (rewritten topic
"Plugins & the marketplace": 12 H2 sections, same id/routes, two inline
shots), new docs-only `e2e/seed_docs_plugin_catalog/{main,main_test}.go` and
`e2e/run-till-plugins.sh` (till on 8107), `e2e/playwright.docs.config.ts`
(webServer), `e2e/tests-docs/docs-shots.spec.ts` (two read-only recipes +
"plugins till" describe), `e2e/README.md`,
`.github/workflows/docs-shots-determinism.yml` (paths), 8 new PNGs
`web/help/img/{en,fa,ar,tr}/plugins-{store,listing}.png`,
`web/help/img/manifest.json`.

**Author:** Dev agent (Sonnet). **Reviewer:** Opus 5.5 (independent).

## What shipped

- The single 16-item "How to use it" list becomes a shop-owner manual with
  a "Trust and safety" section ahead of the install steps, store/listing
  steps, card tags and buttons, settings and permissions, updates and
  rollback, import/export, themes and language packs, plugin pages and
  panels, several tills, the country tax plugin, and a "What can go wrong"
  table. All of BA's F1–F32 are present (checked against
  `git show origin/main:web/help/en/plugins.md`). The only fact dropped is the
  word "manifest" in the permissions fact. Every bold or quoted UI string is
  in `web/locales/en.json`, except prose lead-ins and three strings the
  templates assemble: `Broken ⚠`, `— catalog unavailable (offline?)`, and the
  raw `trust_level` values.
- docs-shots: a dedicated till (8107) boots with a fixed, stale, offline
  catalogue snapshot (six fictional `sample-*` listings, `Example …`
  vendors, letter logos). Its marketplace endpoint is `127.0.0.1:1`, so the
  background refresh fails at once and the snapshot stays. Only the
  `plugins` topic's extra shots use this till. The header `plugins.png` stays
  on the default till, so no other topic's picture can change.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Low | Install step 7 said to press **Manage installed plugins** "on a plugin's page". `plugins_store_detail.html` shows that button only once the plugin is installed, where it replaces **Download**. The store page always shows it. | **Fixed:** "…at the top of the store. On a plugin's page, it takes the place of **Download** once the plugin is installed." |
| 2 | Low | Intro said "Plugins never get in the way of selling … the store only needs the internet while you browse". Two problems: the table's **Broken ⚠** row says checkout can be unavailable, and the topic itself says browsing works offline from the cached catalogue. | **Fixed:** "Using the store never gets in the way of selling … only needs the internet to load its catalogue the first time, to download and to update." |
| 3 | Low | The card trust tag was described as "**trusted** or **untrusted**". A built-in shop-type layout (`builtinlayouts`, `TrustLevel: "system"`) shows **system**. | **Fixed:** added "(a layout built into the till for your type of shop shows **system**)". The raw, untranslated tag and official→untrusted are code issues, filed as ut-docs#6549 (K2). |
| 4 | Low | `plugins-store.png`: the 6th tile (Sample Language Pack) is cut off by the status bar in all locales. | **Accepted.** The capture is one viewport. All five tier and Paid states, and all 7 chips, are fully in frame, and a partly visible next row is what the shop owner sees too (a scroll cue). Going down to 5 listings would lose a category chip, so it is not worth it. |
| 5 | Info | Store Download/Install return a raw `download failed: <go error>` to the UI. The topic now describes it generically (row "✗ download failed: …"). | Code follow-up **ut-docs#6548** (K1). |
| 6 | Info | Spec: plugins-till recipes are refused at load if they declare setup/teardown, so the till never needs restoring. `ready` runs after `capture()` and deletes the PNG on failure, the same pattern as the default-till extras. The data is a static file, so the `ready` assertions (6 tiles, 7 chips, every tier + Paid, the listing's `net:validation:` line, the unticked terms box, Download fully in viewport) are deterministic. The only async source is the background refresh, which fails against port 1 and keeps the snapshot (Tester proved keep-on-failure live). Port 8107 is not used by any other config, script or spec (8091–8106 taken). | No change |
| 7 | Info | Seed isolation: none of the listing ids starts with `com.universaltill.`, none matches the seeded FAQ plugin, and no listing is a tax/fiscal type. So there are no update chips and no base-plugin auto-install (which runs only on the setup-finish POST, never on the GET shots). `store.name` is set through `data.NewSettingsRepo`, with no raw SQL. Files are written after `os.MkdirAll`, and paths come from `UT_DATA_DIR`, not the cwd. This matches `seed_faq`/`seed_docs_sales`. | No change |
| 8 | Nit | `TestSnapshotFileMatchesTheTill` largely repeats `TestSeed_SnapshotIsTheFileTheTillReads` and ignores the `NewCatalogRepository` error. | Accepted (harmless) |
| 9 | Info | `docs-shots-locale-drift.yml` will list the 8 new PNGs once (captured with Chromium 153, while the pin is 149; Tester §3). The gate does not compare PNG bytes. | Accepted. Take the drift run's artifact if wanted. |

Wording guards: no compliance claims and no competitor names. Offline framing
is right (sales complete offline; a store that has loaded once keeps its
catalogue). No dev jargon beyond permission/setting names the screen prints.

## Verification

- Worktree at `9853985ec` plus the three wording fixes. `gofmt -l .` is empty.
  `go build ./...` and `go vet` (seed, manual, pages) passed. `go test
  ./e2e/seed_docs_plugin_catalog/... ./internal/manual/...
  ./internal/pages/...` passed (manual re-run after the fixes).
  `golangci-lint run ./...` reports 0 issues. `shellcheck scripts/ci/*.sh
  e2e/run-till-plugins.sh` reports 0.
- Guards, all rc=0 after the fixes: help-topics, help-breadcrumbs,
  help-terms, help-drift (warnings only), i18n, compliance-claims,
  competitor-naming, fixed-dates, mail-addresses, core-neutral,
  data-access, and run-docs-shots-guard (33 topics, 5 extra shots, surface
  `7810505e940d…`). The `topics.plugins.en` hash in `manifest.json` was
  updated for the prose-only fix (PNGs untouched; the header shot shows
  `/plugins`, not the topic text).
- Re-verified the seed TDD claim: changing `snapshotFile`'s separator to `-`
  makes both round-trip tests fail with "no catalog snapshot available".
  After restoring it, they pass.
- `playwright test -c playwright.docs.config.ts --list` lists 152 tests,
  8 of them `plugins-*`. No captures were re-run (prose-only fixes).
- Looked at `en/plugins-store.png`, `en/plugins-listing.png` and
  `ar/plugins-store.png` (RTL mirrored, Latin names isolated, no focus ring,
  no "Name your shop" banner). Download and the terms box are in frame.
- `guard-docs-shots-determinism.sh` was not run (two full harness runs; CI
  runs it, now also triggered by the seed/script paths).

## Verdict

**PASS** with the three wording fixes applied.

## Deferred

- ut-docs#6548: store Download/Install show a raw Go error (`download
  failed: request failed: Post …`) and never record the "failed" tag (K1).
- ut-docs#6549: `marketplaceTrustTier` maps `official` to `untrusted`, and the
  card shows the raw `trust_level` value untranslated (K2; check ADR-0006).
- From BA (to file as cards if not already filed): split into sub-topics
  once translated front matter can move; screenshots of the settings page and
  Versions panel (need a docs-only sample plugin); a `plugins-import` shot
  once #338's `ExtraShot.interact` is on main; an offline-store screenshot.

Pre-flight: help topic `updated` (plugins.md + 8 docs-shots PNGs/manifest);
`reference/` page `n/a` (docs-only, no behaviour change); `README.md` `n/a`
(no stale text); `en.json`/`en.meta.json` `n/a` (no new keys); ut-cloud
locales `n/a` (till only); ADR index `n/a`; plugin taxonomy/mirrors `n/a`
(seed uses existing canonical types, asserted by its test); review record
`updated` (this file); follow-ups `deferred → ut-docs#6548, ut-docs#6549`
plus the BA cards above.

# 2026-09-10 — Shop-type → builtin layout wiring (ut-docs#1902)

## What shipped

Two finished, unconnected pieces of prior work now talk to each other:

- The ADR-0026 `shop_type` setting (`cafe|retail|service|hospitality|market_stall|other`),
  captured in the setup wizard and changeable later in Settings, previously
  captured but drove nothing.
- `plugins/layout-salon/`, a real ADR-0088 `layout`-type plugin manifest
  (built as ut-docs#1904's own e2e fixture) that hides `/tables` and
  `/kitchen-stations` from the menu and relabels `/items` to "Services" —
  previously only ever installed inside its own e2e test.

New package `internal/plugins/builtinlayouts` (`Sync`) installs/removes the
salon layout through the standard `plugins.PersistManifest`/
`plugins.UninstallPlugin` paths when `shop_type` is saved as `service`/
anything else, wired into both existing shop_type write handlers
(`setup_page.go`, `settings_page.go`). `plugins/layout-salon/embed.go`
embeds the manifest + locale files into the compiled binary so a shipped
till never depends on the source tree at runtime (mirrors
`internal/data/seeddata`'s embed pattern). One new Settings-page copy line
+ i18n key; one new help-topic sentence (`web/help/en/display.md`).

## What the independent review found

Spawned a fresh Opus subagent (this session is Sonnet — the required
different-model review for a `complexity:medium` card), in an isolated
git worktree, with instructions to actually build/test/run rather than
just read. Full findings list and the agent's own verdicts are preserved
in the pipeline's issue-comment history on ut-docs#1902; summarized here:

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Blocker | `filepath.Join` fed to an `embed.FS` read — always slash-separated per `io/fs`'s own contract, so a build running on Windows (a real shipped target — NSIS installer in `release.yml`) would fail to find `locales/en.json`, silently aborting the whole feature before `PersistManifest` ever runs | **Fixed** — `path.Join` for the `fs.FS` read; `filepath.Join` stays correct for the real on-disk write target |
| 2 | Should-fix | `t.Setenv("UT_DATA_DIR", ...)` in the test helper is inert — `paths.DataDir()` reads an `atomic.Value` set only by `paths.Init`, never the environment — so the locale-write assertion could false-pass against a stale directory and, worse, a real test run writes into this package's own source tree (not covered by the root-anchored `.gitignore` entry) | **Fixed** — `paths.Init(dataDir)` + `t.Cleanup(func(){ paths.Init("") })`, the same pattern `internal/secrets/keystore_test.go` already uses. Re-verified: a fresh run leaves no stray `data/` directory under the package |
| 3 | Should-fix | `Sync` keyed removal off `PluginActive` (is_active=1) — a plugin an operator manually **disabled** from Settings → Plugins stays installed forever once shop_type moves away, and can resurrect on a later re-enable against a now-different shop type | **Fixed** — new `PluginRepo.GetInstalledPluginVersion` (version-agnostic existence check) replaces the active-only guard for the removal branch |
| 4 | Should-fix | No startup/provision-time reconciliation — a shop with `shop_type=service` already saved from before this wiring existed gets nothing until an operator re-saves the same dropdown value | **Deferred with a card** — ut-docs#2001. Zero real shops exist yet and neither real pilot (café, salon-not-yet-built) is currently `service`, so today's blast radius is nil; the right startup hook (`internal/pages/init.go`, not the Debian-only `internal/app/provision.go` the initial read suggested) needs its own diff and tests |
| 5 | Should-fix | Version-blind guard: an embedded manifest version bump across a till self-update would leave the old version's row + locale files in place forever | **Fixed** — same `GetInstalledPluginVersion` change: a version mismatch now triggers remove-then-reinstall. Regression test `TestSync_StaleInstalledVersion_ReplacedWithCurrentOnResync` added and TDD-verified (see below) |
| 6 | Should-fix | The new Settings copy is untranslated (English-only) in `ar`/`fa`/`tr` — the self-hosted NAS translation endpoint (`192.168.1.231:11434`) is confirmed unreachable from this sandbox (`curl` timeout) | **Accepted, stated honestly** — not a silent gap; `guard-i18n.sh` still passes because the key exists in every file, only the value is a placeholder pending a session with NAS access |
| 6b | Nit (caveat) | `InstallOptions.TrustLevel: "system"` is real (pre-existing field, written but previously unused) and doesn't bypass any signature verification (`PersistManifest` runs the full ADR-0088 validation set regardless; the marketplace installer's *extra* steps are transport/provenance only — Ed25519, checksum, executable checks — none of which apply to content embedded in the already-signed binary). ADR-0006/ADR-0088 don't yet explicitly name this case | **Noted, not blocking** — a one-line ADR clarification is a reasonable future cleanup, not required for this diff's correctness |
| 7 | Should-fix | `web/help/en/display.md`'s existing "Settings → Shop type" sentence no longer fully describes the control after this diff | **Fixed** — sentence extended to name the Salon layout, what it hides/relabels, and the Hidden-menu-tiles recovery path. `guard-help-drift.sh` unaffected (added prose within an existing numbered item, not a new heading/list entry, so the structural counts it compares don't change) |
| 8 | Should-fix | New core key `settings.shop_type.service_layout_note` is new drift for `ut-plugin-language-es` (confirmed via `check-lang-pack-drift.sh`) | **Owned by this lane, same cycle** — pack follow-up filed after this PR merges, per `scrum-master/SKILL.md`'s "the lane that merges the core change owns the implied follow-up" |
| 9 | Nit | `InstallOptions.Uploader: "core"` was write-only and read by nothing | **Fixed** — dropped (also true of `TrustLevel`... no, `TrustLevel` is genuinely read at `internal/data/plugin_repo.go`'s three write sites; only `Uploader` was decorative) |
| 10 | Nit | If `os.RemoveAll` failed after a successful `UninstallPlugin`, `Sync` returned an error and the caller skipped `ReloadPlugins`, leaving already-uninstalled amendments stuck in memory | **Fixed** — file removal is now best-effort (logged warning), matching `handleUninstallPlugin`'s own "DB is the source of truth" convention |
| 11 | Nit | Settings copy claimed "nothing else changes" while the plugin also relabels/reorders `/items` | **Fixed** — wording corrected, now also points at Settings → Hidden menu tiles |
| 12 | Nit | Dead, unreferenced `id="settings-shop-type-service-note"` | **Fixed** — removed |

## TDD re-verification

Independently re-verified (not taken on the implementer's word):

- **Handler-level test** (`TestShopTypeEndpoint_ServiceActivatesSalonLayout_SwitchAwayRemovesIt`): reviewer stubbed out the `Sync`/`ReloadPlugins` call in `settings_page.go`, re-ran — failed with the exact claimed error; restored, passed again. Also tried a variant not in the original claim (keep `Sync`, drop `ReloadPlugins`) — also correctly fails.
- **This review's own two new regression tests**, added for findings 3 and 5: both independently confirmed to fail against the pre-fix (`PluginActive`-only) `Sync` logic, then confirmed to pass against the fix, by literally swapping the function body back to the old logic, re-running, and restoring (`TestSync_DisabledSalonLayout_StillRemovedOnSwitchAway`, `TestSync_StaleInstalledVersion_ReplacedWithCurrentOnResync`).
- **Test-isolation fix** (finding 2): confirmed a fresh `go test` run leaves no `internal/plugins/builtinlayouts/data/` directory behind (there was none to begin with in this checkout, but the fix removes the mechanism that could produce one).

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l .` — clean.
- `go test ./internal/plugins/... ./internal/pages/... ./internal/data/...` — all green, `-count=1` (not cached).
- `guard-i18n.sh`, `guard-data-access.sh`, `guard-help-topics.sh`, `guard-help-drift.sh` — all green.
- `check-lang-pack-drift.sh` — confirms exactly one new-drift key (`es`), tracked as this lane's own follow-up.
- Real e2e regression: `layout-plugin-menu-1904.spec.ts` (the salon plugin's own pre-existing Playwright suite) — 5/5 passing against this diff, run twice (once mid-review, once after all fixes).
- Real driven visual check: booted a throwaway till (`e2e/run-till.sh`), screenshotted Settings → Shop type with real Chromium at en/1024×600, en/360×800, fa/1024×600 (RTL — card mirrors correctly), de/1024×600 (no bundled `de` core locale, correctly falls back to English end-to-end). All render cleanly; the new note wraps as plain flowing text in the same `.muted` class the sibling line above it already uses. `ar`/`tr` screenshots and real kiosk/tablet hardware were not checked (stated, not silently skipped — same class of gap as untranslated `ar`/`fa`/`tr` values, low incremental risk given the identical rendering mechanism already verified in `fa`).

## Safe to merge

Yes, after the fixes above. All blocker/should-fix items are either fixed in this branch or deferred with an explicit, justified Backlog card (ut-docs#2001). Nits fixed. The core design — reusing `PersistManifest`/`UninstallPlugin` rather than hand-rolling installation — was correct from the first pass and gets the full ADR-0088 validation set for free.

## Deferred / follow-up work

- ut-docs#2001 — startup-time `shop_type` reconciliation for a shop that already had it set before this wiring existed.
- Real `ar`/`fa`/`tr` translation of `settings.shop_type.service_layout_note`, once a session has NAS access.
- `ut-plugin-language-es` (and any other pack found behind) needs the new core key — follow-up PR(s) opened by this same lane after this PR merges.
- A one-line ADR-0006/ADR-0088 clarification recognizing binary-embedded `layout` plugins at `trust_level: system` as outside the marketplace signature chain (documentation-only, not a code change).

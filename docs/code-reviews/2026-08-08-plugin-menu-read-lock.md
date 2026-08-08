# Code review: read-side lock for d.Menu / d.Pm.Installed / d.Pm.MenuPlugins

**Card:** universaltill/ut-docs#478
**Date:** 2026-08-08
**Complexity:** medium — Dev at Sonnet (inline), Review at Opus (fresh-context subagent, isolated worktree), per this pipeline's model-routing rule.

## What shipped

`common.Deps.ReloadPlugins` already serialized the plugin
reload-and-rebuild sequence (`Pm.Reload` + the `Menu` rebuild) under a
`PluginMu` lock. Since ut-docs#460 that sequence fires routinely from the
replica sync-pull goroutine every 30s, not just from occasional admin
actions — but every HTTP handler that *read* `d.Menu` or `d.Pm.Installed`
did so with no lock at all. `plugins.Manager.Reload` reassigns `Installed`
(and, as review round 1 found, `MenuPlugins`) to a fresh map and repopulates
it key-by-key, so an unlocked concurrent read racing that window is a
genuine fatal Go `concurrent map read and write` crash — not just a
stale-read bug — and ut-docs#460 made the write side routine enough to make
this a real, not theoretical, exposure.

This change:

- Changes `PluginMu` from `sync.Mutex` to `sync.RWMutex`. `ReloadPlugins`
  keeps using `Lock()`/`Unlock()` for the write side unchanged.
- Adds three locked read accessors on `Deps`: `MenuSnapshot() []MenuItem`,
  `InstalledPlugin(id string) (plugins.Plugin, bool)`, and
  `MenuPluginByKey(key string) (plugins.MenuPlugin, bool)` — each taking
  `PluginMu.RLock()`. All three are nil-safe on `Pm`.
- Sweeps every read site under `internal/pages/**` that used to read
  `d.Menu`, `d.Pm.Installed`, or `d.Pm.MenuPlugins` directly (~29 files) to
  go through the matching accessor instead — mechanical, no behavior
  change to any rendered output.
- Adds `TestReloadPlugins_ConcurrentReadersSurviveRace` in
  `internal/pages/sync_plugins_test.go`: runs `ReloadPlugins` in a loop from
  one goroutine while several others call `MenuSnapshot`/`InstalledPlugin`/
  `MenuPluginByKey`, under `-race`.

## Independent review

Opus, fresh context, isolated git worktree (branched from the feature
branch so the revert-then-restore TDD verification never touched the
shared checkout — ut-docs#386). Findings:

- **MAJOR (fixed):** the original sweep covered the two fields the ticket
  named (`Installed`, `Menu`) and missed a third: `internal/pages/
  external_api.go`'s `/ext/{pluginID}` proxy handler read `d.Pm.MenuPlugins[pid]`
  unlocked. `Manager.Reload` reassigns `MenuPlugins` in the exact same
  critical section as `Installed` — same fatal-crash profile. The reviewer
  reproduced the race with a throwaway test before reporting it. Fixed by
  adding `MenuPluginByKey` (mirroring the other two accessors), routing
  `external_api.go` through it, extending the race test to exercise it, and
  broadening the `PluginMu` doc comment to say *every* field `Manager.Reload`
  reassigns needs a locked accessor — not just the two named in the
  originating ticket, so the next field Manager grows doesn't repeat this.
- **MINOR (accepted, not fixed here):** the new invariant ("no unlocked
  read of these three fields") is enforced only by a doc comment, not a CI
  guard, unlike this repo's usual practice of one guard script per
  invariant (`guard-data-access`, `guard-kiosk-engine`, `guard-i18n`,
  `guard-help-topics`). A `guard-plugin-menu-read.sh` grepping for
  `\.Pm\.(Installed|MenuPlugins)\[` / `d\.Menu\b` outside `common/deps.go`
  would have caught the MAJOR finding above at authoring time. Filed as a
  follow-up rather than expanding this card's scope further:
  universaltill/ut-docs#486.
- **NIT (accepted, not fixed):** `menu_page.go` takes two independent
  `MenuSnapshot()` calls (once for the tile loop, once for the template
  render) rather than hoisting one — a reload landing between them renders
  two adjacent-but-different-generation menus. Both are individually valid
  and this is the one screen where the tiles *are* the nav, so cosmetic at
  worst; not worth the churn for this card.
- **NIT (accepted, not fixed):** `plugins_store_page.go` calls
  `InstalledPlugin` once per loop iteration (N RLock/RUnlock pairs) rather
  than hoisting a snapshot outside the loop. RWMutex read locks are cheap
  and store listings are small; not worth changing unless the loop grows.
- Confirmed clean: sweep completeness (verified independently via grep —
  no unlocked reads of any of the three fields remain outside
  `common/deps.go`'s own write/lock sites and single-goroutine test
  assertions); no other read exposure anywhere outside `internal/pages`;
  `Manager.InstalledIDs()`/`CatalogList()` also iterate `Installed`
  unlocked but have zero non-test callers today (dead as far as any live
  race goes — noted, not touched, out of this card's scope); no deadlock
  or lock-order hazard (`PluginMu` is acquired in exactly three places, all
  in `deps.go`, none re-entrant); returning the raw `[]MenuItem`/`Plugin`/
  `MenuPlugin` values without a defensive copy is safe because `Menu` is
  only ever wholesale-reassigned under the write lock, never mutated in
  place, and `Plugin`/`MenuPlugin` are small value types; the `panic → 404`
  behavior change from routing `plugin_api.go`'s `handleUpdatePlugin`
  through the nil-safe `InstalledPlugin` is a non-issue since `Pm` is
  always non-nil in production (`pages.Init` calls `pm.SetLocalizer`
  unconditionally at boot); no recurring MkdirAll/`paths.Data()` bug class
  present (diff adds no file I/O); no SQL/money/i18n/repository-pattern
  issues; no shop-owner-visible behavior change, so no `web/help/` update
  needed (confirmed explicitly, not just assumed).

## Verified beyond automated tests

- Reviewer independently re-verified the TDD claim by reverting **each**
  of the three accessors' `RLock`/`RUnlock` in turn (not just the one
  originally claimed) and confirming `-race` fails on each, then restoring
  and confirming green — evidence captured in the review transcript.
- `go build ./... && go vet ./...` clean.
- `go test ./... -race` — every package green, no failures, no new
  flakes.
- `bash scripts/ci/guard-data-access.sh`,
  `bash scripts/ci/guard-kiosk-engine.sh`,
  `bash scripts/ci/guard-i18n.sh`, `bash scripts/ci/guard-help-topics.sh`
  — all green.

## Verdict

Safe to merge.

## Deferred

- universaltill/ut-docs#486 — add a CI guard for "no unlocked read of
  `Pm.Installed`/`Pm.MenuPlugins`/`Deps.Menu` outside `common/deps.go`",
  mirroring this repo's other mechanically-enforced invariants.

# Review — replica plugin sync: the prune keeps a plugin the main re-listed (ut-docs#7012)

Date: 2026-11-25 · Lane: lane:cloud-41 · Branch: `fix/7012-prune-keeps-relisted-plugin`
Author (Dev + fix round): Sonnet subagents · Review: Opus 5.5 subagent (fresh context) · Tester: separate
Sonnet subagent · Fix-round re-check: Opus orchestrator (different model from the author).

Pre-flight: till user-visible behaviour: none (replica background sync only; no UI, no help topic change);
docs-shots n/a; `docs/reference/` n/a; ut-docs `architecture/lan-sync.md` gains one sentence on the
relist case (ut-docs branch `docs/7012-prune-relisted-plugin`); ADRs: none changed; `README.md` n/a;
new `en.json` keys: none; migrations: none; review record: this file; follow-up: ut-docs#7034 for
the relist-upgrade rollback gap and re-keying the stale record (see Findings 1 and 3).

## What shipped
- `internal/pages/sync_admin.go` `convergePluginSet`: alongside `inPrimary` (listing ids), it now builds
  `inPrimaryPlugin` (plugin ids, same filter: rows need both a ListingID and a PluginID). The prune loop
  skips an Active record whose listing is gone but whose `PluginID` the main still lists under another
  listing. Before, it removed the plugin by id, so its settings and storage were lost, and the install
  loop re-installed it from the new listing in the same tick.
- The stale record stays in place: no per-listing delete exists, and `ClearForPlugin` would also drop the
  new listing's record. It is pruned once the main drops the plugin entirely.
- Tests, `internal/pages/sync_plugin_relist_7012_test.go` (`-run 7012`):
  - `…_7012_RelistedPluginKeepsInstallAndSettings`: stale L1, main lists P under L2. P is not removed and
    its setting survives.
  - `…_7012_RelistedPluginSecondTickIsQuiet` (Tester): two more ticks, no installs or removes.
  - `…_7012_RelistedThenDroppedPluginIsPruned` (Tester): a later tick with P gone from the main prunes
    P once, so the stale record cannot keep a dropped plugin alive.
  - `…_7012_RelistedAtNewVersionUpgradesInPlace` (fix round): L1@1.0 → L2@1.1.0. One install, no
    remove, the setting survives, the L2 record is at 1.1.0.
  - All four assert no open `plugin_sync` problem for L1 or L2.

## TDD evidence
- Dev: before the fix, `removes = [remove:com.t.p], want none (P is still listed, under L2)`.
- Reviewer (own run) and the orchestrator (after the fix round): with `sync_admin.go` stashed, all four
  tests FAIL. With it restored, all four PASS.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | Relist at a different version: `cloudInstallPluginVersionChecked` looks up the prior record under L2, finds none, and so takes no rollback snapshot. If the pinned version then mismatches, it falls through to `cloudRemovePlugin(P)`, and a plain failure writes a Failed L2 record while P still runs. This was already there and is narrower than the bug fixed here: before, every relist removed P. | Deferred: ut-docs#7034 (fall back to the Active record carrying the row's PluginID). |
| 2 | Low | The different-version relist path was untested. | Fixed: `…_7012_RelistedAtNewVersionUpgradesInPlace`. |
| 3 | Low | The store page, plugins page, update checker and plugin API map a plugin to its listing via the leftover L1 record (tile shows L2 "not installed"; update count compares against L1). These paths only count and display, they never apply anything. | Deferred: ut-docs#7034 (re-key the record to L2 with a new per-listing delete in `internal/data`). |
| 4 | Nit | The skip comment overstated the same-version case. | Fixed: reworded. |
| 5 | Nit | The test names lacked `7012`, so `-run 7012` silently selected nothing. | Fixed: renamed. |

Verdict: ship after fixes. Fixes 2, 4 and 5 were re-verified by the orchestrator.

## Gate
`gofmt -l`, `go vet ./internal/pages`, `go build ./...`, `golangci-lint run ./internal/pages/...`, every guard
in `ci.yml`'s `build` job, `go test ./internal/pages -count=1` (the full `-race` run of `internal/pages`
exceeds this container's time budget; CI runs it). The Tester saw one red `internal/pages` run while the
reviewer had `sync_admin.go` stashed in the same checkout; it passed on an immediate rerun.

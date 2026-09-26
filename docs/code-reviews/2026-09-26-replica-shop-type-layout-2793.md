# Review — additional tills follow a pulled shop_type's builtin layout (ut-docs#2793)

**Date:** 2026-09-26 · **Lane:** lane:cloud-54 · **Complexity:** medium ·
**Author model:** Opus 5.5 · **Reviewer:** Fable (independent subagent)

## What shipped

- `internal/pages/shop_type_layout.go` — `StartShopTypeLayoutReconcile`, a
  30 s background loop (wired in `init.go` beside the other `Start*` loops)
  that runs `builtinlayouts.Sync` for the current `shop.type` and reloads
  plugins when it changed something. Before this, only boot and the two
  local shop_type handlers reconciled, so a shop_type arriving by admin
  pull (additional till) or a cloud `SetSetting` directive left the layout
  on the old value until a restart.
- It never runs mid-sale: while the cashier basket, the kiosk basket or a
  table-QR session has items (`autoUpdateBusy`, the same gate the
  unattended update uses) the tick defers and the next one retries.
- A failed Sync reloads once per distinct failure (the ut-docs#2006
  "reload on error" rule), not every tick.
- `builtinlayouts.Sync` is serialised by a package mutex, so the loop can't
  race a handler's or boot's Sync.
- Manual: `web/help/{en,de,fa,ar,tr}/multitill.md` step 13 says a shop-type
  change switches each joined till's layout without a restart, never in
  the middle of a sale. Docs-shots topic hashes refreshed; surface hash
  refreshed with `update-docs-shots-surface-hash.sh` — no rendered pixel
  changes (background loop only).

## Findings (Fable review of 8cca619)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | major | A persistent Sync error (e.g. a conflicting marketplace layout plugin makes `PersistManifest` refuse) would reload plugins and `NudgeLink(ScopePlugins)` every 30 s forever; on the main till that drives an extra admin pull on every joined till each tick. | Fixed: `shopTypeLayoutReconciler.lastFail` — reload + warn only on a new (shop_type, error) pair. |
| 2 | minor | Test called `paths.Init(t.TempDir())` without restoring it. | Fixed (restore in `t.Cleanup`, as `ai_api_test.go`). |
| 3 | minor | `syncMu` sat between `Sync`'s doc comment and the func, detaching the doc. | Fixed (moved above the doc block). |
| 4 | nit | Nothing tests that `init.go` wires the loop. | Accepted — same as the `StartSelfOrderSessionSweep` precedent. |

Reviewer also checked and found fine: basket reads are mutex-guarded and
nil-safe; `syncMu` and `PluginMu` never nest (no deadlock); on a replica
`NudgeLink` has no peers, and the plugin-follow pull only converges
listings with a `listing_id`, so it never fights the system salon plugin;
a sale starting between the busy check and the reload sees at worst a
brief render-lock stall (the existing settings handler reloads with no
check at all); shutdown via wg/ctx is correct; no user-facing strings,
no cwd-relative paths.

## Verified

- `TestShopTypeLayout_ReplicaFollowsPulledShopTypeWithoutRestart` drives a
  real main-till server and the real `syncPullTick`: shop_type=service on
  the main till → replica pulls → the tick defers while the cashier basket
  and then the kiosk basket hold items (nothing installed) → once empty the
  salon plugin is installed **and** live in the menu amendments (it hides
  `/tables`) → main till moves to retail → the replica drops it.
- Mutation checks (author and reviewer): removing the busy gate, the
  reload, or the reload-on-changed each fail the test.
- Full gate: `go build ./...`, `go test ./...`, `golangci-lint` (0 issues),
  gofmt, data-access / i18n / kiosk-engine / core-neutral / help-topics /
  help-drift / compliance / competitor-naming / docs-shots guards.
- Not driven on a real two-till device pair; no UI surface changed.

## Pre-existing, not this change

`go test ./internal/app -race` fails on `main` too (server.Start vs
enroll.Effective); CI runs without `-race`. Filed as ut-docs#2990.

A persistent-Sync-error regression test was not added: provoking a
repeatable Sync failure needs a conflicting layout manifest fixture —
disproportionate for a log/reload de-duplication.

**Verdict:** safe to merge.

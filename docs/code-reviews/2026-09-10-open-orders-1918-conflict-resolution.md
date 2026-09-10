# Conflict resolution: open-orders stable identity (#982, ut-docs#1918)

## What was broken

PR #982 sat unmergeable (`mergeable_state: dirty`, zero check runs — the
exact `pull_request`-trigger-can't-compute-a-conflicted-merge-ref shape
documented in ut-docs#2016) since 2026-09-09: `main` moved out from under
it, most significantly PR #1042 (ut-docs#2008), which replaced
`menu_page.go`'s imperative `add(...)` tile-building with the ADR-0088
declarative `uislot.CoreMenu` slot registry. #982's own tile addition
(`/open-orders`, ungated) was written against the pre-#1042 shape and
textually conflicted with it.

## Resolution

- `internal/pages/menu_page.go`: took `origin/main`'s whole
  `menuVisibility`/`menuPredicates`/`registerMenu` rewrite; dropped #982's
  old `add(...)`-based `registerMenu` entirely. `iconSVGFor` keeps only
  `origin/main`'s residual 4 non-core entries (per its own test's guidance:
  core icons now live on `uislot.CoreMenu` entries).
- `internal/uislot/slot.go`: added `/open-orders` as a new `CoreMenu` entry
  (ungated, not `InNav`, `Order: 1900` — between the plugin-page band and
  `/help` at 2000, matching the old add()-sequence's position) since it is
  not part of `d.MenuSnapshot()` and must render unconditionally, same as
  `/help`.
- `internal/pages/menu_page_test.go`: kept both new-on-each-side test
  functions (`TestMenuPage_OpenOrdersTileRendered` from #982,
  `TestMenuPage_Fiscal{Register,Device}TileStaysManagerGatedForCashier`
  from `main`) — purely additive, no real conflict of intent.
- `internal/pages/menu_layout_test.go`: `goldenManagerTiles` (the pinned
  golden tile list) and the cashier-view slice needed `/open-orders`
  inserted between `/items` and `/help` — this is the list
  `TestMenuPage_EveryCoreTileResolvesThroughTheSlot` iterates too, so
  fixing the one golden list fixed both failing tests.
- `web/help/img/**` + `web/help/*/sell.md` + `manifest.json`: regenerated
  for real via `make docs-shots` (headless Chromium), not hand-merged —
  these are `guard-docs-shots.sh`-checked generated artifacts.
- **Unrelated pre-existing `main` breakage, fixed as a rider (per
  PR-SWEEP's port-an-existing-fix rule):** `go test ./...` failed
  everywhere on `duplicate migration version 22` — `main` itself carries
  two colliding `022_*.sql` migrations (PR #1043 and #1044 both merged
  claiming version 22). universal-till#1048 already diagnosed and fixed
  this cleanly (rename `022_sync_admin_version.sql` →
  `023_sync_admin_version.sql`, by `main`'s own merge order); ported the
  identical rename + comment-string update into this branch rather than
  leaving this PR red for a bug that isn't its own and that a fix already
  exists for.

## Verification

- `go build ./...`, `go vet ./...` — clean.
- `go test ./internal/pages/...` — green (full package, including the two
  golden-tile-list tests above).
- `go test ./...` — green after the migration-rename rider (was: ~40
  unrelated failures, all the same collision error, before the rider).
- `gofmt -l .` — empty.
- `golangci-lint run ./internal/pages/... ./internal/uislot/... ./internal/data/...` — 0 issues.
- `bash scripts/ci/guard-i18n.sh` — 1626 keys resolve, all locales match.
- `bash scripts/ci/guard-docs-shots.sh` — fresh (surface `7a5bef07357a…`).
- `bash scripts/ci/guard-deadcode-baseline.sh` — could not run in this
  sandbox (`deadcode` itself fails: `pkg-config` can't find
  `gtk+-3.0`/`webkit2gtk-4.1` for the desktop webview build — a sandbox
  library gap, not a Go/deadcode finding; not run, not claimed green).

## Why this PR still isn't merged

Unchanged from the PR's own note: `web/locales/en.json` keys this PR adds
need `ut-plugin-language-{de,es}` translations before `main`'s
`lang-pack-drift` gate stays green, and producing real translations
requires the self-hosted-AI-only NAS model (ADR), unreachable from this
cloud sandbox. Conflict resolution only; the `blocked:env` disposition on
the linked issue stands.

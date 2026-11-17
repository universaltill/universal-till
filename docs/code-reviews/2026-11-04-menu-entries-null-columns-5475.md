# Code review — ListMenuEntries tolerates NULL route/menu_group (ut-docs#5475)

**Date:** 2026-11-04 · **Branch:** `fix/5475-menu-entries-null-columns` · **Card:** universaltill/ut-docs#5475 (complexity:easy)

## What shipped

- `internal/data/plugin_repo.go` — `PluginRepo.ListMenuEntries` selects
  `COALESCE(pe.route,'')`, `COALESCE(pe.label,'')`, `COALESCE(pe.menu_group,'')`.
  One NULL in a page row used to fail the whole query, so `plugins.Init`
  returned an error and the till would not boot. A NULL now reads exactly
  like the `''` that `ReplacePluginEntries` stores for an empty value.
  (`label` is `NOT NULL` in every shipped schema; its COALESCE is defensive.)
- Tests: `TestPluginRepo_ListMenuEntries_NullRouteAndGroup` (repo level) and
  `TestLoadMenuEntries_ToleratesNullRouteAndGroup` (`Init` level, boot path).

## Process

Dev (Sonnet) TDD → Tester (Sonnet, fresh, isolated worktree) → Reviewer (Opus 5.5, fresh).

TDD evidence, re-verified by the Tester in a separate worktree: with the
fix reverted both tests fail with
`sql: Scan error on column index 2, name "route": converting NULL to string is unsupported`
(Init test: `load plugin menu entries: … Scan error … "route"`); with the fix
both pass. Mutation check: reverting the COALESCE on `route` alone or on
`menu_group` alone each fails the repo test.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Low | GROUP BY keeps raw columns, so a NULL-route row and a `''`-route row with the same plugin/key come back as two identical rows. `loadMenuEntries` overwrites by key; no shadow warning (same plugin). Before the fix that data blocked boot. | Accepted |
| 2 | Info | Empty route/menu_group downstream: `common.BuildMenu` skips `Route == ""`, `/ext/` proxy 404s on empty route, plugin view/slot/page skip non-`/plugin/` routes — so the row becomes a harmless non-tile; COALESCE preferred over skip-and-log. | No change |
| 3 | Info | Other boot-path `List*` queries in `plugin_repo.go`: all raw columns are NOT NULL; nullable ones already COALESCEd. | No change |

Recurring checks: no file writes (MkdirAll n/a), no cwd-relative paths, no migration touched, SQL stays in `internal/data`.

## Gate

`gofmt -l .` clean · `go build ./...` ok · `go vet ./internal/data/ ./internal/plugins/` ok ·
`go test ./internal/data/` ok · `go test ./internal/plugins/ -run Menu` ok ·
`guard-data-access.sh` ok. The whole `go test ./internal/plugins/` package
exceeds 400 s in the cloud sandbox **on plain `origin/main` too** (wasm/TCP
tests), so it was left to CI.

Pre-flight: help topic n/a (no user-visible behaviour beyond "till still boots"); reference docs n/a; README n/a; en.json n/a; ADR n/a; review record updated; follow-ups none.

## Verdict

Approve.

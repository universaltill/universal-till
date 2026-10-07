# Review — reserved-route page entries get no menu tile or Docs button (ut-docs#3817)

Date: 2026-10-07 · Lane: `lane:cloud-54` · Author model: Opus 5.5 · Reviewer: Sonnet (independent subagent)

## What shipped

A plugin `page` entry under a core-reserved route (`/help/…`, `/settings`, …)
installed before install time refused such routes (ut-docs#3786/#3791/#3818)
was never dispatched (`findPageEntry` skips it) but still produced:

- a **menu tile** (`common.BuildMenu`) that opened a 404, and
- a **Docs button** on the Plugins page (its `docs` entry route lookup) that opened a 404.

Both now skip a route for which `plugins.ReservedPageRoutePrefix` reports
reserved, so dispatch, menu and Docs button agree. `ReservedPageRoutePrefix`'s
doc comment names all three call sites. ut-docs `reference/plugin-manifest.md`
updated to match (separate ut-docs PR).

## Tests (TDD)

- `TestBuildMenu_SkipsReservedRoutes` — `/help/vendor/guide` and `/settings`
  get no tile; `/plugin/faq` and `/helpix` (per-segment prefix) keep theirs.
- `TestPluginsPage_ReservedDocsRouteHidesDocsRoute` — a `docs` entry at
  `/help/vendor/guide` yields an empty `docsRoute`.

Both failed before the fix with the stated reason (author run, and
re-verified independently by the reviewer: revert the two production hunks →
fail, restore → pass).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | `/ext/{key}` still proxies a legacy reserved-route `MenuPlugin` (direct URL hit returns 502). No tile or button leads there any more. | Accepted; out of scope. Filtering in `loadMenuEntries` would cover both, but `/ext` treats `Route` as an outbound URL, a different contract. |
| 2 | nit | Reserved check in the Docs lookup runs before the `docs`-key check (extra prefix scan per entry). | Accepted — negligible cost, reads clearly. |
| 3 | nit | No new test for a non-reserved docs route. | Already covered by `TestPluginsPage_DocsEntryExposesDocsRoute`. |

No blockers/majors. Rules checked: no raw SQL outside `internal/data`, no new
user-facing strings, no network on the checkout path.

## Verified

`gofmt`, `go build ./...`, `go vet ./...`, full `go test ./...`, and every
guard in `ci.yml`'s `build` job (shellcheck unavailable locally; no shell
files changed). No UI layout change (an entry is omitted), so no
screenshots or help prose changes needed.

## Verdict

Safe to merge.

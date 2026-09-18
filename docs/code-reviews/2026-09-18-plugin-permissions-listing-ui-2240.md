# Code review — Plugin permission grant/revoke API has no listing UI (ut-docs#2240)

- **Date:** 2026-09-18
- **Ticket:** ut-docs#2240 (`complexity:medium`, `ux`)
- **Branch:** `feat/2240-plugin-permissions-listing-ui`
- **Reviewer:** independent pass, fresh-context Opus subagent (per this
  card's `complexity:medium` routing, `MODEL-ROUTING.md`: Sonnet built it,
  Opus reviewed it), isolated in its own git worktree.
- **Verdict: SAFE TO MERGE.** No blocking findings.

## The gap

`internal/plugins/permissions.go`'s `ListPluginPermissions` (returns a
plugin's declared permissions with grant status) had no production caller
(`ut-docs#1566`, confirmed by `deadcode`). Its write half —
`POST /api/plugins/permissions/grant` and `/revoke`
(`internal/pages/plugin_api.go`) — was already live, gated
(`plugin_management`), audited, and already bumped the plugin event-bus
generation on change. No page listed an installed plugin's declared
permissions with local grant state for an operator to act on; the two
existing places that render permissions (the store listing card, the
manual-import preview) show a manifest's *requested* set, not local grant
status.

The card asked to decide: wire the read half into a small UI addition, or
delete the dead code. Decision: wire it in — the write API already
existed, tested and audited, so this completes existing plumbing rather
than adding new architecture. No ADR needed.

## What shipped

- `internal/pages/plugin_settings_page.go`: `GET /plugins/{id}/settings`
  now also calls `plugins.ListPluginPermissions` and passes a
  `Permissions` view model to the template. A list failure degrades to an
  empty permissions section (logged), the same display-degradation
  standing the file's existing `secretSettingCheck` already uses — it does
  not fail the whole settings page.
- `web/ui/pages/plugin_settings.html`: a new **Permissions** card lists
  each permission with a Granted/Not-granted tag and a Grant/Revoke
  button. The button calls the *existing* grant/revoke endpoints via plain
  `fetch` + `URLSearchParams` (those handlers answer plain text, not
  JSON, so htmx swap wasn't a natural fit for updating just one status
  tag) and reloads on success — mirrors the existing `window.act` pattern
  already on `web/ui/pages/plugins.html`.
- `internal/pages/plugin_settings_page_test.go`: new tests —
  `TestPluginSettingsPage_GET_RendersPermissionsWithGrantStatus`,
  `TestPluginSettingsPage_GET_NoPermissionsShowsEmptyState`,
  `TestPluginSettingsAPI_GrantThenPageShowsGrantedStatus` (the last drives
  the *real* grant handler via `registerPluginAPI`, then re-fetches the
  settings page GET and confirms the granted status renders — proves the
  wiring end to end, not just the template in isolation).
- `web/locales/{en,ar,fa,tr}.json`: 10 new i18n keys under
  `plugins.settings.permissions.*` (title/help/none/granted/not_granted/
  action.grant/action.revoke/header.name/header.status/header.action),
  real translations for ar/fa/tr (not English copies).
- `web/help/en/plugins.md` and `web/help/de/plugins.md`: one sentence
  added to the existing settings-page manual item describing the new
  section (de was kept in lockstep since it was already 1:1 with English
  before this change; fa/ar/tr's pre-existing structural drift on this
  topic, tracked under ut-docs#1962/#1973, is unchanged in size).
- `web/help/img/manifest.json`: regenerated via the real `make docs-shots`
  harness (124/124 shots passed) since the app surface hash changed.
  `/plugins/{id}/settings` is not any help topic's `routes[0]` (the one
  URL the harness screenshots per topic — the "plugins" topic's is
  `/plugins`, untouched by this change), so nothing in the actually
  screenshotted surface visually changed; the incidentally-regenerated
  PNG bytes for unrelated pages (Chromium anti-aliasing noise across
  reruns, not content changes) were discarded, keeping only the
  hash-manifest.
- `scripts/ci/deadcode-baseline.txt`: removed the now-stale
  `ListPluginPermissions` entry.

## What the independent review found and verified by execution

- **TDD re-verified, not trusted on word** — reverted the `Permissions`
  wiring (hardcoded `permViews := nil`): both new tests failed with the
  expected errors. Went further and mutated the template's per-row
  conditional in both directions (`{{ if true }}` / `{{ if false }}`):
  each mutation broke exactly the row-specific assertion it should,
  proving the granted/not-granted state is pinned **per row**, not just
  globally. Restored; all tests pass; worktree byte-identical to the
  reviewed commit afterward.
- **Full gate run live**: `gofmt -l .`, `go build ./...`, `go vet ./...`,
  `golangci-lint run ./...` (whole repo, 0 issues), `go test ./...`
  (entire suite green), and every relevant guard
  (`guard-i18n`, `guard-data-access`, `guard-help-topics`,
  `guard-help-drift`, `guard-docs-shots`, `guard-docs-shots-cross-check`,
  `guard-compliance-claims`, `guard-page-http-error`, `guard-htmx-loaded`,
  `guard-plugin-menu-read`, `guard-plugin-settings-bump`) all pass.
  `guard-deadcode-baseline.sh` couldn't run in the review sandbox
  (missing GTK/WebKit headers, an environment limitation unrelated to
  this diff — CI has them); verified the underlying claim manually
  instead: `registerPluginSettings` is wired from `internal/pages/
  init.go:511`, a real production call path.
- **Security, proven not reasoned** — wrote and ran throwaway probes,
  then deleted them:
  - A permission name containing `"><script>...` renders fully
    HTML-attribute-escaped in `data-perm-name`. This escaping is
    load-bearing: `Manifest.Permissions` is a bare `[]string` with no
    charset validation, so the value is genuinely attacker-influenced via
    a plugin manifest.
  - `plugin_id: "{{ .PluginID }}"` embedded in the inline `<script>`:
    a value containing `";alert(1);var x="` renders correctly
    JS-string-escaped (`"`) by `html/template`'s contextual escaper.
    No breakout.
  - Auth gate: `UT_AUTH=on`, no session → GET redirects to `/plugins`
    (303), POST revoke returns 403. The new section needs no separate
    gate — it's covered twice over by the existing page-level and
    endpoint-level checks.
  - The known ut-docs#2157 trap (a 401 answering with a JSON
    `{code,message}` object, rendering as `[object Object]` if
    mishandled) is correctly handled via the `res.status === 401`
    redirect in the new script, same as the sibling pattern.
- **Claim traced, not assumed**: "Revoking takes effect immediately" (the
  i18n help text) is accurate — every permission read is a live per-call
  DB read (no caching of grant state anywhere), and the pre-existing
  `plugins.SharedBus(d.Db).BumpGeneration()` in the revoke handler already
  drops cached event-bus `.ask` answers.
- **Recurring bug classes checked**: no new file writes at all (no
  `os.MkdirAll` gap possible) and no new `paths.*`/cwd-relative path use.

## Findings — triaged

1. **LOW, filed as follow-up (ut-docs#2419)**: `plugin_permissions` rows
   are never pruned when a manifest update drops a declared permission —
   `PersistManifest` only ever inserts (`ON CONFLICT DO NOTHING`), never
   deletes. Pre-existing gap in the write path (predates this card); this
   change only makes it visible (the new listing can show a permission no
   longer in the current manifest). Not a merge blocker — filed as its
   own card with a concrete repro and suggested fix.
2. **LOW, fixed**: the new permissions table had no `<thead>` (the
   sibling takeaway-overrides table in the same file does). Added a
   3-column header (`Permission` / `Status` / `Action`) with matching
   i18n keys in all four locales.
3. **NIT, fixed**: a stray space in the not-granted tag's `<span>` when
   `.Granted` is false. Cosmetic; corrected.
4. **NIT, accepted as-is**: no in-flight disable on the Grant/Revoke
   buttons (a double-click sends two POSTs; idempotent at the DB level,
   just an extra audit row). Matches the existing `window.act` pattern's
   own behaviour elsewhere in this file — not a regression this card
   introduces.

## Explicitly deferred

- ut-docs#2419 (stale permission-row pruning) — filed, not fixed here;
  out of this card's own scope (a write-path gap in `PersistManifest`,
  not the read-side listing UI this card was scoped to build).

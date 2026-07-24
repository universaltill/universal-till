# 2026-07-24 — Render a plugin's manifest icon on the buttons it contributes

## Context
`ut-docs/QUEUE.md` gap: "Buttons can carry icons — tender/menu buttons show
an icon; a plugin can ship its own icon in the manifest and it shows on the
plugin card AND on the button it contributes." Scoping (via an Explore
subagent) found the data plumbing already existed but was write-only:
`manifest.json`'s per-entry `icon_path` gets persisted into
`plugin_entries.icon_path` at install time (`PersistManifest`), and
`PluginRepo.ListButtonEntries` already scanned it back out — but no template
anywhere ever rendered it, and no route existed to serve the file.

**Scoped down from the full backlog item.** The full ask has two other
pieces, both genuinely needing a design decision first, not just a render
fix:
- Tender-button icons — `payment_methods` (the table backing the pay-grid)
  has no icon column or any plumbing to one at all; would need new schema.
- The plugin manage-card icon (`/plugins`) — `Manifest` has no plugin-level
  icon field, only a per-entry one; showing "the" icon for a plugin with
  multiple entries needs a semantics decision (which entry's icon, or a
  separate manifest field) before any code.

This change ships only the well-defined subset: action/menu buttons
(`plugin_buttons.html`, the sale-screen panel), which already had a
per-entry icon field with nowhere to decide "which one" — it's exactly one
icon per button.

## Design
- `internal/data/plugin_repo.go`: `ButtonEntryRow` gains `PluginVersion`
  (added `p.version` to `ListButtonEntries`'s SELECT) — needed to build the
  on-disk path `{pluginID}/{version}/{icon_path}`.
- New `internal/pages/plugin_icons.go`: `GET /plugin-icons/{plugin}/{version}/
  {file...}`, guarded with `filepath.Clean` + prefix-check against the
  plugin's own resolved directory — the same pattern
  `resolvePluginThemeCSS` (`themes.go`) already uses for theme CSS, itself
  plugin-shipped, semi-trusted content.
- `internal/auth/middleware.go`: `/plugin-icons/` added to the exempt-prefix
  list alongside the existing `/themes/` exemption — static plugin assets,
  not sensitive.
- `plugin_buttons.html`: renders the `<img>` when `.IconPath` is set, none
  otherwise.

## Independent review
Opus-model review, adversarial brief, explicitly weighted toward the
path-traversal question since that's the highest-stakes part of a new
plugin-supplied-input file-serving route.

**Confirmed correct (reviewer verified independently):**
- Path traversal guard is sound against encoded (`%2e%2e`) and segment-level
  (`pluginID`/`version` containing `..`) attempts — `http.ServeMux`'s
  `{file...}` wildcard decodes before the handler sees it, and
  `Clean`+prefix-check catches what reaches the handler. Symlink escape
  isn't caught (no `EvalSymlinks`) — same limitation the existing theme-CSS
  guard already has; plugins are Ed25519-verified at install, consistent
  risk posture, not a new hole.
- Auth exemption is justified the same way `/themes/` already is; only
  leaks a plugin id/version existence oracle to an unauthenticated prober,
  matching the pre-existing theme route's exposure.
- The new `p.version` column doesn't break any other `ListButtonEntries`
  caller or `ButtonEntryRow{}` literal — checked every call site directly.
- Test schema additions (`icon_path`, `parent_page_key`, `target_action`,
  `trigger_event` on the hand-rolled test `plugin_entries` table) don't
  diverge from the real migration in a way that would make the test pass
  falsely — `ListButtonEntries` selects by name, not position.
- Template interpolation of `.IconPath`/`.PluginID`/`.PluginVersion` is
  safe — plain `html/template` auto-escaping, no `template.HTML`/`URL`
  bypass.
- Scope claim is honest: tender-button and plugin-card icons are genuinely
  untouched, not silently incomplete.

**Fixed:**
- **HIGH — the new icon route silently ignored `UT_DATA_DIR` on every real
  deployment.** `pluginPagesDir` and `pluginThemesDir` both get repointed
  at `paths.Plugins()` inside `pages.Init`, once `paths.Init` has actually
  run — but the matching line for the new `pluginIconsDir` was missing, so
  it stayed frozen on the `"./data"` cwd-relative fallback forever. The
  original code comment's justification ("a package var initializer runs
  before `paths.Init`, so `paths.Plugins()` would just freeze anyway") was
  only true in isolation — it ignored that `pages.Init` already re-assigns
  the two sibling vars *after* `paths.Init` runs, and the implementer had
  simply forgotten to add the third line. On any real install (macOS
  Application Support dir, the `.deb`'s `/var/lib/unitill`), every plugin
  icon would have 404'd while themes and plugin pages worked, because those
  two do get repointed. The implementer's own live-verification happened to
  use `UT_DATA_DIR=./data`, which coincidentally matched the frozen
  literal — masking the bug. Fixed by adding
  `pluginIconsDir = paths.Plugins()` to `pages.Init` alongside its two
  siblings, and re-verified live with `UT_DATA_DIR` pointed at a directory
  that is *not* `./data` relative to the process's cwd — the icon route now
  resolves correctly (confirmed it would have 404'd before the fix).

## Verification
`go build ./...`, `go vet ./...`, `go test ./...`,
`bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh` —
all green, both before and after the review-driven fix, and independently
by the reviewer. Live-verified twice against a real built binary and
scratch SQLite DB: once with `UT_DATA_DIR=./data` (before the fix — passed
coincidentally) and once with `UT_DATA_DIR` pointed at an unrelated
directory (after the fix — the case that actually matters, and the case
the first pass would have failed). New tests:
`TestPluginRepo_ListButtonEntries`, `TestPluginIcons_ServesFileUnderPluginDir`,
`TestPluginIcons_BlocksPathTraversal`, `TestPluginIcons_MissingFileIs404`,
`TestPluginButtonsTemplate_RendersIconOnlyWhenSet`.

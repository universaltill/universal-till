# Review — plugin content slots get one place per entry (ut-docs#3946)

Branch `feat/3946-plugin-slot-switcher`. Docs: ut-docs
`docs/3946-plugin-slot-switcher` (`reference/plugin-views.md`).

## What shipped

- `settings.sections` panels were drawn under whichever Settings section
  was open, and `admin.pages` panels under every /admin destination. Now:
  - **Settings**: one `.card` per entry inside `#settings-grid`, listed in
    the switcher under a **Plugins** group (`nav.plugins`), with a Plugins
    tile on the landing grid just before Advanced
    (`pluginSettingsNavRows`). The card asks only its own plugin, from the
    new `GET /ui/slot/{slot}/{plugin}/{entry}` (same host gate as
    `/ui/slot/{slot}`; 404 for an entry not filling the slot for this
    viewer), with `hx-trigger="intersect once"` so a hidden section never
    asks. A failing plugin shows its label and `plugin.view.unavailable`.
  - **Admin**: the placeholder is gone; `withPluginAdminGroup` adds a
    **Plugins** tree group of plain links to each entry's own (slot-gated)
    `/plugin/` page. All six destinations' OOB tree refreshes use
    `adminTreeGroups`, so the group never drops out.
- `contentSlotPanels` split into `contentSlotEntries` (filter, unchanged
  rules) + `askSlotEntries` (parallel asks), reused by both paths.
- Demo mode allows the new read-only route (404 there: no plugins).
- Help `plugins.md` step updated in en/de/tr/ar/fa.

## Review

Independent review by a different model (Fable; author Opus 5.5). It ran
build, vet, the slot/settings/admin tests and the i18n/help/data-access
guards, and mutation-tested the change. No blockers.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | The six destinations' OOB tree calls were untested: reverting one to `adminGroupsFor(...)` dropped the Plugins group after a row click with CI green. | Fixed: `TestAdminPage_EveryOOBTreeKeepsPluginGroup_3946` (source scan; mutation of `locations_page.go` confirmed to fail it). |
| 2 | should-fix | Index-based card/panel ids: disable entry A in another tab, open B → B's panel takes A's id and its action answers swap into A's card. | Fixed: ids derive from a hash of (plugin id, entry key) (`pluginSlotEntryID`); `TestPluginSlot_EntryIDsStable_3946`. |
| 3 | nit | Admin rows passed the translated label as `LabelKey` and the template translated it again. | Fixed: passes the declared label. |
| 4 | nit | Each OOB refresh recomputes `visibleAdminEntries` + entry list + grant checks. | Accepted: a few local SQLite reads per click. |
| 5 | nit | Settings search indexes the DOM once, so a lazily loaded plugin section's controls aren't searchable (its heading is). | Accepted: inherent to lazy loading. |

TDD: the new tests did not compile against the old code; the reviewer
also reverted only the templates (3 of 4 new tests fail) and mutated the
Go seams (each caught).

## Verified beyond automated tests

Driven run of a real built till (fresh data dir, FAQ fixture plugin with a
`settings.sections` and an `admin.pages` entry; the plugin does not answer
views, so the section shows the unavailable notice) in Chromium:
1024×600 and 360×740 in English, 1024×600 in Persian (RTL). Looked at the
screenshots: landing grid (Plugins tile before Advanced), Plugins
category list, the plugin section (heading + notice, no other card
visible), Shop name with no plugin panel under it, /admin with a Plugins
tree group and no slot placeholder. No console errors. Not checked: a
plugin that really answers a view inside a Settings section (covered by
the handler tests), dark theme, real touch hardware.

## Gate

gofmt, `go build ./...`, golangci-lint (0 issues), `go test
./internal/pages/` (ok), CI build-job guards (all pass locally except
`guard-shellcheck-version`: no shellcheck binary in this container; no
shell changed).

**Verdict:** safe to merge.

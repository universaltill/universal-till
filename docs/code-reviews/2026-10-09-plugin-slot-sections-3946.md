# Review — settings.sections / admin.pages as switcher entries (ut-docs#3946)

**Date:** 2026-10-09 · **Card:** ut-docs#3946 · **Branch:** `fix/3946-plugin-slot-sections`
Docs: ut-docs `docs/3946-plugin-slot-sections` (`reference/plugin-views.md`).

## What changed

- `settings.sections` entries are no longer loaded as panels under every
  Settings section. Each entry is its own section under **Plugins** in the
  Settings switcher (sidebar row + card), with a Plugins category tile on
  the landing grid.
- `admin.pages` entries are rows under **Plugins** in the /admin tree that
  link to the entry's own `/plugin/...` page; the per-destination panel
  block in `admin.html` is gone.
- New `GET /ui/slot/{slot}/{plugin}/{entry}`: the same slot gate as
  `GET /ui/slot/{slot}` (404 for an unknown slot, 403 when the gate fails),
  404 for an entry that does not fill the slot, and the unavailable notice
  when the plugin answers nothing. The panel id is the entry's index in the
  whole slot, so panel actions' `HX-Target` keeps working.
- `contentSlotEntries` now owns the entry list, order and cap of 8. The
  whole-slot route, the switchers and the per-entry route all share it.
  ADR-0121 §7 is unchanged: who is asked, the budget, the order/cap and the
  `ui:slot:<slot>` permission are the same.
- Demo mode allows the per-entry route; a demo has no plugins, so it is 404.

## Independent review (different model)

Nothing blocking. Gate ordering, path-value handling (`==` against DB rows,
`url.PathEscape` in hrefs, single-segment patterns), the active-plugin
filter, the nil-view notice, panel ids, the demo allow-list, i18n, RTL and
`showModal` were all checked and found sound.

Non-blocking findings:

1. **Section id collisions.** Plugin `a-b` with entry `c`, and plugin `a`
   with entry `b-c`, both become `plugin-section-a-b-c`, which duplicates
   the card id and the switcher row. **Fixed.** A later colliding id now
   gets `-2`, `-3`, …. Normal ids stay stable, so a remembered switcher key
   is not invalidated by installing another plugin. The test
   `TestSettingsPage_PluginSectionIDsNeverCollide_3946` failed before the
   fix (verified) and passes after it.
2. **Admin tree recomputes `visibleAdminEntries` through the slot gate.**
   This is a small cost per render. Left as is: the gate is the single
   source of truth, and testing only the `reports` permission would copy
   its logic.
3. **Plugin rows navigate the whole page.** Intended. This matches the
   existing "Other" rows, because a plugin page is not fragment-capable.
4. **Query pass-through.** Confirmed: both routes call
   `pluginViewParams(r)`, so they behave the same.
5. **Test audit.** The 3946 tests assert positions inside
   `#settings-grid`, exact sidebar rows, gate denial and the absence of the
   whole-slot `hx-get`. None can pass vacuously.

## Checks

gofmt clean; `go build ./...`; `go vet`; `golangci-lint` 0 issues; every
`build`-job guard passes, with the docs-shots surface hash refreshed (no
rendered pixel changes, since the docs-shots harness installs no plugins).
`shellcheck` is not installed in this container; no `.sh` file changed.

**Verdict:** safe to merge.

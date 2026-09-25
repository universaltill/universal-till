# Review — hidden quick buttons stay greyed in edit mode; trash removes from quick buttons only (ut-docs#2698)

- **Author:** Opus 5.5 (Dev subagent). **Reviewer:** Fable 5.1, fresh context. Card complexity:hard.
- **Scope:** migration 043 `items.sell_screen_removed`; hide keeps the `shortcut_buttons` row (greyed in jiggle/Designer, CSS-hidden at rest); trash → `/api/buttons/remove-from-grid` (`delete-item` + legacy `remove` aliased), never deactivates; Add clears both flags; UnhideAll clears hidden only; jiggle mode persists across tabs/overflow/search; search-in-edit "Add to quick buttons".

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| F1 | Medium | Hide keeps the row, so hide → Add with a changed tile code (e.g. a barcode plugin enabled later) upserted a **second** row → two live tiles | **Fixed**: `AddButton` is one tx; it reuses the item's existing row (code + position), refreshes label/image and clears both flags. Tests `TestAddButton_ReusesTheItemsExistingRow`, `TestButtonStoreAdd_HideThenAddWithChangedCodeKeepsOneRow` (failing first) |
| F2 | Low/Med | Hiding the last visible tile of the current category, then Done, left the vanished tab selected ("No quick buttons yet") | **Fixed** (buttons.html restoreGridState/visible-tab fallback + jiggle-exit handler; app.js publishes jiggle state). The new e2e also caught an extra bug: a never-clicked default tab wasn't persisted, so mid-edit it jumped to another category. Fixed by persisting the tab straight away |
| F3 | Low | Cloud `quick_button_layout_set` leaves hidden rows with stale `sort_order` | Follow-up **#2709** |
| F4 | Low | 3 dead locale keys | Follow-up **#2710** |
| F5 | Process | de/es packs need 5 new keys + changed `designer.long_press_hint` | Done in the same cycle as the merge |

Behaviour change from F1: adding an item that already has a quick button now updates that one button instead of creating another. 4 Go tests and `designer-search.spec.ts` were updated to match.

## Checked, no issue (reviewer)
Every hidden/removed consumer (All grid, category tiles/popups, counts, hidden list); the at-rest hidden tile is `display:none` (not focusable/clickable); scan/search selling unchanged; cloud heartbeat/directives unchanged; `requirePrimary` + `catalog_management` elevation on the new route and aliases; cache invalidation via the 023 triggers; migration 043 is additive (`NOT NULL DEFAULT 0`), checksum pinned; delete-item alias has no other caller (cloud, docs, android, plugins, website); a11y (glyph + visually-hidden state text, contrast ≥4.5:1 measured, 44 px badge); i18n en/ar/fa/tr + help in 5 languages; tests fail without the change.

## Verification
`go test -race` all packages (pages/plugins with the Makefile timeouts); after the fixes, ui/data race + pages targeted pass; e2e 2698 (5) + 2541 + 2339 + tab/add specs pass headless on the worktree's own tills; gofmt/vet clean. Real-touch device test is follow-up **#2711**.

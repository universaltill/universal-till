# Review — categories list: tap to edit, leading icon, long-press drag reorder (ut-docs#2699)

- **Author:** Opus 5.5 (Dev subagent). **Reviewer:** Fable 5.1, fresh context. Card complexity:medium (one round).
- **Scope:** pencil + chevrons removed from /categories and the Designer categories list; row name = edit button; leading thumb via `ui.CategoryThumb` (image → `iconid.AssetPath` icon → swatch → placeholder); shared `web/public/list-reorder.js` (long-press 450 ms/<8 px, pointer events, Escape/pointercancel, Alt+↑/↓ + live region, serialized saves, revert on refusal); Move up/down + "Position N of M" in the dialog / inline form.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Low | `touch-action: pan-y` on `<tr>` is a no-op (doesn't apply to table rows) | **Fixed**: applied to the row's cells; comment corrected |
| 2 | Low | Every row's name button described by the full hint → ~25 words per row for screen readers | **Fixed**: the table/list is described once; rows keep `aria-keyshortcuts`. Tests assert exactly one reference |
| 3 | Low | With a search filter, "Position N of M" counted hidden rows while Move stepped over visible ones | **Fixed**: position/move/canMove share the visible set. New e2e (failed with "Position 16 of 16" before) |
| 4 | Maint | Designer inline Move buttons use a second reorder code path | Follow-up **#2713** |
| 5 | Maint | `refreshAfterSave` listener can linger if a refresh is dropped | Follow-up **#2713** |

## Checked, no issue (reviewer)
Tap still edits (no arm under 450 ms; drop-click swallowed; a cancelled gesture can't eat the next tap); callout/selection suppressed; second finger/right button ignored; Chromium real-gesture probe on a scrollable page: arming stops the pan, swipe before arming scrolls; focus restored after DOM moves; no nested-interactive controls; `Icon` only via `iconid.AssetPath`, `ImagePath` validated + `imgv`; refusal text via `textContent`; serialized saves + revert logic; ADR-0098 shell signature lists the new head script (first boosted nav after update is a full load, by design); RTL isolate marks; i18n en/ar/fa/tr + help in 5 languages; tests fail without the change (11 assertions at HEAD).

## Verification
`go test -race` pages/plugins (Makefile timeouts) + all packages; `go test ./internal/pages -run '2699|Categor|Designer'`; e2e 2699 (14) + 2010 + 2174 + 2284 → 37/37 twice; gofmt clean. Real-finger test on the Pi's WebKitGTK and the tablet is still outstanding (see #2711-style device follow-up).

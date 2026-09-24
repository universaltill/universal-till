# Review: Designer "Show all N on the sell screen" (ut-docs#2614)

Date: 2026-09-24 · Branch `feat/2614-designer-show-all-hidden` · Built by an
Opus 5.5 dev subagent, reviewed by an independent Fable subagent (lane:cloud-24).

## Scope decision (BA/Architect)

The card asks for a one-click "Add all items" in the Designer. #2541
(universal-till#1338) already makes every active, non-hidden item an implicit
quick button. That change is not in any release yet: v0.21.4 predates it, which
explains the owner's "still not added". So "add every item that isn't a
button, hidden ones excluded" always finds 0 items. The only items that can
still be off the sell screen are the ones someone hid. The one-click action
therefore brings back every hidden item. The optional per-category variant is
left out, since after #2541 there's nothing per-category left to add.

## What shipped

- `data.CatalogRepo.UnhideAllSellScreen` runs one
  `UPDATE items SET sell_screen_hidden = 0 WHERE sell_screen_hidden = 1 AND is_active = 1`
  and returns how many items it changed.
- It is wired through `ui.ButtonStore.UnhideAll` and `ui.ButtonsHTTP.UnhideAll`, which returns `(n, ok)`.
- New route `POST /api/buttons/unhide-all`. It has the same gates as
  `/api/buttons/unhide`:
  - `requirePrimary` (409 on a replica)
  - `checkOrElevate("catalog_management")` and the elevation prompt
  - an audit row when an elevated PIN was used (`entity_id="all"`, `{"count": n}`)
  - 405 for any method other than POST: the route takes no input, so a
    prefetch or `<img src>` GET would otherwise unhide every item in one request
- Designer: when something is hidden, the "Hidden from sell screen (N)" heading
  gets a **Show all N on the sell screen** button (`.btn`, ≥44px). There is no
  confirm, because unhiding is not destructive. When nothing is hidden the
  button isn't rendered.
- New keys `designer.hidden.unhide_all` and
  `elevation.summary.buttons_unhide_all` in en/ar/fa/tr. The de/es packs get
  them in follow-up PRs (`ut-plugin-language-{de,es}`, branch
  `feat/2614-unhide-all-keys`).
- Help topic `till-designer` step 5 is updated in en/ar/de/fa/tr. The docs-shots
  manifest was regenerated; no PNG changed.
- One CSS line: `.designer-hidden-unhide-all { align-self: flex-start; }`
  (flex-relative, so it is RTL-safe).

## Review findings (Fable, round 1)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The German help says "auf dem Verkaufsbildschirm" but the de pack's button says "im Verkaufsbildschirm" | Fixed: the help now reads "Alle N im Verkaufsbildschirm anzeigen" |
| 2 | nit | The 405 check is written by hand, and the sibling hide/unhide routes accept any method | Accepted: the Go 1.22 `POST /path` mux pattern would be a style change across the whole file. Hide and unhide take an itemId, so they are single-item changes, not shop-wide ones |
| 3 | nit | The template test resets the global translator to nil instead of restoring the previous one | Accepted: `httpx` has no getter, and no test in `internal/ui` wires a translator or runs in parallel |

The reviewer also checked these and found them correct:
- Parity with `SetSellScreenHidden`. The `items` admin-sync bundle is keyed on
  the trigger-bumped `sync_admin_version`, so satellites receive the change
  exactly as they do for a per-item unhide.
- The elevation retry works with nil hidden fields.
- The ar/fa/tr translations are correct and keep `%d`.

The reviewer re-checked the TDD claims by stubbing `UnhideAllSellScreen` to
`return 0, nil`. All five targeted tests then failed (data, ui store, ui HTTP,
pages gate, pages audit).

## Verified beyond unit tests

- A new e2e spec, `e2e/tests/designer-unhide-all-2614.spec.ts`, runs in real
  Chromium at the default e2e viewport (1280×720). It hides two items, opens
  the Designer and clicks Show all. It then checks:
  - both tiles are back
  - the empty message shows
  - the button is gone
  - the button was at least 44px tall
- The existing `sell-tile-hide-delete-2541` spec still passes.
- The dev agent looked at the first screenshot and saw the button stretched to
  full width. That is fixed with `align-self`.
- `go test ./...` passes in full. gofmt, vet and golangci-lint are clean. Every
  `build`-job guard passes, except `guard-shellcheck-version` and shellcheck,
  which can't run because shellcheck isn't installed in this container. No
  `.sh` file changed.
- **Not looked at:**
  - the button under ar/fa RTL in a live run (only the logical CSS was checked)
  - the 1024×600 kiosk and 360px widths
  - real touch hardware

## Deferred

- **#2541's default reaching tills:** that needs a release after v0.21.4. The
  release count is due (6 feat/fix merges since v0.21.4), so it is handled in
  this cycle's DevOps step.
- **Turkish help wording mismatch (existing, not caused here):** the Turkish
  help calls the per-item button "Gizliliği kaldır", but `tr.json` labels it
  "Göster".

Verdict: safe to merge.

# Code review — Bluetooth Devices menu tile icon (ut-docs#1720)

**Date:** 2026-09-07
**Branch:** `fix/1720-bluetooth-menu-icon`
**Reviewer:** independent fresh-context Sonnet subagent (different model from the
implementer, per the `complexity:easy` routing in the `scrum-master` skill's
"Model routing by complexity")
**Verdict:** SAFE TO MERGE

## What changed

The `/menu` launcher's Bluetooth Devices tile rendered `📶` (ANTENNA BARS).
Reported by the product owner on a real tablet. That glyph means mobile signal
strength; ut-docs#76 picked it knowingly because Unicode has no Bluetooth
codepoint. The runic approximation (U+16D2) depends on font coverage Android
WebView does not reliably have, so the standard mark can only be drawn.

Reuses the icon system ut-docs#1423 already introduced for the nav rail
(Lucide paths, shared 24×24 `currentColor` wrapper) rather than adding a second
mechanism: a `bluetooth` entry in `internal/httpx/icons.go`, an exported
`httpx.Icon()`, a sparse `iconSVGFor` override map in `internal/pages/menu_page.go`,
and `.menu-ico svg` sizing in `web/public/app.css`.

## What the reviewer verified independently

- **XSS / injection — traced, safe.** `menuTile.IconSVG` is `template.HTML`, so
  escaping is bypassed by design; the reviewer traced every value that can reach
  it rather than accepting the author's word. `common.BuildMenu` confirms plugins
  genuinely can register arbitrary `Href`/`Label`, but `href` is only ever used as
  a **map-lookup key**, never concatenated into output. `iconSVGFor` is a
  compile-time literal with one key; an unknown key yields `""` and
  `httpx.Icon("")` renders nothing. No plugin- or locale-controlled data can reach
  the raw HTML.
- **`▪️` fallback (ut-docs#1371) preserved.** The condition now requires both the
  emoji and the SVG to be empty. Confirmed by the pre-existing
  `TestMenuPage_UnmappedRouteGetsFallbackIcon` still passing.
- **TDD claim re-verified, not trusted.** The reviewer reverted the production
  behaviour itself (emptied the `iconSVGFor` value), reran the suite, observed all
  three expected failures with their exact messages, restored the file, diffed to
  confirm it was byte-identical, and reran clean. The claim holds.
- **The SVG really is the Bluetooth mark.** Vertices traced as
  (7,7)→(17,17)→(12,22)→(12,2)→(17,7)→(7,17): a centre spine with two crossing
  flags — the Hagall+Berkanan bind rune — and verbatim Lucide's own `bluetooth`
  icon, not `bluetooth-connected`/`-off`. Fits the shared wrapper with no
  self-declared `width`/`height`/`fill`.
- **Theming.** `.menu-tile { color: var(--text) }` + `stroke="currentColor"` means
  the glyph tracks the theme. The reviewer checked all three sibling theme plugin
  repos (`midnight`, `buttons-left`, `screen-top`): none overrides `.menu-ico`,
  `.menu-tile` or `svg`, so no collision.
- **Gates.** `gofmt`, `go vet`, `go build`, `golangci-lint` (0 issues), and the
  CI-blocking guards all pass. `go test ./...` green.

## Findings and what was done about them

1. **Visual result not evidenced in the diff.** The tile is manager-gated and sits
   in the 4th row; `docs-shots` captures at a fixed 1024×600, so none of the ~90
   regenerated screenshots actually shows the new glyph beside its emoji
   neighbours. **Acted on:** drove the real app locally at the tablet's own
   1280×800 and captured `/menu` full-page. The glyph renders correctly and reads
   consistently against the emoji tiles around it. **Still outstanding:** a real
   TECLAST look, which is the card's AC 6 — this codebase has a specific history
   (ut-docs#1332/#1348) of icon sizing looking right on desktop Chromium and wrong
   on the device. The tablet takes its server build from a release, so this cannot
   be checked until ut-docs#1720 ships. The card stays In Review until then, the
   same way ut-docs#1648 was handled.
2. **Test asserted `▪️` appeared nowhere on the page.** Independently spotted by
   the implementer while reviewing the regenerated `web/help/img/en/menu.png`,
   which shows the Help/FAQ tile rendering that very square — so the assertion
   encoded something untrue of the real product and would have failed for a reason
   unrelated to Bluetooth. **Fixed:** the glyph assertions are now scoped to the
   Bluetooth tile's own markup. The page-wide `📶` assertion is kept deliberately
   (that tile was its only use). The black square itself is filed as ut-docs#1722.
3. **Stale agent worktree still carrying the old mapping.** Not part of this diff
   (gitignored) but it poisoned the reviewer's repo-wide grep. **Not acted on, by
   design:** `make prune-worktrees` was run and correctly refused all four — each
   holds commits not on `origin/main`. The script's own safety rule outranks the
   tidiness; forcing it would discard unmerged work.

## Note on the pre-existing failure

The reviewer hit `TestImport_ConcurrentDirectCommitsOfSameFileRejectSecond`
failing in `internal/pages`. It confirmed this is **not** this change's
regression by stashing the branch entirely, returning to the unmodified base
commit, and reproducing the failure there 3×. Unrelated and pre-existing; the
full `go test ./...` run on this branch was otherwise green.

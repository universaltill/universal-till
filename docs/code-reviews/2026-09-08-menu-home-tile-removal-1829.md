# Code review: Menu launcher — remove the redundant Home/Start tile (ut-docs#1829)

**Date:** 2026-09-08
**Branch:** `fix/1829-remove-redundant-menu-home-tile`
**PR:** universaltill/universal-till#TBD
**Card:** ut-docs#1829 (`p2`, `source:user`, `complexity:easy`)

## What shipped

The ☰ Menu launcher's first tile was `nav.home` — "Home" in English, "Start"
in German — leading to `/`, the selling screen. The Menu screen's own
"← Back to sale" button (`menu.back_to_sale`) already returns to that exact
same screen, visible on the same page at the same time: two controls, two
different names, for one destination. Live pilot-merchant feedback called
the tile meaningless; a follow-up research pass against the SumUp app
installed on the pilot tablet found SumUp has no such tile at all — it opens
directly onto the selling screen, so there's nothing to name. Revised
recommendation from the product owner (on the issue): remove the tile
rather than rename it.

Fix:
- `internal/pages/init.go`: `baseMenu` (the Menu launcher's core tile list)
  hoisted from a function-local var to a package-level `var`, and the
  `{Href: "/", Label: "nav.home"}` entry removed.
- `internal/pages/menu_page.go`: the now-dead `"/": "🧾"` entry removed from
  `iconFor`.
- `web/help/img/{ar,en,fa,tr}/menu.png` + `manifest.json` regenerated
  (`make docs-shots`); `web/help/img/ar/sell.png` and
  `web/help/img/en/till-designer.png` also regenerated as an incidental
  side effect of the harness's whole-surface hash (documented existing
  behavior, see `docs/code-reviews/2026-09-01-menu-orders-tile-icon-1371.md`).

`web/locales/{en,ar,fa,tr}.json` still carry the (now-unused) `"nav.home"`
string — deliberately left in place rather than removed. `guard-i18n.sh`
only enforces that every locale file's key set matches `en.json`'s; it does
not flag an unused key sitting in `en.json`. Three unrelated tests
(`translations_page_test.go`, `setup_base_plugins_test.go`,
`sync_admin_repo_test.go`) use `"nav.home"` purely as an arbitrary example
key for testing generic translation-override/sync mechanics, independent of
the actual menu tile — removing the locale string would risk touching those
for no user-facing benefit.

## TDD

Added `TestBaseMenu_NoRedundantHomeTile` in
`internal/pages/menu_home_tile_test.go`, asserting directly against the real
package-level `baseMenu` (not a synthetic fixture) that no tile has
`Href == "/"`. Verified red→green myself before review: reintroduced the
`{Href: "/", Label: "nav.home"}` entry, confirmed the test failed with a
clear message, restored the file, confirmed it passed again and the tree
matched the commit exactly.

## Independent review

Spawned a fresh-context Sonnet subagent (`complexity:easy` — review at
Sonnet, different instance, per the `scrum-master`/`reviewer` skills'
model-routing table), isolated in its own git worktree.

**Verdict: SAFE TO MERGE.** No blocking findings. The subagent:
- Diffed `origin/main...HEAD` and confirmed the diff matched the stated
  scope exactly — 10 files, no stray edits.
- Ran `go build ./...` / `go vet ./...` / `gofmt -l .` — all clean.
- Ran the full `go test ./...` (every package, not just `internal/pages`) —
  all green.
- Ran `guard-i18n.sh`, `guard-help-topics.sh`, `guard-docs-shots.sh` — all
  green, manifest surface hash matches.
- **Independently re-verified the TDD claim**: reintroduced the `nav.home`
  entry, reran the test, confirmed it failed with the same message; restored
  the file, confirmed the tree was byte-for-byte clean against the commit.
- Read `menu.html` and confirmed `menu.back_to_sale` renders unconditionally,
  outside and independent of the tile loop — removing the tile cannot remove
  the merchant's way back to selling.
- Read `nav.html` and confirmed the separate `nav.till` rail link (also
  `href="/"`) is a wholly distinct nav element, untouched by this change.
- Grepped for every `baseMenu`/`iconFor[` call site — no positional
  indexing anywhere, nothing else depends on the removed tile.
- Confirmed no file-write/`os.MkdirAll` gap and no cwd-relative-path issue
  (diff touches neither class of code).
- Confirmed no real client/shop name or secret-shaped literal in the diff.
- Checked `web/help/en/menu.md` (and `de/menu.md`, since the pilot merchant
  is German) — both describe the screen only generically ("a page of big
  touch tiles"), neither names "Home"/"Start" specifically, so no manual
  text needed updating.
- Visually inspected the regenerated `en/menu.png` and `ar/menu.png` (RTL)
  screenshots — grid starts cleanly with Designer/Inventory/Shifts/Journal,
  no empty first slot, "← Back to sale" renders correctly in both LTR and
  RTL.
- Independently re-verified the `nav.home`-left-in-locale-files reasoning
  above (read `guard-i18n.sh` itself, read all three named tests) rather
  than taking it on faith — confirmed sound.

**One non-blocking observation, not fixed here (out of scope for this
card):** `web/help/en/menu.md` says "tap the logo to get back to selling,"
but the nav logo (`web/ui/partials/nav.html`) renders as a bare `<div>`
with no `<a>`/click handler — the logo does not appear to actually be
clickable in the current UI. Pre-existing, unrelated to this diff (identical
on `origin/main`). Filed as ut-docs#1855 rather than chased here.

## Beyond automated tests

- Manual (`web/help/en/menu.md`, `de/menu.md`) confirmed already generic
  enough not to need updating — no per-tile prose describing the removed
  tile.
- `make docs-shots` regenerated and visually reviewed (both by me and by
  the independent reviewer) in LTR and RTL locales.

## Deferred / out of scope

- ut-docs#1855: stale "tap the logo" manual copy (logo not actually
  clickable) — pre-existing, found during this review, filed as a new
  Backlog card rather than fixed here.

## Verdict

Safe to merge via `merge_method: "merge"` (never squash/rebase — see
`reviewer` skill's "Merge method" note, ut-docs#250).

# Code review: Menu launcher Orders tile icon (ut-docs#1371)

**Date:** 2026-09-01
**Branch:** `fix/1371-menu-orders-tile-icon`
**PR:** universaltill/universal-till#TBD
**Card:** ut-docs#1371 (`bug`, `p3`, `source:user`, `complexity:easy`)

## What shipped

The ☰ Menu launcher's "Orders" tile rendered a plain black square instead
of a real icon, live on v0.8.2 (adb screencap, user-reported). Root cause:
`internal/pages/menu_page.go`'s `iconFor` map — which maps every nav route
to a touch-friendly emoji glyph for the `/menu` grid — had no entry for
`/orders`, so it fell through to the deliberate `▪️` fallback used for
genuinely-unmapped routes (e.g. plugin pages). Confirmed `/orders` is a
real, reachable tile (`internal/pages/init.go`'s `baseMenu`), not dead
code — this wasn't a font-coverage gap, just a missing map entry.

Fix: added an `/orders` entry to `iconFor`.

## TDD

Added `TestMenuPage_OrdersTileHasAMappedIcon` (mirrors the existing
`TestMenuPage_RendersConfiguredTilesWithMappedIcons` pattern). Verified
red→green myself before review: removed the map entry, confirmed the new
test failed (tile fell back to `▪️`), restored it, confirmed pass.

## Independent review

Spawned a fresh-context Sonnet subagent (this is a `complexity:easy` card
— review at Sonnet, different instance, per the `scrum-master`/`reviewer`
skills' model-routing table), isolated in its own git worktree.

**Verdict: SAFE TO MERGE.** No blocking findings. The subagent:
- Ran `go build ./...` / `go vet ./...` / `gofmt -l .` — all clean.
- Ran the full `TestMenuPage*` suite (12 subtests) — all pass.
- **Independently re-verified the TDD claim itself**: removed the
  `/orders` map entry, reran the specific test, confirmed it failed with
  the fallback glyph; restored the file, confirmed it passed again.
- Ran `guard-emoji-font.sh`, `guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-docs-shots.sh` — all green.
- Confirmed no file-write/`os.MkdirAll` gap and no cwd-relative-path
  issue (the diff has neither — pure map literal + a `httptest`-based
  unit test).
- Confirmed no real client/shop name or secret-shaped literal anywhere
  in the diff.
- Confirmed `/orders` really is in `baseMenu` (production-reachable).

**One non-blocking nit, taken:** the initial choice (🔔, a generic
notification bell) reads as "you have a notification" rather than
matching what `/orders` actually is — a kitchen-progress board
(preparing → ready → collected, per `web/help/en/order-status.md`).
Swapped to 🛎️ (service bell — "order ready for pickup"), re-ran the
scoped test suite and `gofmt`/`go build` clean, and regenerated
`make docs-shots` a second time for the final glyph.

## Beyond automated tests

- Read `internal/pages/menu_page.go` in full for the surrounding
  convention (single-codepoint vs. VS16 glyphs already in the map) —
  🛎️ uses VS16, consistent with several existing entries (⚙️, 🏷️, 🌍,
  🖥️).
- Manual (`web/help/en/menu.md` etc.) already documents the tile grid
  generically ("big touch tiles for every destination") with no
  per-icon prose to go stale here — no manual update needed beyond the
  screenshot.
- `make docs-shots` regenerated: `web/help/img/**/menu.png` shows the
  real glyph; the harness's whole-surface hash means every other
  routed topic's screenshot also regenerates in the same commit even
  though only `/menu`'s content changed — expected behavior of this
  repo's docs-shots tooling (confirmed against existing repo history),
  not a mistake.

## Deferred / out of scope

Nothing deferred. This is a single-file root cause with a single-line
fix; no follow-up card needed.

## Verdict

Safe to merge via `merge_method: "merge"` (never squash/rebase — see
`reviewer` skill's "Merge method" note, ut-docs#250).

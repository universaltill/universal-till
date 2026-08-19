# Code review: Table map / floor plan designer (ut-docs#814)

**Date:** 2026-08-19
**Card:** universaltill/ut-docs#814 — "Table map / floor plan designer + assign
table number to an order (dine-in table service)", scope narrowed by
ADR-0054 to the floor-plan editor + live free/occupied state (table-to-order
assignment is split into ut-docs#820, not built here).
**Complexity:** hard. **Branch:** `feat/814-table-floor-plan`.
**Build model:** unknown (this card was picked up mid-flight — a prior cycle
left a substantial WIP commit, `1d464312`, on the branch with the bulk of the
implementation already in place and its own tests passing; this cycle
finished it: closed the i18n-drift gap the WIP left, ran the independent
review, fixed every finding, and is completing the pipeline from here).
**Review model:** Opus, via an independent worktree-isolated subagent (per
"Model routing by complexity" — hard cards review at Opus, deliberately not
the build model, so the review shares none of the implementer's blind
spots).

## What shipped

- `internal/data/tables_repo.go` (+ migration `054_tables.sql`): a `tables`
  entity (label, area/zone, seat count, shape, 2D position, soft-disable)
  and a `ListTablesWithState` query that LEFT JOINs `held_sales.table_id`
  for live free/occupied state — the join is real and tested today, even
  though nothing writes that column until #820 ships (every table
  legitimately reads as free in the meantime; ADR-0054's own scope split).
- `internal/pages/tables_page.go`: `GET /tables` (full page + form),
  `GET /ui/tables/state` (the live-state SVG partial, HTMX-polled every 15s,
  paused while editing), and `POST /api/tables[/{id}][/position][/active]` —
  all manager-gated.
- `web/ui/pages/tables.html` + `web/ui/partials/tables_state.html`: the
  product's first free-position 2D editor — an inline SVG on a fixed
  1000×1000 logical canvas, pointer-events drag-to-place (one code path for
  touchscreen and desktop, extending `bugreport_panel.html`'s existing
  free-drag pattern), auto-saving on drop.
- Full i18n (32 `tables.*` keys + this cycle's follow-ups, in en/ar/fa/tr),
  RTL-safe (logical CSS properties throughout, SVG geometry unaffected by
  document `dir`), and a help topic in all four locales
  (`web/help/{en,ar,fa,tr}/tables.md`).

## Independent review — findings and disposition

The reviewer's full report is preserved in this cycle's transcript; verdict
was **"safe to merge with fixes"**, no blocker. Every finding below was
either fixed in this same branch or explicitly deferred with a linked card —
nothing was fixed by asserting the reviewer wrong.

| # | Severity | Finding | Disposition |
|---|---|---|---|
| S1 | should-fix | `held_sales_archive` was left out of migration 054's `ALTER` — violates 040's own documented invariant that every `*_archive` twin stays column-identical to its live table across every later `ALTER`. Harmless today (nothing writes `table_id` until #820), but a reset→restore after #820 lands would have silently dropped every parked order's table assignment. | **Fixed.** New migration `055_held_sales_archive_table_id.sql` adds the column; `internal/data/reset_archive_repo.go`'s explicit `held_sales` column list updated to include it; `internal/data/reset_test.go`'s `seedFullSale`/`TestResetThenRestoreRoundTrip` extended to assert `table_id` actually round-trips through archive→restore, not just documented. Also required updating the upgrade-simulation fixtures in `internal/db` (`rewindTables054` — 055 has no independent rewind path, since it only exists because 054 does) and the hand-rolled schema fixture in `internal/pages/ui_smoke_test.go` (`seedForPages`), both of which the guard scripts don't cover — caught by the full `go test ./...` gate, not by review alone. |
| S2 | should-fix | `label`/`area_zone` had no length bound anywhere, and both render into an SVG `<text>` with no wrap/overflow container — a pasted multi-KB label would draw one unclipped line across the whole plan. `seat_count` accepted any non-negative int. | **Fixed.** Server-side caps in `parseTableForm` (label/area_zone ≤ 64 chars, seats ≤ 999, both with a new `tables.error.too_long` key across all 4 locales and an extended `tables.error.seats` message), plus matching `maxlength`/`max` on the HTML inputs. |
| S3 | should-fix | The floor plan is `role="img"` with one static `aria-label` — a screen-reader user gets no per-table free/occupied/elapsed detail. No keyboard path to reposition a table (pointer-drag only). | **Partially fixed, partially deferred.** Added a "Live status" column to the existing HTML table fallback (free / `%d min` elapsed), so a screen-reader user can now read every table's live state even though the SVG map stays opaque to them. The keyboard-reposition gap is real but a materially bigger change (focus management inside SVG, arrow-key nudge + save semantics) — filed as **ut-docs#826** (`complexity:easy`, Backlog), not built here; low urgency today since every table reads as free until #820 gives the floor plan real operational meaning. |
| N1 | nit | `clampToCanvas` (Go) and the JS `clamp` both pinned a table's **centre** to `[0, 1000]`, ignoring the shape's own half-extent (rect ±65/±45, circle r=55) — a table dragged to a corner rendered ~three-quarters clipped outside the SVG `viewBox`. | **Fixed.** New `data.TableEdgeInset = 65` shared constant; `clampToCanvas` now bounds to `[65, 935]`; the JS clamp mirrors it via a new `canvasInset` template value (server clamp is the source of truth, JS clamp is cosmetic — avoids a visible snap-back on drop). `TestTablePositionPersistsAndClamps` updated to assert the new exact bound. |
| N2 | nit | ADR-0054 Decision 2 says live-state swaps should replace just the per-table `<g>`'s fill/label, "not the whole SVG" — the implementation swaps the entire `<svg>`. | **Not fixed, judged correct as shipped.** The code's own comment gives a sound reason (a fragment rooted at `<svg>` keeps the SVG namespace through htmx's `parseHTML` path with `useTemplateFragments` off — the reviewer independently verified this against the vendored htmx 1.9.12). Recorded here per ADR-0007 rather than left as a silent divergence; not worth a superseding ADR for an implementation-detail deviation with a documented, verified reason. |
| N3 | nit | `tr.json`: `tables.active`/`tables.inactive` were lowercase (`"etkin"`/`"devre dışı"`) while every sibling key in the same block is sentence-case. | **Fixed** — capitalized to match. |
| N4 | nit | `tables.seats_n` (`"%d seats"`) doesn't pluralize (renders "1 seats"); a future de/es lang pack shipping the key without `%d` would render `%!d(MISSING)`. | **Not fixed** — established repo convention (7 other `printf (T ...)` call sites use the same pattern); out of scope to change repo-wide here. Noted for awareness only. |
| N5 | nit | Both commits on the branch were titled `WIP: ... — NOT DONE` / `WIP: pre-review snapshot`, not matching `main`'s conventional-commit style. | **Fixed** — reworded before merge (see commit history). |
| N6 | nit | No `docs/code-reviews/` record existed yet. | **Fixed** — this file. |

## What was actually run (this cycle, after applying the fixes above)

- `go build ./...` — clean.
- `go test ./...` — full suite green (every package `ok`, zero `FAIL` lines),
  including the extended `internal/data` archive round-trip test and the
  `internal/db` upgrade-simulation suite (`TestSeedBarcodeChecksumsFixedOnUpgrade`,
  `TestDemoCatalogueUpgrade*`, `TestMigration052*`, etc. — these initially
  broke on the S1 fix because of the `rewindTables054` non-idempotent-DDL
  fixture gap described above; fixed and re-verified green).
- `bash scripts/ci/guard-data-access.sh` — ✓ no inline SQL outside
  `internal/data`/`internal/db`.
- `bash scripts/ci/guard-i18n.sh` — ✓ all locales match `en.json` (1110 keys).
- `bash scripts/ci/guard-help-topics.sh` — ✓ no route conflicts, every
  locale complete, every page route claimed.
- `bash scripts/ci/guard-kiosk-engine.sh` — ✓ (this page is manager-only,
  not `/self-order`; confirmed no `Engine` reference regardless).
- `bash scripts/ci/guard-plugin-menu-read.sh` — ✓.
- `bash scripts/ci/guard-compliance-claims.sh` — ✓.
- `gofmt -l internal/ web/` — clean on every file this diff touches (the 9
  files it lists are pre-existing drift on `main`, tracked separately as
  ut-docs#779, not introduced here).

## Verified beyond automated tests

A real driven run against the built binary (not just `go test`), before the
independent review's fixes were applied — confirmed the underlying page/
interaction genuinely works, not just the unit tests:

- Booted the till fresh (`/api/auth/setup` first-boot flow, real PIN login
  as the seeded admin/manager), navigated to `/tables` in a real headless
  Chromium session (Playwright), with a viewport large enough that the
  floor-plan section wasn't below the fold (the first attempt used the
  default viewport and looked like a broken drag — it was a test-harness
  sizing issue, not a product bug, caught by instrumenting the actual
  pointer-event targets).
- Added a table via the form → appeared in both the HTML list and the SVG
  floor plan.
- Entered edit mode, dragged the table on the real rendered SVG (pointer
  down/move/up, not a synthetic DOM mutation) → position updated live,
  auto-saved via the `POST /api/tables/{id}/position` fetch.
- Reloaded the page → the dragged position persisted exactly, confirming
  the save round-trips through the real repository layer, not just an
  in-memory DOM state.
- Screenshotted the rendered page to confirm the SVG/legend/edit-toggle/
  form render correctly against the product's real theme tokens.

The clamp-inset fix (N1) and the new "Live status" column (S3 partial) were
verified via the updated/extended Go tests
(`TestTablePositionPersistsAndClamps`, `TestTablesPage_CreateEditPositionDeactivateAndRender`)
rather than repeating the full browser run — neither change altered the
drag interaction mechanics, only a numeric bound and a template addition
already covered by handler-level tests.

## Safe-to-merge verdict

Safe to merge. Every should-fix finding is either fixed or explicitly
deferred with a linked, labelled follow-up card (ut-docs#826); every nit is
either fixed or recorded with a stated reason for leaving it. Full gate
(build, tests, all six guards) is green.

## Explicitly deferred

- **ut-docs#826** — keyboard-accessible table repositioning (S3 remainder).
- **ut-docs#820** — table-to-order assignment (already tracked, this card's
  own explicit out-of-scope split per ADR-0054; the `held_sales.table_id`
  join this card built is the forward-compat seam #820 wires into).
- **N4** (seat-count pluralization) — noted, not actioned; matches existing
  repo-wide convention.

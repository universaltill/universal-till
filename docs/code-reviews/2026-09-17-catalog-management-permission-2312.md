# Code review: catalog_management permission action (ut-docs#2312)

**Date:** 2026-09-17
**Card:** ut-docs#2312 (security, p2, complexity:medium)
**Author (Dev):** Sonnet subagent, orchestrated by lane:cloud-24
**Reviewer:** Opus subagent, isolated worktree, fresh context (never saw the Dev's reasoning)

## What shipped

`/designer`, `/items`, `POST /api/buttons/{add,remove,reorder,move}`, `GET
/ui/pos/tile-sheet`, and every mutating `/api/catalog/*` route were open to
any signed-in operator — a cashier could reorder/remove quick buttons and
edit/deactivate catalog items with no gate at all. This shipped a
`catalog_management` permission action (seeded granted for
manager/admin/super_admin, not cashier — same shape as
`tax_code_management`/`stock_location_management`) and gated every route
above:

- **Buttons routes** (`internal/pages/buttons_api.go`): `checkOrElevate(d,
  r, "catalog_management", …)` on add/remove/reorder/move, with a real
  PIN-elevation prompt on each.
- **Catalog routes** (`internal/pages/catalog/handlers.go`): a new
  `requireCatalogManagement` gate (plain `canPerform`+403, not
  `checkOrElevate` — see "Accepted deviation" below) on all 25 mutating
  routes.
- **Tile sheet** (`web/ui/partials/tile_sheet.html`,
  `internal/ui/buttons.go`): renders Move/Remove/Edit as locked
  (lock icon, muted style) rather than hidden for a non-granted operator —
  "show, don't hide" per #2285's UX decision — and crucially **not**
  `disabled`: a tap still reaches the server and lands on the real
  elevation prompt.
- **Nav**: `VisibleIf: "catalog_management"` on the `/designer` and
  `/items` menu entries (`internal/uislot/slot.go`).
- **Migration**: `internal/db/migrations/001_init.sql` seeds the new
  action for manager/admin/super_admin; append-only, no existing grant
  altered (confirmed pre-first-paying-shop regime under ADR-0074).
- **i18n**: 6 new keys × 4 locales (en/tr/ar/fa), hand-translated.
- **Help docs**: `web/help/en/catalog.md`, `web/help/en/till-designer.md`
  updated; screenshots regenerated (`make docs-shots`).
- **Tests**: Go gate tests per route (cashier denied/manager passes) for
  both the buttons and catalog surfaces, a migration-seed test, and a
  Playwright e2e spec (`tile-sheet-locked-cashier-2312.spec.ts`) driving
  the full real-browser flow — cashier session, long-press, locked tile,
  tap, real elevation modal, manager PIN, mutation applied.

## Independent review — what was actually run, not just read

A fresh-context Opus subagent, isolated in its own git worktree (never
shared the orchestrator's or the Dev's working tree), reviewed the
snapshot as a WIP commit and actually executed the gate rather than
reading the diff:

- `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test ./...`
  (`internal/pages` 156.8s, `internal/pages/catalog` 2.9s, `internal/db`
  18.3s — all `ok`), `golangci-lint run ./...` (0 issues),
  `guard-data-access.sh`, `guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-help-drift.sh`, `guard-docs-shots.sh`, `guard-compliance-claims.sh`,
  `guard-page-http-error.sh`, `guard-kiosk-engine.sh`, `guard-htmx-loaded.sh`,
  `guard-e2e-fixtures-import.sh` — all green.
- **Gate-completeness enumeration** (the most important check for a
  security card): every one of the 34 `mux.HandleFunc` registrations in
  `internal/pages/catalog/handlers.go` was mapped to a gate call site —
  24 gate calls cover all 25 mutating routes (opt-out/opt-in share one
  gate inside a shared closure); the 9 uncovered registrations are all
  read-only GETs. A repo-wide sweep found two more mutating
  `/api/catalog/*` routes outside that file
  (`/api/catalog/tax-codes*`, `/api/catalog/export-save`) — both already
  gated by pre-existing, unrelated permission actions
  (`tax_code_management`, `import_export`). All four buttons routes
  confirmed gated. `VisibleIf` confirmed wired on both `/designer` and
  `/items`. **Verdict: gate coverage against the acceptance criteria is
  complete.**
- **Real revert-then-restore TDD verification**, not taken on the Dev's
  word, on three separate slices:
  1. Catalog gate reverted → all 25 routes' gate tests failed on-point;
     most tellingly, `cashier POST /api/catalog/barcode-backfill` returned
     `200 Assigned 0 barcode(s).` — the mutation actually executed for an
     ungated cashier pre-fix. Restored → green.
  2. Buttons/tile-sheet gate reverted → cashier reorder/add/remove/move
     all succeeded (204/200) instead of being gated; the tile-sheet
     lock test failed. Restored → green.
  3. Migration seed reverted → the seed test failed with the exact
     expected-vs-actual mismatch. Restored → green.
  Working tree confirmed byte-identical to the snapshot afterward
  (`git status --porcelain` empty).

## Findings — fixed before this commit

1. **Blocker-class (user-visible on the security dialog itself):**
   `buttons_api.go`'s Move handler passed
   `elevation.summary.buttons_move` (`"Move quick button %s next to its
   neighbour."`) to `renderElevationPrompt` unformatted — `httpx.T` takes
   no variadic args, so the manager-PIN approval dialog literally showed
   `%s` instead of the button's code, on every locale. `buttons_add` and
   `buttons_remove` both wrapped the identical pattern in `fmt.Sprintf`
   correctly; only `move` was missed. Fixed: wrapped in `fmt.Sprintf(...,
   code)`. Re-verified live via the `auth`-project e2e spec, which now
   drives this exact modal.
2. **Should-fix:** a stray `*/}}` at `tile_sheet.html:24` closed the
   file's opening `{{/* ... */}}` doc comment early, dropping ~900 bytes
   of subsequent developer prose out of the comment and into literal
   template output. Not user-visible today (the template is always
   rendered via the named `{{ define "tile_sheet" }}`, never its root
   body), but wrong and a trap for a future edit. Fixed: removed the
   stray closer, the whole header is one comment block again.
3. **Should-fix:** `web/help/en/till-designer.md` claimed a cashier
   editing an item from the tile sheet gets "the same manager approval"
   (i.e. an on-the-spot PIN prompt) — inaccurate, since the catalog
   mutation routes return a hard 403 with no elevation path (see the
   accepted deviation below). Reworded to "a cashier can look but can't
   save — a manager or admin has to make the change."
4. **Nit:** the new e2e spec's CSS selector fix and honesty-note comment
   were stale/misleading after the spec was actually run (see below) —
   updated to reflect what was verified, and documented why the spec is
   correctly *not* added to `guard-e2e-fixtures-import.sh`'s exempt list
   despite sharing `AUTH_ONLY_SPECS` membership with five specs that are
   exempt (it imports `test`/`expect` from `./fixtures` correctly, and
   `resetPosOncePerFile`'s session-less first request here redirects
   rather than throws — verified by running it, not inferred).
5. **Nit:** `app.css`'s `.locked` rule used the over-broad `a.btn.locked`
   selector (every `<a class="btn ... locked">` on the page, not just the
   tile sheet's Edit link). Narrowed to `.tile-sheet-edit.locked` and
   added that class to the Edit anchor in `tile_sheet.html` (it only
   carried a `data-testid` before, no class hook existed).
6. **Nit:** `fa.json`'s `tile_sheet.locked` used the noun **قفل** ("lock")
   where `tr`/`ar` use adjectives; corrected to the idiomatic **قفل‌شده**
   ("locked").

## Accepted, not fixed — deviation from `checkOrElevate` on catalog routes

`internal/pages/catalog/handlers.go` gates with plain `canPerform`+403,
not `checkOrElevate`'s inline PIN-elevation flow the buttons/tile-sheet
routes use. Verified real, not assumed: `internal/pages/init.go` imports
`internal/pages/catalog` to mount its routes, and `checkOrElevate`/
`renderElevationPrompt`/`canPerform` are all unexported in `internal/pages`
— the reverse import needed for the elevation flow is a genuine cycle.
This is also the established precedent the card's own acceptance
criteria point at: both named sibling actions
(`tax_code_management` in `tax_codes_page.go`, `stock_location_management`
in `locations_page.go`) use the identical plain-403 shape for exactly this
reason. Documented as an accepted scope boundary, not a defect — the
acceptance criterion's "403/elevation prompt" wording is satisfied by the
403 half for this surface.

## Follow-up cards filed (out of scope for this card, not blocking)

- Page-level GET routes (`/designer`, `/items`, `/catalog`, `/modifiers`,
  `/catalog/option-sets`) are hidden via `VisibleIf` but not
  server-gated — a cashier who types the URL directly still gets the
  page (every mutation on it is still gated, so this is information
  disclosure only, not a write bypass). Both named precedents
  (`tax_codes_page.go`, `locations_page.go`) *do* gate their own GET page.
- `auditButtonsElevated` only covers the buttons `reorder`/`move` paths;
  `add`/`remove` are un-audited because `ui.ButtonsHTTP.Add`/`Remove` are
  handler-shaped with no return value to hook into. A PIN-approved
  add/remove currently writes no audit row.

## Verified beyond automated tests

- Manually confirmed no real client/shop name in any test/seed data
  (`Task Runner`-style synthetic names only:
  `Sheet2312 Item <run>`, `Cashier 2312`).
- No secret-shaped literal in the diff (`CASHIER_PIN` is a synthetic
  6-digit test fixture matching the existing `ADMIN_PIN` convention).
- Full `auth`-project e2e suite (27/27) and a 137-test regression subset
  of the `default`-project suite covering every designer/catalog/buttons/
  items/menu/tile-touching spec — both run for real in this sandbox
  (pre-installed Chromium + `npm ci`'d `e2e/node_modules`), both green,
  both before and after the review-driven fixes above.
- `make docs-shots` run for real (not skipped as "no toolchain available",
  unlike the Dev's own sandbox) — 124/124 screenshots regenerated and
  `guard-docs-shots.sh` green.

## Safe to merge

Yes, on the code itself — every finding above is fixed or accepted with
reasoning. **Not merged in this cycle regardless**: ut-docs#2277 (P1,
Admin Review, open since 2026-09-16 08:48) asks the product owner to
reconfirm whether the standing auto-push/auto-merge authorization still
holds given evidence of a live production till fleet. Per that card's own
reasoning and the precedent set by several other PRs today
(universal-till#1189, #1200, #1201, #1209; ut-cloud#152), this PR is
pushed and open for CI but deliberately held unmerged pending a human
answer on #2277.

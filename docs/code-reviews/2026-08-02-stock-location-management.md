# Code review: Stock-location management (create/rename/deactivate)

**Date:** 2026-08-02
**Scope:** `internal/db/migrations/029_stock_location_active.sql`,
`internal/data/pos_repo.go`, `internal/data/pos_repo_stock_location_test.go`,
`internal/pages/locations_page.go`, `internal/pages/locations_page_test.go`,
`internal/pages/init.go`, `internal/pages/menu_page.go`,
`internal/pages/menu_page_test.go`, `internal/pages/inventory_page.go`,
`internal/pages/ui_smoke_test.go`, `internal/db/barcode_seed_test.go`,
`internal/db/dead_seed_test.go`, `web/ui/pages/locations.html`,
`web/locales/{en,ar,fa,tr}.json`.
**Trigger:** universaltill/ut-docs#49 (BA scoping found the multi-location
inventory *data layer* — `stock_locations`, per-location inventory
aggregate/movements/low-stock — already existed; the only real gap was
that nothing could ever create a second location besides the hardcoded
"Main" one).

## What shipped

Back-office CRUD for `stock_locations`, manager/admin-gated, mirroring
`internal/pages/users_page.go`'s `requireManager` + soft-disable-with-guard
pattern:

- Migration 029 adds `is_active` to `stock_locations` (028 was latest by
  the time this landed on `main` — a sibling PR claimed 028 for an
  unrelated `items.lead_time_days` column first; renumbered post-review
  during the stale-PR merge sweep, append-only respected throughout).
- `POSRepo`: `CreateStockLocation`, `RenameStockLocation`,
  `SetStockLocationActive`, `StockLocationInUse` (refuses deactivating a
  location referenced by `inventory`/`stock_movements`/`registers`),
  `ListActiveStockLocations`, `CountActiveStockLocations`,
  `ListStockLocationsForAdmin`. `ListStockLocations` itself is untouched —
  still unfiltered, for whichever future callers may want every location
  regardless of state.
- `internal/pages/locations_page.go`: `GET /locations`,
  `POST /api/locations`, `POST /api/locations/{id}`,
  `POST /api/locations/{id}/active`.
- Menu tile (`/menu`, manager-only section, reuses `locations.title` as
  its label the same way `/users` reuses `users.title`).
- i18n keys in all four locales.

## New tests

7 repository tests (`pos_repo_stock_location_test.go`), 5 HTTP-layer tests
(`locations_page_test.go`), plus one extended existing test
(`TestMenuPage_ManagerOnlyTilesGatedByRole`).

## Verification (self, before independent review)

- `go build ./... && go vet ./...`, `gofmt -l .`: clean (new/changed Go
  files).
- `go test ./...`: green except the pre-existing, already-filed
  `TestSaveCleansUpDirectoryOnWriteFailure` (ut-docs#258, fails under a
  root-run sandbox) — confirmed unrelated via `git stash` against a clean
  `main`.
- `guard-data-access.sh`, `guard-i18n.sh`: green.
- Mutation-tested personally: forced `StockLocationInUse` to always
  return `false`, forced `requireManager` to always pass — both broke the
  tests meant to catch them, both reverted and confirmed green again.
- Drove the real running binary end to end (built + ran from a temp data
  dir, real first-boot setup, real session cookie, via curl): created a
  location, confirmed it appeared in the existing inventory picker,
  renamed it, deactivated the empty one (succeeded), attempted to
  deactivate `loc_main` which has real seeded inventory (refused with the
  correct message), confirmed final state via the DB-backed page render.

## Independent review

Different-model subagent (Opus), full independent re-verification (own
build/vet/test/guard run, plus two of its own from-scratch mutations).
Findings:

- **Real, fixed (blocking):**
  - **The page had no navigation entry.** `/menu`'s manager-only tile
    list (`internal/pages/menu_page.go`) and `iconFor` had no
    `/locations` entry — a manager could only reach the feature by typing
    the URL directly. Fixed: added the tile (reusing the existing
    `locations.title` key, exactly as `/users` reuses `users.title`, so
    no new locale key was needed) and a `📍` icon. Extended the existing
    `TestMenuPage_ManagerOnlyTilesGatedByRole` test to cover it; confirmed
    it fails without the fix (real assertion failure, not a compile
    error) and passes with it.
  - **Deactivation was write-only — cosmetic.** `ListStockLocations`
    (deliberately left unfiltered) was the *only* production consumer of
    the location list, used by the inventory page's stock-adjust/receive/
    return picker (`internal/pages/inventory_page.go`). A "deactivated"
    location kept appearing in that picker and could still receive stock
    — the feature's stated purpose (stop a location from being usable)
    had no observable effect anywhere. Fixed: added
    `ListActiveStockLocations` and switched the inventory picker to use
    it (leaving `ListStockLocations` itself untouched, since it has no
    other callers to break). New test
    `TestLocationsPage_DeactivatedLocationHiddenFromInventoryPicker`
    proves a location disappears from the real `/inventory` page's
    picker once deactivated; confirmed it fails against the pre-fix code
    (reverted the picker call locally, re-ran, got the exact expected
    failure) and passes with the fix.
  - **The `stock_movements` branch of `StockLocationInUse` was completely
    untested.** The existing tests covered `inventory` (via seeded
    `loc_main`) and `registers` (explicit insert) but never inserted a
    `stock_movements` row — independent review mutated that branch to
    never match (a real query bug that would produce a false negative,
    silently allowing a location with real movement history to be
    deactivated and orphaned) and the full test suite stayed green. Fixed:
    added a third case inserting a `stock_movements` row against a fresh
    location and asserting `StockLocationInUse` reports it. Reproduced
    the reviewer's exact mutation locally, confirmed the new assertion
    fails with it, reverted, confirmed green.
- **Real, fixed (should-fix, not originally flagged as blocking but
  upgraded once the picker fix above made it a live risk rather than a
  theoretical one):**
  - **No "last active location" guard.** Once deactivation actually does
    something (per the fix above), nothing stopped a shop from
    deactivating every location, leaving the stock-adjust picker empty.
    Fixed: added `CountActiveStockLocations` and a guard in the
    deactivate handler mirroring `users_page.go`'s last-active-admin
    check (`locations.error.last_location`, new key in all four
    locales). New test
    `TestLocationsPage_CannotDeactivateLastActiveLocation`; mutation-
    reverted the guard locally, confirmed the test fails, restored,
    confirmed green.
- **Real, fixed (minor, cheap enough to just close rather than defer):**
  - No whitespace-only name rejection — `strings.TrimSpace` added to both
    create and rename before the empty check, with a new test
    (`TestLocationsPageCreate_WhitespaceOnlyNameRejected`).
- **Real, documented rather than fixed (deliberately deferred — the
  independent reviewer's own triage marked these non-blocking, and fixing
  them risked introducing a *less* accurate error message under time
  pressure rather than a real correctness fix):**
  - Rename-to-a-duplicate-name reuses the generic
    `locations.error.rename` message rather than hinting at the
    collision the way the create path does — `RenameStockLocation`
    doesn't currently distinguish "not found" from "UNIQUE violation" at
    the repository layer, and inventing that distinction under this
    review's time budget risked shipping a wrong message for the "not
    found" case rather than a real fix. Left as-is; a follow-up would add
    typed/sentinel error classification to `RenameStockLocation` first.
  - `{{ T .errKey }}`'s fallback (`internal/httpx.T`) prints an unknown
    key verbatim, so `/locations?err=whatever` reflects arbitrary
    attacker-chosen text on the page. `html/template` escapes it in that
    text-node context, so there's no XSS — reflected-text noise only, and
    this is pre-existing, identical behavior on `/users` today, not new
    to this diff.
  - Locale keys were inserted mid-file rather than at a globally-sorted
    position — cosmetic only, `en.json` isn't globally alphabetically
    sorted to begin with, and the i18n guard (key-set parity) passes.
- **Checked and confirmed clean by independent review:** manager gate
  present on all four routes (specifically looked for a missing one —
  none found); `StockLocationInUse`'s three `EXISTS` clauses are the
  complete, correct set of FKs to `stock_locations` (verified by grepping
  every migration for `location_id`); no SQL injection (every new method
  read individually, all parameterized); migration 029's `DEFAULT 1`
  correctly backfills existing rows; the two recurring bug classes this
  pipeline watches for (missing `os.MkdirAll`, cwd-relative path instead
  of `paths.Data`) don't apply — this change does no file I/O at all; no
  real client/shop name used anywhere in test data.

## Verdict

**Safe to merge after fixes.** Independent review found three real
blocking issues — none of them "wrong code" in the sense of a logic bug
in what was written, but a feature that didn't actually do what it
claimed: unreachable from the UI, deactivation with no observable effect,
and a completely unguarded code path masked by an incomplete test. All
three fixed in this same pass, each with its own new regression test that
was mutation-tested against the reviewer's own reproduction before being
trusted. One additional guard (last-active-location) was added
proactively once the picker fix made its absence a live risk rather than
a hypothetical one. Two minor findings were deliberately left as
documented, low-value follow-ups rather than risking an inaccurate fix
under time pressure. Full gate (build/vet/test/guards) green after every
fix.

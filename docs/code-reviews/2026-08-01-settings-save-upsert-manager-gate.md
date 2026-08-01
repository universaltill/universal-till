# 2026-08-01 — Settings save/upsert manager gate (ut-docs#179)

## What shipped

`POST /api/settings/save` and `POST /api/settings/upsert` in
`internal/pages/settings_page.go` had no role check — any signed-in
cashier could change the store currency/country/tax rate, or write an
arbitrary settings key/value pair (including `store.tax_inclusive` /
`pos.allow_negative_inventory`), unlike every sibling settings-mutating
endpoint in the file. The raw key/value settings table in
`web/ui/pages/settings.html` also rendered outside the `.isManager`
conditional, so a cashier could see and use it in the UI.

Fix:

- Both handlers now start with the same `isManagerOrAuthOff` gate used
  by `/api/settings/printer` (`internal/pages/print_api.go`), refusing a
  non-manager with `403 manager or admin required`.
- Both routes are now registered `POST /api/settings/...` (were bare
  `/api/settings/...`), matching every sibling route in the file — closes
  a GET-mutates gap the independent review found empirically (a manager
  loading a hostile `<img src=".../save?currency=XXX">` could mutate
  settings; there is no CSRF middleware in `internal/`).
- `web/ui/pages/settings.html`: the currency card and the raw key/value
  table are now both inside one `{{ if .isManager }}` block (previously
  neither was gated in the template).

## Independent review

Spawned via `Agent` (opus, different model from the one implementing the
fix), briefed with the exact diff scope, `CLAUDE.md` rules, and the
`print_api.go` precedent this change mirrors.

**Findings, triaged:**

1. **should-fix, fixed** — the currency card posts to the now-gated
   `/save` but rendered unconditionally, so a cashier saw a dropdown +
   Apply button that always failed with a generic error — a UX
   regression introduced by this change (not a pre-existing gap the
   issue declined to fix). Wrapped in `.isManager` alongside the table.
2. **should-fix, fixed** — no test asserted the template half of the fix
   (a cashier's rendered `/settings` HTML actually omits these cards).
   Added `TestSettingsPage_HidesManagerOnlyCardsFromCashier`.
3. **nit, fixed** — `/save` and `/upsert` were method-unrestricted
   (bare path, not `POST /api/settings/...`), so a GET request reached
   the handler and mutated state; verified empirically by the reviewer
   (`GET /api/settings/save?currency=ZZZ` as a manager returned 204 and
   changed the currency). One-word fix, matches every sibling route's
   existing convention in this exact file.
4. **nit, fixed** — the manager-gate test used a plain loop with
   `t.Fatalf`, so a regression on the first case (`no session`) would
   mask the second (`cashier`). Converted to `t.Run` subtests.

No blocking findings. No repository-pattern, money, i18n, RTL, or
offline-first rule violations — this change is settings-only and does
not touch checkout.

## Verified beyond automated tests

- **TDD claim re-verified independently**: with both `isManagerOrAuthOff`
  guard blocks removed, `TestSaveAndUpsertSettings_RequireManager` fails
  (`no session: save = 204, want 403`); restored, it passes. Not a
  false-pass test.
- **Template nesting verified by parsing the full file** (if/range/with
  vs `end`): balanced, the new `{{ if .isManager }}` closes the correct
  block, the preceding pre-existing `.isManager` block is untouched.
- **Real driven run** against a live server (fresh DB, real first-boot
  setup wizard, a real cashier user created and PIN-logged-in through
  the actual HTTP endpoints, not mocked): a cashier gets HTTP 403 from
  both endpoints and the rendered `/settings` HTML contains neither the
  currency card nor the raw table; a manager still gets 204 and sees
  both.
- **Pre-existing-failure claim re-verified independently**: the sole
  full-suite failure, `TestSaveCleansUpDirectoryOnWriteFailure`
  (`internal/issuereport`), was confirmed via `git stash` to fail
  identically on a clean base — a sandbox/root-permissions artifact
  (tests run as uid 0, so a `0o500` read-only-dir test never actually
  hits a permission error), unrelated to this diff.
- `go build ./...`, `go vet ./...`, `scripts/ci/guard-data-access.sh`,
  `scripts/ci/guard-i18n.sh` all clean.

## Safe to merge

Yes. No blocking findings; all should-fix/nit findings from the
independent review were applied and re-verified.

## Explicitly deferred

Nothing from this task. The broader question of CSRF protection across
`internal/` (there is none anywhere in the package, not specific to
these two endpoints) is out of scope for this issue and not filed as a
new card here — flagging for awareness only, since the method-restriction
fix above narrows but does not eliminate the underlying gap for routes
that remain GET-reachable elsewhere.

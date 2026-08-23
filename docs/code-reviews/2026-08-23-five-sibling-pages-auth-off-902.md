# Code review — ut-docs#902: five sibling pages permanently 403 under `UT_AUTH=off`

**Date:** 2026-08-23
**Card:** [ut-docs#902](https://github.com/universaltill/ut-docs/issues/902)
**Complexity:** medium
**Build model:** Sonnet (inline) — **Review model:** Opus, independent worktree-isolated subagent

## What shipped

`internal/pages/country_settings_page.go`, `kitchen_stations_page.go`,
`promotions_page.go`, `tables_page.go` and `translations_page.go` all gated
their routes with a page-local `requireManager` closure built directly on
`auth.FromContext(r.Context()).IsManager()` — the same bug ut-docs#901
(PR #451) fixed for `locations_page.go`/`registers_page.go`: `internal/pages/
init.go` skips `auth.Middleware` entirely when `UT_AUTH=off` (the dev/CI
escape hatch), so `auth.FromContext` never has a value set in that mode,
and these five pages' `requireManager` failed closed **permanently**.
Found and filed by #901's own independent review.

Fix: migrated all five onto `canPerform(d, r, "settings")` — the exact
action `menu_page.go`'s nav-tile gate already uses for every one of these
admin destinations (users/locations/registers/kitchen-stations/tables/
country-settings/translations). `canPerform` has the `auth.Disabled(...)`
bypass the raw check never had. No new migration, no new permission
action — `"settings"` (039's catalog) is granted to exactly
manager/admin/super_admin, the same set `IsManager()` recognized, so
production (`UT_AUTH` on/unset) behavior is unchanged.

Each test file's mux constructor now wires `AuthSvc: auth.NewService(db)`
into `common.Deps` — required, not incidental: `canPerform` calls
`d.AuthSvc.Can(...)` on any non-`UT_AUTH=off` request, and the existing
session-based `Test*PagePermissions` tests would nil-pointer-panic without
it (`AuthSvc` was nil in every fixture before this change).

Added 10 regression tests (a GET- and a mutating-POST-reachable-under-
`UT_AUTH=off`-with-no-session test per page):
`TestCountrySettingsPage_ReachableUnderAuthOff` /
`TestCountrySettingsPageCreate_ReachableUnderAuthOff`,
`TestKitchenStationsPage_ReachableUnderAuthOff` /
`TestKitchenStationsPageCreate_ReachableUnderAuthOff`,
`TestPromotionsPage_ReachableUnderAuthOff` /
`TestPromotionsPageCreate_ReachableUnderAuthOff`,
`TestTablesPage_ReachableUnderAuthOff` /
`TestTablesPageCreate_ReachableUnderAuthOff`,
`TestTranslationsPage_ReachableUnderAuthOff` /
`TestTranslationsPageSet_ReachableUnderAuthOff`.

`e2e/playwright.config.ts`: `AUTH_ONLY_SPECS` narrowed from
`/(login|tables-keyboard-reposition-826)\.spec\.ts$/` to `/login\.spec\.ts$/`
— `/tables` no longer needs the auth till, so
`tables-keyboard-reposition-826.spec.ts` moves onto the `default`
(auth-off) project (its `test.use({ baseURL: … })` override removed, header
comment updated). Added `e2e/tests/admin-pages-auth-off-902.spec.ts`: a
minimal reachability smoke spec (GET renders, no console errors) for
`/country-settings`, `/kitchen-stations`, `/promotions` and `/translations`
— `/locations`/`/registers` already have real e2e coverage from #901 and
`/tables` from `tables-keyboard-reposition-826.spec.ts`, so this card's own
non-goals didn't require a full CRUD walk like #901's spec, just minimal
reachability for the remaining four.

## What the independent review found

Spawned an Opus subagent (`general-purpose`), isolated in its own git
worktree, given the diff and told to try to break it, not confirm it's
fine.

**Initial verdict: one BLOCKER, since fixed.** `scripts/ci/guard-docs-shots.sh`
failed on this branch: all five changed `.go` files are each their help
topic's `routes[0]` (`web/help/en/{country-settings,kitchen-stations,
promotions,tables,translations}.md`), so they're in the guard's screenshot-
surface fileset — editing them changed `surface_sha256` in `web/help/img/
manifest.json` without a matching `make docs-shots` run. This is exactly
why #901 didn't hit it: there's no `web/help/en/locations.md`/`registers.md`,
so those two pages fall into the guard's excluded bucket — the precedent
genuinely didn't cover this case. Fixed by running `make docs-shots` (reused
the pre-installed Chromium via `e2e/scripts/resolve-chromium.sh`,
ut-docs#622) and committing the regenerated `web/help/img/**`. The five
fixed pages' own screenshots came back pixel-identical (the fix only
changes auth gating under `UT_AUTH=off` in dev/CI, not page content);
`alerts.png`/`designer.png` changed only in a live "RECENT PROBLEMS" log
timestamp baked into the page at capture time — unrelated to this change,
expected to shift on any docs-shots run. `guard-docs-shots.sh` now passes.

Independently re-verified the TDD claim (its own worktree, not mine):
stashed only the 5 production `.go` files (test files left in place) —
confirmed all 10 new regression tests fail red with 403 against the
pre-fix code, then restored and confirmed all pass.

Confirmed the `"settings"` action choice is genuinely correct, not
copy-pasted: cross-checked the full `role_permissions` catalog (039,
042–047, 057) — no narrower action fits any of the five pages;
`menu_page.go:66` already gates the kitchen-stations/tables/
country-settings/translations tiles on exactly `canPerform(d, r,
"settings")`; promotions has no tile but its only nav entry groups it with
`/users` and `/translations` in `session_chip.html`, so `"settings"` is
right there too. Traced every use of `requireManager`'s returned user
across all five files (only ever `actor.ID` fed to `InsertAudit`/
`SetOverride`) and confirmed zero production behavior change; verified live
that the auth-off audit row is still written with `actor_id = NULL` (no FK
violation), same as #901.

Verified the new mutating-POST tests are not vacuous: every handler's
failure paths redirect to `/<page>?err=…`, and the tests assert `Location`
equals the bare success path, so any non-success path fails the test.
Cross-checked every form field against the real handler logic (promotions'
`type: amount` + `value_amount`, translations' `edit_locale`+`key`+`value`,
tables' `shape: rect`, kitchen-stations' `name`+`printer_address`,
country-settings' `code`+parseable rate/retention) — all correct.

Confirmed the existing session-based `*_Permissions` tests still assert
the real thing post-`AuthSvc` wiring (each file's manager-success
assertions — 200/303/body content — would have caught a fixture missing
the 039 catalog tables; all green). Confirmed the `.kitchen-stations`
spec's `.card.users-list` assertion (used instead of `table.table`) is
real, not plausible: the template renders a "none configured" message
instead of a table when the shop has zero stations, and the seeded e2e
till has none — verified live (`0× table.table`, `1× .card.users-list`).

Ran `go test -race -timeout 30m ./internal/pages/...` independently
(pages 829s, catalog 5.2s, common 47.3s) — clean, no data races. Could not
run the Playwright browser suite itself (chromium version mismatch in its
worktree) but substituted live-server curl verification covering the same
reachability claims: all five previously-403 routes now 200, with the
exact `<h1>`/`table.table` shape each spec asserts.

Two non-blocking nits, fixed here (comment-only, no behavior change):

1. `internal/auth/auth_test.go`'s `TestUser_IsManager_IncludesSuperAdmin`
   doc comment named these five pages (plus locations/registers) as
   gating on `IsManager()` — none do any more. Recorded why the test case
   still matters: it pins the manager/admin/super_admin set `"settings"`
   must keep mirroring, and `auth_page.go`'s session chip still reads
   `IsManager()`.
2. `e2e/tests-docs/docs-shots.spec.ts`'s `AUTH_TILL_TOPICS` header comment
   asserted "no `UT_AUTH=off` bypass" for pages that now have one.
   Recorded why the list itself is deliberately left unchanged: the auth
   till is a fresh wizard-seeded install, while the default till is shared
   with `tables-keyboard-reposition-826.spec.ts`, which leaves its own
   `E2E Kbd …` rows behind — moving these topics off the auth till would
   leak e2e junk into the shipped manual screenshots.

Observations, no action needed:

- `/promotions` is reachable under `UT_AUTH=off` but has no nav path
  there (no menu tile; `session_chip.html` renders empty with no
  session) — pre-existing, out of this card's scope.
- `tables-keyboard-reposition-826.spec.ts` moving onto the shared default
  till is safe today (only that spec and the new admin-pages spec
  reference `/tables`, and the new one doesn't drive it) — worth
  remembering if a future spec starts asserting table counts there.
- No i18n/help-topic/README obligations: no new locale keys, no
  user-visible change (only dev/CI `UT_AUTH=off` behavior changes), and
  all five pages already have `web/help/en/*.md` topics claiming their
  routes.

## What was verified beyond automated tests

- `gofmt -l .` — clean. `go build ./...` — clean.
- `go test $(go list ./... | grep -v '/internal/plugins$')` — full suite,
  zero failures.
- `go test -race -timeout 30m ./internal/pages/...` — clean (both my own
  run and the reviewer's independent one).
- Guards: `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-i18n.sh`,
  `guard-compliance-claims.sh`, `guard-help-topics.sh`,
  `guard-docs-shots.sh` (after `make docs-shots`) — all pass.
- TDD revert-verify at the Go layer, independently, twice (implementer and
  reviewer, separate worktrees): all 10 new tests fail red pre-fix, pass
  post-fix.
- Real Playwright run (pre-installed Chromium) of the full `default`
  project (33 spec files, 150 passed) plus the `auth` project (now just
  `login.spec.ts`, 7 passed) against a real `run-till.sh`/`run-till-auth.sh`
  till. One unrelated pre-existing failure
  (`catalog-image-to-till.spec.ts`, an image-load timing assertion —
  reproduced identically on retry, touches nothing this diff changes).
- `make docs-shots` real run (84 screenshots, reused pre-installed
  Chromium per ut-docs#622) confirmed the five fixed pages render
  pixel-identical to before the fix.
- No real client/shop name or secret-shaped literal anywhere in the diff.

## Verdict

**Safe to merge.** No blocking findings remain (the `guard-docs-shots.sh`
blocker found mid-review is fixed and re-verified green). Both acceptance
criteria met: all five pages' GET routes succeed under `UT_AUTH=off` with
no session; production (`UT_AUTH` on/unset) behavior is unchanged;
`tables-keyboard-reposition-826.spec.ts` moved off the `auth` Playwright
project onto `default`, and a minimal e2e spec exists for the other four
pages.

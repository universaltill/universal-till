# Code review: setup wizard never provisioned a default register (ut-docs#429)

**Date:** 2026-08-07
**Author (Dev):** scrum-master pipeline, Sonnet (complexity:easy)
**Reviewer:** independent fresh-context Sonnet subagent
**Card:** universaltill/ut-docs#429

## What shipped

A genuinely fresh till, driven through the real first-boot setup wizard
(language → country → shop name → admin PIN → finish) with no other
setup, could not open its first shift: submitting the Open Shift form on
`/shifts` sent `register_id=reg-default` (the value
`web/ui/pages/shifts.html`'s register `<select>` falls back to when
`POSRepo.ListRegisters` returns none) to `POST /api/shifts/open`, and the
`INSERT` failed with `FOREIGN KEY constraint failed (787)` because no row
for that id — or any register at all — had ever been created. First boot
provisioned an admin user and PIN as real usable state, but never a
register.

- `internal/pages/setup_page.go` (`POST /api/setup`, the guided wizard)
  and `internal/pages/auth_page.go` (`POST /api/auth/setup`, the bare
  fallback that `TestFirstBootSetupThenLogin` covers) both now call the
  existing `POSRepo.EnsureRegister(ctx)` before completing first boot.
  `EnsureRegister` already existed and was already the established
  "self-heal a missing register" pattern used identically in
  `pos_api.go` (POS sale checkout) and `self_order_shop.go` (self-order
  checkout) — no new SQL, so `internal/`'s repository-pattern rule
  (raw SQL only in `internal/data`/`internal/db`) is untouched by
  construction, confirmed by `guard-data-access.sh`.
- On error, both call sites fail the request the same way the handler's
  other provisioning steps do (`http.Error(..., 500)`) — first boot must
  never silently complete without a working register.
- Test harnesses (`newFullAuthDeps` in `setup_page_test.go`,
  `newAuthTestMux` in `auth_page_test.go`) gained a `registers` table
  matching the production schema (`internal/db/migrations/001_init.sql`).
- New Go regression coverage: `TestSetupWizardCreatesDefaultRegister`
  (asserts `ListRegisters` returns exactly one row after the wizard
  completes) and an extension to `TestFirstBootSetupThenLogin` for the
  bare-fallback path.
- New e2e coverage: `e2e/tests/login.spec.ts` gained "a fresh till can
  open its first shift right after the wizard" — drives a real browser
  through the real wizard on a genuinely fresh till
  (`run-till-auth.sh`), then submits the real `#open-shift-form` and
  asserts no `500`/`FOREIGN KEY` in `#shift-result`, and that the page
  actually shows "Shift open since" after the form's own
  `hx-on::after-request` reload — only reachable on a genuine 2xx.

## Verified beyond automated tests

- **Real browser repro, both directions.** Ran the new e2e test against
  the code with the two `EnsureRegister` calls reverted: it failed with
  the exact reported error, `FOREIGN KEY constraint failed (787)`, and a
  failure screenshot shows the real Shifts page with that message printed
  under the Open Shift button. Restored the fix, reran — all 7 tests in
  `login.spec.ts` (including the two pre-existing tests around it) pass.
- Confirmed the two new Go tests fail pre-fix (`ListRegisters` returns
  `[]`) and pass post-fix.
- Killed the Playwright-launched dev servers and cleaned
  `e2e/test-results`/`playwright-report` after driving them; no stray
  processes on 8091/8092 afterward. `playwright.config.ts` carries no
  leftover changes from getting a local Chromium executable path resolved
  for this sandbox — reverted after use, confirmed via `git diff`.

## Independent review — findings

**PASS, no blockers.** The reviewer re-derived the bug chain independently
(FK from `shifts.register_id` to `registers.id`, the template's fallback
option), checked the third first-boot path (`POST /api/setup/join`,
replica pairing) and confirmed it's correctly out of scope — a joining
replica inherits `registers` rows from the primary's snapshot restore, it
doesn't need its own default. Re-verified the TDD claim personally
(stripped the fix, reran the two Go tests, watched them fail with
`ListRegisters ... = []`, restored, confirmed byte-identical diff). Ran
the full gate itself (build/vet/`go test ./...`/all guards) and
independently reproduced the pre-existing `internal/issuereport` failure
on bare `main`. Checked the manual: `web/help/en/quickstart.md`'s
"Opening and closing the day" section already describes the Open Shift
step generically with no UI/step change from this fix — confirmed rather
than assumed, and `guard-help-topics.sh` passes.

One non-blocking nit, not fixed here: the auto-created register's display
name is `EnsureRegister`'s existing hardcoded `"Default Register"`
literal, not routed through `T`/a locale key — pre-existing string in a
repo method used by three call sites already, not introduced by this
diff, and the broken fallback it replaces never rendered a working
register anyway (no regression to document). Not blocker-class, so no
second review round per this pipeline's process-depth rule.

## Gate — all green

`go build ./...`, `go vet ./...`, `go test ./...` (one pre-existing,
unrelated failure — `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`,
already tracked as ut-docs#415, independently reproduced on bare `main`
by both Dev and Reviewer via `git stash`, root-caused to `go test`
running as uid 0 in this environment). `guard-data-access.sh` and
`guard-i18n.sh` green. `e2e/tests/login.spec.ts` (auth project) green,
7/7, driven for real against a genuinely fresh till.

## Verdict

**Safe to merge.** Single-repo, no ADR needed (bugfix using an
already-established repository-layer pattern, not a new architectural
decision), no i18n/money impact, no manual-content change required.

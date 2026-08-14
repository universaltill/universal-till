# Auth `Can()` sweep — reports/EOD/audit/journal (ut-docs#709)

## What shipped

`ut-docs#709` is one of 5 subsystem-scoped successor cards splitting
`ut-docs#555` (itself split from `ut-docs#520`), found too large (95
`isManagerOrAuthOff` call sites / 27 files) to land as one diff. This card's
scope: `internal/pages/eod_api.go`, `reports_page.go`, `journal_page.go`,
`audit_page.go`, `my_reports_page.go` (13 real call sites once re-grepped —
the original estimate drifted, as expected).

Replaced the blanket `isManagerOrAuthOff(r)` gate (manager/admin only) with
a new shared `canPerform(d, r, action)` helper (`internal/pages/authz.go`)
backed by `d.AuthSvc.Can()` and the `role_permissions` catalog from
`ut-docs#554`. Action mapping:

- `eod_report` (existing catalog action) — all 6 sites in `eod_api.go`
  (run/print/range/settings/report-retention/archive-export) and the EOD
  tab's gate in `reports_page.go`.
- `reports` (new) — `reports_page.go`'s top-level `CanAsk`/`IsManager`
  template flags, `journal_page.go`'s `InvoicingOn` flag, and
  `my_reports_page.go`'s page gate.
- `audit` (new) — both `audit_page.go` sites (page + CSV export).

New migration `042_reports_audit_permissions.sql` adds `reports`/`audit` to
the action catalog, seeded identically to 039 (manager/admin/super_admin
granted, cashier denied) — additive, no existing till's access changes.
`isManagerOrAuthOff` itself is untouched (~35 remaining call sites across
the other 4 successor cards still use it).

`ut-docs#555` was converted from a single 95-site card into a tracking
umbrella and split into 5 successors: #710 (Settings page), #706 (Plugin
management), #707 (Data/sync/pairing), #709 (this card), #708
(Print/import/misc) — all `complexity:medium`, all Ready.

## Independent review (Opus, different model from the Sonnet that wrote it)

Full review spawned in an isolated worktree (`isolation: "worktree"`, per
`ut-docs#386`) with instructions to actually run build/vet/tests/guards and
independently re-verify the behavior-preservation claim by reverting
`canPerform` back to `isManagerOrAuthOff` in 3 of the 5 files and re-running
those files' tests.

**Blocking, both fixed:**

1. `eod_api_test.go`'s fixture was missing `AuthSvc: auth.NewService(db)`.
   Every test in that file used either `UT_AUTH=off` or no session, so none
   of the 6 changed EOD gates — the largest single cluster in this card —
   were ever exercised against a real `Can()` call; the reviewer proved a
   test that *did* reach that path would nil-panic. Fixed: `AuthSvc` wired
   into the fixture, plus a new `TestPostEODRun_RealSessionGatesByRole`
   table test (cashier/manager/admin/super_admin) mirroring the pattern
   already used in `my_reports_page_test.go`.
2. `internal/pages`' hand-rolled `seedForPages` fixture (bare SQLite, no
   real migrations) duplicated migration 042's seed data rather than
   testing it — a typo in the migration would have left every test in this
   diff green. Fixed: extended `TestAuthRepo_HasPermission` in
   `internal/data/auth_repo_test.go` (which runs real migrations) to cover
   `reports`/`audit` alongside `refund`, confirming 042 produces exactly
   the intended 27 grant rows (3 roles × 9 actions, cashier zero) on a
   genuinely migrated DB.

**Non-blocking, deferred:**

3. The `super_admin` broadening is *inconsistent*, not just inert: some
   newly-migrated call sites (e.g. the `/invoices` and `/audit` links on
   `reports.html`) would show a link to a super_admin session while the
   destination page (still gated by `isManagerOrAuthOff`) would reject it,
   until the other 4 successor cards land. Inert today — nothing creates a
   super_admin-role user — but worth a line on `#555`'s tracking comment so
   no card makes that role real before all 5 successors ship. Noted there.
4. `reports.html`'s `/audit` link is gated by the `reports` action while
   the destination page uses `audit` — a role holding one but not the
   other would see a dead link. Same inert-today caveat; cheap follow-up
   (a separate `CanAudit` template flag) left for a future card.
5. Two `eod_report` sites (`/api/settings/report-retention`,
   `/api/reports/archive/export`) are a judgment call rather than an
   obvious fit — documented the rationale directly in the migration
   comment rather than changing the mapping (behaviorally identical either
   way while every role holding one action holds the other).

## Verified beyond automated tests

- Reviewer independently reverted `canPerform` → `isManagerOrAuthOff` by
  hand in 3 of the 5 files (audit + my-reports, and reports_page.go) inside
  the isolated worktree, re-ran each file's tests, and confirmed: every
  pre-existing test still passes under the old gate (the fixture/seed
  changes aren't silently load-bearing for old assertions), and every new
  test fails — specifically and only on the `super_admin` subtest — proving
  the new tests genuinely exercise `Can()`/`role_permissions` and the only
  real behavioral delta for any role is the documented broadening.
- Separately confirmed by directly revoking `manager`'s `reports` grant in
  a live test DB and observing denial, ruling out `canPerform` silently
  short-circuiting to "always allow" for a role that happens to still pass.
- Confirmed production nil-safety: `internal/pages/init.go` always
  constructs `AuthSvc` before `common.Deps` is built — `canPerform` cannot
  nil-panic outside of an incompletely-wired test fixture (which is what
  finding 1 above actually was).
- Confirmed `UT_AUTH=off` short-circuits before ever touching
  `d.AuthSvc`, verified directly against the (at-the-time nil-`AuthSvc`)
  EOD fixture.
- Full `go build ./...`, `go vet ./...`, `go test ./...` and all 5 CI guard
  scripts (`guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-i18n.sh`, `guard-help-topics.sh`) run
  clean, both by the reviewer and re-run after applying the two fixes.

## Scope notes

No UI/template changes and no shop-owner-visible behavior change for any
real role today (pure backend permission-check refactor) — `web/help/`
manual topics and `reference/ux-guidelines.md` deliberately not touched,
confirmed by the reviewer as intentional, not overlooked.

## Verdict

Safe to merge. Both blocking findings fixed and re-verified; non-blocking
findings 3–4 carried forward to `ut-docs#555`'s tracking comment as a
standing note for the remaining 4 successor cards.

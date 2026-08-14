# Code review — Auth Can() sweep: import / issue-report / menu / misc pages (ut-docs#713)

**Date:** 2026-08-14
**Card:** ut-docs#713, split of #708, successor of #555, complexity: medium
**Dev:** inline (Sonnet, this pipeline cycle)
**Reviewer:** independent Opus subagent, worktree-isolated, fresh context

## What shipped

Replaced all 12 `isManagerOrAuthOff(r)` gate call sites across 7 files with
`canPerform(d, r, action)` against the #554 `role_permissions` catalog —
the same established mechanism as #554/#706/#707/#709/#710/#712:

| File | Sites | Action |
|---|---|---|
| `menu_page.go` | 1 | `settings` (existing) |
| `import_page.go` | 4 | `import_export` (new, migration 045) |
| `import_dispatch.go` | 1 | `import_export` (new, migration 045) |
| `issue_report_page.go` | 3 | `issue_reporting` (new, migration 045) |
| `index_page.go` | 1 | `reports` (existing) |
| `backoffice_page.go` | 1 | `reports` (existing) |
| `ask_api.go` | 1 | `reports` (existing) |

Migration 045 adds `import_export` and `issue_reporting` to the catalog,
seeded identically to 039/042/043/044 (manager/admin/super_admin granted,
cashier denied — additive, no existing till's access changes).

## Review

Independent Opus review, worktree-isolated, fresh context — actually ran
the diff (not just read it), the full gate, and an independent
revert-then-restore TDD re-verification of all 12 sites individually
(not just a sample).

**Verdict: safe to merge**, with one medium finding fixed pre-merge and
one deferred to a follow-up card.

### Findings

- **F1 (medium, FIXED).** The revert-then-restore pass found 3 of the 12
  sites had no test that actually distinguishes the new `canPerform` gate
  from the old `isManagerOrAuthOff` — `super_admin` is the only
  behavioral discriminator between the two (the old gate only recognized
  manager/admin), and it was missing from `index_page.go`'s `/` →
  `/backoffice` redirect, `issue_report_page.go`'s `GET
  /ui/bugreport-chip`, and its `POST /api/issue-reports`. The dev's own
  TDD claim (having tested a `menu.go`/`ask_api.go`/`import_page.go`
  cluster) was accurate for those sites but overstated as covering "all
  12" — it didn't extend to these three. Fixed by adding `super_admin`
  cases to `TestBackofficeModeFallsThroughForNonManagerSession`,
  `TestBugReportChip`, and `TestIssueReportAPI_ManagerOnly`; verified in
  both directions (fail with the gate reverted, pass restored).
- **F2 (medium, DEFERRED — ut-docs#720).** `POST /api/data/import`
  (`import_dispatch.go`) moved to `import_export`, but its documented
  mirror `POST /api/data/export` (`data_api.go`, migration 044) is under
  `data_management`, and the file's own doc comment calling itself "the
  same manager gate" as its mirror is no longer literally true. Inert
  today (manager/admin/super_admin hold both actions identically) — a
  real judgment call worth reconsidering, not an obvious bug. Migration
  045's comment corrected to describe this site accurately rather than
  folding it into the "catalog data" framing that fits its sibling sites;
  follow-up card filed (ut-docs#720) to decide the right action and align
  the code/comments/tests.
- **F3 (nit, ACCEPTED).** `menu.html`'s manager-only tile block now gates
  on `settings`, but the tiles it reveals target pages still gated by the
  untouched `isManagerOrAuthOff`/`u.IsManager()` (which denies
  super_admin) — a role/target divergence, inert today, out of #713's
  file-list scope. Flagged for whichever future card converts
  `users_page.go`/`locations`/`kitchen-stations`/`translations`.
- Two recurring bug classes checked, both clean: `export-save`'s
  `os.MkdirAll(dstDir, 0o755)` runs before `os.Create` into
  `UserHomeDir()/Downloads` (an intentional absolute path, correctly not
  `paths.Data(...)`); `import_dispatch.go` stages via `os.CreateTemp("")`
  (system temp, no directory needed);
  `issuereport.PendingDir`'s cwd-relative default is overridden in
  production by `paths.Data("issue-reports", "pending")`
  (`internal/pages/init.go`).
- No real client/shop name or secret-shaped literal in any test/migration.

## Verification

- `go build ./...`, `go vet ./...`, `go test ./...` (whole repo,
  `-count=1`) — green, both before and after the review's fixes.
- `guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-i18n.sh`,
  `guard-plugin-menu-read.sh`, `guard-help-topics.sh`,
  `guard-compliance-claims.sh`, `guard-docs-shots.sh` (screenshots
  regenerated via `make docs-shots` — internal/pages/**.go surface hash
  changed; `menu.png` itself is byte-identical, confirming the tile
  visibility change is non-visual for the manager view already captured)
  — all green.
- TDD: dev's own revert-then-restore across
  `menu_page.go`/`import_page.go`/`import_dispatch.go`/`ask_api.go` (new
  test file `import_ask_menu_manager_gate_test.go`), plus
  `backoffice_page.go`/`index_page.go` (`TestBackofficeRequiresManagerRole`
  strengthened with a `super_admin` case) and `issue_report_page.go`
  (`TestReportIssuePage_ManagerOnly` strengthened) — every one of the 12
  sites individually reverted and confirmed to fail its covering test,
  then restored and confirmed green. Reviewer independently repeated this
  across all 12 sites (not a sample) and found the 3-site gap above.
- `AuthSvc` nil-panic risk checked: two pre-existing test helpers
  (`backoffice_mode_test.go`'s `TestBackofficeRequiresManagerRole`,
  `issue_report_page_test.go`'s shared `newIssueReportTestMux`) built
  `common.Deps` without `AuthSvc` for routes that now call
  `d.AuthSvc.Can(...)` on a real session — both panicked
  (`nil pointer dereference`) before being fixed to set
  `AuthSvc: auth.NewService(db)`, matching #710's own precedent comment.
  The single production `Deps` already sets `AuthSvc`
  (`internal/pages/init.go`), so this was test-fixture-only, never a
  production risk — but worth calling out since it would have been an
  easy false pass otherwise (all four affected tests only exercised the
  no-session/UT_AUTH=off paths before this card).
- No new user-facing string, no `web/help/` update needed (no visible
  behavior change for any role that exists in production — cashier
  denied and manager/admin allowed identically before and after; the
  only broadening is super_admin, and no code path can create a
  super_admin user today, per #555's standing note), no README change
  needed.

## Deferred / out of scope

- ut-docs#720 — reconsider `POST /api/data/import`'s action
  (`import_export` vs. `data_management`).
- The menu-tile-vs-target-gate divergence noted in F3 — not this card's
  file list.

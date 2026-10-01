# Review — Report-an-issue tile and settings card gate on `issue_reporting` (ut-docs#3135)

Date: 2026-09-30 · Lane: `lane:cloud-54` · Card: universaltill/ut-docs#3135 (`complexity:easy`)

## What shipped

- `uislot.CoreMenu`'s `/report-issue` tile now uses `VisibleIf: "issue_reporting"`, not
  `"settings"`. There is a new `issue_reporting` predicate in `menuPredicates`
  (`internal/pages/menu_page.go`). The page, the nav chip and the API gate on the same action,
  now through a shared `issueReportingAction` const (`issue_report_page.go`).
- Because the permission page's derived "Unlocks" list reads the core tables, it now lists
  "Report an issue" under the System group's `issue_reporting` action instead of under Settings.
- Review finding, fixed in this branch: the `/settings` "Report an issue" card and its sidebar
  row (`settings-issuereport`) also led to `/report-issue` while gating on `settings`. They now
  follow `canReportIssue`: the `canReportIssue` template key and a new
  `filterSettingsNavForRender` parameter.
- `web/help/img/manifest.json`: only `surface_sha256` was refreshed
  (`update-docs-shots-surface-hash.sh`). For the default roles no rendered pixel changes:
  admin, manager and super_admin hold both `settings` and `issue_reporting` in `001_init.sql`,
  and no later migration touches either.

## Default behaviour

Unchanged. A cashier still sees neither the tile nor the card. Behaviour only differs when an
owner edits `role_permissions` so that the two actions diverge. That divergence was the bug.

## Tests (TDD)

Each test below was written first and seen to fail for the stated reason.

- `TestReportIssueTile_GateMatchesPageAction` checks that the tile's `VisibleIf` equals the
  page's action const.
- `TestReportIssueTile_FollowsIssueReportingNotSettings` is a real `/menu` render with
  `role_permissions` edited both ways.
- `TestMenuUnlocksByAction_DerivedFromCoreTables` now requires `issuereport.title` under
  `issue_reporting` and not under `settings`.
- `TestSettingsPage_ReportIssueCardFollowsIssueReporting`: when a manager loses
  `issue_reporting`, both the card and the sidebar row disappear.

## Independent review

- Built by Sonnet and reviewed by Opus 5.5 in a fresh context, in a separate worktree.
- The reviewer re-verified the TDD claims. With the fix reverted, the new tests fail with the
  expected messages. With only the predicate registration reverted, the existing
  `TestMenuPage_EveryCoreVisibleIfPredicateIsRegistered` fails. Restored, everything passes.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | `/settings` Report-an-issue card and sidebar row are still gated on `settings` (a button that 403s) | **Fixed** in this branch, test first |
| 2 | nit | `web/help/*/bug-reporting.md` says "managers and admins only" | Accepted: true for the default grants, and changing it would need all five translations for no behaviour change |

The reviewer also checked these and found nothing to change:

- the rail has no `/report-issue` entry, and the chip was already on `issue_reporting`;
- the `/admin` "administration" composite does not include `/report-issue`;
- `ProtectedMenuKeys` and the `demo_mode.go` route list are not visibility gates;
- the only new raw SQL is in a `_test.go` file;
- no new strings, and no file writes or paths.

## Gate (orchestrator, after the last edit)

- `gofmt -l .` is empty.
- `go vet ./...` is clean.
- `go test ./... -count=1` passes in every package.
- Every `guard-*.sh` in `ci.yml` passes except two that fail only in this container:
  - `guard-deadcode-baseline` reports `logging.Stderr` as unreachable. Its only callers are in
    `cmd/unitill-desktop`, which is skipped here because the GTK headers are missing. CI
    analyses it.
  - `guard-shellcheck-version`: no shellcheck binary here.
- Playwright: `bugreport-panel*.spec.ts`, `menu*.spec.ts` and `settings*.spec.ts` all pass
  (40/40).

## Visual attestation

No layout or template structure changed; only the `{{ if }}` gate condition on one existing
card did. I took no screenshots. For every default role the rendered pages are identical to
before: same data, same gates.

## Verdict

Safe to merge.

# Review: bug-report panel hides "View my reports" from users without `reports` (ut-docs#3120)

**Date:** 2026-09-28 · **Lane:** lane:local · **Built by:** Claude Opus 5.5 · **Reviewed by:** Claude Fable 5.1 (independent subagent)

## What shipped
- `web/ui/partials/bugreport_panel.html`: the `/my-reports` link is wrapped in
  `{{ if allowed "reports" }}`, the per-request predicate from ut-docs#3079,
  which evaluates the same `canPerform(…, "reports")` the handler
  (`internal/pages/my_reports_page.go`) enforces. Before, a cashier saw the
  link and got a 403 page (found in the #3079 device check on the TECLAST
  tablet, v0.30.5).
- `internal/pages/cashier_sale_only_test.go` `TestCashierSaleOnly_MenuAndRail`:
  a cashier's page has no `href="/my-reports"` but still has the panel; a
  manager's has the link.
- Manual (en/de/tr/fa/ar): `my-reports.md` and `bug-reporting.md` say the
  page is for managers and admins (it lists every report the till sent).
  `make docs-shots` regenerated.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Low | Manual still told every user to use the link | **Fixed** in this branch (5 locales + screenshots) |

Reviewer also checked, clean: `allowed` is rebound per request on every path
that executes `base` (Render, RenderWith, RenderError via `withHelpHref`;
`ui.NewRenderer` only executes fragments in production); predicate is
byte-for-byte the handler's gate; every identity change (login 303, logout
HX-Redirect, idle-lock `location.replace`) is a full document load, so the
panel outside `#ut-page` can't keep a stale link; no other surface emits
`href="/my-reports"`.

## Verified
- TDD: test failed on the unfixed template (line 163, "cashier bug-report
  panel still links /my-reports"), passes with the fix. Re-verified by
  revert→run→restore by the orchestrator and independently by the reviewer.
- `go build ./...`, `go vet ./...`, full `go test ./...` green; guards
  data-access, i18n, help-topics, help-drift, docs-shots green.
- e2e `bugreport-panel.spec.ts` + `bugreport-panel-persists-2342.spec.ts`: 26 passed.
- Not yet looked at on the device as a cashier: due after the next release
  (tablet screenshot of the panel as a cashier).

**Verdict:** safe to merge.

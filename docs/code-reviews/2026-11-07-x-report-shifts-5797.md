# Review — X report 2/3: X section and JSON on /shifts (ut-docs#5797)

**Date:** 2026-11-07 · **Branch:** `feat/5797-x-report-shifts` (ut-docs: `docs/5797-x-report-pointer`)
**Author model:** Sonnet (dev subagent) · **Tester:** Opus 5.5 subagent · **Reviewer model:** Opus 5.5 (independent subagent, separate worktree)

Pre-flight: web/help topic — updated (`web/help/en/reports.md` "X report (mid-shift read)"; docs-shots: the X fragment is not screenshotted, surface hash refreshed, commit carries `Docs-Shots-Unchanged: true`); `reference/` — updated (ut-docs `architecture/end-of-day-report.md` X section, `reference/plugin-manifest.md` reserved routes now list `/shifts`); README — n/a (no text went stale); en.json/en.meta — updated (27 keys, all with meta); ADR — n/a (design-5780 §4, X report is core per ADR-0121 §1 default); plan/price, platform claim, plugin taxonomy — n/a; review record — this file.

## What shipped
- `internal/data/x_report_repo.go`: `ListOpenShifts` — newest open shift per register with register/cashier display names (the only new SQL).
- `internal/pages/shift_x_report.go` (new): `POST /shifts/x-report`, HTML fragment or JSON by `Accept`. Body capped at 4 KiB; PIN and register read only from the form body; register length-capped and must match an open shift. Shift choice: picked register → this till's stored register (`xTillRegister`, read-only — never `ResolveTillRegisterID`, which can write) → newest open shift. `reports` permission or manager-PIN elevation; JSON refusals mirror `/api/pos/no-sale` (403 `manager_pin_required`, 429 when locked out). Closed/no shift → 404. Writes nothing, no audit row (spec D2).
- `/shifts`: Current Shift card shows register and cashier names (was raw ids); visible "X report" section with optional register picker; figures only in the fragment, with a "Hide X report" button. Reports → End of day links to `/shifts#x-report`.
- `internal/pages/demo_mode.go`: route in `demoAllowedRoutes`. `internal/plugins/manifest.go`: `/shifts` added to `reservedPageRoutePrefixes` (else `TestReservedPageRoutesCoverLiteralChildNamespaces` fails).
- Tests: 4 data tests, ~23 handler tests, e2e `e2e/tests/shifts-x-report-5797.spec.ts` (5 tests).

## Tester findings (fixed before review)
- High: CI red — `/shifts` not reserved (above).
- Medium: tender names rendered as raw keys ("tender.cash") on a real till; fixed with `{{ T .Name }}`, `TestShiftXReport_TenderNamesTranslated`.
- Medium: at 360px Tips hidden behind "More"; `data-cards-collapse="off"` on the tenders table, pinned by e2e.
- Tester mutation run: 14 code mutations, 11 caught initially; tests strengthened for the 2 escapes (tender JSON tag, wrong shift for this till's register); 429 lockout test added.

## Review findings
- L1 JSON `tenders[].name` is the stored value (locale key for built-ins) — **accepted**: the JSON is a machine contract; `method_id`/`type` identify the method and a locale-dependent label would make the payload vary by `Accept-Language`. Documented here.
- L2 open-shift lookup (404) runs before the permission check — accepted: `/shifts` already shows the current shift to the same sessions.
- L3 no CSRF token on the POST — accepted, out of scope: same as every `checkOrElevate` POST; read-only endpoint.
- L4 register select / Show button had no spacing with two registers — **fixed** (`#x-report-form` flex + gap).
- L5 500 branch has no handler test — accepted (straight-line; Tester drove it with a stubbed 500).
- L6 ut-docs review record stale — **fixed**.

## Verification
- Reviewer re-verified two TDD claims by revert→fail→restore: `/shifts` reserved prefix and `{{ T .Name }}`.
- Tester drove the built till: 1024×600 kiosk + windowed, 1366×768, 360×740, auth off and on (cashier then manager PIN), German via a test overlay (the de pack lacks the new keys), ar/fa (new keys fall back to English). All 16 UX §C points pass. **Not run: real Pi touch check** (touch targets/one-keyboard only emulated in Chromium).
- Gates: gofmt clean, `go build ./...`, `golangci-lint` 0 issues, shellcheck 0, all build-job guards pass, full `go test ./...` passes (internal/plugins needs >10 min locally).

## Verdict
Approve.

## Deferred
- Card 3/3 (print, `x_report_printed` audit, no-printer 502) stays on the X report split.

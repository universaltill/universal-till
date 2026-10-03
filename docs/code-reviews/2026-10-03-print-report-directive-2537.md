# Review: `print_report` directive + 80mm till printout (ut-docs#2537)

**Date:** 2026-10-03 · **Lane:** cloud-41 · **Author model:** Opus 5.5 (subagent) · **Reviewer:** Fable (independent subagent, isolated worktrees)
**Branch:** `feat/2537-print-report-directive` (same name in `ut-cloud`, `universal-till`, `ut-my-shop`)

## What shipped

The remaining gap of ut-docs#2504 (its scope was already split by an earlier
BA pass into #2535/#2536, both Done, and #2537/#2538, both open) — slice (c):
a cloud manager report, computed over `SalesAggregate` the way the reports
read API (#2536) already does, can be printed on one chosen till. Design:
BA + Architect comments on ut-docs#2537; a design addendum in
`adr/0095-portal-till-configuration-directives.md`'s Notes (committed to
ut-docs main ahead of this work, no new ADR — this extends ADR-0095's
existing directive-queue decisions).

- **`ut-cloud`:** `internal/claims/print_report.go` (`RequestPrintReport`,
  the bounded payload shaper, `formatPrintMoney`), a new `print_report`
  directive type (any-till, no TTL/supersede, `DirectiveMinTillVersion`
  placeholder), `internal/api/app_endpoints_print_report.go` (owner-gated
  `POST /ui/app/stores/{id}/devices/{device_id}/reports/print`, no
  step-up), a `store.print_report_result` audit wired into the existing
  directive-result handler, 5 new my. locale keys in all 10 locales.
- **`universal-till`:** `internal/cloudsync/print_report.go` (device-check +
  payload decode, defensively re-capped), `internal/pages/cloudsync_print_report.go`
  (the hook: builds a `print.Doc` from the already-computed payload and
  prints it — never recomputes). `rename_till.go`'s device check became a
  shared `ownDeviceSkipReason`.
- **`ut-my-shop`:** a `PrintOnTill` control in `ReportsPanel.tsx`, enabled
  only when one specific till is chosen (not "All tills"); fire-and-confirm
  UX (no polling — matches how the UI already treats every other directive).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| B1 | Blocker | `internal/claims/print_report_test.go`'s `TestRequestPrintReportCapsRowsInNaturalOrder` wrote `SetReportedAt(time.Now())` (not `.UTC()`) — failed `guard-utc-time-writes.sh`, so the Dev's "verify.sh green" claim was wrong | Fixed: `.UTC()` added; `guard-utc-time-writes: OK`, test still green |
| S1 | Should-fix | `TestAppPrintReportByOwner`'s comment claimed a stale/pre-ADR-0141 session still prints (no step-up), but used `asSubject` which always sets a fresh `AuthTime` — the test could not have caught a `requireStepUp` regression (verified: injecting `requireStepUp` into the handler left the original test passing) | Fixed: now exercises `staleAuthTimes()` via `asAuthedAt`, as the kiosk-unlock step-up tests already do; re-verified it fails under the same injection and passes restored |
| N1 (deferred) | Non-blocking, UX | The my. UI reuses the till **filter** as the print **target**, so the all-tills overview can never be printed on the main till (the API's `till` filter and `device_id` target are independent; the UI collapses them) — a shop owner will want "print today's shop total on the counter till" on day one | Deferred to a new Backlog card (filed below) |
| N2 (deferred) | Non-blocking | A removed till that still has figures in the period's `by_till` stays selectable in the filter; printing to it 404s `till_not_found`, shown as the generic retry-won't-help error text | Deferred — pre-existing filter behaviour, not introduced here |
| N3 | Nit, accepted | `formatBasisPoints` computes the 2-place string before the `places>=0` branch recomputes it — harmless dead work | Accepted as-is (cosmetic) |

Checked and fine: payload shape/caps (30 rows / 8 KPIs) match ADR-0095's
Notes exactly; any-till targeting with defence-in-depth on both sides;
tenant isolation (another store's device, another owner's store); no
TTL/supersede (several pending prints coexist, proven); till renders
without recomputing (no sales-table reads in the till-side hook); money is
Latin-digit, locale-independent; i18n complete across all 10 locales on
both `ut-cloud` and `ut-my-shop`; no file writes / no `MkdirAll`-relevant
code; no real shop/client names in test data; no secrets; no manual/help
topic needed (purely an owner-side my. action — the till only emits a
slip, no new till screen).

## Verified

- `go build ./...`, `go vet ./...`, full `go test ./...` (ut-cloud, and
  universal-till under both UTC and `TZ=Asia/Tehran`) — all green, re-run
  independently by both Tester and Reviewer (not just the Dev's own
  report).
- `bash scripts/ci/verify.sh` (ut-cloud) green after the B1 fix;
  `guard-data-access.sh`/`guard-i18n.sh`/`guard-docs-shots.sh` (universal-till)
  green; `npm run check` (ut-my-shop, lint+test+build) green.
- Reviewer independently re-verified 8 TDD claims across the three repos by
  mutating the logic each test claims to catch, confirming the test fails
  with the right symptom, then restoring: the any-till/satellite targeting
  test, the row-cap/truncation-count test, the device-match queue-refusal
  test, the till's `Tick`-level own-device skip, the till-side defensive
  re-cap, the no-printer failure path, and both of the new ut-my-shop UI
  tests (disabled-state logic, request-body shape).
- Tester independently ran the full universal-till/ut-cloud suites, read
  the new test files for tautology risk (all carry concrete, specific
  assertions — exact formatted strings, row ordering, "+N more" text —
  not generic pass-throughs), and did a real browser visual check of the
  new my. UI control (Playwright against the demo build) across en-GB,
  de-DE (longest-string check) and fa-IR (RTL check), in idle/disabled,
  enabled, sending, sent and error states, plus the empty-cashiers view —
  no visual defects found (tokens reused, correct RTL mirroring, no
  clipping/overlap).
- An end-to-end handler-level test (`TestSyncServesPrintReportToTargetSatelliteAndAuditsResult`)
  proves the cross-repo contract: the till-side failure string
  (`"no receipt printer is configured on this till"`) and the cloud-side
  audit/test expectation agree verbatim.

## Not verified (honest gaps)

- No real printer, no real till, no real cloud round-trip — every claim
  above is against fakes/in-process tests, consistent with a cold cloud
  cycle's limits (no hardware). The till-side print path was exercised
  against a fake TCP printer and a deliberately-closed port, not a real
  ESC/POS device.
- `DirectiveMinTillVersion["print_report"] = "0.30.16"` is a placeholder
  (latest real tag is v0.30.15); whoever cuts the release confirms the real
  number, per this codebase's existing convention for every other
  not-yet-shipped directive type.

## Deferred follow-up filed

New Backlog card for N1 (the all-tills print gap) — filed separately on the
board as part of close-out, referencing this review record.

## Verdict

**Safe to merge** after the two fixes above (B1, S1), both applied and
re-verified in the live checkouts (not just the review worktrees) before
this record was committed.

---
_Generated by [Claude Code](https://claude.ai/code)_

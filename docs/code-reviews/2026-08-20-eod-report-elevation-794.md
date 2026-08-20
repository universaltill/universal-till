# Code review: wire eod_report mutations onto the manager-override elevation mechanism (ut-docs#794)

**Date:** 2026-08-20
**Card:** universaltill/ut-docs#794
**Author (build):** scrum-master cycle, inline (Sonnet, `complexity:medium`)
**Reviewer:** independent Opus subagent (fresh context), two rounds — the
second earned by the first round finding 2 blocker-class issues

## What shipped

`internal/pages/eod_api.go` had 6 `canPerform(d, r, "eod_report")` sites
still on the flat `403` gate from before ADR-0052/#557: `POST
/api/reports/eod/run`, `/print/{period}`, `/range`, `/api/settings/eod`,
`/api/settings/report-retention`, `/api/reports/archive/export`. All 6 now
go through `checkOrElevate`/`InsertAuditElevated` (`elevation.go`, #557),
so a denied session gets the shared in-place manager-PIN dialog instead of
a flat refusal, with dual attribution in the audit trail — same mechanism
as the 3 already-shipped sites (`backup_api.go`, `sync_api.go`,
`permission_settings_page.go`).

Two of the six (`/range`, `/archive/export`) trigger a file download via
`Content-Disposition`, which htmx can't drive — a new client helper,
`window.utPostWithElevation` (`web/public/app.js`), manually detects (via
an explicit `Content-Type: text/html` renderElevationPrompt now sets),
renders, and drives the same shared dialog for those two.

`generateEOD` gained `actor, blockedActorID string` parameters so its one
existing `InsertAudit` call (used by both the manual `/run` handler and
`StartEODScheduler`'s unattended ticker) can carry the resolved actor
without forking the function in two; the scheduler's tick was extracted to
a package-level `eodSchedulerTick` so a test can drive the real call site
rather than only proving `generateEOD` honors whatever args it's given.

ADR-0052 §2 scoped `checkOrElevate` to handlers that already wrote an audit
entry — only `/run` met that bar. Wiring elevation onto the other 5 (2 of
which are read-only exports, not mutations) also meant giving them the
audit write §2 requires; recorded as a judgment call inline in
`eod_api.go`'s doc comment rather than a formal ADR amendment, per the
precedent migration 042 itself already set for the same kind of scope note.

## Independent review — round 1: 2 blockers, 5 should-fix, 6 nits

**Blocker 1 — the elevation dialog was unreachable for 4 of 6 sites.** The
EOD tab's visibility gate (`reports_page.go`'s `isManager :=
canPerform(d, r, "eod_report")`, wrapping the entire
`reports_tab_eod.html` partial) was the SAME action as the new
authorization gate. A role denied `eod_report` got no card at all — no
button to ever trigger `checkOrElevate` on. Real once a shop uses the
permission matrix (#557's own sibling feature) to grant `reports` without
`eod_report` to some role between cashier and manager — exactly the
scenario ADR-0052 exists to serve.

**Blocker 2 — `/api/settings/report-retention`'s own
`hx-on::after-request="if(event.detail.successful){window.location.reload()}…"`
destroyed the dialog.** `needsElevation` is HTTP 200, and htmx computes
`successful` from status code alone — so the page reloaded immediately
after the OOB-swapped dialog appeared, before a PIN could ever be entered.

**Should-fix:** a `204 No Content` never swaps under htmx (not even OOB
content), so the two settings-style sites' elevation retries could never
show a confirmation; the elevation summaries were static sentences that
hid the actual value being approved (ADR-0052 §3's own stated reason for
summaries existing at all — concretely, a manager could be walked into
silently *disabling* the automatic Z-report without the dialog saying so);
`utPostWithElevation` left the range/export buttons permanently disabled
if the dialog was cancelled; the scheduler-actor test asserted its own
supplied inputs rather than driving real scheduler code; 5 of 6 sites had
no elevated-path audit assertion at all.

**Nits:** a factually-wrong "MutationObserver" comment (none exists in the
vendored htmx 1.9.12); a `close` callback parameter that was always a
no-op; a double-body-read footgun in an unreachable fallback branch;
`print/{period}` validated the period AFTER elevating, burning a PIN entry
on a request that could still 404; the ADR-0052 §2 scope expansion wasn't
recorded anywhere; the help text (already fixed as a side effect of
blocker 1) would have over-promised.

## Fixes applied

- **Blocker 1:** split the gate — `canView := canPerform(d, r, "reports")`
  now controls the card's visibility (matching the page-level `reports`
  gate this tab already lives under, and the settings.html/backup_api.go
  precedent: view permission ≠ per-action operate permission);
  `canRunEOD := canPerform(d, r, "eod_report")` gates only the archived
  rows' money figures (Net/Sales) and their population. New regression
  test `TestReportsPage_EODTabButtonsVisibleWithoutEODReportPermission`
  grants `reports` without `eod_report` to `cashier` and asserts the
  buttons render, the schedule shows real values, and the money figures do
  not.
- **Residual on blocker 1** (found verifying the fix): the Reprint button
  — `print/{period}`'s only UI trigger — lived inside the same
  `eod_report`-gated table as the money columns, so that one site stayed
  unreachable even after the split. Fixed by rendering the row list (period
  + button, no figures) for any viewer, gating only the Sales/Net
  `<td>`s/`<th>`s on `CanRunEOD` in the template. Test above extended to
  assert the Reprint button too.
- **Blocker 2:** the form's `hx-on::after-request` now also checks
  `event.detail.xhr.getResponseHeader('Content-Type')` for `text/html`
  before reloading — the same signal `utPostWithElevation` uses to detect
  the elevation prompt, verified against the vendored htmx 1.9.12 source
  (OOB swap runs before the `hx-swap` dispatch, so the dialog still
  appears regardless; a real 204 success carries no Content-Type and still
  reloads; a real 4xx/5xx still shows `#settings-save-error`).
- **Should-fix (204s):** `/api/settings/eod` and `/api/settings/report-retention`
  now return a 200 `✓ Approved and saved.` fragment (new `elevation.approved`
  key, all 4 locales) on the `elevated` branch specifically; the
  plain-allowed path keeps its existing 204 unchanged.
- **Should-fix (static summaries):** all 5 non-`/run` summaries are now
  parameterized with the real request values — period, from/to, format,
  the localized retention-mode label, and (for the schedule) a genuinely
  distinct "turn on, scheduled for %s" vs. "turn off" sentence depending on
  the actual `enabled` value sent.
- **Should-fix (Cancel wedges the UI):** `utPostWithElevation` gained an
  `onCancel` parameter, fired via the dialog's native `close` event only
  when the flow hadn't already reached a terminal state; both raw-fetch
  call sites re-enable their button in it. Also hardened: the `!dialog`
  fallback and a failed retry submission now both fire `onCancel` too
  (found while re-verifying — neither released the button before).
- **Should-fix (false-pass scheduler test):** `StartEODScheduler`'s tick
  body extracted to package-level `eodSchedulerTick(ctx, d, repo)`; new
  `TestEODSchedulerTick_RunsAndWritesPlainSystemAudit` configures real
  settings and calls it directly, so a mutation to the actual call site
  (verified: swapping the `("system", "")` args) fails the test.
- **Should-fix (missing coverage):** added elevated-path + dual-attribution
  audit tests for `/settings/eod` (exercises Hidden-field replay), `/range`,
  `/settings/report-retention`, `/archive/export`. `print/{period}`'s
  elevated path is left uncovered with an inline comment explaining why (no
  fake-printer test double exists anywhere in this package today — a
  pre-existing gap, not introduced here); its `needsElevation` branch is
  covered like every other site.
- **Nits:** removed the wrong MutationObserver comment; dropped the dead
  `close` parameter from `onDone`'s signature; removed the double-read
  fallback; moved `print/{period}`'s existence check before
  `checkOrElevate`; added the ADR-0052 §2 scope note as a doc comment in
  `eod_api.go`.

## Independent review — round 2 (scoped to the fixes above)

Verified all 9 items **empirically**, not by re-reading: mutation-tested
blocker 1's new test twice (reverting either half of the split gate fails
it); traced htmx 1.9.12's actual `successful`/OOB-swap-ordering source for
blocker 2; wrote and ran throwaway tests exercising all 6 summaries'
real-value interpolation and the 204's actual `Content-Type` absence
(deleted after use, confirmed via `git status`); mutation-tested the
scheduler test by swapping its real call site's args. Re-ran
`go build`, `go vet`, `gofmt`, the full `internal/pages` suite (green,
~178s), and all 6 CI guards.

**Result: all 9 findings confirmed genuinely fixed, nothing regressed.**
One residual surfaced (print/{period} still UI-unreachable) and fixed
above per this same round. Two further nits noted and independently
addressed before commit: the retry's own promise wasn't chained to the
caller's `.catch()` (an unhandled rejection on a retry-specific network
failure), and the `!showDialog` fallback didn't release the caller's
button. One pre-existing, explicitly out-of-scope gap noted and left
alone: the elevation dialog itself doesn't auto-close after a successful
htmx-driven retry on any of the (now 6) sites that use it directly —
true of the 3 sites #557 already shipped, unrelated to this card.

## Verification beyond the two review rounds

- `go build ./...`, `go vet ./internal/pages/...`, `gofmt -l` — clean.
- `go test ./...` (full suite, no `-race`, per this repo's documented
  gate) — all green. (`-race` was also run scoped to `internal/pages`
  during development — clean; a full-suite `-race` run in this sandboxed
  session hits its own 10-minute per-package wall-clock ceiling on
  `internal/plugins`'s wazero JIT compilation, unrelated to this diff —
  confirmed by the plain `go test ./...` run completing in the same
  session with no such timeout.)
- `scripts/ci/guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-i18n.sh`, `guard-plugin-menu-read.sh`,
  `guard-compliance-claims.sh`, `guard-help-topics.sh` — all green.
- `node --check web/public/app.js` — clean (no JS build/lint tooling
  exists in this repo).
- Manual httptest-level exercise of all 6 endpoints against a real
  booted binary confirmed the outer session-auth middleware 401s an
  unauthenticated request before ever reaching `checkOrElevate` — the
  elevation flow is specifically for an authenticated-but-under-privileged
  session, matching every test's use of `auth.WithUser`.

## Docs

`web/help/{en,ar,fa,tr}/elevation.md` updated to include "running or
reprinting the end-of-day report" in the existing representative example
list (now accurate — see blocker 1's fix; would have over-promised
otherwise).

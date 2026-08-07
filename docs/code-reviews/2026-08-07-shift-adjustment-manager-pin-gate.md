# Code review — manager-PIN gate on the generic cash adjustment/payout endpoint

- **Ticket:** universaltill/ut-docs#266 (`complexity:medium`)
- **Repo / branch:** `universal-till`, `fix/266-shift-adjustment-manager-pin-gate`
- **Implemented by:** Dev (Sonnet, inline)
- **Reviewed by:** Reviewer (Opus, independent subagent), 2026-08-07
- **Verdict:** **PASS — safe to merge** after four Reviewer fixes (below).

## What shipped

`POST /api/shifts/adjustment` (`RecordCashAdjustment`, `internal/pages/shifts_api.go`)
let any authenticated cashier record a cash payout with no manager approval —
producing an `audit_log` row indistinguishable from a manager-gated one, and
bypassing the audit distinction the newer `POST /api/shifts/pfandrueckgabe`
endpoint already enforces for bottle-deposit payouts. A cashier could POST
`type=payout&amount=-500&reason=Pfandrückgabe` unapproved.

The gate is keyed on **`Amount < 0`** (cash actually leaving the till), not on
the client-supplied `type` field: `type` is just a string the client picks
with no server-side sign enforcement, so gating only `type=payout` would leave
a trivial bypass via `type=adjustment` with the same negative amount — that's
the same audit hole this change exists to close. Positive adjustments (float
top-ups) stay ungated, matching the existing refund/PfandRückgabe precedent of
only gating cash leaving the till. On success the PIN's manager becomes the
audit actor, same as PfandRückgabe/refund.

A second, adjacent gap was found live-testing this in a real browser (not by
reading the diff): `shifts.html`'s three forms had no `htmx:responseError`
handler, so **any** error from this page — including the new 403 this change
introduces — rendered nothing at all to the operator; htmx doesn't swap
non-2xx responses by default. Fixed with the same handler pattern
`refund.html` already uses for its own `#refund-msg` target.

## Findings

| # | Severity | Finding | Status |
|---|----------|---------|--------|
| 1 | **Blocker** | `scripts/ci/guard-docs-shots.sh` failed: `web/ui/pages/shifts.html` / `internal/pages/shifts_api.go` changed and all four `reports.md` topics changed, but the manual's screenshots weren't regenerated. Verified as introduced (not pre-existing) by checking the guard against `HEAD~1` in a temp worktree. | **Fixed** |
| 2 | Should-fix | A blank `manager_pin` (the natural first mistake — the field can't be HTML-`required`, since positive adjustments must submit it blank) reached `AuthorizeManager` and burned a failed-attempt count shared device-wide with keypad login (5 failures → 30s lockout). Reviewer proved it empirically: 5 blank submissions, then even the *correct* PIN got 429. | **Fixed** |
| 3 | Should-fix | The new `htmx:responseError` handler does `target.innerHTML = xhr.responseText`, but the three `respond*Error` helpers interpolate the message into `<div class='error'>%s</div>` with **no escaping**. Not a live XSS today (Reviewer tried and couldn't reach an attacker-controlled message on this exact path — register IDs are system-generated, and `pos.OpenShift`'s one echoing error path can't fire when the open form that would trigger it isn't rendered) but converts a previously-inert injection (htmx used to drop non-2xx bodies) into a live sink the moment any caller threads user text into one of these helpers. | **Fixed** |
| 4 | Should-fix | Untranslated English error strings (`"manager PIN required"`, `"shift_id required"`, …) now actually reach fa/ar/tr operators, since finding 3's fix makes them render instead of being silently dropped. `guard-i18n.sh` doesn't catch this class (its own comment calls it "knowingly-deferred" — scoped to same-line `w.Write`/`fmt.Fprint*` literals). Real gap, but converting this file's error strings to i18n keys is a larger, separate piece of work than this card's scope. | **Deferred — new Backlog card (ut-docs#428)** |
| 5 | Nit | `type=payout` with a **positive** amount was still accepted, writing an audit row that lies about its own direction (a "payout" that adds cash). No cash-theft vector — `SumShiftAdjustments` sums by sign only, ignoring `type` — but it's the same family of gap this whole change is about: an audit row that doesn't mean what it claims. | **Fixed** |
| 6 | Nit | `TestRecordCashAdjustment` now runs with `UT_AUTH=off`, so nothing in this file exercises the plain accounting path with auth on. Acceptable — the new/updated PIN-gate tests cover the auth-on path directly, and the comment documents why. | **Accepted, no change** |
| 7 | Nit | `reports.md`'s screenshot is captured at its topic's first route (`/reports`), so the manual's screenshot never actually shows the Shifts-page section the new prose describes. Pre-existing structural limitation of the multi-route-topic screenshot harness, not introduced or worsened here. | **Out of scope / pre-existing** |
| — | — | Design decision (gate on sign, not `type`) independently re-derived and confirmed correct: expected-cash math (`ComputeExpectedCash`/`SumShiftAdjustments`) is sign-only and never reads `type`, so gating the label would have gated nothing. | Verified |
| — | — | No SQL outside `internal/data`/`internal/db` in this diff; no money-type violations (`money.FromMinor` at the DB boundary, as before); no file I/O, so neither recurring pipeline bug class (missing `os.MkdirAll`, cwd-relative path) applies. | Verified |
| — | — | No real client/shop name in test data (`"Front Till"`, `"Manager One"`, `mgr1`/`cashier1`); PIN `482913` is the project's standard test fixture, not a secret. | Verified |

### Fix 1 — regenerated manual screenshots

Ran the real harness (`playwright test --config=playwright.docs.config.ts` +
`tests-docs/write-manifest.js`): 56 captures, 14 topics × 4 locales, all
passed; `web/help/img/manifest.json` rewritten. `guard-docs-shots.sh` now
passes. `alerts.png`/`designer.png` also changed across all four locales —
pre-existing, accepted non-determinism in those two topics specifically
(`designer`'s receipt preview bakes in wall-clock time, per the harness's own
documented exception; `alerts` has similar relative-time content), unrelated
to this diff. `reports.png` itself is unchanged, correctly — the prose edit
is in the help topic's markdown, not the live `/reports` page this route
screenshots.

> Environment note for the orchestrator: `npx playwright install --with-deps
> chromium` is blocked by the sandbox's egress policy (403 from
> `cdn.playwright.dev`). Ran with a temporary, unpushed `launchOptions:
> {executablePath: '/opt/pw-browsers/chromium'}` override in
> `playwright.docs.config.ts` (and, earlier, the same override in
> `playwright.config.ts` for the `auth` project login-spec run), reverted
> before commit in both cases — `git diff` on both config files is clean.
> `package.json`/`package-lock.json` untouched.

### Fix 2 — blank-PIN lockout guard

Added `if strings.TrimSpace(req.ManagerPIN) == "" { …403…; return }` before
`AuthorizeManager` in both `RecordCashAdjustment` **and** `PfandRueckgabe`
(the same pre-existing gap sits in the already-shipped endpoint, same file,
same one-line fix — applied for consistency rather than shipping the fix
next to a known-duplicate of the bug it fixes). New tests:
`TestRecordCashAdjustment_BlankManagerPINRejectedWithoutBurningLockoutBudget`
and `TestPfandRueckgabe_BlankManagerPINRejectedWithoutBurningLockoutBudget`
each submit 6 blank-PIN requests (one past the real 5-failure budget) and
then confirm the correct PIN still works immediately.

### Fix 3 — escape error-helper output

`respondShiftError`/`respondCloseError`/`respondAdjustmentError` now wrap the
message in `html.EscapeString(...)` before interpolating into the `<div
class='error'>` fragment. `TestRespondAdjustmentError_EscapesHTMLInMessage`
calls the helper directly with an `<img onerror=...>` payload and asserts the
body contains `&lt;img`, not `<img`.

### Fix 5 — reject a positive `type=payout` amount

One-line 400 in `RecordCashAdjustment`: `if req.Type == "payout" &&
req.Amount > 0 { …400…; return }`, placed before the manager-PIN gate (so it
can't be used to probe whether a shift exists without triggering the PIN
check). `TestRecordCashAdjustment_PositivePayoutAmountRejected` covers it.

## Verified beyond the automated tests

- **Manual, live-browser round trip** (real login via the auth-enabled
  wizard, real manager PIN, real htmx swap): logged in as a cashier, opened a
  shift, submitted a negative payout with no PIN → server returned 403,
  **and** the operator actually saw "manager PIN required" render in
  `#shift-result` (this is what surfaced the responseError-handler gap in the
  first place); then submitted the correct manager PIN → "Adjustment
  recorded: …" rendered, and the audit row's actor was the manager, not the
  cashier.
- **Visual check**: screenshotted the adjustment form (scrolled into view —
  `.catalog-form` is an internally-scrolling sticky panel by long-standing
  design, unrelated to this change) in English light, the app's alternate
  ("dark") color theme, Turkish, and Farsi/Arabic RTL. Every case: label
  above its own field, no overlap/wrap/cut-off, RTL lays out correctly
  (no literal `left`/`right` in the new markup — logical properties only, and
  there were none to change). The `pos-alert`/`error`/`success` bare CSS
  classes render as unstyled plain text — a pre-existing gap (same for the
  success message today), not introduced or worsened here.
- **Reviewer's mutation testing** (see its report, reproduced in the PR):
  deleting the `Amount < 0` gate block, substituting a `type`-only gate, and
  dropping `actorID = approver.ID` each independently made the corresponding
  new test fail with a legible message; restoring the code made them pass
  again. The tests are real, not tautologies.
- **Attempted e2e automation** of the live-browser flow above (append to
  `e2e/tests/login.spec.ts`, the only spec wired to the auth-enabled
  Playwright project) but hit a genuine, pre-existing, unrelated bug: the
  first-boot wizard's "Default" register is offered in the Shifts page's
  register dropdown but never actually inserted into the `registers` table,
  so opening a shift through the wizard-created till 500s with a `FOREIGN
  KEY constraint failed`. Reverted the spec addition rather than fix an
  unrelated wizard bug under this card's scope; filed as a new Backlog card
  (ut-docs#429). The manual live-browser verification above stands in for
  it this round.

## Gate status after the review pass

| Gate | Result |
|------|--------|
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `gofmt -l` on both changed Go files | clean |
| `go test ./internal/pages/... ./internal/pos/...` | PASS |
| `go test ./...` | one unrelated failure: `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure` — already tracked as ut-docs#415 (sandbox runs as uid 0), pre-existing on `main`, confirmed by re-running the same test on `main` directly |
| `guard-data-access.sh` | PASS |
| `guard-i18n.sh` | PASS (855 keys, all locales match) |
| `guard-help-topics.sh` | PASS |
| `guard-docs-shots.sh` | PASS (was the blocker; fixed) |

## Explicitly deferred

1. **Finding 4** — error strings in this file's `respond*Error` helpers are
   hardcoded English, now user-visible in fa/ar/tr since the responseError
   fix. New Backlog card (ut-docs#428): convert to i18n keys, likely across
   more than just this file.
2. **ut-docs#429** — the setup wizard's "Default" register is offered but
   never inserted into `registers`, so opening a shift on a freshly-wizarded
   till 500s. Found while attempting e2e coverage for this card; unrelated to
   the manager-PIN gate itself.
3. **Finding 7** — `reports.md`'s screenshot harness only captures a topic's
   first route; the Shifts-page section this change documents has no
   screenshot of its own. Pre-existing multi-route-topic limitation, not
   worth a dedicated card on its own merits.

## Files changed (final tree, before commit)

```
 M internal/pages/shifts_api.go
 M internal/pages/shifts_api_test.go
 M web/ui/pages/shifts.html
 M web/locales/{en,fa,ar,tr}.json
 M web/help/{en,fa,ar,tr}/reports.md
 M web/help/img/manifest.json
 M web/help/img/{en,fa,ar,tr}/{alerts,designer}.png   (docs-shots regen, Reviewer fix 1)
 M web/help/img/ar/sell.png                            (docs-shots regen, Reviewer fix 1)
```

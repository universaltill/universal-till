# Test coverage batch 31: settings_page.go / setup_page.go / auth_page.go

2026-07-29

The security-sensitive auth/session/first-boot surface:
`settings_page.go` (`registerSettings` ~20%, `shortDeviceID` 0%),
`setup_page.go` (`registerSetup` ~15%), `auth_page.go` (`registerAuth`
~42%, `ensureFirstBootAdmin` ~42%). `auth_page_test.go` already existed
with partial first-boot/login coverage — this batch was specifically
briefed to read it first and find real gaps, not duplicate it.

Implemented by an Opus-model agent while this session (Sonnet) continued
the coverage push — same cross-model-review flow as batches 25/27/29/30.

## No bug found — but this was the most carefully audited "no bug" batch yet

Given the security sensitivity (login, PIN handling, lockout, session
management), this batch was explicitly asked to read the code for auth
bypass, lockout, and session-fixation issues, not just add coverage
mechanically. It came back clean, and the tests prove real security
properties, not just route coverage:

- **Lockout is a genuine brute-force guard, not just a counter**: after 5
  failed PIN attempts, the 6th attempt is refused **even with the
  correct PIN** — the lockout blocks by device state, not by
  re-evaluating the submitted PIN, so it can't be bypassed by finally
  guessing right. Confirmed via two *distinct* audit action types
  (`login_failed` ×5, `login_locked_out` ×1) and confirmed no session
  cookie was set on the locked attempt.
- **PIN change revokes the live session**: after changing a PIN, the
  OLD session token no longer resolves and the OLD PIN can no longer log
  in — tested by actually attempting a fresh login with the stale PIN
  afterward, not just checking a DB flag.
- **Logout actually revokes server-side**, not just clears the cookie:
  `svc.Resolve(token)` is checked to return `ok=false` after logout, plus
  the cookie's `MaxAge<0` and the HTMX-vs-plain-303 branches.
- **First-boot admin reuse/reactivation doesn't duplicate users**: both
  tested by asserting the total user count is unchanged after setup runs
  against a pre-existing dormant admin, not just that setup "succeeded."

No file-write paths exist in these three handlers, so neither of this
push's two recurring bug classes (batch 28's MkdirAll gap, batches
11/23's cwd-relative path) applies here.

## Independent verification (sonnet, different model from the Opus implementer)

- Read all three new/extended test files in full (683 lines total,
  `auth_page_extra_test.go` new, `setup_page_test.go` new,
  `settings_page_test.go` new). No false-pass patterns found — every
  security property above is checked by its actual observable effect
  (a real re-login attempt, a real `svc.Resolve` call, a real audit-row
  count), not by inference from a status code alone.
- `go build ./...`, a full `go clean -testcache && go test ./...` (whole
  repo), and both CI guards — all pass.
- Coverage confirmed: `registerAuth` 82.5%, `ensureFirstBootAdmin`
  83.3%, `setSessionCookie` 100%, `shortDeviceID`/`isManagerOrAuthOff`
  100%, `registerSettings` 72.3%, `registerSetup` 83.1%. `internal/pages`
  overall: 67.1% → 71.2%.

## Coverage added

- **`settings_page.go`**: `shortDeviceID` (empty/short/exactly-16/
  truncated-with-ellipsis); payments default-method + per-provider fee
  (manager gate, missing-method/out-of-range-percent validation, basis-
  point + minor-unit storage correctness); idle-lock, kiosk-idle-reset,
  telemetry opt-in (manager gate, range validation, runtime-state +
  `AuthSvc` threading); UI scale/theme/store-save/generic upsert
  (bounds validation, state reflection); the enrol claim-code/register-
  now/fleet-devices endpoints refusing a non-manager before ever
  touching the marketplace (offline-first: no network call in the
  forbidden path).
- **`setup_page.go`**: the guided wizard happy path (country/currency/
  tax/store name → admin created, session set, settings persisted,
  runtime state applied, the new PIN actually logs in); both `GET
  /setup` and `POST /api/setup` refusing once an operator with a PIN
  exists; PIN format/mismatch validation re-rendering the wizard rather
  than creating an operator, and leaving the till in first-boot state.
- **`auth_page.go`**: the lockout, logout, PIN-change, and first-boot
  reuse/reactivate properties detailed above, plus `GET /login`
  redirecting an already-authenticated session away from the keypad,
  `GET /pin`'s session gate, and the nav session chip (empty when
  signed out, name + manager links when signed in as manager).

## Verification

`go build ./...`, `go clean -testcache && go test ./...` (whole repo),
`scripts/ci/guard-data-access.sh`, `scripts/ci/guard-i18n.sh` — all pass.
`internal/pages` coverage: 71.2%.

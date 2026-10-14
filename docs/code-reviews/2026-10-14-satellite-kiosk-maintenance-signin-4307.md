# Review: staff sign-in through a satellite's kiosk lock keeps it a kiosk (ut-docs#4307)

**Problem.** `POST /api/auth/login` with `next=kiosk` on a till in self-order mode switched it to Register with no role check. Every other writer of `display.mode` refuses Register/Back office on a **satellite** (#2781, #4263, #4295), so this was the last door that left a satellite running as a register while `tills.role = satellite` on both sides.

**Shipped** (`internal/pages/auth_page.go`, design option (a) from the card, plus a cashier refusal):
- `kioskMaintenanceTill(ctx, d)`: the till is in self-order mode and its role refuses Register (`displayModeRoleRefusal`). Main, standalone and additional tills are unchanged.
- On such a till, staff sign-in through the kiosk lock:
  - Someone who can open Settings (`svc.Can(user, "settings")`, the same gate `/settings` uses) gets a **maintenance sign-in**. `display.mode` stays `self_order`, they land on `/settings` (Status, lock and exit-to-OS reachable), audit `kiosk_maintenance_sign_in` with the role.
  - Someone who can't (a cashier, sale-only since #3079) is **refused** with the existing `settings.display.satellite_mode_blocked` message. Their token is revoked, no session, audit `kiosk_mode_refused`.
  - A DB error on the permission check revokes the token and shows `auth.error.unavailable` (fail closed, honest message).
- **Lock hands the screen back to customers** (`lockDestination`): `POST /api/auth/logout` redirects (or `HX-Redirect`s) to `/` on such a till, and the anonymous-root seam sends that to `/self-order`, where the Android app pins again. `POST /api/auth/idle-lock` adds `X-UT-Lock-To: /`; `app.js`'s idle timer follows only that exact value, else `/login` as before.
- **Help:** `self-order.md` in en/de/tr/ar/fa, one sentence each, using each locale's existing word for satellite. Prose only, no screenshot changes.
- **Locale keys:** none new, so no language-pack follow-ups.
- **Docs:** ut-docs `architecture/pos-auth.md` (separate ut-docs PR).

**Tests (TDD), `internal/pages/kiosk_pin_login_test.go`:**
- `TestKioskPINLogin_SatelliteManagerSignInKeepsKioskAndReachesSettings`: mode and role stay consistent, session live, `GET /settings` is 200, audit rows.
- `TestKioskPINLogin_SatelliteCashierRefusedWithSatellitePointer`: refusal message, no session, mode unchanged, audit rows.
- `TestKioskPINLogin_SatelliteLockReturnsToKiosk`: plain and htmx Lock go to `/`, then anonymous `/` goes to `/self-order`, and idle-lock sends the hint. On a main/standalone till and on an additional till, Lock still goes to `/login` with no hint.

Red→green was checked by the author and, for the first two tests, independently by the reviewer: with the handler reverted, the sign-in tests fail (`303 → "/"`), and the Lock test fails with `"/login"`.

**Independent review** (Fable, fresh context, own worktree; the author was Opus 5.5):
1. *major*: after a maintenance sign-in, Lock or the idle auto-lock left the customer-facing satellite on the staff PIN keypad. On Android that keypad stays pinned, or is unpinned if the manager left Settings first. The help's "tap the lock" sentence was not true. **Fixed** with `lockDestination` for logout and idle-lock, plus the test above.
2. *minor*: a DB error on the permission check told a manager "this till is a satellite". **Fixed**: it now goes to `loginUnavailable`.
3. *minor*: the `selforder` template flag stays true during maintenance, so full-screen record dialogs opened from that session drop their status row (`record_dialog.html`). Closing the dialog brings back the rail. **Filed as a Backlog card** rather than widening this PR.
4. *nit*: a refused cashier gets a `login` row followed by `kiosk_mode_refused`. **Accepted**: this is the same shape as the #4295 kiosk-user refusal.
5. *nit*: Turkish "geri bırakmak" read awkwardly. **Fixed** to "geri vermek". The help says "a manager's PIN" while the gate is the settings permission. **Accepted**: this matches the topic's existing wording.

The reviewer also checked these, and none were issues: the role check on main, standalone and additional tills; the `/settings` gate agreeing with the login-time check; exposure compared with the old behaviour (narrower: `/` still sends every session to the kiosk, and idle auto-lock applies); no raw SQL outside `internal/data`; no new strings.

**Gate:**
- Passed: `gofmt`, `go build ./...`, `go vet`, `golangci-lint` (0 issues), `node --check web/public/app.js`, `guard-help-drift`, `guard-help-topics`, `guard-i18n`, and the other `ci.yml` build-job guards. `go test ./...` result is in the PR.
- Failed locally, but only for container reasons:
  - deadcode: the tool is built with go1.26 and this container has go1.27.
  - shellcheck-version: no shellcheck binary.
- **Not driven on real hardware:** a satellite needs a second till. The handler tests run the real mux, the auth middleware, the settings page and the login template.

**Verdict:** safe to merge.

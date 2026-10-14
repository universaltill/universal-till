# Review: kiosk PIN sign-in and cloud set_setting follow the till role (ut-docs#4295)

**Shipped:** two more doors around the Device profile confirm (ut-docs#2781, #4263) are now closed, and all of them share one helper in `internal/pages/till_role.go`:
- `validDisplayMode` accepts `""`, `register`, `backoffice` or `self_order`.
- `displayModeRoleRefusal(role, mode)` returns the locale key of the refusal: a satellite may only be `self_order`, and an additional till may not become `self_order` this way.

The call sites:
- **Kiosk-user PIN sign-in** (`auth_page.go`): on an additional joined till it is **refused**, not run as make-satellite. A PIN sign-in has nowhere to show the "this makes it a satellite" confirm, so the operator gets the existing message `settings.display.additional_kiosk_use_profile`, which points to Settings → Device profile. That path asks first and then tells the main till. On a refusal:
  - the kiosk token is revoked (the kiosk user never holds a session);
  - the browser's staff session is kept;
  - `display.mode` and the role do not change;
  - a `kiosk_mode_refused` audit row is written.
- **Cloud `set_setting`** (`cloudsync_wire.go`): `display.mode` is per-till, so it passes `refuseShopWideDirectiveOnAdditionalTill`. It is now checked against the enum and the role, and refused with an error before anything is written or re-derived.
- **`/api/settings/upsert`**: uses the same helper and now also refuses a value outside the enum (`err_invalid_mode`). This was the optional nit on the card.
- **Help:** `self-order.md` (en/de/tr/ar/fa) and the satellites bullet in `multitill.md` (en/de/tr/ar/fa). Screenshots were regenerated with `make docs-shots`.
- **Locale keys:** none new, so no language-pack follow-ups.

**Tests (TDD):**
- `TestKioskPINLogin_AdditionalTillRefusedWithDeviceProfilePointer`
- `TestKioskPINLogin_SatelliteStillEntersSelfOrder` (regression guard)
- `TestCloudSetSettingDisplayMode_FollowsTillRole`: 8 role/value cases, checks both rosters
- `TestUpsertDisplayMode_RefusesUnknownProfile`

With the three handler hunks reverted (helpers kept so it compiles), the author and the reviewer each saw every new refusal case fail: redirect to `/self-order`, `204` for `Self_Order`, and the directive applied on 5 cases.

**Independent review** (Fable, fresh context, own worktree; the author was Opus 5.5):
1. *minor*: the Arabic `multitill.md` bullet was missing the new sentence. **Fixed.**
2. *minor, out of scope*: a staff PIN with `next=kiosk` on a satellite kiosk still switches it to Register (`auth_page.go`). Simply refusing it would lock staff out, so it needs a design call. **Filed as ut-docs#4307.**
3. *nit*: the generic setters store `register` literally, while `applyDisplayMode` stores `""`. **Accepted.** This predates the change for upsert, and every reader treats both values as Register.
4. *nit*: a refused kiosk sign-in left no audit trace. **Fixed** with the `kiosk_mode_refused` audit row (the test asserts it). The log line now names the role.
5. *nit*: de/tr wording ("beigetretene Kasse", "Bağlı bir ek kasada"). **Fixed.**

The reviewer also checked these, and none were issues:
- No other writer of `display.mode` exists (`set_till_setting`'s whitelist has no `display.*`, and the admin pull never pushes `display.*`).
- No ut-cloud sender queues `display.mode`, so the enum check breaks no legitimate sender.
- No file writes, no raw SQL outside `internal/data`, and no hardcoded UI strings.

**Gate:**
- Passed: `gofmt`, `go build ./...`, `go vet`, `golangci-lint` (0 issues), `go test ./...`, and the `ci.yml` build-job guards.
- Failed locally, but only for container reasons:
  - deadcode: the tool is built with go1.26 and this container has go1.27.
  - shellcheck-version: no shellcheck binary.
  - `guard-docs-shots_test.sh`'s `.DS_Store` case.

  All three fail the same way on `main` here, and CI is the judge.
- **Not driven on real hardware:** making a joined additional till needs a second till. The handler tests run the real mux, the auth middleware and the login template.

**Verdict:** safe to merge.

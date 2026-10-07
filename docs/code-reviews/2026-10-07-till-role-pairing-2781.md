# Review: till role (additional | satellite) chosen at pairing (ut-docs#2781)

**Card:** ut-docs#2781. Every joined till gets a role — `additional` (works
offline, rings up sales; the default and what every pre-existing till is)
or `satellite` (kiosk, table-QR or order station that needs the main
till). The role is chosen when the till joins, can be changed later by a
manager on the main till's Tills page, travels to the joined till with the
admin pull, and gates that till's device profile. Until ut-docs#1154 a
satellite has no other runtime behaviour — the role is a label plus that
gate, and the manual says so.

## What shipped

- Migration `067_tills_role.sql` (append-only; checksum pinned in
  `shipped_migrations_test.go`): `tills.role` and
  `pending_pairings.requested_role`, both `NOT NULL DEFAULT 'additional'`
  with a `CHECK (… IN ('additional','satellite'))`, plus a WHEN-gated
  `AFTER UPDATE OF role` trigger bumping `sync_admin_version` so a role
  change moves the generation-keyed bundle (023's tills trigger watches
  only name/enrolled_at).
- `internal/data`: `TillRoleAdditional`/`TillRoleSatellite`,
  `ValidTillRole`, `InsertTillWithRole`, `RoleByID`, `SetRole` (returns
  the old role; same-role write is a no-op), `TillRow.Role` on
  `ListTills`/`TillByBearerHash`; pairing repo
  `CreatePendingRequestWithRole`, `ApproveWithRole` (blank keeps the
  requested role), `RoleForToken`.
- Pairing: `POST /api/sync/pair-request` accepts `role` (absent = additional,
  so older tills keep working); the approval card gets a role `<select>`
  preselected to what the till asked for, and the manager's choice at
  approval wins at `/api/sync/enroll` (`RoleForToken`). QR / short-code
  enrolment takes the joining till's own choice, validated **before** the
  one-time token is spent. "Join as" radios on `tills.html` and
  `setup.html`, `hx-include`'d by both tabs and the discovery button.
- `internal/pages/till_role.go`: `POST /api/tills/{id}/role` (Tills page
  Change role — `sync_management` gate server-side, 409 on a joined till,
  400 on a bogus role, 404 unknown till, `role_changed` audit row
  `{"from","to"}` on entity `till` only on a real change) and
  `POST /api/sync/till-role` (a joined till's own report, bearer-authed via
  `syncTill`, can only move the caller's own row, actor `system`).
- Admin pull (`GET /api/sync/admin`) answers with the requesting till's
  `role` on every poll, unchanged ones included; the replica keeps it as
  `sync.till_role` (`rememberOwnTillRole`, no-op on blank/garbage from an
  older main till). Enrol response and `ReplicaIdentity` carry it too.
- Device profile gate (`POST /api/settings/display-mode` and the
  `/api/settings` key side door): a satellite refuses `register` /
  `backoffice` with a specific 400 shown beside the form; an additional
  till choosing `self_order` gets a new PIN-less confirm dialog
  (`confirm_prompt.go` / `confirm_prompt.html`, `.show()` over `#ut-scrim`,
  cloned from the elevation prompt), and confirming tells the main till
  first — if the main till can't be told, nothing changes locally.
- Tills roster: Role column (authoritative `tills.role`, replacing the link
  hello's "replica"/"satellite" label), Change role button on the main till
  only with an `hx-confirm` naming till and target role (same native
  confirm precedent as Promote/Revoke on this admin page).
- Demo mode: both new POST routes classified as denied.
- i18n: 12 new keys in en/ar/fa/tr (`confirm.make_satellite_*`,
  `settings.display.satellite_mode_blocked`, `tills.col_role`,
  `tills.role.change_*`, `tills.join_role.*`). Manual: a new "Additional
  tills and satellites" section in `web/help/{en,de,ar,fa,tr}/multitill.md`
  (real prose in all five, spot-checked de/tr/ar against en), drift
  baseline updated for the pre-existing ar/fa/tr gap only.
- CSS: `.tills-roster { overflow-x: auto }` (same escape hatch as
  `.pairing-table` for a 1024px kiosk with tr/ar labels), `.join-role`
  using logical properties only; no new colours.

## Review

Author: Opus (Dev + Tester phases). Reviewer: Fable (different model),
one round, one fix. Verdict: **safe to merge** with the fix below.

**Found and fixed (high):**
1. `POST /api/sync/till-role` was not in `internal/auth/middleware.go`'s
   exact-path exempt list. The replica's `reportOwnRoleToMain` sends only
   its sync bearer, so the session middleware would have bounced it to
   `/login` before `syncTill` ran, and every "Make satellite" confirm on an
   additional till would have been refused as "can't reach the main till"
   with the main till right there — the documented `/api/sync/stock`
   failure class. The unit test missed it because the fixture mounted the
   main till on a bare mux. Fixed: path added to the exempt switch and
   pinned in `TestSyncPullPathsAreExempt`; the display-mode fixture now
   wraps the main till in the real `auth.Middleware`, and with the
   exemption removed that test reproduces the exact production symptom
   (`Can't reach the main till …` in the response, no `HX-Redirect`).

**Checked and accepted as-is (not bugs):**
- Manager gates are server-side: `POST /api/tills/{id}/role` runs
  `canPerform(sync_management)` first (403 tested for a cashier) and is an
  ordinary session route, not under the `/api/sync` exemption; approve's
  `authorizePairingManager` runs before the new role field is parsed.
- Repository pattern: all new SQL is in `internal/data` / the migration;
  `guard-data-access.sh` green. `shipped_migrations_test.go` pins 067
  over its comment-stripped statements (the raw sha256 differs by design).
- `tills` is dumped whole-row into the admin bundle (dynamic columns), so
  `role` reaches every replica's synced roster without an apply change;
  `ownTillRole`'s fallback to `RoleByID` on that copy is sound. A replica's
  local `SetRole` fires 067's trigger on its own `sync_admin_version`, which
  nothing reads (`have=` is the `sync.pull_version` setting) — harmless.
- Older peers: a till that sends no `role` is enrolled as additional; a
  main till that sends no `role` in the pull leaves `sync.till_role` alone;
  promote wipes `sync.%` so the role never survives into a main till.
- Offline-first: the only new network call is the make-satellite confirm,
  a deliberate main-till-authoritative write-through (same shape as
  users/settings/catalog apply) that refuses and changes nothing when the
  main till is unreachable; nothing on the checkout path is touched. The
  confirm dialog is `.show()`-based (test asserts no `showModal`); the
  satellite refusal is a form-side message, not a modal.
- Untrusted strings (till name in `hx-confirm`, device name in the
  select's `aria-label`) go through html/template escaping; the 400 bodies
  are plain `http.Error` text rendered via `textContent`.
- `hx-confirm` (native `confirm()`) on the Tills page follows the existing
  Promote/registers/catalog precedent on admin surfaces, not kiosk/checkout.
- Low, accepted: when an additional till's make-satellite confirm also
  needs elevation and the elevated retry then fails (main till down), the
  elevation dialog closes but the confirm dialog stays open over the
  message; Cancel dismisses it and the retry path is sound. Also low: the
  main till is told before `applyDisplayMode`, so a failed local save
  leaves the till a satellite still in register mode — the next attempt
  (satellite + self_order) is allowed, so it self-heals.
- No real client/shop names in test data (Kiosk, Counter, Till 2, Order
  station); secrets are placeholders (`token-abc`, `role-secret`).

**Deferred (Backlog card notes):**
- `fleetlink.Hello.Role` still reports "replica" for every joined till;
  the roster now shows the authoritative column instead. Wiring the stored
  role into the hello is ut-docs#2741 (already referenced in code).
- A satellite whose role was flipped by a manager while it sits in
  `register` mode keeps running as a register until someone changes its
  profile — the gate only refuses new choices. The manual states that a
  satellite has no other behaviour yet; the behaviour itself is
  ut-docs#1154.
- Language packs: 12 brand-new `en.json` keys need the usual
  `ut-plugin-language-*` follow-up PRs in this cycle (`main` goes red on
  `lang-pack-drift` until they land, as expected).

## Verified

- `go build ./...`, `go vet` and `gofmt -l .` clean on the affected
  packages. `golangci-lint` could not run in this container (binary built
  with an older Go than the module targets) — CI runs it.
- `go test` green for `internal/data`, `internal/db`, `internal/auth`,
  `internal/fleetlink` (full packages) and `internal/pages` (full package,
  no `-race`).
- CI guards run locally and green: i18n, help-topics, help-drift,
  no-showmodal, data-access, demo-env, page-http-error,
  no-inline-handlers, compliance-claims, competitor-naming, core-neutral,
  netaccess, migration-version-collision, card-data-schema, kiosk-engine,
  htmx-loaded, osk-loaded, autofill-suppression, emoji-font,
  plugin-menu-read, pipefail-grep-q, price-history-sync,
  plugin-settings-bump.
- TDD re-verified personally (revert the production fix → run the test →
  confirm the claimed failure → restore → green), each inside one command
  so no reverted state ever persisted:
  1. `TestDisplayMode_SatelliteRefusesRegisterAndBackoffice` with the
     satellite gate in `settings_page.go` disabled → "register on a
     satellite: 204, want 400".
  2. `TestTillRoleChange_ManagerOnlyAuditedAndShownOnRoster` with the
     `canPerform` gate in `till_role.go` disabled → "cashier: 204, want
     403".
  3. `TestTillsRole_CheckConstraintRejectsBogusValue` with the `CHECK`
     removed from 067 → "tills.role accepted 'replica'".
  4. (reviewer's own fix) `TestSyncPullPathsAreExempt` failed on
     `/api/sync/till-role` before the middleware change and passes after;
     `TestDisplayMode_AdditionalToSelfOrderAsksFirstThenBecomesSatellite`
     fails with the unreachable-main message when the exemption is removed.
- Manual spot-checked by reading: all five `multitill.md` translations
  carry the four new bullets with matching content (join-as choice,
  approval-card role, Change role with confirm + audit + ~30 s sync, the
  device-profile gate and make-satellite confirm).
- Accepted gap: Playwright e2e not run (`e2e/node_modules` absent in this
  container, as in every previous LAN-pairing review); no physical
  two-till run. `tills-pairing-layout-1548.spec.ts` measures the
  `.pairing-table` overflow the new Role column widens — it asserts the
  table scrolls inside its own box, which the unchanged
  `overflow-x: auto` rule still provides.

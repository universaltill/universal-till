# Auth `Can()` sweep — plugin management (ut-docs#706)

## What shipped

`ut-docs#706` is another of the 5 subsystem-scoped successor cards splitting
`ut-docs#555` (itself split from `ut-docs#520`), following the exact pattern
already reviewed and merged in `ut-docs#709` (PR `universal-till#343`,
migration 042). This card's scope: `internal/pages/plugin_api.go`,
`plugin_settings_page.go`, `plugins_store_page.go`, `update_api.go` (18 real
`isManagerOrAuthOff` call sites, matching the issue's estimate).

Replaced every `isManagerOrAuthOff(r)` call in those 4 files with
`canPerform(d, r, "plugin_management")` — the shared helper already added by
`#709` in `internal/pages/authz.go` (unchanged by this card). New migration
`043_plugin_management_permissions.sql` adds the `plugin_management` action
to the `#554` catalog, additive and seeded identically to 039/042 (manager/
admin/super_admin granted, cashier denied — no existing till's access
changes).

Covered endpoints: plugin permission grant/revoke, trust-level changes,
install-from-marketplace, enable/disable/uninstall/update/rollback,
import-from-file, export, the plugin settings editor (view + save), the
marketplace store lifecycle (download/install/delete-download), and the
self-update apply/check/schedule endpoints (folded in as a judgment call —
see below).

`ut-docs#555`'s tracking umbrella now has 2 of 5 successors merged: `#709`
(reports/EOD/audit/journal) and this card (plugin management). Remaining:
`#710` (Settings page), `#707` (Data/sync/pairing), `#708` (Print/import/
misc) — all `complexity:medium`, all Ready.

## Independent review (Opus, different model from the Sonnet that wrote it)

Full review spawned in an isolated worktree (`isolation: "worktree"`, per
`ut-docs#386`) with instructions to actually run build/vet/tests/guards and
independently re-verify the swap by breaking it two different ways and
re-running the real-session tests.

**No blocking findings.**

**Non-blocking, one applied, two deferred:**

1. **Applied**: the self-update endpoints (`/api/update/apply`,
   `/api/update/check`, `/api/settings/update-schedule`) govern the till
   *application's* own binary self-update, not plugin lifecycle — folding
   them onto `plugin_management` means a future custom role granted "manage
   plugins" would silently also get "apply a new POS build to this till."
   Inert today (no custom-role feature exists), but the migration comment
   was missing the "a future card is free to split this out" escape hatch
   042's own judgment-call note has. Added directly to `043_*.sql`.
2. **Deferred, non-blocking**: two of the four new test functions
   (`TestPluginManagementEndpoints_RealSessionGatesByRole`,
   `TestPluginStoreEndpoints_RealSessionGatesByRole`) rely on ambient
   `UT_AUTH` rather than explicitly pinning it like the other two new tests
   do. Verified safe — CI only sets `UT_AUTH=off` for the e2e server run,
   never for `go test`, and an accidental "off" would fail loudly (every
   role would pass, including the cashier-denied subtest), never
   false-pass. Left as-is; a cheap follow-up if it bothers a future reader.
3. **Deferred, non-blocking**: the two endpoint tables above assert
   cashier-denied vs. manager-past-gate only (not the full cashier/manager/
   admin/super_admin matrix the settings-page and update-schedule tests
   use) — acceptable given the weak `!= 403` assertion is already a
   deliberate scope reduction (many of these 14 endpoints need real plugin/
   marketplace state to reach 200, which this minimal fixture doesn't
   provide) and is partly self-checking via the paired `cashier_denied`
   subtest.

## Verified beyond automated tests

- **TDD re-verification, probe (a)**: reverted the swap in
  `plugin_settings_page.go` back to `isManagerOrAuthOff(r)` in both sites,
  re-ran `TestPluginSettingsPages_RealSessionGatesByRole` — failed exactly
  on `super_admin` (got 303, want 200), the only role where the two gates
  actually diverge (`isManagerOrAuthOff`/`IsManager()` only recognizes
  manager/admin; `canPerform`/`Can()` also grants super_admin per `#554`'s
  seed). Cashier/manager/admin passing under both gates is the intended
  behavior-preservation criterion, not a test weakness. Restored after.
- **TDD re-verification, probe (b), stronger**: mistyped the action string
  to `"plugin_managementXX"` in `update_api.go` —
  `TestPostSettingsUpdateSchedule_RealSessionGatesByRole` failed for
  **manager, admin, and super_admin** (403, want 204), proving the test
  resolves the action through the real seeded catalog and
  `d.AuthSvc.Can()`, not a role shortcut. This closes the exact gap `#709`'s
  own review found (no test ever really executing `Can()`). Restored after.
- **Migration load-bearing check**: temporarily excluded `043_*.sql` from
  the migration glob — `TestAuthRepo_HasPermission` (in
  `internal/data/auth_repo_test.go`, which runs against a REAL migrated DB,
  not `internal/pages`' hand-rolled fixture) failed with "expected manager
  granted plugin_management by the seed data." Confirms 043's seed actually
  produces the intended grants, not just a passing hand-rolled test fixture.
  Restored after.
- Confirmed **18/18 call sites swapped**, all with the correct
  `"plugin_management"` action string, zero remaining
  `isManagerOrAuthOff` in the 4 files, and no `d` shadowing anywhere (every
  site sits inside a `registerX(mux, d *common.Deps)` or
  `handleX(d *common.Deps) http.HandlerFunc` closure).
- Confirmed no other file's `isManagerOrAuthOff` site was accidentally
  touched — remaining call sites are all other subsystems (settings, data/
  import, sync, invoices, receipt designer, print, pairing, ask,
  backoffice), correctly out of this card's scope per its own non-goals.
- Confirmed `internal/plugins/manifest_verifier.go` and the Ed25519
  verification path are completely untouched by this diff — this card is
  the HTTP-layer auth gate only, never the plugin trust chain.
- Confirmed the `guard-docs-shots.sh` pass is legitimate, not a fluke:
  `web/help/en/plugins.md`'s `routes[0]` is `/plugins`; none of the 4 edited
  files register that specific route (`/plugins/store` is routes[1],
  `/plugins/{id}/settings` is routes[2], the rest are `/api/*`) — so all
  four are correctly excluded from the surface hash by the guard's own
  documented "routes but none is routes[0]" rule. No manual topic is owed
  either: the change is invisible to every real role's screen.
- Full `go build ./...`, `go vet ./...`, `go test ./...` (all 40 packages,
  zero failures) and all 8 CI guard scripts
  (`guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-docs-shots.sh`, `guard-compliance-claims.sh`) run clean, both by
  the reviewer and re-confirmed after applying the one migration-comment
  fix.
- No real client/shop name anywhere in the diff — test data is generic
  (`u1`, `p1`, `some-plugin`).
- No file-write handler added (both recurring `os.MkdirAll`/
  `paths.Data(...)` bug classes this pipeline watches for are N/A — the
  diff is a pure auth-gate swap with no new file I/O).

## Scope notes

No UI/template changes and no shop-owner-visible behavior change for any
real role today (pure backend permission-check refactor, confirmed by the
docs-shots route analysis above) — `web/help/` manual topics deliberately
not touched.

## Verdict

Safe to merge. No blocking findings; the one non-blocking migration-comment
gap was fixed directly; the two remaining non-blocking test-hygiene notes
are carried forward here as a standing record for whoever next touches
these test files, not owed a follow-up card of their own.

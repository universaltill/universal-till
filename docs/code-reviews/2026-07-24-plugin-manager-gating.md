# 2026-07-24 — Plugin management now requires a manager session

## Context
Spec-audit gap (`ut-docs/QUEUE.md`): "no marketplace audit trail/filter UI,
no permission or telemetry-opt-in badges" — the entry included a claim that
"Manager-PIN install gating itself does work
(`authorizer.go` `requireManagerPIN`)". Investigation found that claim was
**false**: `internal/plugins/authorizer.go`'s entire `Authorizer` type
(`CheckPermission`, `AuditLog`, `requireManagerPIN`) has zero callers
anywhere in the codebase — it was never instantiated, never wired into any
handler. `internal/pages/plugin_api.go`'s install/uninstall/enable/disable/
update/rollback/permission-grant/permission-revoke/trust-level/upload/
import handlers had no role check of any kind: any authenticated user,
including the lowest-privilege cashier role, could manage plugins.

## Design
Applied the codebase's existing, already-proven gate —
`isManagerOrAuthOff(r)` (`settings_page.go`, same `pages` package; already
used for printer settings, idle-lock, and today's telemetry opt-in toggle)
— to every mutating plugin-management handler, as the first check (after
any pre-existing HTTP-method check), before any side effect. Left
deliberately ungated: `/api/plugins/marketplace` (catalog browsing) and
`handleCheckUpdates` — both read-only, no DB write, no download, no install.

`handleInstallFromMarketplace` returns its rejection through the handler's
own JSON envelope (`writeInstallResponse`, new `message_key:
"plugins.install.error.forbidden"`) to match its existing response
contract, rather than a bare `http.Error`; new i18n key added to all 4 core
locales and wired into `plugin_install_modal.html`'s client-side message
mapping.

## Independent review — caught a critical gap before merge
Opus-model review, explicitly instructed to search more broadly than the
one file this change touched.

**Fixed (HIGH — the fix was incomplete without this):**
- A **second, fully-live, parallel plugin install path** was missed
  entirely: `internal/pages/plugins_store_page.go`'s `registerPluginStoreAPI`
  (`POST /api/plugins/store/download`, `/install`, `/delete-download`) is
  registered on the same mux via `registerPluginStore`
  (`internal/pages/init.go`) but is a completely separate route
  registration from `registerPluginAPI` — the file this change originally
  touched. This is the actual install button on `/plugins/store`
  (`PluginStoreHandler`), so a cashier could still install/download/delete
  plugins through the store page despite every route in `plugin_api.go`
  being gated. Fixed: same `isManagerOrAuthOff` gate added to all three
  handlers, first statement, before any side effect. New test
  (`TestPluginStoreEndpoints_RejectWithoutManagerAuth`) covers all three;
  the read-only store *page* render itself stays ungated, consistent with
  catalog browsing elsewhere.

**Confirmed correct (verified independently, not just accepted my
claim):**
- Full route coverage within `plugin_api.go` re-derived independently from
  `registerPluginAPI` — all 12 mutating routes gated; `/api/plugins/{id}
  /settings` (a different file, already gated pre-existing) and
  `/api/plugins/entries/{plugin}/{key}/action` (a plugin runtime event, not
  lifecycle management) correctly out of scope.
- `isManagerOrAuthOff` fails closed: traced `auth.Disabled` (exact-match
  `"off"` only), `auth.FromContext` (returns `ok=false` with no session),
  and `IsManager()` (`Role == "manager" || "admin"`) — no edge case
  (nil user, empty role, malformed session) slips through as manager.
- `handleInstallFromMarketplace`'s `writeInstallResponse` genuinely returns
  403 (not 200), and the new i18n key is wired end-to-end — all 4 locales
  carry an identical key set, `guard-i18n.sh` passes.
- The gate test exercises real route registration via `mux.ServeHTTP` with
  `UT_AUTH` left unset (not `"off"`), so a missing gate would surface as
  200/500, not silently pass; a typo'd path would 404, which also fails the
  assertion — the test can't pass by accident.
- The three pre-existing tests updated to add `t.Setenv("UT_AUTH", "off")`
  still exercise their own original logic (version-required,
  traversal-rejection, operator-visible-status) — the gate bypass doesn't
  mask anything unrelated.
- Every gate is the first statement, before `ParseForm`, any download, DB
  write, or status-store save — no side effect precedes a gate anywhere.

## Verification
`go build ./...`, `go vet ./...`, `bash scripts/ci/guard-i18n.sh` (688
template keys, all locales match), `go test ./...` (full repo) — all
green. New tests: `TestPluginManagementEndpoints_RejectWithoutManagerAuth`
(13 routes in `plugin_api.go`), `TestPluginStoreEndpoints_RejectWithoutManagerAuth`
(3 routes in `plugins_store_page.go`, added after independent review caught
the gap), `TestPluginCatalogBrowsing_DoesNotRequireManagerAuth`.

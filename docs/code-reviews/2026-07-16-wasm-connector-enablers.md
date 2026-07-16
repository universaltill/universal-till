# Code review — WASM host enablers for configurable connector plugins

**Date:** 2026-07-16 · **Branch:** feat/wasm-connector-enablers · **Reviewer:** self (Claude)

## Why

ADR-0014 connectors (ERP/webhook) are **configurable per install** and their
target host is only known from settings. Two gaps blocked them:

1. A WASM plugin receives only the event on stdin — it had **no way to read its
   own settings** (endpoint URL, auth token).
2. The HTTP host function checks `net:<exact-host>`, so a plugin whose endpoint
   is set at install time could never be granted the right host at build time.

## Changes

- **`settings_get` host function** (`wasm_hostfns.go`): a plugin reads one of
  its OWN global settings by key. Own-plugin only (keyed on the caller's
  `pluginID` from host state) — no cross-plugin access, no extra permission
  needed. Values are stored as JSON; the host unwraps a JSON string so the
  guest gets the plain value (`https://…`, not `"\"https://…\""`). Returns
  `hostErrNotFound` when unset. Backed by new `PluginRepo.GetPluginSetting`.
- **`net:*` wildcard** (`wasm_hostfns.go` `hostHTTPRequest`): when the exact
  `net:<host>` isn't granted, the call is allowed if the plugin holds `net:*`.
  Configurable connectors declare `net:*` and are review-gated accordingly
  (ADR-0006). The exact-host grant path is unchanged for narrow plugins.

## Security notes

- `settings_get` exposes only the calling plugin's own settings — the same data
  a manager typed into that plugin's settings editor. No escalation.
- `net:*` is a broader grant (any https host, or http to loopback). It is a
  declared, granted, review-gated permission — a plugin without it is unchanged.
  This matches how the runtime already tracks `net:*` (`pluginHasNetPermission`,
  `hasNet` wider deadline). The scheme allow-list (https, or http to loopback)
  still applies.

## Tests

Extended the wasip1 test guest (`testdata/hostfn_guest`) to call `settings_get`.
Two new host tests, both green:
- `TestHostHTTPWildcardNet` — grants `net:*` (not the exact host) and asserts an
  outbound call is authorised.
- `TestHostSettingsGet` — seeds a plugin setting and asserts the guest reads the
  unwrapped value via `settings_get`.

Full `go build ./...`, `go test ./...`, and `guard-data-access.sh` green.

## Follow-up

These unblock the reference webhook connector plugin (ADR-0014 rollout step 2):
subscribe `sale.completed` → `settings_get` the endpoint/auth → `http_request`
(under `net:*`) → queue on failure in `storage`.

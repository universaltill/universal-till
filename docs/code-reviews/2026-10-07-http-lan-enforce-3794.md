# Review: http:lan required for a LAN address on the http path (ut-docs#3794, ADR-0121 §3)

Date: 2026-10-07 · Lane: lane:cloud-24 · Built by Opus 5.5 (complexity:medium, security), reviewed by
Fable in a fresh context, with a mutation check in its own worktree.

## What shipped
- Owner decision on ut-docs#3794, option A: enforce ADR-0121 §3. For `http_request` and `http_open`,
  a non-public address now needs `http:lan` **on top of** the exact grant (`net:<host>`,
  `net:@setting:<key>`, or an endpoint setting).
  - Before this change, any exact grant reached the LAN over https.
  - Exception: an exact grant for a loopback host *name* (`localhost`, `127.0.0.0/8`, `::1`, mapped
    forms) reaches loopback without `http:lan`, so a local Ollama still works. Any other name that
    resolves to loopback needs `http:lan`.
- `wasm_egress.go`:
  - `admitHTTPHop` now probes `http:lan` on every hop and records it per hop (`httpLAN`, overwritten
    on each redirect like `lanOnly`).
  - The dialer (`egressPolicy.needsHTTPLAN`) refuses fail-closed. The refusal is audited through
    `CheckPermission(…, "http:lan")`, declared or not, on a ctx detached from the dial deadline.
  - `tcp_open` passes `needsHTTPLAN=false` and is unchanged. Cloud-metadata, till-own-port and
    unspecified-address refusals run first and are unchanged.
- Migration sweep, done before enforcing: every first-party `ut-plugin-*` manifest was read. Only
  `ut-plugin-integration-webhook` (`net:@setting:endpoint_url` → a LAN ERP) needed `http:lan`; it is
  re-published as v1.2.0 (its PR #9, merged). The cloud marketplace API is unreachable from a cloud lane,
  so third-party listings were not enumerated. No third-party vendors are known yet.
- Docs: `README.md`, `docs/plugin_guidelines.md`; ut-docs `reference/plugin-host-functions.md` and
  `architecture/wasm-runtime.md`.

## Findings (Fable)
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `admitHTTPHop` doc said no audit row for an undeclared http:lan; the dialer now audits. | Fixed. |
| 2 | minor | A refused LAN request is audited once per request. A pre-#3794 plugin polling a LAN host would write one row per attempt. | Accepted: matches the per-request `net:<host>` denial audit and the owner's "audited denial". Release note below. |
| 3 | minor | Three `ListPermissions` reads per hop (validation, net, granted). | Accepted: correct and fail-closed; a perf follow-up if it ever shows up. |
| 4 | minor | Pre-existing: with several resolved IPs, a later dial error overwrites an earlier denial, so that path gets no audit and a generic error. | Deferred: changing which error wins changes `-2`/`-3` semantics outside this card. |
| 5 | nit | README "tcp_open follows the same rule" read as if tcp needed http:lan. | Fixed. |
| 6 | nit | Zone literals (`::1%lo`) are never loopback names, so they are refused without http:lan. | Accepted: fails closed. |
| 7 | nit | The audit write used the dial ctx and could fail near the deadline. | Fixed (`context.WithoutCancel`). |

No bypass found: redirect hops, `http_open`, validation grants, the endpoint path, the plain-http
path, nil grants, and the `grantedPermissions` error path (→ denied) were all checked.

## Verified
- Red→green, checked twice: once by the author in place, once by the reviewer in its own worktree.
  With the new `check()` rule disabled, the new cases in `TestHTTPEgress`, `TestHTTPSettingBoundGrant`,
  `TestEgressDialerHTTPLANRule` and `TestEgressDialerAuditsMissingHTTPLAN` fail; restored, they pass.
- `go build ./...`, `go vet`, `gofmt`, the full `go test ./...`, and the data-access, netaccess,
  i18n, help-topics, core-neutral, compliance, competitor and README-link guards all pass.
- `guard-deadcode-baseline` could not run locally (the tool was built with go1.26; the module needs go1.27). CI runs it.
- No UI change: the `http:lan` consent string ("devices on your shop's local network") is still accurate.
  The `setting_bound_hint` ("including one on the shop network") is now true only together with
  `http:lan`, as the one setting-bound http plugin (webhook v1.2.0) has. It is left unchanged to
  avoid translation churn.

## Release note
Installed plugins that reach a LAN host over http through an exact grant, without `http:lan`, stop
reaching it after this till update. The fix is to update the plugin to a version declaring `http:lan`
and grant it. The webhook connector from v1.2.0 needs `http:lan` granted after the update, because a
new permission arrives ungranted.

**Verdict: safe to merge.**

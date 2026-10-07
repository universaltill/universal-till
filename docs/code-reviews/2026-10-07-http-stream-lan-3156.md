# Review: streaming HTTP, http:lan and metadata refusal (ut-docs#3156, ADR-0121 build 4)

Date: 2026-10-07 · Lane: lane:cloud-54 · Built by Opus 5.5 (complexity:hard, security), reviewed by
Fable in a fresh context and an isolated worktree.

## What shipped
- **Cloud metadata always refused** (`wasm_egress.go` `isCloudMetadataIP`, in `egressDialer.check`):
  `169.254.169.254` and `fd00:ec2::254`, including the IPv4-mapped, NAT64 and 6to4 forms, are
  refused at dial time for `http_request`, `http_open` and `tcp_open`, whatever the grant.
  Before this change an exact `net:169.254.169.254` reached it.
- **`http:lan`** (`admitHTTPHop`, shared by the first hop and every redirect hop):
  - plain `http` is allowed to a host with an exact `net:<host>` / `net:@setting` grant, or to the
    host of one of the plugin's own `type: "endpoint"` settings (`endpointSettingHostMatch`);
  - a hop admitted that way is recorded `lanOnly`, and the dialer refuses a public IP for it;
  - an endpoint host admitted only by `http:lan` is LAN-only for https too, unless the plugin also
    holds `net:*`;
  - without `http:lan`, behaviour is unchanged.
- **`http:stream`** (`wasm_httpstream.go`): `http_open` / `http_write` / `http_status` / `http_read` /
  `http_close`.
  - Request bodies are buffered and sent on the first status/read; responses are streamed.
  - The request body and the response bytes read are each capped at `limits.http_body_mb` (→ `-5`).
  - At most 4 handles per event (→ `-6`). Handles live on the per-event `hostState`, and
    `handleEvent` closes them all when the event returns.
  - Each read has a 30 s idle timeout and is also bounded by the event deadline. Demo mode is denied.
- New codes `-5` (quota exceeded) and `-6` (busy). `README.md` and `docs/plugin_guidelines.md` are
  updated. In ut-docs: `reference/plugin-host-functions.md`, `architecture/wasm-runtime.md`, and the
  ADR-0121 amendment's function pointer.
- Out of scope: `upload_*` is split to ut-docs#3793 (blocked on #3160). `MaxSupportedWasmABI` is
  unchanged. Whether an exact grant without `http:lan` may still reach the LAN is left to an owner
  decision, ut-docs#3794.

## TDD
- **Re-verified by the reviewer** (mutate → fail → restore → pass):
  - metadata refusal → `TestEgressDialerRefusesCloudMetadataEvenWhenExact`;
  - the `closeAll` defer → `TestHTTPStreamHandlesClosedWhenEventReturns`;
  - the lanOnly dial check → `TestEgressDialerLANOnly` and 3 `TestHTTPLANEgress` subcases.
- **Re-verified by the orchestrator for the review fixes:**
  - the new `TestHTTPLANEgress` subcase "net:* still reaches a public endpoint-setting host…"
    failed with `code -2` before fix 1;
  - `TestAuditDeclaredDenialOnlyForDeclaredPermission` failed ("undeclared http:lan wrote 1 audit
    rows") with the old always-audit behaviour.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | `admitHTTPHop` checked `endpoint` before `wildcard`, so a `net:*` + `http:lan` connector whose endpoint setting named a public host was made LAN-only and refused | Fixed: `lanOnly: !wildcard`, plus a regression subcase |
| 2 | minor | Every plain-http refusal audited `http:lan` "not declared", even for plugins that never asked for it, so a poll loop floods `audit_log` | Fixed: `auditDeclaredDenial` audits only a declared-but-ungranted `http:lan`, plus a test |
| 3 | minor | 4 handles × `http_body_mb` of request body can sit in host RAM per event (up to 256 MiB on desktop) | Accepted: ADR-0121 §3 defines the cap per handle, the ceiling is a host constant, and the platform concurrency caps (4/16 calls) bound it. Revisit if build 1's device measurements show pressure |
| 4 | nit | Comment named the removed `httpNetPermission` | Fixed |
| 5 | nit | `admitHTTPHop` runs about 3 permission queries plus a manifest read per hop | Accepted: at most 6 hops per request, all local SQLite |
| 6 | nit | `egressGrants.exact` accumulates across a redirect chain, so a grant revoked mid-chain still applies (pre-existing) | Accepted: academic, not widened by this diff |

Also checked and found sound:
- mixed public/private DNS answers are judged per IP;
- IPv6 brackets and zones are normalised;
- endpoint values with userinfo are rejected by `ValidEndpointURL`;
- `net:validation:` stays public-only with `http:lan` + `net:<host>`;
- the quota boundary means a guest never receives more than the limit;
- guest memory is bounds-checked before reading a body that cannot be re-read;
- 307/308 replay uses `GetBody`;
- no URL or token is logged.

## Verification
- `gofmt -l .` empty; `go build ./...`; `go vet ./...`; `go test ./... -timeout 40m` passed.
- `-race` on the new tests passed (`HTTPStream|HTTPLAN`, 459 s). The whole package under race
  exceeds the local timeout: `TestHTTPEgress` alone takes 416 s.
- Guards run locally, all green: card-data-schema, competitor-naming, compliance-claims,
  core-neutral, data-access, netaccess, kiosk-engine, i18n, no-showmodal, readme-local-links,
  help-topics, help-drift, migration-version-collision, plugin-settings-bump, page-http-error,
  no-inline-handlers.
- `golangci-lint` and `guard-deadcode-baseline` could not run locally (their binaries are built with
  go1.25/1.26 and the module needs go1.27); CI runs them.
- No UI surface, so no visual check is applicable.

## Verdict
Safe to merge once CI is green.

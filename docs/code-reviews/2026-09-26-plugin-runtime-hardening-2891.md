# Review — plugin runtime security hardening (ut-docs#2891)

**Change:** WASM plugin memory capped at 64 MiB (`WithMemoryLimitPages`);
`http_request` and `tcp_open` go through one egress dialer that checks the
**resolved IP at connect time** — non-public addresses (loopback, RFC1918,
link-local incl. 169.254.169.254, CGNAT, ULA, multicast, unspecified,
IPv4-mapped/NAT64/6to4 forms) only with the plugin's exact `net:<host>` /
`tcp:<host>:<port>` grant, `net:*`/`tcp:*` public-only, the till's own listen
port always refused, redirects re-checked per hop (≤5), no proxy, no
keep-alive, timeouts; per-handle TCP authorisation re-checked on every
read/write. Plugin **id and version** validated (single safe path segment,
no trailing dot or Windows device names) before every filesystem use —
install, import, marketplace verify, rollback, version history, export,
uninstall (HTTP, cloud directive, LAN prune), download temp file. Missing
manifest `runtime` → `wasm`, never `go`; migration **049** corrects stored
`go` rows whose entrypoint is a `.wasm`. Docs: ut-docs
`reference/plugin-host-functions.md`, `architecture/wasm-runtime.md`.
Author: Opus 5.5. Reviewer: Fable (two passes).

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | `tcp_open` bypassed the new policy (a `tcp:*` plugin could reach the till's own API port, cloud metadata, LAN via rebinding) | fixed: same egress dialer; tests |
| 2 | major | Rollback / version history joined an unvalidated version (`../../other.plugin/1.0.0` → another plugin's module under this plugin's grants) | fixed: validated in handler (400) and manager; tests incl. `%2e%2e` |
| 3 | (found while fixing 2) | **Uninstall accepted id `.` → deleted every installed plugin's files** (HTTP handler and `cloudRemovePlugin`); export version `..` exported the whole plugins dir; download `.part` path escaped the temp dir | fixed + regression tests |
| 4 | major (pass 2) | tax-tr (ÖKC) declares `tcp:*` but talks to a loopback bridge / LAN register → no longer reachable | accepted as the secure default (no live Turkish tills); exact-grant for admin-configured endpoints = **#2899 (p1)**, cc Turkey track |
| 5 | minor | id `com.foo.` aliases `com.foo` on Windows; device names `nul`/`com1` | fixed (`windowsUnsafe`) + tests; all 17 real ids re-checked |
| 6 | minor | exact grants compared case-/IDNA-sensitively | fails closed; noted on #2899 |
| 7 | minor | pre-existing `runtime='go'` rows for wasm plugins | migration 049 (idempotent, tested apply + replay) |
| 8 | minor | redirect edge cases unpinned | pinned: 307/308 with body, relative / scheme-relative Location, `0177.0.0.1`, `2130706433`, `0x7f.1`, `[::ffff:127.0.0.1]` |

**Checked, no issue (reviewer):** single wazero runtime → cap applies to every
instantiation; payment-stripe exits 2 identically with and without the cap
(its own `declined`, no secret key configured); DNS rebinding / mixed A records
(dialer resolves, checks every IP, dials by IP); proxy env ignored; Host/SNI;
HTTP/2 single-use; response caps kept; runtime default on every manifest load
path; all 8 shipped wasm plugins start at 43–45 pages (≈2.8 MiB).

**Verification:** `go build ./...`, `go vet ./internal/...`, gofmt;
`go test -race ./internal/plugins/...` (~31 min) and targeted
pages/db/server tests; guards data-access + migration-collision.

**Behaviour changes to announce:** `net:*`/`tcp:*` no longer reach LAN or
loopback (webhook-to-LAN-ERP and tax-tr ÖKC need #2899).

**Verdict:** safe to merge.

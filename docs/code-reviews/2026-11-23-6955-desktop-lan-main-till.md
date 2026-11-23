# Review record: desktop main till reachable from the LAN (ut-docs#6955)

## Change
- `cmd/unitill-desktop`: child till gets `UT_LISTEN_ADDR=:<port>` and `UT_LAN_GATE=1` (the window still uses loopback).
- `internal/lanaccess`: listener wrapper; local peers (loopback or the machine's own addresses) always served, others closed before any read unless `Allowed()`. No policy = closed.
- `internal/server`: wrap only when `UT_LAN_GATE=1`, before the TLS wrapper. Pi/Android/Docker never set it, so they are unchanged.
- `internal/pages/lan_access.go`: setting `lan.access` = auto|on|off (auto = pairing window of 10 min after "Show pairing code", or at least one enrolled till); Settings fragment + POST, both `sync_management`; denied in demo; `lan.access` is per-till (not synced).

## Security checks
- IPv6 zones stripped, v4-mapped loopback parsed as loopback, unparsable address = not local (closed). A remote peer cannot present one of our own interface addresses as its source.
- Gate sits before TLS: a refused peer never reaches the handshake. Fallback bind to another port keeps the wrapper (wrap happens after bindListener).
- Does not widen ADR-0157: the gate only decides who can connect; internal/languard still limits LAN callers to /api/sync/*, /healthz and guest pages. ADR-0086 unaffected (main stays sole fiscal recorder; no master/slave wording).
- New endpoints: `canPerform(sync_management)`; with UT_AUTH on, an unauthenticated user or cashier gets 403 (test added).
- lanAllowed: window check and cache are under one mutex; policy cached 2 s (a setting change takes up to 2 s). DB read under the mutex only on cache miss.

## Findings
| # | Sev | Finding | Status |
|---|-----|---------|--------|
| 1 | blocker (CI) | 6 new `en.json` keys had no `en.meta.json` entries; `guard-i18n-meta.sh` failed | Fixed |
| 2 | gap | No test that the new endpoints refuse a caller without `sync_management` | Fixed (`TestLANAccessSettingEndpoints_CashierRefused`) |
| 3 | info | Connections accepted while open (keep-alive) stay up after the window closes or mode goes to Never; only new connections are gated. Short idle timeouts bound it. | Accepted |
| 4 | info | Auto mode with a revoked-only till list still counts any listed till; same as Pi behaviour of an enrolled main. | Accepted |

## Verification
`go build ./...`, `go vet` (lanaccess, pages, server, cmd/unitill-desktop, data) clean; `go test` of lanaccess (real TCP listener: local served, remote dropped until allowed, closed gate drops remote), server, cmd/unitill-desktop, web/..., data, and pages `-run 'LAN|Demo|SettingsWriteGuard'` all ok; guard-i18n, guard-i18n-meta, guard-data-access green. Not run: Playwright e2e, a real second device across a LAN, Windows firewall prompt, visual look at the Settings fragment (not viewed).

Pre-flight: help topic `web/help/` for Settings not updated in this diff (follow-up if the owner wants a manual line); en.json + en.meta.json done; ADR n/a (follows ADR-0086/0157/0198).

Verdict: approve after fixes 1-2.

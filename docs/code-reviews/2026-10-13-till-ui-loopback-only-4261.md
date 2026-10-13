# Review: the till UI answers only its own machine (ut-docs#4261, ADR-0157)

- **Date:** 2026-10-13
- **Branch:** `fix/4261-till-ui-loopback-only`
- **Author:** autonomous pipeline cycle `lane:cloud-41` (Opus 5.5)
- **Reviewer:** independent subagent on a different model (Fable), in its own worktree

## What shipped

- `internal/languard`: an HTTP guard. A caller is local when its TCP peer
  (`RemoteAddr`, never a header) is loopback, one of this machine's own
  interface addresses (cached, re-read at most every 30 s), or inside
  `UT_UI_ALLOW_CIDRS`. Any other caller reaches only `/api/sync/*`, `/healthz`
  and the guest pages (`/self-order*`, `/api/self-order/*`, `/o/*`, plus static
  `/public/`, `/themes/`, `/plugin-icons/`). Everything else gets a bare
  `403 Forbidden`, with no redirect and no product page. A path that is not
  already clean is refused. `ParseCIDRs` rejects invalid entries and anything
  wider than /8.
- `internal/pages/init.go`: `recover(csp(languard(demo(auth(mux)))))` in both
  branches (auth on, and `UT_AUTH=off`). The guard is off in demo mode. An
  invalid `UT_UI_ALLOW_CIDRS` is logged and dropped as a whole.
- `internal/config`: `UIAllowCIDRs` from `UT_UI_ALLOW_CIDRS`.
- Both compose files set `UT_UI_ALLOW_CIDRS=172.16.0.0/12`, because the host
  browser arrives through Docker's bridge. README section "Who can open the
  till UI".
- Help `display.md` (en/de/fa/tr/ar): the Pi-appliance recovery tip now says
  "SSH tunnel" instead of "another device's browser", and a new item 22,
  "Network access", explains the 403 and the router check. Manifest
  regenerated (`make docs-shots`). No rendered pixel changed, so the PNGs were
  restored.
- Tests: the `languard` unit suite. Init-level tests through the real chain
  (LAN peer gets the guard's own 403 on `/login`, `/`, `/api/auth/login`,
  exit-to-os, `/setup` and `/api/window-mode`; `/api/sync/ping` and `/healthz`
  reach their handlers; loopback is served; allow-list and invalid-list
  behaviour). Three existing Init-chain tests now send from `127.0.0.1` via
  `localRequest`, because httptest's default peer, 192.0.2.1, is a LAN caller.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Init test's exit-to-os case could not fail: the handler answers 403 to a PIN-less POST by itself | **Fixed.** `guardCode` recognises the guard's own bare-`Forbidden` body. Verified: with the guard unwired, the exit-to-os case now fails. |
| 2 | minor | IPv6 zone ids (`[fe80::1%eth0]`) made the till's own link-local address non-local | **Fixed.** `netip.ParseAddrPort`, then `WithZone("").Unmap()`. Tests added for zoned and IPv4-mapped peers. |
| 3 | minor | The guard judged the cleaned path while auth and the mux judge the raw one (`/healthz/` from the LAN got auth's 303 instead of the guard's 403) | **Fixed.** An unclean path is refused. Tests cover `/healthz/`, `/api/sync/ping/`, `/self-order/./cart` and `//`. |
| 4 | minor, pre-existing | The recovery-mode server (`internal/recovery`) binds the same port outside the guard | **Deferred** to ut-docs#4268 (Backlog). Named in ADR-0157. |
| 5 | nit | README: Docker pool fallbacks and rootless Docker; a same-host reverse proxy makes every caller loopback; the 30 s own-address cache | **Fixed** (README notes; reverse proxy also in ADR-0157). |

The reviewer probed path tricks (`%2F`, `%2e%2e`, `//`, `..;`, case, trailing
slash) and peers (IPv4-mapped, zoned) through the real Init chain: no bypass.
Every auth-exempt `/api/sync/*` route is bearer-checked or rate-limited by
design. Every till-to-till client path is under `/api/sync/`. The desktop
shell, Android/iOS WebView, Pi kiosk, e2e and docs-shots all use loopback.

## Verified beyond unit tests

- TDD claims re-verified by breaking and restoring each piece: `path`
  handling, the cache's keep-on-error branch, and the Init wiring.
- Driven run: the real binary on `0.0.0.0:18080` with a fresh data dir.
  Loopback and the container's own interface address are served (`/login`
  303 to setup, `/healthz` 200). A true off-host peer could not be driven in
  this container (no `ip`/netns tooling); the Go tests cover it.
- `make docs-shots`: 120/120 screenshots rendered through the guard on
  loopback.
- Gate: `gofmt`, `go build`, `golangci-lint` (0 issues), `go test` for
  languard/pages/config/app/server/recovery, the help, i18n, docs-shots,
  compliance and competitor guards. `internal/plugins` hit the default 600 s
  timeout locally; CI runs it on its own step with a wider timeout (no change
  here touches it). The deadcode guard can't run in this container
  (toolchain mismatch); CI runs it.

## Verdict

Safe to merge. Deferred: ut-docs#4268 (recovery server), ut-docs#4267 (table
self-order → cloud, which removes the guest bullet), #4238 (tracking page →
cloud). A human should still check the pilot router (no port forward, DMZ or
UPnP for :8080), as noted on ut-docs#4261.

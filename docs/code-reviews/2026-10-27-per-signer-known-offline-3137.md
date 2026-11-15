# Review: per-signer fiscal known-offline (ut-docs#3137)

**Date:** 2026-10-27 · **Branch:** `fix/3137-per-signer-known-offline` ·
**Docs:** ut-docs `docs/3137-per-signer-known-offline` (ADR-0044 D1 inline
amendment, `reference/contracts/fiscal-sign-ask.md` 1.17.0)
**Built by:** Sonnet (Dev subagent) + Opus (review fixes) · **Reviewed by:**
Fable, two rounds (fiscal/tax = high-risk, `MODEL-ROUTING.md`)

## What shipped

The known-offline short-circuit at the four fiscal dispatch points
(`fiscal.sign.ask`, `fiscal.sign.start`, `fiscal.order.start`,
`fiscal.order.cancel`) used only the tender's declared offline flag
(`navigator.onLine`). It is now judged per signer
(`internal/pages/fiscal_known_offline.go`):

- **Declared offline** skips only when every subscriber needs the network
  (a granted `net:`/`tcp:` permission). A network-free signer is asked, as
  ADR-0044 D1 always said.
- **Not declared offline**, the dispatch is also skipped only when all of
  these hold:
  - every subscriber needs the network through probeable grants only (a
    plain `net:<host>` naming a public DNS name — `internal/plugins/permission_network.go`);
  - each of those hosts was confirmed unreachable within 30 s by a background
    probe (`internal/netreach` `Set`, `Monitor.Fresh`, `DownOnly`);
  - the till's own cloud probe is not reachable either.
- A probe-derived skip names the host only and never marks the sale offline
  or sync-queued.
- `signerProbeClient` dials direct (no proxy, no redirect follow), like
  plugin egress.
- `Set.RunRefresh` re-checks held hosts every 10 s, started through
  `netaccess.StartService("signer backend probe refresh")` (demo inventory
  updated).

## Findings

| # | Sev | Finding | Status |
|---|---|---|---|
| R1-B1 | blocker | `net:@setting:` probe target lost the URL's scheme/port → persistent false skip (unsigned, unrecoverable sales) | Fixed: every `net:@`/`net:validation:` grant is unprobeable |
| R1-B2 | blocker | Dotted LAN names / plain-http plugins probed over https → false skip for a LAN signer | Fixed: `http:lan`, IP literals, single-label and LAN-suffix names (`.local`, `.localhost`, `.home.arpa`, …) unprobeable |
| R1-M1 | major | Probe used the environment proxy, unlike plugin egress | Fixed: `signerProbeClient` (Proxy nil, no redirects); test `TestSignerProbeClient_NoProxyNoRedirect` |
| R1-M2 | major | Probes only kicked by sales, so a quiet till never benefited | Fixed: `Set.Refresh`/`RunRefresh` every 10 s; `TestSetRefreshReprobesStaleMonitors` |
| R1-m1 | minor | Declared semantics for several subscribers: code vs docs disagreed | Docs/comment aligned to the (safer) code |
| R1-m2 | minor | Synchronous permission reads per sale with a signer | Accepted: indexed SQLite reads, only when a signer is subscribed; zero-plugin path untouched |
| R1-m3 | minor | Contract sections for `fiscal.sign.start` / `fiscal.order.*` not updated | Fixed |
| R2-M3 | major | A plain `net:<host>` grant doesn't pin the port; a 443 probe reading a refused port as Unreachable → false skip | Fixed: `DownOnly` (only DNS/timeout/no-route errors are Unreachable) + cloud-probe corroboration; `TestDownOnlyCountsOnlyNetworkDownErrors`, `TestFiscalKnownOffline_CloudReachableStillAsks` |
| R2-m1 | minor | Refresh goroutine bypassed `netaccess.StartService` | Fixed; added to `networkServices` |
| R2-m3 | minor | `RunRefresh(nil ctx)` panic | Fixed (nil → Background) |
| R2-m4 | minor | Held hosts are probed for the process lifetime (≤16) | Accepted, documented in the contract |
| nits | nit | stale `ctx` comment, vendor name in a core test, docs wording | Fixed |

## Verified beyond automated tests

- Revert checks:
  - The old `if in.Offline` in `dispatchFiscalSignAsk` fails
    `…UnreachableBackendIsNotAsked` and `…DeclaredOfflineAsksNetworkFreeSigner`.
  - The round-1 `permission_network.go` fails the six new
    `TestPluginNetworkNeeds` cases (setting, port, http, `.local`,
    `.home.arpa`, `http:lan`).
  - Removing the cloud corroboration fails `…CloudReachableStillAsks`.
- The seven existing known-offline tests only gained a `net:*` grant (now
  required for a declared-offline skip). The reviewer checked that none was
  weakened.
- No UI surface, string or locale change. Backend only, so there was no
  driven UI run.

## Gate

- `gofmt -l` empty.
- `go build ./...` and `go vet` (touched packages) pass.
- `golangci-lint run ./...` reports 0 issues.
- These guards pass: `guard-core-neutral`, `guard-data-access`,
  `guard-netaccess`, `guard-i18n`, `guard-page-http-error`,
  `guard-sdk-hostfns`, `guard-kiosk-engine`, `guard-card-data-schema`,
  `guard-compliance-claims`, `guard-mail-addresses`, `guard-demo-env`,
  `guard-deadcode-baseline-growth`.
- `guard-deadcode-baseline` could not run in this sandbox: its `deadcode`
  build rejects every go1.27 package, an environment issue. CI runs it in
  its own job.
- Full `go test ./...` passed on the pre-review snapshot. A later run hit one
  unrelated flake, `TestPluginIdentify_UploadRefusals_3873` (TempDir
  cleanup race; passes `-count=5` alone), filed as ut-docs#5117. The final
  full `go test ./...` on the finished code passed (exit 0).

## Verdict

Safe to merge once CI is green. A false skip now needs every one of these at
once:

1. a public `net:` host failing with a DNS, timeout or no-route error;
2. the till's own cloud probe not reachable either;
3. the failure within the last 30 s.

That is the outage `proceed-and-declare` already covers.

# Review — pinned LAN TLS 3/3: replicas dial the main till over pinned https/wss (ut-docs#4092)

ADR-0114 §7, slice 3 of ut-docs#2736 (after universal-till#1846 serving TLS
and ut-docs#4091 storing the pin at pairing). Built on Opus 5.5; independent
review on Fable (fresh context, its own worktree).

## What shipped

- `internal/lantls/peer.go`: one process-wide peer transport behind
  `lantls.NewPeerClient(timeout)`. `internal/app` installs a `PeerSource`
  that reads `sync.primary_url` and `sync.primary_cert_pin` at request time.
  A request whose host and port match the main till is cloned and upgraded
  `http` → `https` on the **same port** (port 80 made explicit for a
  port-less URL, Host header kept). It goes through a pinned
  `*http.Transport`: TLS 1.3 minimum, HTTP/1.1, no proxy, and
  `InsecureSkipVerify` plus a `VerifyConnection` SPKI pin check (no names,
  dates or CA). There is **no plain fallback**. Each transport is bound to
  one pin, and a pin change swaps in a fresh one. Every other host passes
  through untouched. An unreadable pin fails the request closed.
- Every replica → main till client now uses it: pull, push, assets,
  no-sales, link probe (`ping`), the link websocket dial (`wss`), the
  secrets-key fetcher, `cloud-device` (enroll's shared client, where cloud
  calls pass through), and the live proxies (held sales, tables + claim,
  orders + stream bridge, vouchers, users, settings, till role, catalogue
  forward).
- `discovery.PrimaryWatch.challenge`: with a pin held, `primary-proof` runs
  over TLS with that pin only and never falls back to plain. A different
  certificate is not adopted (re-proof is #2737). An unreadable pin is an
  error, not "unpinned".
- `completeJoin`: a pairing that learned a pin sends `enroll` (the one-time
  token) and the snapshot download (the new bearer) over the pinned client.
- fleetlink `report`: `TLSPinned: lantls.PeerPinned(ctx)`.
- Docs: ut-docs `architecture/lan-sync.md` (pinned dialling section); stale
  `#2736` placeholder comments updated. The Tills help topic already
  describes the TLS column. `web/help/img/manifest.json` gets a surface-hash
  refresh only (constructor changes in `internal/pages`, no rendered pixel).

## Tests (TDD: written first, seen failing)

- `lantls/peer_test.go`: a pinned main till is dialled over TLS with no
  plain request; a wrong certificate gives `ErrPinMismatch` and the server
  sees zero requests; unpinned, no source, and other hosts stay plain; a pin
  change drops verified connections; an unreadable pin fails closed; port
  and Host are preserved; the caller's request is not mutated;
  `samePeerHost` table.
- `discovery`: a pinned replica proves over TLS only; another certificate
  gets nothing; `TestPrimaryWatch_PlainProofKeepsAStoredPin` (the #4091
  interim behaviour) is rewritten as
  `TestPrimaryWatch_PinnedReplicaNeverProvesOverPlain`.
- `pages/pinned_lan_tls_test.go` (two tills over real sockets, sniffing
  TLS + plain listener): a pinned replica's pull, push, probe and link all
  arrive with `r.TLS != nil`, zero plain requests, `tls_pinned: true`
  reported; a wrong-cert main till serves zero requests and the replica
  never links; an unpinned replica stays plain and reports false.
  `pairing_tls_test`: a pinned join sends zero plain `/api/sync/*` requests.
- Mutation checks:
  - `NewPeerClient` turned into a plain client → 5 tests fail.
  - Pin-change swap disabled → `PinChangeDropsVerifiedConnections` fails.

## Findings (Fable)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | Fail-open: settings read errors were swallowed, so an errored pin read meant "unpinned" and the bearer went over plain HTTP. Same in `challenge`. | **Fixed.** `PeerSource` returns an error and `RoundTrip` fails closed (body closed); `challenge` errors on an unreadable pin; test `UnreadablePinFailsClosed`. |
| 2 | minor | A connection in flight at a pin change could return to the pool and be reused under the new pin. | **Fixed.** Each transport is bound to one pin and is swapped on change. |
| 3 | minor | A port-less primary URL changed the Host header to `host:80`. | **Fixed.** The original Host is kept, with a test. |
| 4 | nit | The peer source was never removed when `app.Run` returned (it read a closed DB). | **Fixed.** Deferred restore, registered after `database.Close`, so it runs first. |
| 5 | nit | Two settings reads per peer request. | Accepted: SQLite point reads; the slowest caller is the 800 ms proxies. |
| 6 | nit | `CloseIdleConnections` on a peer client also closes `http.DefaultTransport`'s idle pool. | Accepted: harmless and matches netaccess's guard. |
| 7 | info | The `pages` package needs more than 10 minutes under `-race`. | CI runs `pages` without `-race`, with `-timeout 20m`; unchanged. |

## Gate

- `gofmt`, `go build ./...`, `go vet ./...`, `golangci-lint` (0 issues).
- Every guard in `ci.yml`'s build job passes, except
  `guard-shellcheck-version`: no shellcheck binary in this container, and
  this diff touches no shell scripts.
- Full suite the CI way: `go test -count=1 -timeout 30m ./...`, 91/91
  packages ok.
- `-race` on enroll, logging, netreach, app, server, lantls, discovery and
  fleetlink: ok. The reviewer also ran `pages` under `-race` with a 45-minute
  timeout: ok.
- `TestRun_JoinsBackgroundGoroutinesOnEarlyServerError` failed once in a
  loaded `-race` run. It fails identically on `origin/main` in this
  container and passed alone and in the race set: a pre-existing timing
  flake, not this change.

## Not verified

No real-device run (two physical tills on a LAN). The two-till socket tests
use the production listener and handlers. The main till's per-till
plain-HTTP refusal, rotation and re-proof are #2737.

**Verdict: safe to merge.**

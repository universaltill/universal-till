# Review — request-body bounds on the till's listeners (ut-docs#5338)

Date: 2026-11-04 · Branch: `fix/5338-body-bounds` · Built by Sonnet, reviewed
independently by Opus 5.5 · Tester: separate agent, live run on a non-loopback
address.

## What shipped

- `internal/httpserver`: `New` wraps every handler (main and recovery
  servers) with a body-read deadline: 15 s grace from the handler's first read
  plus 8 KiB/s of body received, latched once the body hits EOF so the
  request context is never cancelled mid-handler. Requests without a body get
  no deadline, so SSE, the sync websocket and long-polls are untouched. The
  deadline is pre-armed at handler entry, so net/http's post-handler drain of
  an unread body is bounded too (that drain was the wider hole: any route that
  refused before reading could be held indefinitely). `MaxHeaderBytes` 64 KiB
  (431 above). The request is shallow-copied, never mutated.
- `internal/auth/body_limit.go`: auth-exempt request bodies are capped at
  64 KiB by default, with per-route overrides (`/api/sync/sales` 64 MiB,
  `/closes` and `/held-sales/upsert` 4 MiB, `/catalog/apply` 1 MiB,
  `/settings/apply`, `/no-sales`, `/cleared-baskets` 256 KiB). A declared
  oversize body gets 413 `body_too_large` (JSON envelope on `/api/*`) before
  the handler runs; a chunked one fails the handler's read with
  `*http.MaxBytesError`.
- `TestRealServersUseNew` scans all of `internal/` for `http.Server{` (with or
  without `&`), `ListenAndServe(`, `Serve(` and their TLS variants.
- ut-docs `architecture/pos-auth.md`: "Request-body bounds" subsection.

## Findings

| id | sev | finding | outcome |
|---|---|---|---|
| R1 | major | `boundBodies` replaced `r.Body` on net/http's own request, so its after-handler logic could no longer recognise its body (unread large body / `Expect: 100-continue` handling) | fixed: shallow copy; tests `TestUnreadBigBodyAnsweredAtOnce`, `TestUnreadExpectContinueAnsweredAtOnce` (green before and after on go1.27.1, whose `writeHeader` checks `w.reqBody`; kept as a defence) |
| R2 | minor | deadline clock started at handler entry, so a handler that waits >15 s before reading would time out instantly | fixed: clock restarts at first Read; red→green `TestSlowHandlerBeforeFirstReadKeepsGrace` ("i/o timeout") |
| R3 | — | `/api/sync/sales` at 64 MiB instead of the design's 32 MiB (worst-case 100-sale batch is 19.9 MB; 2x headroom) | accepted: bearer checked before reading; halve-on-413 → ut-docs#5428 |
| R4 | nit | `rateBody` divide-by-zero at rate 0, int64 overflow on huge counts | fixed: `bodyDeadline()` clamps; `TestRateBodyDeadlineArithmetic` |
| R5 | nit | server-construction test missed non-`&` and `ListenAndServe`/`Serve` | fixed; no hit under `internal/` (`cmd/unitill-desktop/control.go` loopback server already sets `ReadHeaderTimeout`; `scripts/mock-marketplace` is dev tooling) |
| R6 | nit | doc didn't say `UT_AUTH=off` skips the size cap | fixed in pos-auth.md |
| R7 | residual | no write timeout: a peer that never reads a large response holds a connection | deferred → ut-docs#5429 |
| T1 | residual | a refusal to a stalled small body still waits up to the 15 s grace; a 413 keeps the connection for the grace; cut bodies log "context canceled" at ERROR | deferred → ut-docs#5430 |

Per-IP connection cap: not done by design (replicas, kiosks and shared-IP
Wi-Fi legitimately open many connections; the time bounds already cap how
long a slow client holds one).

## Verification beyond unit tests

- Tester broke the code twice (removed the wrapper; disabled the exempt
  cap) and the matching tests failed; restored.
- Live run on 192.0.2.2: 1 B/s trickle cut at 15.0 s on `/api/setup` and
  `/api/self-order/scan`; stalled body to an early-refusing route answered at
  15.0 s then closed; 10 MiB to `/api/sync/held-sales/upsert` and 64 KiB+1 to
  `/api/setup/join` got 413 `body_too_large` at once; 70 KiB header → 431,
  30 KiB fine; small exempt POSTs and GETs < 8 ms; SSE `/api/orders/stream`
  alive at 70 s; shell long-poll held 25 s. Websocket not held live (needs an
  enrolled replica bearer; same no-body path as SSE).
- `go test -race -count=3` on httpserver and auth; new pages tests under
  `-race`; full `go test ./...` green (pages and plugins rerun alone with a
  long timeout after a CPU-contention timeout); golangci-lint 0 issues;
  guards data-access, i18n, netaccess, kiosk-engine, core-neutral,
  mail-addresses.
- The full `internal/pages` package under `-race` did not finish locally in
  40 min (shared CPU, 0 races reported); CI is the authority.

Pre-flight: till user-visible behaviour — n/a (no UI, no help topic: LAN
transport bounds only); reference/architecture — updated (`pos-auth.md`);
en.json keys — n/a (none); README — n/a; follow-ups — ut-docs#5428, #5429,
#5430.

## Verdict

Approve. R1, R2, R4–R6 fixed; R3 accepted; R7/T1 carded.

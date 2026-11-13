# 2026-11-03 — till HTTP server timeouts (ut-docs#5337)

## What shipped
- New leaf package `internal/httpserver`: `ReadHeaderTimeout = 10s`,
  `IdleTimeout = 120s`, `New(h)`. ReadTimeout/WriteTimeout stay zero on
  purpose (SSE, 25s shell long-poll, large uploads).
- The till server (`internal/server/server.go`) and the safe-mode recovery
  server (`internal/recovery/serve.go`, same LAN `ListenAddr`) both use it.
  Previously neither set any timeout: a LAN host could hold a goroutine and a
  connection forever with a half-sent header block (slowloris), most
  exposed since `/api/sync/link` takes no-bearer pending links (#5297).
- `internal/pages/window_state_api.go` "Long-poll safety" comment updated.

## Process
BA+Architect (Sonnet), Dev (Sonnet, TDD), Tester (Sonnet), independent
review (Opus 5.5, different model from author).

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | low | Slow request bodies on auth-exempt POSTs still unbounded (ReadTimeout 0 by design) | deferred → ut-docs#5338 |
| 2 | nit | Comment claimed a WriteTimeout would sever the replica websocket; Hijack clears deadlines | fixed |
| 3 | nit | Comment said a peer "never" reuses a closed conn; true only for Go's DefaultTransport | fixed |
| 4 | low | `TestRealServersUseNew` checks two named files only | accepted; widening noted on ut-docs#5338 |

Reviewer checked net/http (go1.27.1) source: header deadline cleared after
headers when ReadTimeout=0; IdleTimeout only between requests; Hijack clears
deadlines; TLS handshake now capped at 10s on `*tls.Conn` (was unbounded);
`lantls.PinnedClient` has keep-alives off; no other LAN `http.Server` literal
(desktop control server is loopback and already sets ReadHeaderTimeout).

## Verification
- TDD re-verified by the reviewer in a separate worktree: zero timeouts →
  `TestNewPinsTimeouts`, `TestStalledHeaderClosed`,
  `TestIdleKeepAliveClosedButServed` fail; WriteTimeout added / bare literal
  restored → pin, source-pin and `TestStreamingHandlerOutlivesTimeouts` fail;
  restored → all pass, `-race -count=15` green (15.4s).
- Real driven run (actual till binary, non-demo so the lantls sniff was
  active): partial header closed at 10.00s; silent conn closed at 20.00s
  (10s first-byte sniff + 10s header); keep-alive served a 2nd request at
  15s; SSE `/api/orders/stream` still open at 40s; 1 byte/s body for 16s not
  cut; idle keep-alive closed at 120.00s.
- gofmt clean, `go build ./...`, go vet, golangci-lint 0 issues; `go test
  -race` httpserver/server/recovery; `go test ./internal/pages/...`; guards
  data-access, core-neutral, netaccess, kiosk-engine, mail-addresses green.

Pre-flight: help/docs-shots n/a (no user-visible behaviour); en.json/i18n n/a
(no keys); README n/a; ADR n/a (hardening within existing design);
reference docs n/a; follow-up slow-body bounds deferred → ut-docs#5338.

## Verdict
Approve.

# Code review: bound `/ext/{plugin}` proxy with timeout + request context

**Card:** universaltill/ut-docs#912
**Date:** 2026-08-23
**Author:** Farshid Mirza (autonomous pipeline cycle)
**Reviewer:** independent fresh-context Sonnet subagent (complexity:easy tier)

## Summary

`internal/pages/external_api.go`'s `registerExternalProxy` (mounted at
`/ext/{plugin}`, proxies external menu plugins) called `http.Get(mp.Route)`,
which uses `http.DefaultClient` — no timeout — and does not propagate the
inbound request's context. A hung external menu-plugin host could pin the
handling goroutine indefinitely, with no way for a client disconnect or
request-scoped deadline to unblock it. Found in passing during the
independent review of ut-docs#893 (unrelated to that card's scope), filed
separately as this card.

Fix: build the outbound request via `http.NewRequestWithContext(r.Context(),
...)` and issue it through a package-level `externalProxyClient` carrying a
60s timeout — the same bound already used for outbound HTTP elsewhere in this
codebase (`sync_admin.go`'s `StartSyncPull` client, `sync_api.go:601`).

## What was verified

- **TDD, independently re-verified by the reviewer, not taken on trust.**
  New test `TestExternalProxy_UpstreamHangRespectsRequestContext`: a hung
  upstream (blocks forever) + a request with a 100ms context deadline must
  return 502 promptly, not hang. The reviewer reverted only
  `external_api.go` to the pre-fix `http.Get` code (keeping the new test),
  confirmed the test **fails** — hitting its own 2s watchdog because the
  old code neither carries `r.Context()` nor has a client timeout — then
  restored the fix and confirmed the test **passes** in ~0.12–0.14s. Ran
  repeatably (3x) and once under `-race`: stable, no flakiness.
- `go build ./...` clean.
- `gofmt -l` clean on both changed files.
- `go vet ./internal/pages/...` clean.
- Full `internal/pages` package test suite green (existing
  `TestExternalProxy_ProxiesUpstreamBody`,
  `TestExternalProxy_UnknownAndEmptyPlugin`,
  `TestExternalProxy_UpstreamDownIsBadGateway` all still pass, no
  regressions).
- Full repo `go test ./...` green, no failures anywhere.
- `scripts/ci/guard-data-access.sh` and `scripts/ci/guard-i18n.sh` both pass
  — this diff touches no SQL and no user-facing strings (the `err.Error()`
  passed to `http.Error` on the new request-construction failure path is a
  pre-existing pattern in this handler, unchanged by this diff, and is a
  diagnostic 502 body rather than template-rendered UI text; narrowing that
  class is ut-docs#893's separate, already-tracked scope).

## Reviewer findings

None of consequence. Specifically checked and cleared:
- 60s timeout matches existing codebase convention exactly (identical
  `&http.Client{Timeout: 60 * time.Second}` construction already used
  elsewhere).
- The new `http.NewRequestWithContext` error branch (→ 502) is reasonable
  defensive handling; in practice `mp.Route` is admin-configured so this
  path is close to unreachable — not a bug, just unremarkable.
- The regression test's `defer upstream.Close()` / `defer close(block)`
  ordering is correct (Go defers run LIFO — `close(block)` unblocks the
  stuck handler *before* `upstream.Close()` waits on it), so no deadlock
  risk in the test itself.
- No repository-pattern, money, i18n, kiosk-isolation, or offline-first
  rule violations — diff is scoped entirely to outbound-proxy HTTP client
  wiring.
- No real client/shop name, no secret-shaped literal.
- Backend-only diff (no UI surface, no shop-owner-visible behaviour change)
  — `reference/ux-guidelines.md` checklist and `web/help/` manual-update
  requirement don't apply.

## Verdict

**Safe to merge as-is.** No fixes required post-review.

## Deferred / out of scope

- The wider `http.Error`/i18n literal sweep across `internal/pages`
  (ut-docs#893) is separate, already-tracked, in-progress work — this card
  deliberately didn't touch that class in `external_api.go`.

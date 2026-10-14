# Review — recovery-mode server behind the ADR-0157 LAN guard (ut-docs#4268)

**Date:** 2026-10-14 · **Lane:** `lane:cloud-41` · **Author model:** Sonnet 5.5
(easy card) · **Reviewer:** independent Opus 5.5 subagent (different model
from the author), fresh context, own worktree

## What shipped

- `internal/recovery/serve.go` — the recovery mux (healthz, `GET /`,
  `POST /api/recovery/retry`, optional safe-mode routes) is built in
  `newHandler` and wrapped in `languard.New`. `UT_UI_ALLOW_CIDRS` is parsed
  exactly like `internal/pages/init.go`: an invalid list is logged and
  dropped (stricter, never wider). The guard applies in demo mode too,
  unlike the main server: the recovery screen shows failure detail and a
  retry button, nothing a public visitor should reach. Safe-mode routes
  keep their stricter `requireLoopback`.
- `internal/recovery/languard_test.go` — LAN peer (TEST-NET 192.0.2.10)
  gets 403 on `GET /` and on retry (no retry fires, no ref code in the
  body); LAN `/healthz` is not refused; loopback reaches the page (200,
  ref code) and retry (202, retry fires); an allowed CIDR opens the page
  and a peer outside it stays refused; an invalid CIDR list fails closed;
  demo mode stays guarded.
- `README.md` "Who can open the till UI" — one sentence. ut-docs ADR-0157
  "still exposed" list updated (separate ut-docs PR).

## TDD

The 403-asserting tests failed against an unguarded `newHandler`
(`LAN peer GET / = 200, want 403`, retry `= 202, want 403`, allowed-CIDR,
invalid-CIDR and demo cases likewise) and pass with the guard. Re-verified
independently by the reviewer (guard replaced by `return mux` → 5 failures;
restored → all 7 pass).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor (pre-existing) | A peer the guard admits but that isn't loopback (Docker host via `UT_UI_ALLOW_CIDRS`, the till's own LAN IP) sees the safe-mode link, then `requireLoopback` refuses it. | Out of scope; filed ut-docs#4290. |
| 2 | nit | A bad `UT_UI_ALLOW_CIDRS` logs its error again on each Retry (`newHandler` runs per `Serve`). | Accepted — harmless, same message as the main server. |
| 3 | nit | No HEAD/OPTIONS test. Reviewer read the code: languard decides on path only, so HEAD/OPTIONS from the LAN get 403; unclean paths are refused; LAN-allowed prefixes 404 on this mux. | Accepted. |

The reviewer also confirmed every shell reaches the till over loopback
(desktop, mobile, Pi kiosk, postinstall), the recovery templates load no
external assets, no deployment sets `UT_DEMO` (the demo runs in the
browser since ADR-0143), and `/healthz` probes are unaffected.

## Verified beyond automated tests

No driven run: a LAN peer can't be simulated from a single container
(any address it owns counts as local). The handler tests run the real
guard and mux; `TestServe_*` still bind a real socket on loopback and pass.
No UI or string changed.

## Gate

`gofmt -l .` clean; `go build ./...`, `go vet ./...`, `golangci-lint run
./...` (0 issues), `go test ./...`, `go test -race ./internal/recovery/...
./internal/languard/...`; every `ci.yml` build-job guard passes except two
that can't run in this container (`guard-deadcode-baseline` — local
deadcode built with an older Go; `guard-shellcheck-version` — no
shellcheck), both left to CI.

**Verdict:** safe to merge.

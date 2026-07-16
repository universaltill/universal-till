# Code review — mac app marketplace endpoint + auto port fallback

Date: 2026-07-16
Branch: `fix/app-endpoint-and-port`

## Problems

1. **Store registration could never succeed in the mac `.app`.** `main.go`
   loads `pos.env` from the working directory; the `.app`'s working dir is
   `Contents/Resources`, which shipped **no `pos.env`**, so
   `UT_MARKETPLACE_ENDPOINT_URL` fell back to the dev default
   `http://127.0.0.1:8081`. With nothing there, enrolment and the plugin store
   silently failed — the "register" path looked broken. (The tar/deb/zip
   archives already ship `pos.env`; only the `.app` was missing it.)

2. **A busy port stopped the till from starting.** The server bound
   `cfg.ListenAddr` (default `:8080`) with `ListenAndServe`; if the port was in
   use (a second till, or any other app) startup failed outright.

## Changes

- `packaging/macos/build-app.sh`: stage `packaging/pos.env.example` →
  `Contents/Resources/pos.env` **before** codesign (so it's sealed). A fresh
  app now points at `https://marketplace.universaltill.com/api` out of the box.
- `internal/server/server.go`: `listenWithFallback` binds the configured port,
  else the next 20, else an OS-assigned free port (`:0`). The server now
  `Serve(ln)`s that listener and updates `cfg.ListenAddr` to what it actually
  bound, so the browser-open opens the right port. Clean bind → unchanged.
- `cmd/unitill-desktop/desktop.go`: the WebView shell picks a free loopback
  port up front (`freePort`, fallback `8080`), passes it as `UT_LISTEN_ADDR`,
  and navigates there — so the window always matches the server's real port.
- Tests: `internal/server/listen_test.go` (free port bound as-is; busy port
  falls back to a different one).

## Risk / correctness
- `pos.env.example` sets only the marketplace endpoint — no secrets, no other
  overrides — so bundling it is safe; an operator's explicit env still wins.
- Fallback surfaces the *original* bind error if nothing is free, so a genuine
  permission/parse failure is not masked. The tiny probe→bind race in the
  desktop path is covered by the server-side fallback.

## Checks
`go build ./...`, `go build -tags desktop ./cmd/unitill-desktop`,
`go test ./...`, data-access + i18n guards — all pass.

## Note for Farshid
This makes the shipped mac app reach the real marketplace and survive a busy
port. `8081` in your message is the **marketplace** endpoint; the till itself
listens on `8080` — both are now robust (endpoint shipped in the app; :8080
auto-falls-back). The deeper two-tier store→device model (fleet view of every
device under one store) remains the tracked follow-up.

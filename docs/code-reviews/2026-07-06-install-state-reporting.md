# Code Review — POS install-state reporting (story 1-1, AC 5)

Date: 2026-07-06
Branch: `feature/install-state-reporting`
Reviewer: independent Sonnet subagent (running).
Scope: `internal/plugins/install_reporter.go` (+ test),
`internal/plugins/installer_marketplace.go` (wiring), `internal/config/config.go`
(UploadToken).

## What it delivers

Closes story 1-1 AC 5: as the POS `MarketplaceInstaller` installs a plugin, it
reports each lifecycle state back to the marketplace install-intent
(`POST {endpoint}/ui/api/installs/{intent}/state`):

- `MarketplaceReporter` — best-effort HTTP reporter (10s timeout; failures logged,
  never fail the install). Nil when the endpoint is unset; no-op for empty intent.
- `MarketplaceInstallRequest.IntentID` — the intent this install fulfils.
- `installer.emitState` fires the caller's `OnStateChange` **and** the reporter.
- `Install` uses a named-return `defer` to report the terminal state:
  `active` on success, `failed` (with the error) otherwise; `downloading` /
  `installing` reported inline.
- `UT_MARKETPLACE_UPLOAD_TOKEN` authenticates the token-gated report endpoint.

## Verification

- `go build ./...`, `go test ./...` green; new reporter tests
  (reports POST/path/state/error/auth; nil + empty-intent no-ops) and the
  **full existing installer suite** still pass (the `Install` refactor is safe).

## Sonnet review

Running; dispositions to be appended.

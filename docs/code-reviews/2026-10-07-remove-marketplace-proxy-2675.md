# Review: remove the raw /api/plugins/marketplace proxy (ut-docs#2675)

Date: 2026-10-07 · Lane: lane:cloud-24 · Built by Sonnet (complexity:easy), reviewed by Opus 5.5 in a fresh context.

## What shipped
- `internal/pages/plugin_api.go`: deleted the `/api/plugins/marketplace` handler — a raw proxy to
  `{EndpointURL}/v1/catalog/plugins` with an untimed client that echoed upstream errors. Nothing in
  `web/`, `internal/`, `e2e/`, `android/`, `mobile/`, `cmd/` or ut-cloud called it; the store pages use
  `marketplace.CatalogRepository`. The path now falls through to the `/` catch-all and 404s.
- Tests: `TestPluginCatalogBrowsing_RequiresPluginManagement` replaced by
  `TestMarketplaceProxyRoute_Removed` (registerIndex + registerPluginAPI, UT_AUTH=on, no-session /
  cashier / manager / admin → 404). The two legacy tests that exercised only the proxy were deleted;
  the stale demo deny-list, demo spot-check and cashier sale-only entries were removed.
- `specs/007-plugin-host/{quickstart,COMPLETION}.md` mark the endpoint removed. ADR-0112's
  Consequences bullet is struck through in a ut-docs PR.

## TDD
Re-verified by the orchestrator: with the old `plugin_api.go` restored, the new test fails with
`GET /api/plugins/marketplace role="" = 403, want 404 (route removed, ut-docs#2675)`; with the
deletion it passes.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | ADR-0112 still lists the proxy as an open gap | Fixed in ut-docs (strike-through, "Removed by ut-docs#2675") |
| 2 | minor | No review record yet | This file |
| 3 | nit | `demoDeniedRoutes` comment still lists "the marketplace" | Fixed |
| 4 | nit | `legacyMarketplaceStub` still records a catalog query nobody reads | Fixed (returns only the server) |
| 5 | info | Unrouted paths (incl. this one) reach `findPageEntry`; page-entry routes aren't prefix-restricted | Pre-existing, not widened by this diff → Backlog ut-docs#3786 |

No blocker. No new disk writes, no new paths, no user-facing strings (no i18n/help change).

## Verified
`gofmt -l .` clean, `go build ./...`, `go vet ./internal/pages/...`, full `go test ./...`; guards
data-access, netaccess, i18n, help-topics, help-drift, page-http-error, demo-env, kiosk-engine,
no-showmodal, compliance-claims, competitor-naming, core-neutral, pipefail-grep-q, readme-local-links.
`golangci-lint` (v2.5.0 built with go1.25) and the deadcode guard can't run against go1.27 in this
container; the reviewer ran deadcode with the go1.27 toolchain — nothing new from this diff. The
plugin-store Playwright e2e runs in CI (no UI surface changed). No visual surface touched.

Verdict: safe to merge.

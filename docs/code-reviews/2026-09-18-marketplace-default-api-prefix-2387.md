# 2026-09-18 — Marketplace default endpoint missing `/api` prefix (ut-docs#2387)

## What shipped

`pos.env.example` and `internal/config`'s compiled-in default for
`UT_MARKETPLACE_ENDPOINT_URL` pointed at `http://127.0.0.1:8081` — missing
the `/api` prefix `ut-cloud`'s real gRPC-gateway mount actually requires
(`mux.Handle("/api/", http.StripPrefix("/api", gw))`,
`ut-cloud/internal/httpapi/router/router.go:708`). A till running off
either default hit `GET /v1/revocations` with no `/api` prefix, got a 404,
and `RevocationChecker.SyncRevocations` failed silently every 30-minute
tick (only a scheduler-level log line, nothing surfaced to an operator).
`pos.env`/`pos.env.dev` already had the correct value.

Fixed:
- `internal/config/config.go` — compiled-in default now
  `http://127.0.0.1:8081/api`.
- `internal/config/config_test.go` — `TestInitDefaults`'s assertion
  updated to the corrected default (confirmed red against the old code,
  green after the fix — see "Verified" below).
- `pos.env.example` — same fix, matching `pos.env`/`pos.env.dev`.
- `docs/marketplace-config.md` — the same missing-`/api` mistake was
  present in several copy-paste `UT_MARKETPLACE_ENDPOINT_URL=...`
  examples for the *real* marketplace (production `cloud.universaltill.com`
  and local `:8081`). Fixed those; deliberately left the local *mock*
  marketplace (`:8082`) examples alone — `scripts/mock-marketplace/main.go`
  mounts its routes directly at `/v1/...` with no `/api` prefix, so those
  examples were already correct.
- `test_marketplace_integration.sh` — two hardcoded `curl` URLs against
  "a real marketplace at localhost:8081" had the identical bug (no `/api`),
  bypassing `UT_MARKETPLACE_ENDPOINT_URL` entirely. Fixed both.
- `scripts/dev.sh` — the startup echo's fallback display string was out
  of sync with the corrected compiled-in default; updated to match.

## Independent review

Delegated to a fresh-context Sonnet subagent (card is `complexity:easy`,
per `scrum-master`'s model-routing table — Sonnet built it, Sonnet reviews
it in a clean instance that never saw the dev reasoning).

Findings:
- Confirmed the fix matches the real `ut-cloud` gateway mount (read
  `router.go:708` directly).
- Found two additional stale-default sites the initial diff missed:
  `test_marketplace_integration.sh` (two hardcoded URLs) and
  `scripts/dev.sh` (a cosmetic fallback-display string). Both folded into
  this same PR rather than deferred, since they're the identical
  mechanical one-line fix and directly in scope of the reported bug class.
- Confirmed the `docs/marketplace-config.md` edit correctly distinguished
  the real marketplace (needs `/api`) from the local mock (doesn't) by
  reading `scripts/mock-marketplace/main.go`'s actual route registration.
- No i18n/data-access/kiosk-engine surface touched; no `web/help/**` topic
  references this config value (client-side default, not shop-owner-facing
  behaviour) — no manual update needed.

## Verified beyond automated tests

- TDD claim re-verified personally (not just taken on the implementer's
  word): reverted the `config.go` default line alone, ran
  `go test ./internal/config/... -run TestInitDefaults -v` → failed with
  `EndpointURL = "http://127.0.0.1:8081"`; restored the line → passed.
  The review subagent independently repeated the same revert/restore and
  got the same result.
- Full gate run clean: `gofmt -l .` (clean), `go build ./...`,
  `go test ./...` (all packages green), `golangci-lint run ./...`
  (0 issues), and every CI-blocking guard under `scripts/ci/` relevant to
  this diff (`guard-data-access`, `guard-i18n`, `guard-compliance-claims`,
  `guard-help-topics`, `guard-help-drift`, and the rest of the
  `ci.yml` `build`-job list) — all passed; `guard-help-drift`'s three
  pre-existing, already-tracked `fa`/`ar`/`tr` drift entries (ut-docs#1962/
  #1973) are unrelated to this change.
- `shellcheck` is not installed in this sandbox, so
  `shellcheck scripts/ci/*.sh` could not be run directly; `bash -n` syntax
  checks passed on both edited shell scripts
  (`test_marketplace_integration.sh`, `scripts/dev.sh`). Neither script is
  under `scripts/ci/`, so this gap doesn't affect the CI-blocking guard
  list itself.
- `bash -n` on both edited shell scripts: clean.
- `go test ./packaging/...` re-confirmed `packaging/pos.env.example`
  (a separate, already-correct file) is unaffected.

## Verdict

Safe to merge. Small, mechanical, easy-tier fix; no architecture/ADR
implications; no behaviour change beyond correcting a wrong default to
match the already-correct `pos.env`/`pos.env.dev` values.

## Deferred

Nothing identified beyond this PR's scope — the review's two additional
findings were folded in rather than deferred, since they were the same
one-line fix pattern.

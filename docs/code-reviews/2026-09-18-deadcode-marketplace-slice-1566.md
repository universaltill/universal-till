# Code review: `internal/plugins/marketplace` dead-code slice (ut-docs#1566)

**Date:** 2026-09-18
**Card:** universaltill/ut-docs#1566 ("Burn down 97 unreachable functions in universal-till")
**Slice:** `internal/plugins/marketplace` (6th slice, after `internal/httpx`, `internal/data`, `internal/plugins`, `internal/pages`, `internal/manual`)
**Complexity:** hard (per `MODEL-ROUTING.md`) — Dev on Fable, Review on Opus (isolated worktree, no implementation reasoning shared)

## Scope

4 entries from `scripts/ci/deadcode-baseline.txt`:

- `internal/plugins/marketplace/catalog_repository.go: unreachable func: CatalogRepository.Filter`
- `internal/plugins/marketplace/client.go: unreachable func: Client.AckDownload`
- `internal/plugins/marketplace/client.go: unreachable func: Client.GetRevocations`
- `internal/plugins/marketplace/client.go: unreachable func: Client.ReportPluginStatus`

## Outcome: all 4 kept, none deleted

Unlike most prior slices, this one found no safe deletions. Each function was
investigated via a real repo-wide caller search (production, tests,
`scripts/`, `e2e/`) and resolved:

| Function | Outcome | Reason |
|---|---|---|
| `CatalogRepository.Filter` | keep, doc comment | Test-only; every production consumer filters `snapshot.Plugins` inline instead (5 named call sites), 3 of which need a locale criterion this signature can't express. |
| `Client.AckDownload` | keep, doc comment | **Unwired step of a live flow**, not superseded — both till download paths issue a token and never ack it. ut-cloud's server side is real and serving. Follow-up: ut-docs#2381. |
| `Client.GetRevocations` | keep, doc comment | Superseded by the live `RevocationChecker.SyncRevocations` (30-min ticker), which never adopted this client or its `since_version` cursor. |
| `Client.ReportPluginStatus` | keep, doc comment | Superseded by the live `TelemetryClient.ReportNow` (5-min ticker); this method posts to a route ut-cloud has never served and gates on a config field nothing else reads. Follow-up: ut-docs#2382. |

`scripts/ci/deadcode-baseline.txt` is **unchanged** — correct outcome for an
all-keep slice, same precedent as the `internal/data` and `internal/manual`
slices.

## Independent review (Opus, isolated worktree)

Verified every factual claim in all 4 doc comments against the real code
(not the comments' own prose) — re-derived caller lists via repo-wide grep,
independently confirmed the "live mechanism" claims by reading the actual
ticker wiring in `internal/server`, and confirmed the diff is genuinely
comment-only (`git diff` against `main` touches only doc comments; baseline
file byte-identical).

**One factual error found and fixed before merge:** the `AckDownload`
comment named a nonexistent method, `MarketplaceInstaller.InstallFromMarketplace`.
The real method is `MarketplaceInstaller.Install` (`installer_marketplace.go:79`).
Fixed in commit `aee3ea0`. Everything else in that comment checked out
(both download paths do issue tokens and checksum-verify; neither acks).

**Flagged as unverifiable from this repo** (no `ut-cloud` checkout in the
review's worktree): the `AckDownload` comment's claim that ut-cloud serves
`POST /v1/download/ack` and its own code names this method "unwired", and
the `ReportPluginStatus` comment's claim that `/v1/telemetry/status` has
never been served by ut-cloud. Both were independently confirmed true by
the orchestrating session reading the actual `ut-cloud` repo (see below) —
not left unverified in the final state, just outside this review pass's
own repo scope.

## Verified beyond automated tests

Cross-repo verification against `ut-cloud` (outside this review subagent's
scope, done by the orchestrating session):

- Confirmed `ut-cloud/internal/api/downloadsvc/service.go` actually serves
  `AckDownload` and its own comment names the till client "currently
  unwired" — corroborates the doc comment's ut-cloud-side claim.
- **Found and independently verified a likely live security bug while
  investigating `GetRevocations`'s "superseded by `RevocationChecker`"
  claim**: `RevocationChecker`'s `RevocationEntry` struct uses snake_case
  JSON tags (`plugin_id`, `developer_id`, `revoked_at`), but ut-cloud's
  `GET /v1/revocations` is served via `runtime.NewServeMux()` with no
  `UseProtoNames` marshaler option anywhere in the repo — grpc-gateway's
  default protojson emits the proto `json_name` (`pluginId`), and the
  `Revocation` proto message doesn't even have `developer_id`/`revoked_at`
  fields. Every decoded `PluginID` is therefore the empty string, so
  revocation lookups silently fail and no plugin actually gets disabled —
  invisible on both sides since `revocation.go`'s own logging counts
  attempts, not successes. **Not fixed here** (behavior change, outside
  this slice's no-behaviour-change scope, and a decode fix belongs in its
  own reviewed PR) — filed as **ut-docs#2380**, `p1`+`security`.

## Gate (full, both before and after the review's fix commit)

`gofmt -l .` clean · `go build ./...` pass · `go vet ./...` pass ·
`go test ./internal/plugins/...` (incl. `marketplace`, `oauth`,
`builtinlayouts`) and `./internal/server/...` all `ok` ·
`golangci-lint run ./...` (v2.5.0) 0 issues · `guard-data-access.sh` +
`_test.sh`, `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
`guard-page-http-error.sh` all pass.

`guard-deadcode-baseline.sh` itself couldn't run in either sandbox (needs
GTK/WebKit headers for the `-tags=desktop` build) — substituted the same
rootless `deadcode@v0.48.0 -test=false . ./cmd/unitill-uninstall` run the
`internal/plugins` slice used: 70 findings, 0 new vs. baseline, all 4
marketplace entries still reported as expected (kept, not deleted). Real
CI (`build` job) is the actual gate for this check.

## Follow-ups filed

- **ut-docs#2380** (p1, security) — revocation feed decode mismatch, likely live no-op enforcement.
- **ut-docs#2381** (p3) — `AckDownload` unwired, ut-cloud accounting/metrics gap.
- **ut-docs#2382** (p3) — `ReportPluginStatus` + `TelemetryOptIn` dead/inert, keep-and-fix vs. delete decision needed.

## Merge

Not closing ut-docs#1566 — it's an ongoing multi-slice burn-down (~66
entries remain after this slice, concentrated in `internal/pos` tax code
and misc single-entry files). Moving the card back to **Ready** after
merge, releasing this cycle's `lane:cloud-54` claim, per the same pattern
every prior slice used.

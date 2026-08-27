# Code review: replica-banner tests hardcoded a platform-dependent assumption

**Card:** universaltill/ut-docs#1057
**Date:** 2026-08-27
**Complexity:** easy — Dev inline (Sonnet), Review via an independent
fresh-context Sonnet subagent. One review round; no blocker-class finding,
so a second round wasn't earned per this pipeline's process-depth rule.

## What shipped

`TestInventoryReplicaBannerNeverLinksAcrossDevices`
(`internal/pages/inventory_sync_banner_test.go`) and
`TestCatalogReplicaBannerNeverLinksAcrossDevices`
(`internal/pages/catalog/handlers_test.go`) both hardcoded the assumption
that the `crossdevicelinkactionable` template predicate is `false`. That
predicate is `selfupdate.DownloadLinkActionableNow()`, which returns
`true` on darwin — so both tests failed on any Mac while passing on CI's
Linux runners, discovered during ut-docs#1039's Dev phase and confirmed
pre-existing/unrelated to that card.

Fix: added `httpx.CrossDeviceLinkActionable` (`internal/httpx/httpx.go`),
a package `var` defaulting to `selfupdate.DownloadLinkActionableNow`,
sitting behind the `crossdevicelinkactionable` template func instead of
that func calling `selfupdate.DownloadLinkActionableNow()` directly. A
`text/template.FuncMap` entry has to be a zero-arg `func() bool` and the
two failing tests live in different packages (`internal/pages`,
`internal/pages/catalog`) than `httpx`, so an explicit-parameter seam
(the pattern `selfupdate.DownloadLinkActionable(goos string)` /
`update_api.go`'s `updateUnavailableHTML(locale, latest, goos string)`
already use elsewhere in this codebase) can't reach across the template
boundary — a package var is the seam that can. Both tests were rewritten
into `t.Run` subtests stubbing the var to exercise **both** the
actionable and inactionable render paths, restoring the original value
via `t.Cleanup`, rather than asserting only whichever answer the test
runner's own `runtime.GOOS` happens to give.

## Independent review (fresh-context Sonnet subagent)

Ran `go build ./...`, `go vet ./internal/httpx/... ./internal/pages/...`,
`gofmt -l` on the three changed files, the two targeted tests plus the
full `internal/httpx`, `internal/pages`, `internal/pages/catalog`,
`internal/pages/common` suites (all `-race`), and
`guard-data-access.sh`/`guard-kiosk-engine.sh`/`guard-i18n.sh`/
`guard-compliance-claims.sh` — all clean.

**Finding (nit, not fixed — deliberately deferred):** `updatedownloadlink`
(`internal/httpx/httpx.go`, the line above `crossdevicelinkactionable`)
still calls `selfupdate.DownloadLinkActionableNow()` directly, unindirected
— same underlying predicate, same class of platform-dependence, but only
one of the two funcs got the seam. It isn't a live bug today: its own test
(`internal/httpx/template_helpers_test.go`) overrides the func-map entry
directly via `FuncsFor`'s returned map, a same-package trick unavailable
to the two failing tests. Left as-is rather than widening this fix's
scope; a matching `UpdateDownloadLinkActionable` var (or a comment
explaining why it doesn't need one) is a reasonable future follow-up if a
test ever needs to stub it from outside `httpx`.

Also checked the rest of the repo for the same "assert the CI runner's own
`runtime.GOOS` answer" anti-pattern (`internal/paths/paths_test.go`,
`internal/server/server_test.go`, `internal/issuereport/bundle_test.go`,
`internal/plugins/installer_store_test.go`,
`internal/pages/plugins_status_test.go`,
`internal/pages/sync_plugins_test.go`,
`internal/pages/plugin_api_legacy_test.go`) — all of those compare
against `runtime.GOOS`/`GOARCH` itself or `t.Skip` on Windows-specific
semantics, not against a hardcoded assumed value. Nothing else to flag.

## Verified beyond automated tests

- `go test ./internal/pages/... -timeout 10m` (mirrors the CI workflow's
  own `internal/pages`-scoped step) green.
- A full local `go test ./... -race -timeout 15m` run hit an unrelated
  15-minute timeout inside `internal/plugins` (WASM/wazero-heavy tests,
  package unrelated to this diff by any import — confirmed by inspection,
  not by the failure itself). This is a **known, already-documented**
  characteristic of that package, not a new regression: the CI workflow
  (`.github/workflows/ci.yml`) deliberately excludes `internal/plugins`
  from the main `go test` step and runs it separately with a 20-minute
  timeout, with an inline comment citing ut-docs#643/#753/#776 for exactly
  this reason. This local run's 15-minute flag was simply shorter than
  what that package needs — not a finding for this card.

## Explicitly deferred

- `updatedownloadlink`'s matching seam (see finding above) — filed as a
  nit, not a card; small enough to pick up alongside `httpx.go` next time
  someone is in that file, not worth a standalone card.

## Safe-to-merge verdict

Yes. `go build`, `go vet`, `gofmt -l` clean; both fixed tests pass with
`-race` in both the actionable and inactionable branches; the full
`internal/httpx`/`internal/pages`/`internal/pages/catalog`/
`internal/pages/common` suites pass; the four CI guards touchable by this
diff's surface area (data-access, kiosk-engine, i18n, compliance-claims)
plus all 17 `build`-job guards pass. No real client/shop name introduced;
no secret-shaped values in this diff.

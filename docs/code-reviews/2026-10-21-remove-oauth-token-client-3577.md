# Review: remove the dead scheme-(b) oauth token client (ut-docs#3577)

- **Date:** 2026-10-21
- **Branch:** `chore/3577-remove-oauth-token-client`
- **Author model:** Opus 5.5 (lane:cloud-41); **reviewer:** Fable (independent subagent)

## What shipped

- `internal/plugins/oauth` is deleted. Its `TokenClient` only ever asked
  `/v1/auth/merchant-token` for a scheme-(b) token. ut-cloud has refused that
  route since ut-docs#3540 (`authsvc.GenerateMerchantToken` always returns
  `ErrMerchantTokenRetired`), so `GetToken` could never succeed. Every
  caller already treated its error as "send no bearer".
- `marketplace.NewClient(cfg)` drops the token provider.
  - `doRequestAs` sends the ADR-0116 device credential to the cloud
    endpoint only, never to a dev override. This is unchanged.
  - `doRequest`, `ListPlugins`, `GetRevocations` and `AckDownload` send no
    `Authorization` header.
  - The 8 production call sites (`app.go`, 7 in `internal/pages`) and every
    test fake were updated.
- `MarketplaceConfig.ClientSecret` and `UT_MARKETPLACE_CLIENT_SECRET` are
  removed, because they authenticated nothing. `ClientID` stays as the
  merchant id. The log redactor keeps its `CLIENT_SECRET` pattern, which
  mirrors ut-cloud's `logredact`.
- In CI's `build` job, the oauth test step and the exclude regex now name
  only `internal/plugins/marketplace`. `ci-heavy-tests_test.sh` passes.
- Docs updated: `README.md`, `pos.env`, `pos.env.example`,
  `docs/marketplace-config.md` (new "Authentication" section) and
  `docs/data-model.md`. `test_marketplace_integration.sh` no longer fetches
  a merchant token. The mock marketplace drops `/oauth/token`.

## Tests

- New `TestNoRequestCarriesAnOAuthBearer` covers `ListPlugins` and
  `AckDownload` on an enrolled till and expects no bearer on either.
  - **Re-verified against unfixed `main`** in a separate worktree, adapted to
    the old signature with a token provider that succeeds: it fails with
    `/v1/catalog/plugins carried Authorization "Bearer jwt.oauth", want none`
    (and the same for `/v1/download/ack`). It passes on the branch.
- The `IssueDownloadToken` device-credential tests (cloud gets it, dev
  override never does, an unenrolled till sends none) still pass.
- `TestClient_ListPlugins` and `TestClient_IssueDownloadToken` now assert
  that the header is absent.
- Gate:
  - `gofmt -l .` is clean. `go build ./...`, `go vet ./...` and
    `go vet -tags contract ./tests/contract/` pass.
  - `golangci-lint run ./...` reports 0 issues.
  - `go test ./...`: every package passes. `internal/plugins` passes at CI's
    `-timeout 20m` (888s); the default 10m cap is the known margin that CI
    handles in its own leg.
  - All `build`-job guards pass. The local `guard-deadcode-baseline` run
    shows only the two `cmd/unitill-desktop` false positives (GTK headers
    are absent here), and `main` shows the same two.
  - The pact contract test compiles but was not run, because there is no
    `libpact_ffi` here. CI runs it; its expectations now send no bearer on
    the list and send the device credential on the download token.

## Driven run (real binary)

- A fake marketplace logged every request. Both binaries ran with
  `UT_MARKETPLACE_CLIENT_SECRET=dev-secret` and hit `/setup`, which fetches
  the language and tax catalogues.
  - **`main`:** `POST /api/v1/auth/merchant-token`, then both catalogue
    GETs, with no auth.
  - **Branch:** only the two catalogue GETs, with no auth.
- The only runtime difference is the doomed token POST, which is gone. No
  UI surface changed, so there were no screenshots.

## Findings (Fable)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `docs/data-model.md` still described the OAuth2 flow and the `auth/token.json` cache | Fixed |
| 2 | minor | `test_marketplace_integration.sh` aborted unless the retired route returned a token | Fixed: the catalogue GET no longer needs a token |
| 3 | nit | `sync_plugins_test.go` comment named `/v1/auth/merchant-token` | Fixed |
| 4 | nit | The mock marketplace still served `/oauth/token` | Removed |
| 5 | nit | Upgraded tills keep a stale `plugins/auth/token.json` | Accepted (BA non-goal): the token expired long ago, and its issuer is retired and refuses it. `paths.go` keeps `auth` in `transientPluginDirs` for the legacy migration |
| 6 | nit | The "both run with -v" wording in a ci.yml comment | Fixed |

The reviewer confirmed:
- no credential was lost on any path;
- telemetry, cloudsync, enroll and alerts never used `oauth`;
- the tests are real;
- no file-write code was added.

## Verdict

Safe to merge.
- **CI round 1:** `build` failed on the docs-shots surface hash, because `internal/pages/*.go` changed. There was no pixel change, so the hash was refreshed with `update-docs-shots-surface-hash.sh` (`Docs-Shots-Unchanged: true`).

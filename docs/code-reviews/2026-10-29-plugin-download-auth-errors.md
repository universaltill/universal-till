# 2026-10-29 — Plugin download: translated messages for the till-auth refusals (ut-docs#3541)

**Branch:** `fix/3541-plugin-download-auth-errors` · **Built by:** Sonnet (Dev subagent) · **Reviewed by:** Opus 5.5 (independent subagent, fresh context) · **Lane:** `lane:cloud-41`

## What shipped

- `plugins.MarketplaceCodeFailure(err)` (`internal/plugins/install_status.go`): the one
  mapping from a marketplace `APIError` code to the till's message key, HTTP status and
  `Retryable: false`. It covers `not_entitled`, `listing_not_found` (404) and `registered_required`
  (existing keys) plus the codes ut-cloud's download-token route also answers:
  `store_auth_required` → `plugins.install.error.pair_required`; `device_revoked` →
  `plugins.install.error.till_removed`; `unauthorized`, `token_retired`, `device_mismatch` →
  `plugins.install.error.pair_again` (403 for all, never 401 — `app.js` sends a 401 to `/login`).
- `POST /api/plugins/store/download` uses it instead of three if-blocks. These codes used to
  fall through to a raw `502 download failed: marketplace: <code>: …`.
- `ClassifyInstallError` uses it first, so these codes (and `registered_required`, which was
  unmapped there) are no longer stored as a retryable failure offering a Retry that cannot succeed.
- Three keys in en/tr/ar/fa + `en.meta.json`, and in `plugins_store_detail.html`'s inline `T` lookup.
  The breadcrumbs use each locale's own `nav.settings` / `settings.enrol.title` /
  `settings.enrol.pair_title` labels.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major (process) | New `en.json` keys missing in ut-plugin-language-de/es/pt → `lang-pack-drift` red on `main` after merge | Brand-new keys: core merges first, the three pack PRs land in the same cycle (reviewer step 4) |
| 2 | minor | `device_revoked` with `data.reason` `replaced`/`moved` gets "removed from your shop — pair again", while the cloud-auth chip says "replaced by X, use X" | Accepted; follow-up card |
| 3 | minor | An unclaimed (anonymous) till rejected with `unauthorized`/`token_retired` is told to pair, where the chip says "register as a new store" | Accepted; follow-up card |
| 4 | minor (pre-existing) | `403 service_unavailable` (country refused, `writeCountryRefused`) still shows the raw 502 and is retryable | Out of scope; follow-up card |
| 5 | nit (pre-existing) | `partials/plugin_install_modal.html`'s key lookup lacks the newer keys; the partial is included nowhere | Follow-up card (fix or delete) |

Reviewer checked: the codes match what ut-cloud sends (`storeauth.go`, `downloads.go`); the till's
marketplace client decodes the error envelope before looking at the status, so a 401 reaches the
mapping as an `APIError`; nothing on this path handles 401 earlier (the cloudsync lock-out
tracker watches check-in/sync only); no caller relies on `Retryable` beyond the "· retryable" label.

## Verified

- TDD re-verified by the orchestrator in a throwaway worktree: with `main`'s handler,
  `TestStoreAPI_DownloadTillAuthCodes_403` fails `502, want 403 (… download failed: marketplace:
  device_revoked: nope)` for all three subtests; with the fix it passes.
- `gofmt`, `go build ./...`, `go vet`, `golangci-lint` (0 issues), `go test ./internal/pages/...`,
  the new and touched `internal/plugins` tests (`MarketplaceCodeFailure|NewKeysResolve|ClassifyInstallError|InstallStatus`),
  `guard-i18n`, `guard-i18n-meta`, `guard-compliance-claims`,
  `guard-rtl-logical-css`, `guard-help-topics`, `guard-help-drift`.
- Every other guard in `ci.yml`'s `build` job passes locally except two that cannot run in this
  sandbox: `guard-deadcode-baseline` (its tool is built with go1.26, the repo needs go1.27) and
  `guard-shellcheck-version` (no `shellcheck` binary). No shell script changed; CI runs both.
- Language-pack follow-ups prepared (de/es/pt `i18n/3541-plugin-download-auth-errors`); each pack's
  `check-key-drift.sh` against this branch's `en.json`: 3751/3751, 0 drift.
- The whole `internal/plugins` package does not finish inside `go test`'s default 600s in this
  sandbox (wasm tests under load; it also hung for the Dev run). The wasm tests it stalled in
  (`FileImport`, `SecretSettings`) pass in 2s on the branch and on `main` when run alone; the full
  package is left to CI's `ci` job.
- Manual screenshots: the change only alters error responses and the store page's script-only `T`
  lookup, so no screenshotted pixel moves; the docs-shots surface hash was refreshed
  (`update-docs-shots-surface-hash.sh`, `Docs-Shots-Unchanged: true`) instead of a regeneration.
- No help topic documents plugin install errors, so there is no manual change.

**Verdict:** safe to merge.

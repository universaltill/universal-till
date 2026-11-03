# 2026-10-30 — Plugin download errors: replaced/moved, unclaimed and refused tills (ut-docs#5187)

**Branch:** `fix/5187-plugin-download-error-wording` · **Built by:** Sonnet (Dev subagent) · **Reviewed by:** Opus 5.5 (independent subagent, fresh context) · **Lane:** `lane:cloud-41`

Follow-up to ut-docs#3541 (`2026-10-29-plugin-download-auth-errors.md`, findings 2–5).

## What shipped

- `marketplace.APIError` gains `Reason`, read from the error envelope's `data.reason` in
  `IssueDownloadToken` (trimmed, capped). `Error()` text is unchanged; the success path still
  decodes into `IssueDownloadTokenResponse`. The body read is capped (`maxDownloadTokenBody`, 1 MiB)
  and a read error is no longer dropped.
- `plugins.MarketplaceCodeFailure(err, hasOwner)` / `ClassifyInstallError(err, hasOwner)`:
  - `device_revoked` + `replaced` → `plugins.install.error.till_replaced`; + `moved` →
    `plugins.install.error.till_moved`; otherwise `till_removed` as before.
  - `unauthorized` / `token_retired` on a store with no owner → `plugins.install.error.register_new`
    (the same choice as the cloud-auth chip, `storeHasOwner`); `device_mismatch` stays `pair_again`.
  - `403 service_unavailable` (ut-cloud `writeCountryRefused`) → `plugins.install.error.service_unavailable`,
    403 and not retryable, instead of a raw 502 that offered Retry.
  - Every caller in `internal/pages` passes `storeHasOwner(ctx, d.Settings)`.
- The unused partial `web/ui/partials/plugin_install_modal.html` is deleted (it was parsed, never included).
- Four keys in en/tr/ar/fa + `en.meta.json` + the store page's inline `T` lookup; the de/es/pt
  pack follow-ups are prepared in the same cycle.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `till_replaced` promised the replacing till's name "on this till's status bar", which only holds once cloudsync has recorded the replace (and `replaced_by` can be empty) | Fixed: the sentence now just says to use the till that replaced it (en/tr/ar/fa, packs, meta) |
| 2 | minor | `storeHasOwner` is false on a settings error and for a claimed store that never subscribed, so such a till is told to register as a new store | Accepted: the same known gap and choice as the cloud-auth chip (its doc comment), which the card asked to match |
| 3 | minor (test gap) | No test pins the `hasOwner` wiring at the three `ClassifyInstallError` callers (the download handler's wiring is pinned) | Follow-up card ut-docs#5213 |
| 4 | minor (test gap) | The store page's inline-`T` test didn't list the four new keys | Fixed |
| 5 | nit | `io.ReadAll` error discarded; magic `1<<20` | Fixed (read error surfaces as the decode error; named constant) |
| 6 | nit | Dead `plugins.install.modal.*` keys and the `close-install-modal` JS action left by the deleted partial | Follow-up card ut-docs#5213 |
| 7 | nit | No help topic documents plugin download errors (as in #3541) | Accepted; nothing in the manual to update |

## Verified

- TDD re-verified by the reviewer in a throwaway worktree, production code reverted with the new
  signatures/fields kept so the tests compile:
  - `TestClient_IssueDownloadToken_DeviceRevokedReason`: `/replaced` and `/moved` fail
    (`reason "", want "replaced"`); pass with the fix.
  - `TestMarketplaceCodeFailure`: replaced/moved → `till_removed`, unclaimed → `pair_again`,
    `service_unavailable: not mapped`; pass with the fix.
  - `TestStoreAPI_DownloadTillAuthCodes_403`: same, and `service_unavailable` answered 502; pass with
    the fix. Hard-coding `hasOwner` either way fails the owned/anonymous subtests.
- `gofmt`, `go build ./...`, `go vet`, `golangci-lint` (0 issues) on plugins/pages/httpx;
  `go test ./internal/pages/...` (full), `./internal/plugins/marketplace/`, `./internal/httpx/`, the
  touched `internal/plugins` tests (the full package is left to CI, as in #3541).
- Guards: i18n, i18n-meta, docs-shots (surface hash refreshed; no rendered pixel changes,
  `Docs-Shots-Unchanged: true`), help-topics, help-drift, help-breadcrumbs, rtl-logical-css,
  compliance-claims, competitor-naming, core-neutral, mail-addresses, no-showmodal, data-access.
- Translations: tr/ar/fa/de/es/pt breadcrumbs use each locale's own `nav.settings` /
  `settings.enrol.title`. Each pack's `check-key-drift.sh` against this branch's `en.json`: 0 drift.
- No visual surface changes beyond error text returned by the API; not looked at in a browser.

**Verdict:** safe to merge.

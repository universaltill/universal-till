# Review: Plugin Store download/install errors translated (ut-docs#6548)

**Scope:** branch `fix/6548-store-download-install-error-i18n`, 9 files
against `origin/main` (7bfe5c80): `internal/plugins/install_status.go`
(+ test), `internal/pages/plugins_store_page.go` (+
`plugins_store_api_test.go`, `plugins_store_page_test.go`),
`web/locales/en.json`, `web/i18n-meta/en.meta.json`,
`web/help/en/plugins.md`, `web/ui/pages/plugins_store_detail.html`.

**Author:** Dev agent (Sonnet). **Reviewer:** Opus 5.5 (independent,
separate worktree). **Tester:** separate agent (driven run + screenshots).

## What shipped

- `ClassifyInstallError` gains a network case (`marketplaceUnreachable`:
  `*net.OpError`, `*net.DNSError`, `context.DeadlineExceeded`, net.Error
  timeouts; `context.Canceled` excluded) → new key
  `plugins.install.error.unreachable` (retryable). It runs after the
  marketplace-code, cloud-hosted and revoked cases, so a marketplace code in
  a `url.Error` chain still wins. The marketplace install and directive
  install paths get the new key too.
- `POST /api/plugins/store/download` and `/install` no longer send
  `download failed: %v` / `install failed: %v`: they classify the error,
  log the raw error once at Warn with the listing id, and answer the key
  (download: generic → new `plugins.install.error.download_failed`, 502;
  install: classifier status or 400). The 403 / missing listing_id /
  unconfigured-installer answers of all three store handlers now use the
  existing `forbidden`, `invalid_request`, `configuration` keys.
- A failed **install** records `failed` in the install-status store (tile
  tag + heartbeat problem, like install-from-marketplace), except over an
  `active` record whose plugin is still installed. A failed **download**
  records nothing (a later successful download would never clear it).
- The detail page's inline `T` map covers every key the three handlers can
  return; two new `en.json` keys with `en.meta.json` entries (English only,
  ADR-0160); `web/help/en/plugins.md` "What can go wrong" rows updated.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | low | The "plugin still installed" branch of the active-record guard was untested (mutation to an unconditional `return` passed every test). | **Fixed:** `TestStoreAPI_InstallFailure_OverwritesStaleActiveRecord`; re-verified it fails under that mutation and passes restored. |
| 2 | low | The classifier's `canceled` row passed for the wrong reason (a bare wrapped `context.Canceled` is no net error). | **Fixed:** row is now a canceled dial `url.Error{net.OpError{dial, context.Canceled}}`; re-verified it fails with the `Canceled` guard removed. |
| 3 | low | Get→check→Save in `recordStoreInstallFailure` is not atomic: two concurrent installs of one listing could write `failed` over a fresh `active`. | **Accepted:** rare (double press across devices), self-heals on the next install; the install-from-marketplace precedent has no guard at all. |
| 4 | low | x509 / clock-skew TLS failures map to `download_failed` ("press Download again"), which can't help on a till with a wrong clock. Not a regression (was raw text). | Deferred → ut-docs#6555. |
| 5 | low | A failed-install record isn't cleared by Delete download; offline install before the signing key is fetched maps to non-retryable `configuration`. | Deferred → ut-docs#6555. |
| — | info | Tester: any `*net.OpError` (incl. a mid-stream reset after the marketplace answered) reads as "can't reach". | Accepted: the operator action (check connection, retry) is the same. |

## Verification

- Reviewer re-verified TDD claims in a separate worktree:
  `TestClassifyInstallError_Unreachable`,
  `TestStoreAPI_DownloadUnreachable_TranslatedKey`,
  `TestStoreAPI_InstallFailure_KeepsActiveRecord` each fail with the
  production hunk reverted, pass restored.
- `go build ./...`, `go vet`, `gofmt -l` clean; `go test ./internal/pages/`
  full package green (479 s, Dev); plugins tests filtered
  (`ClassifyInstallError|InstallStatus|NewKeys`) green — the full
  `internal/plugins` package hangs in this sandbox on unrelated
  process/network tests; CI covers it.
- Guards: i18n, i18n-meta, help-topics, compliance-claims,
  no-native-confirm, data-access — all pass.
- **Driven run (Tester):** real binary, marketplace killed after the
  catalogue loaded. Download → 502 `plugins.install.error.unreachable`;
  page shows "✗ Can't reach the plugin marketplace right now — check this
  till's internet connection. Selling is not affected." Looked at
  1280×800, 1024×600, 360×740 (en) and 1280 (fa, RTL; English fallback
  until Weblate): wraps cleanly, no clipping, no JS errors; failed-install
  tag rendered. **Not looked at:** dark theme, de/ar, grid tile after a
  failed install, browser-driven install failure, real touch hardware.

Pre-flight: `web/help/` topic updated (`web/help/en/plugins.md`); docs-shots:
`make docs-shots` re-run after CI's `guard-docs-shots` flagged the changed
surface + topic hash — only `web/help/img/manifest.json` is committed. Every
PNG re-rendered differently in this sandbox, including unrelated topics
(`catalog` 33,840 px, `plugins` 19,275 px of 614,400), i.e. renderer noise,
not this change (the new text appears only after a failed action; idle
pages are unchanged), so the committed PNGs are kept; `en.meta.json` updated for both new keys; `reference/` n/a (no
reference page documents these error strings); README n/a (not stale);
follow-ups → ut-docs#6555 (Backlog).

## Verdict

Approve. Findings 1–2 fixed and re-verified; 3 accepted; 4–5 deferred to
ut-docs#6555.

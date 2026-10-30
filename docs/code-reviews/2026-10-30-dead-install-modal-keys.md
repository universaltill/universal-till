# 2026-10-30 — Dead install-modal keys and JS, owner-aware install error wiring test (ut-docs#5213)

**Branch:** `fix/5213-dead-install-modal-keys` · **Built by:** Sonnet (Dev subagent) · **Reviewed by:** Opus 5.5 (orchestrator, different model from the author) · **Lane:** `lane:cloud-41`

Follow-up to ut-docs#5187 (`2026-10-30-plugin-download-error-wording.md`, findings 3 and 6).

## What shipped

- The 12 `plugins.install.modal.*` keys and `plugins.install.error.network` are gone from
  en/ar/fa/tr and from `scripts/ci/i18n-baseline/key-meta-baseline.txt`. A repo-wide grep
  (Go, templates, JS, e2e) found no literal use and no dynamic construction of either prefix
  (`ClassifyInstallError` and the store page's `T` lookup only name other `install.error.*` keys).
- `web/public/inline-actions.js`: the `close-install-modal` action is removed. Nothing renders
  that `data-action` and nothing defines `window.closeInstallModal` since the partial was deleted.
- `web/public/record-dialog.js`: the focus-trap comment no longer names the deleted
  `#plugin-install-modal`.
- New `internal/pages/plugin_install_owner_wiring_5213_test.go` drives
  `POST /api/plugins/install-from-marketplace` against a fake marketplace answering
  `401 token_retired`. An anonymous store gets `plugins.install.error.register_new` and an owned
  store (`plan=shop`) gets `pair_again`, in both the response envelope and the persisted install
  status.
- de/es/pt pack PRs drop the same 13 keys right after this merges (an orphan key fails each
  pack's `check-key-drift.sh`).

## TDD evidence (re-verified by the reviewer)

Changing `plugin_api.go:245` to `ClassifyInstallError(err, true)` makes the anonymous case fail
(`envelope error = "plugins.install.error.pair_again", want "plugins.install.error.register_new"`).
Restoring `storeHasOwner(ctx, d.Settings)` makes it pass.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | The edited comment in `record-dialog.js` wrapped after "and", leaving a ragged line | Fixed: reflowed |
| 2 | minor | The test covers the install route only. The update (`plugin_api.go:715`) and cloud-sync install (`cloudsync_wire.go:1734`) callers pass the same `storeHasOwner(...)` but have no own test | Accepted: the card asked for one marketplace install test. The three call sites are identical one-liners, and `TestStoreAPI_DownloadTillAuthCodes_403` covers the download route |
| 3 | info | Locally, `guard-deadcode-baseline.sh` (deadcode built with Go 1.26) and `guard-shellcheck-version.sh` (no shellcheck) fail, and they fail on `main` too | Environment only. CI has the tools |
| 4 | minor | CI `guard-docs-shots_test.sh` failed: the `web/public/*.js` edits made the docs-shots surface hash stale | Fixed: `update-docs-shots-surface-hash.sh` (no rendered pixel changes: one unused inline action removed, one comment edited), committed with `Docs-Shots-Unchanged: true` |

No UI surface changes. No user-visible text changes (the keys were unused). No help topic is
affected.

## Gate

`gofmt -l` is empty. `go build ./...` passes, and `golangci-lint run ./...` reports 0 issues.
Every other `ci.yml` build-job guard passes locally, including guard-i18n, guard-i18n-meta, the
help guards and the RTL guards. `go test ./...` passed, and CI is green on the PR.

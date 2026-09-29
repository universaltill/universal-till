# Review: engines take their config from the DB inside the apply lock (ut-docs#3245)

**Date:** 2026-09-29 · **Lane:** lane:cloud-24 · **Built by:** Claude Opus 5.5 · **Reviewed by:** Claude Fable (independent subagent, fresh context, own copy of the tree)

## What shipped
- `internal/pages/engine_config.go`:
  - `applyEngineConfig(ctx, d)` no longer takes a `pos.Config`. Inside `engineConfigMu` it reads the saved settings (`common.LoadStateChecked`) and builds the config with the new `engineConfigFor(st)`. So whichever apply runs last applies what the DB holds, and an older save's late apply can't push its stale rate over a newer one's.
  - A batch whose config the `Engine` and the `KioskEngine` already hold is skipped. This compare used to run outside the lock, and in `newRederiveSettings` only.
  - The read uses `context.WithoutCancel`. On a read error the batch is skipped and logged, and the engines stay on their last good config.
- `internal/pages/common/state.go`: new `LoadStateChecked` returns `LoadState`'s result plus the first read error. `LoadState` behaves as before.
- All four call sites now call `applyEngineConfig` after their save: `newRederiveSettings` (init.go), the setup wizard (setup_page.go), and the store-settings save and the `/api/settings/upsert` tax/country case (settings_page.go).
- `web/help/img/manifest.json`: docs-shots surface hash refreshed. No rendered pixel changed (Go-only, no template or string edits).

## Findings (Fable)
1. **Major, fixed:** `LoadState` turns a failed read into the default. The save handlers used to apply their in-memory state, and now depend on that read. So a transient read failure (SQLITE_BUSY, DB closing at shutdown) right after a successful save would have pushed the env-default tax rate to every live basket. Fixed with `LoadStateChecked`: on error the apply is skipped and logged. The new test `TestEngineConfig_FailedReadKeepsLastGoodConfig` closes the DB and asserts that the last good rate is kept. With the guard removed it fails with `0 bp`.
2. **Minor, accepted and documented:** on an additional till, a failed best-effort local mirror of a forwarded save now leaves the engines on the old rate until the next sync pull. Before, they took the new rate immediately. This matches the card's acceptance: the engines end on the config the DB holds, and it self-heals on the next pull. It is noted in `applyEngineConfig`'s doc comment.
3. **Minor, pre-existing, out of scope → new Backlog card:** `POST /api/settings/upsert` with `key=store.tax_rate` updates state but never reaches the engines. It also doesn't validate the value, so wiring it to the engines needs input validation too.
4. **Nit, accepted:** `TestEngineConfig_LateOlderSaveEndsOnDBConfig` documents the new contract. It cannot fail through the new signature, because there is no way left to pass a stale config. The genuine regression guards are the rederive, cancelled-context and failed-read tests.
5. **Nit, no action:** the rederive test needs `newFullAuthDeps`' `chdirRoot` for `config.NewI18n`.

The reviewer checked these and found nothing wrong:
- **Write before apply:** at all four call sites the write lands before the apply.
- **Config fields:** `LoadState` reproduces the handler's `st` exactly for the four `pos.Config` fields. The service charge round-trips losslessly.
- **Nil safety:** `Settings` and `Cfg` are never nil at any caller.
- **Lock order:** `engineConfigMu` stays a leaf, and no caller holds a tx.
- **Skip on no change:** safe. Sessions are minted from the kiosk config and set in the same batch after it.
- **Other config sites:** none that push to the engines were missed. Boot runs single-threaded, and the tender/preview paths are per-request.

## Verified
- TDD, each test seen failing first:
  - `TestRederiveSettings_CatchesUpKioskWhenEngineAlreadyMatches` failed against the old code: KioskEngine and the session stayed at `0 bp`.
  - `TestEngineConfig_CancelledContextStillAppliesDBConfig` failed without `WithoutCancel`: `0 bp`.
  - `TestEngineConfig_FailedReadKeepsLastGoodConfig` failed without the error guard: `0 bp`.
  - The reviewer re-verified the first two by mutating its own copy.
- `TestEngineConfig_ConcurrentAppliesDoNotInterleave` (#3085) is ported to the new signature. It passes under `-race`.
- `gofmt`, `go build ./...`, `go vet`, full `go test ./...`, `golangci-lint run ./...` (0 issues), and every `ci.yml` build-job guard.
- Can't run locally:
  - `guard-shellcheck-version`: there is no shellcheck binary here.
  - `guard-deadcode-baseline`: it flags `internal/logging` (untouched) because this container has no GTK headers, so the desktop root is skipped. CI analyzes every root.
- One full-suite run failed `TestReplicaLink_HelloCarriesItsOwnCloudDeviceID` (sync link, untouched) while the reviewer's suite ran concurrently. It passes 3/3 on its own.
- No visual surface changed, so I looked at no screenshots.

**Verdict:** safe to merge.

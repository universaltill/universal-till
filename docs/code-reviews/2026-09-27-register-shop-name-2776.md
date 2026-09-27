# Review: registration sends the shop name the owner typed (ut-docs#2776)

- **Branch:** `fix/2776-register-shop-name`
- **Author:** Opus 5.5 (lane:cloud-54)
- **Reviewer:** Fable, independent subagent in a separate worktree

## What shipped

**Root cause.** The setup wizard saves the shop name to the `store.name` setting. It never refreshes `d.Cfg.StoreName`, which `settings.LoadRuntimeConfig` loads only at boot and which migration 001 seeds as "My Store". The wizard then registers in the same request: `autoRegisterForSetup` and `installBasePluginsForSetup` call `enroll.EnsureRegistered` → `RegisterNow` → `register(…, cfg.StoreName, …)`. ut-cloud sets a shop's name only at `/v1/stores/register`. So every fresh install created one more cloud shop called "My Store".

**Fix.**
- `internal/enroll`: `RegisterNow` sends the live `store.name` setting through the new `shopName` helper and the new `StoreNameSettingsKey`.
- The name is trimmed. It falls back to `cfg.StoreName` when the setting is unset, blank or unreadable, so a settings read never fails registration.
- Replicas are unaffected: `registerOnReplica` sends no store name (#2730).

**Tests.**
- `internal/enroll/store_name_test.go`: the live name is sent; the fallback holds for unset, blank and read-error.
- `internal/pages/auto_register_test.go` `TestSetupWizardAutoRegisterSendsTheTypedShopName`: a wizard-level regression test. The fake marketplace now records each register request's `store_name`.
- `internal/settings/runtime_test.go` `TestLoadRuntimeConfigReadsEnrollStoreNameKey`: guards the duplicated key against drift. `internal/enroll` cannot import `pages/common`.

## Verification

**TDD.** With the fix reverted (`shopName` returning `cfg.StoreName`), both new behaviour tests fail with `store_name = My Store, want "Corner Café"`. With the fix restored they pass. The author checked this and the reviewer re-checked it independently.

**Full gate.**
- `go build ./...` and `go test ./...` (whole module): green.
- `golangci-lint` on the touched packages: 0 issues.
- `gofmt`: clean.
- Every `build`-job guard: green. Two could not run here, for environment reasons only: `guard-shellcheck-version` (no shellcheck binary) and the apt-update step (network). No shell script was touched.

**Not run.** No UI surface changed, so there were no screenshots or Playwright run; CI runs e2e.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | `shopName` ignores `kv.Get`'s `ok` value | Accepted. An absent value is `""`, which falls back. This matches the file's existing `kv.Get` style. |
| 2 | nit | No server-side length cap on the name sent to the cloud | Accepted. It is pre-existing: `UT_STORE_NAME` was already sent uncapped. ut-cloud owns validation of its own input. |
| 3 | aside | Self-order kiosk header reads the stale `d.Cfg.StoreName` | Filed as ut-docs#3020 |
| 4 | aside | Device name read from `sync.till_name`, while the wizard writes `till.name` | Filed as ut-docs#3019 |
| 5 | aside | Pre-existing `-race` flake in `internal/enroll` (`tokenExplicit` written without `mu`) | Filed as ut-docs#3021 |

**Verdict:** safe to merge. No blocker and no should-fix finding.

## Deferred

- Census of the existing "My Store" shops, which needs prod DB access: ut-docs#3018.
- Renaming shops: ut-docs#2775.
- Deleting empty shops: ut-docs#2800, #2627, #2631.

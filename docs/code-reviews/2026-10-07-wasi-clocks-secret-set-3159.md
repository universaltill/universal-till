# Review: WASI real clocks + `secret_set` host function (ut-docs#3159)

**Date:** 2026-10-07 · **Lane:** `lane:cloud-41` · **Card:** ut-docs#3159 (ADR-0121 build card 7a)
**Built by:** Opus 5.5 · **Reviewed by:** Fable (independent subagent, separate worktree)

## Scope

ADR-0121 build card 7 was split into three cards. This PR is #3159. The blob
store is #3870 and `event_publish` is #3871.

- `internal/plugins/wasm_runtime.go`: the per-event module config sets
  `WithSysWalltime()`, `WithSysNanotime()` and `WithSysNanosleep()`. Guests
  now get the real wall and monotonic clocks and a real sleep. Before this
  they got wazero's fake fixed epoch (2022-01-01), which silently broke
  token-expiry checks in `ut-plugin-tax-uk` (HMRC) and `ut-plugin-tax-de`
  (fiskaly reuse window).
- `internal/plugins/wasm_secret_set.go`: new `ut.secret_set(key, val)`.
  - It needs `secret:write`, checked through `CheckPermission`.
  - The key must be one of the plugin's own settings declared
    `type: "secret"` in its installed manifest.
  - The value is sealed at rest (ADR-0082) through
    `UpsertPluginSettingScoped(…, declaredSecret=true)`. Failures fail
    closed: a seal or manifest error returns `-3` and writes nothing.
  - The value is never logged. The settings generation is bumped, as the
    settings editor does.
- Tests: `wasm_secret_set_clock_test.go`, plus `clock` and `secret_set`
  modes in `testdata/hostfn_guest`.
- Docs (ut-docs): `architecture/wasm-runtime.md` and
  `reference/plugin-host-functions.md`.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | With real clocks but wazero's no-op nanosleep, a guest `time.Sleep` busy-spins a CPU core until the deadline passes. The reviewer measured +2.2 s of host CPU for a 3 s sleep. | **Fixed**: added `WithSysNanosleep()`; docs say sleep really sleeps. |
| 2 | should-fix | `keyLen` was unbounded before `readGuest`. A guest-supplied key of up to 64 MiB could reach a log line on every denied call (log flood). | **Fixed**: key ≤ `data.StorageMaxKeyBytes` (128), checked before guest memory is read; `-4`. Test added. |
| 3 | nit | `json.Marshal` silently replaces non-UTF-8 bytes, so a binary value read back corrupted with code 0. | **Fixed**: non-UTF-8 key or value returns `-4`; doc says to base64 binary data. Test added. |
| 4 | nit | A register-scoped row for the same key shadows the global value that `secret_set` writes. | **Accepted, documented** in the reference page. An operator would rarely set a per-till value for a secret the plugin manages. |
| 5 | nit | Each write bumps the `.ask` cache generation (sealed writes always count as changed). | **Accepted, documented**: "call it only when the value has actually changed". Not on the sale path's correctness. |
| 6 | nit | No review record. | This file. |

The reviewer also checked and found these fine:
- **Own-plugin scope:** the plugin id comes from host state, never from the guest.
- **No plaintext path:** `declaredSecret` plus the key-name heuristic means plaintext never reaches SQL.
- **Error codes** match ADR-0121 §3.
- **No deadlock** from bumping the generation inside a guest call. `publish` releases `eb.mu` around Blocking handlers; non-blocking events and Ask/AskPlugin call handlers unlocked.
- **Nothing depended on the fake epoch**, including `.ask` caches and docs-shots.
- **No new disk writes** and no cwd-relative paths.
- **Guards:** `guard-plugin-settings-bump.sh` and `guard-data-access.sh` both pass.

## TDD verification

The reviewer ran each check in an isolated worktree, and the author re-ran the fixes.
- Removing the clock options made `TestWasmGuestSeesRealClocks` fail with
  `guest wall clock = 2022-01-01 00:00:00 +0000 UTC`. Restoring them made it pass.
- Stubbing `hostSecretSet` to return `-3` made 8 of 10 `TestHostSecretSet`
  subtests fail with the expected wrong codes. The two that expect `-3`
  passed, as they should.
- Removing the key-length and UTF-8 checks made the two new subtests fail
  (`secret_set = -2, want -4`). Restoring the checks made them pass.

## Gate

- `gofmt`, `go build ./...` and `go vet ./...` are clean.
- `go test ./...` passes for every package except `internal/plugins`, which
  ran past `go test`'s default 10m timeout. CI runs that package with
  `-timeout 20m` (ci.yml), and both the reviewer and the author ran it that
  way.
- All `ci.yml` build-job guards pass locally. Two could not run here:
  `guard-shellcheck-version` (no shellcheck binary) and
  `retry-with-backoff.sh` (a helper, not a guard).
- `golangci-lint` could not run here (its binary was built with go1.25,
  older than the module's Go version). CI runs it.

## Verdict

Safe to merge. No UI, locale, migration or sale-path change.

## Deferred

- Blob store → #3870.
- `event_publish` → #3871.

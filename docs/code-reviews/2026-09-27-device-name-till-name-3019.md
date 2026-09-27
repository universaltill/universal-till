# Review — device registers under the typed till name (ut-docs#3019)

**Date:** 2026-09-27 · **Branch:** `fix/3019-device-name-till-name` ·
**Built by:** Sonnet (complexity:easy) · **Reviewed by:** Opus 5.5, fresh context, separate worktree

## What shipped
- `enroll.DeviceName(ctx, kv)` is the one rule for the name a till gives the
  cloud for itself:
  - a replica (non-blank `sync.primary_url`) uses its own `sync.till_name` only;
  - a main or standalone till uses `till.name`, falling back to `sync.till_name`.
- Used by:
  - `enroll.Init`: device registration on a main till;
  - `replicaAttempt`: unchanged key, now trimmed;
  - `register()`: `/v1/stores/register` now sends `device_name`. It used to send none, so ut-cloud named the first device "Till 1";
  - `cloudsync.buildSyncRequest`: the heartbeat device name.
- **Deliberate deviation from the card text.** The card said "`till.name`, falling back to `sync.till_name`" for every reader. But `till.name` is shop-wide (`data.ShopWideSettingPrefixes`), and the admin sync copies it to replicas. So on a replica it holds the main till's name. Applying the card's rule there would register every replica under the main till's name.
- `internal/pages/sync_admin.go`'s chip label is left alone. It sits in the replica-only branch and is already correct.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | On promote (`ClearReplicaIdentity`), the replica's own `sync.till_name` is deleted. Afterwards the till reports `till.name`, which is the old main till's name. The data loss predates this change. | Filed as ut-docs#3025 (needs an Architect call) |
| 2 | nit | The doc and test comments restated the same explanation four times. | Fixed: trimmed to the rule plus a pointer |
| 3 | nit | A test comment referred to "another open PR". | Fixed |
| 4 | nit | The wizard test's stub body was shared without a lock. | Fixed: guarded with a mutex |
| 5 | nit | `DeviceName` lives in `replica.go`. | Accepted: it sits next to the key constants and `isReplica` |

## Verification
- **TDD, re-verified by the reviewer:** with the three production files reverted to the parent:
  - `TestBuildSyncRequestDeviceNameMainTillUsesTillName` fails: `device name = Till, want "Front Counter"`.
  - `TestSetupWizardAutoRegisterPostsTypedTillName` fails: `device_name = <nil>, want "Front Register"`.
  - The enroll tests, run against a shim to the old function, fail: `DeviceName = "", want "Front Counter"` and `enroll_test.go:615: device_name = Till`.
- **Mutation check:** with the replica branch removed (the naive fix), the four replica tests fail.
- **Gate:** `go build ./...` ok; `go vet` ok; `gofmt` clean; `golangci-lint` 0 issues; `-race` on the new tests ok. Full `go test ./...` passed except `internal/cloudlink`, which failed once under full-suite load, passed 3/3 when rerun alone, and does not depend on `enroll` or `cloudsync`. The build-job guards pass, with two exceptions:
  - `guard-shellcheck-version`: no shellcheck binary locally; no shell scripts touched.
  - `guard-deadcode-baseline`: flags `internal/logging/file.go`, which this change doesn't touch. Left to CI.
- **Not driven in a real app:** there is no UI surface. The till↔cloud request bodies are asserted at the httptest level.

## Verdict
Safe to merge.

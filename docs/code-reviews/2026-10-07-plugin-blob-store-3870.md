# Review: plugin blob store — blob:own host functions (ut-docs#3870, ADR-0121 §3/§6)

Date: 2026-10-07 · Lane: lane:cloud-24 · Built by Opus 5.5 (complexity:medium), reviewed by Fable in a
fresh context in its own worktree, with probes and mutation checks.

## What shipped
- `internal/plugins/wasm_blob.go`: `blob_put_open`, `blob_write`, `blob_commit`, `blob_get_open`,
  `blob_read`, `blob_delete`, `blob_list` on the `ut` host module. `CheckPermission` checks `blob:own`
  on every call, so a denial is audited and a revocation takes effect on the next call.
- The store root is `paths.Data("plugin-data", <id>, "blobs")`, created with `MkdirAll` (0700). `<id>` is
  the host-side plugin id, never a guest value.
- Names match `[a-z0-9._-]{1,128}` and must pass `windowsUnsafe`, which rules out `.`, `..`, a trailing
  dot and device names. `blobPath` then re-checks the result after `filepath.Clean`.
- A put writes to a `~put-*` temp file. `~` is outside the name alphabet. `blob_commit` fsyncs the
  temp file and renames it into place.
- Handles are kept per event (`hostState.blobs`): 8 at most, and `handleEvent` closes them all, which
  discards any uncommitted put. A get handle is released when `blob_read` returns 0.
- Quota: `limits.storage_mb` via `EffectiveLimits` (clamped to the platform ceiling). Each blob dir has one
  process-wide `blobStore` (a mutex plus committed and in-flight byte counts):
  - `blob_write` reserves bytes against committed blobs plus every open put, across all of the
    plugin's events.
  - `blob_commit` re-scans the store and renames under the same lock.
  - Over the quota → `-5`, and the put is discarded.
- `scanBlobs` ignores temp files and removes any older than 1 h (left over from a crash).
- Docs: `README.md`, `docs/plugin_guidelines.md`; ut-docs `reference/plugin-host-functions.md` and
  `architecture/wasm-runtime.md`.

## Findings (Fable)
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | The quota was checked per handle, so 8 open puts could each hold `storage_mb` of temp data. Confirmed by a probe. | Fixed: a process-wide in-flight counter is checked on every write. Test `TestBlobQuotaCountsInFlightPuts` was red before the fix and green after. |
| 2 | major | The commit re-check was not serialised: two concurrent events committed 1.43 MiB under a 1 MiB quota. Confirmed by a probe. | Fixed: the scan, check and rename now run under a per-store lock. Test `TestBlobQuotaConcurrentCommits` was red (1 433 600 bytes committed) before the fix and green after. |
| 3 | minor | `blob_read` consumed bytes before checking the guest buffer. | Fixed: the destination is checked first, matching `hostHTTPRead`. |
| 4 | minor | On Windows, rename or remove of a blob open for reading fails (`os.Open` has no `FILE_SHARE_DELETE`). | Documented in the reference as a Windows caveat (`-3`). Not an isolation issue. |
| 5 | nit | The stale-temp sweep is mtime-based; an NTP jump on an RTC-less Pi could sweep an in-flight temp, and that commit then fails `-3`. | Accepted: no corruption and no leak. |
| 6 | nit | `put_open`, `commit` and `list` each scan the directory. | Accepted: fine for the expected blob counts. |

The reviewer also checked these and found no problem: name validation (15 bad names, traversal included),
cross-plugin isolation, temp-name collisions, cleanup of uncommitted puts, overwrite keeping the old blob,
integer bounds, permission-first ordering, and the race detector (clean).

## Verified
- Mutation checks:
  - Removing `defer hs.blobs.closeAll()` fails `TestBlobUncommittedPutLeavesNothing`.
  - Removing the write quota check fails `TestBlobQuota`.
- `go test ./internal/plugins/ -run Blob -race`: all 13 tests pass.
- Full `go test ./internal/plugins/...` passes, and so do `go build ./...` and `go vet`.
- `golangci-lint` v2.14.0 (built with Go 1.27): 0 issues.
- Every `guard-*.sh` in `ci.yml` passes locally, except the shellcheck-version guard (no `shellcheck` binary here).
- The deadcode guard reports nothing new from this change. It flags two `internal/logging` funcs only
  because the desktop root needs GTK headers this container lacks.
- No UI surface was touched, so there is no help topic and no screenshot.

## Deferred
- The plugin-owned SQLite size coming off the same quota, once build card 5 (`db:own`) exists.

Verdict: safe to merge.

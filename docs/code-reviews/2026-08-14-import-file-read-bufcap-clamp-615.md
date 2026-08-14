# Review: wasm_import_file host-side read-buffer clamp has no direct test (ut-docs#615)

**Date**: 2026-08-14
**Card**: universaltill/ut-docs#615 — "wasm_import_file: host-side 256KB read-buffer clamp (importFileReadBufCap) has no direct test"
**Complexity**: easy
**Reviewer model**: fresh-context Sonnet subagent, worktree-isolated (per this card's `complexity:easy` tier — see `scrum-master` skill's model routing)

## What shipped

`internal/plugins/wasm_import_file.go`'s `hostImportFileRead` clamps a
guest-claimed `dstCap` parameter to the host constant
`importFileReadBufCap` (256KB = 262144 bytes) regardless of what the guest
claims — but nothing proved it. `TestImportDispatch_RealWasmModule`'s
chunked-read proof is driven entirely by the GUEST's own 64KB buffer
choice, not this host-side clamp (follow-up from #599's review, finding
F5).

Per the issue's own suggested fix (option 1 — extend the existing real-wasm
guest fixture rather than build a new fake-`api.Module` harness, since this
package has no existing lightweight harness for calling a host function
directly), this diff:

- Adds an `"oversized_read"` mode to `testdata/import_guest/main.go`
  (a `Mode` field on the guest's stdin-JSON payload struct, safely
  zero-valued/ignored when absent — the pre-existing default chunked-read
  behavior is unchanged) that allocates a 2MB buffer and issues exactly
  **one** `import_file_read` call claiming the full 2MB as `dstCap`,
  against a staged file with more than `importFileReadBufCap` bytes
  remaining, then reports back exactly how many bytes the host actually
  wrote (`first_read_bytes`).
- New test `TestImportDispatch_HostBufCapClampsOversizedDstCap`
  (`internal/plugins/import_wasm_dispatch_test.go`): stages a 356KB file,
  drives the guest through the new mode, asserts the reported
  `first_read_bytes` equals **exactly** `importFileReadBufCap` (262144) —
  a real `os.File.Read` on a regular file with that much data remaining
  fills the whole target buffer in one syscall, so an unclamped host would
  return far more than 256KB for that one call.

TDD-first: the new test was confirmed failing (missing `first_read_bytes`
key in the response — the old fixture silently ignores the unknown `mode`
field and runs its normal chunked loop instead) before the guest fixture's
`oversized_read` mode existed, then confirmed passing after.

## Independent review (fresh-context Sonnet, worktree-isolated)

**Verdict: SAFE TO MERGE.** No blocker or medium findings.

- **TDD claim independently re-verified, not taken on trust**: the
  reviewer reverted only the guest fixture to `origin/main`, rebuilt,
  and reran the new test — confirmed it fails with the exact real
  assertion message described above (not a build error), then restored
  the fixture and confirmed it passes with `first_read_bytes=262144`.
- **Flakiness risk (low, assessed and accepted)**: `hostImportFileRead`
  does a single `f.Read(buf)` call, no fill-loop, so the test's exact
  equality assertion technically depends on a regular-file `Read` not
  returning a POSIX-permitted short read. The reviewer ran the new test
  8x (`-count=5` plus 3 standalone runs) with no variance, and noted this
  is the same regular-file read assumption `TestImportDispatch_RealWasmModule`
  already depends on — not a novel risk introduced by this diff.
- **Test wiring traced end-to-end** and confirmed to exercise the real
  path: payload `mode` → guest `main()` → `runOversizedRead` → one
  `importFileRead` call with `dstCap=2MB` → host clamp → `Memory().Write`
  → guest reports `first_read_bytes` → test assertion. Confirmed no
  int32/uint32 truncation in the `dstCap`/clamp arithmetic (2097152 →
  262144, correctly fits `int32` on return).
- **Backward compatibility confirmed**: `TestImportDispatch_RealWasmModule`
  and `TestImportFileCloseAllOnPluginUnload` (which never set `mode`) both
  still pass unmodified.
- **Resource safety**: the guest's 2MB allocation is the largest single
  guest-side buffer in the wasm test suite (previous max 300KB); no
  explicit memory-page ceiling is configured in `wasm_runtime.go`'s
  `wazero.NewRuntimeConfig()`, so wazero's default (4GiB max) applies —
  noted as informational (untested size territory) rather than a risk.
- Naming, comment style ("why not what"), and scope confirmed to match
  the surrounding file's existing conventions.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l` on both changed files —
  clean.
- `go test ./internal/plugins/... -run TestImportDispatch -v` and the
  whole `internal/plugins` package — all green.
- `bash scripts/ci/guard-data-access.sh` / `guard-kiosk-engine.sh` /
  `guard-plugin-menu-read.sh` / `guard-i18n.sh` / `guard-compliance-claims.sh`
  / `guard-help-topics.sh` — all pass (N/A scope: pure Go test + WASM
  test-fixture code, no SQL, no routes, no user-facing strings, no money
  type).
- Whole-repo `go test ./...` (43 packages) green.

## N/A for this diff

Pure internal test code (a new test plus a test-fixture-only guest mode):
no SQL, no i18n strings, no money-type conversion, no plugin manifest
change, no self-order/kiosk route, no user-facing UI or manual topic.

## Safe to merge

Yes. No findings required a fix; nothing deferred.

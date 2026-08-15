# Review: wasm host-fn buffer ABI — invalid dstPtr no longer loses stream bytes (ut-docs#614)

**Date**: 2026-08-15
**Card**: universaltill/ut-docs#614 — "wasm host-fn buffer ABI: an
out-of-range guest pointer silently consumes stream bytes before failing"
**Complexity**: medium
**Reviewer model**: Opus subagent, fresh context, worktree-isolated (per
this card's `complexity:medium` tier — see `scrum-master` skill's model
routing)

## What shipped

`hostImportFileRead` (`internal/plugins/wasm_import_file.go`) and
`hostTCPRead` (`internal/plugins/wasm_tcp.go`) each called their
underlying stream's `Read` (a staged file's cursor / a TCP socket) BEFORE
validating that the guest-supplied `dstPtr`/`dstCap` destination was
addressable in wasm guest memory. A guest passing a bad `dstPtr` got
`hostErrInvalid` back, but the bytes already pulled off the stream were
gone for good — neither host function has a seek-back. Follow-up from the
independent review of #599 (`docs/code-reviews/2026-08-12-import-plugin-dispatch.md`,
finding F3), which flagged the identical shape in all three
buffer-writing host functions in this module.

This diff adds a pre-read bounds check —
`if _, ok := m.Memory().Read(dstPtr, bufCap); !ok { return hostErrInvalid }`
— before the underlying `f.Read`/`conn.Read` call, in both functions.
Scoped deliberately to these two (not every `writeGuest`-based host
function in `wasm_hostfns.go`): they're the only ones that pull from a
non-replayable external cursor before writing to guest memory.
`hostStorageGet`/`hostSettingsGet` re-fetch idempotently from SQLite on
retry; `hostHTTPRequest` does not (see Deferred below).

TDD-first: `TestImportDispatch_InvalidPtrDoesNotConsumeStreamBytes`
(`internal/plugins/import_wasm_dispatch_test.go`) and
`TestHostTCPInvalidPtrDoesNotConsumeSocketBytes`
(`internal/plugins/wasm_tcp_test.go`) were confirmed failing against the
pre-fix code (exact byte-loss/lost-payload symptom described below),
before being confirmed passing against the fix — both by the implementer
and, independently, by the reviewer (see below). Each drives a new guest
mode (`invalid_ptr_read` in `testdata/import_guest/main.go`,
`invalidptrread` in `testdata/tcp_guest/main.go`): one host-function call
with a deliberately out-of-bounds `dstPtr` (`0xFFFFFF00`), then a normal
read that must still recover the full, untouched data.

`ut-docs/reference/plugin-host-functions.md` gained a note on the
convention (later corrected — see M1 below).

## Independent review (Opus, fresh context, worktree-isolated)

**Verdict: SAFE TO MERGE.** No blocker findings.

- **Bounds-check correctness re-derived, not assumed**: confirmed
  `bufCap` (the post-#615-clamp variable) is checked, not `dstCap` —
  using `dstCap` would have been wrong (over-strict whenever
  `dstCap > tcpReadBufCap`/`importFileReadBufCap`). The checked region is
  a provable superset of the region the later write actually touches in
  both functions, so the check can never pass where the write would fail.
- **TOCTOU explicitly reasoned through, not hand-waved**: wasm linear
  memory only ever grows, never shrinks, so a region valid at check time
  stays valid at write time regardless of interleaving; and no guest code
  re-enters between the check and the write within one host call. Noted
  approvingly that the fix discards the slice `Memory().Read` returns
  (`_, ok :=`) rather than holding it — holding it would itself be a bug,
  since it's a live view into a buffer `memory.grow` can reallocate.
- **Over-strictness checked and found to be in the safe direction**: the
  new check can reject a guest-ABI-violating call the old code would have
  half-served by writing past the guest's real allocation — strictly
  better than the pre-fix behavior, not just different.
- **Scope decision independently re-derived** across all five
  `writeGuest` call sites in `wasm_hostfns.go`: confirmed
  `hostStorageGet`/`hostSettingsGet` are genuinely retry-safe (idempotent
  SQLite reads) and correctly excluded. Found the stated rationale does
  **not** hold for `hostHTTPRequest` — filed as ut-docs#754 (see
  Deferred).
- **TDD claim independently re-verified**: the reviewer commented out
  only the two new `if` blocks (not reverted via git, to keep the rest of
  the diff intact), rebuilt, and reran both new tests — confirmed
  `TestImportDispatch_...` fails with exactly `10240 − 64` bytes read
  (the probe's `probeCap`) and a mismatched SHA256; confirmed
  `TestHostTCPInvalidPtrDoesNotConsumeSocketBytes` fails with
  `read_data=""` and a host-side `EOF` log — the whole pushed payload
  consumed by the bad-pointer call. Restored both blocks verbatim and
  reran: both pass, tree byte-identical to the pre-verification commit.
- **Test-pointer validity checked**: `0xFFFFFF00` requires ~4GiB of
  wasm32 linear memory to ever be valid (these test guests use a few MB),
  and the test cannot false-pass even if that changed — a valid probe
  would return 64 and fail both assertions loudly.
- **Test isolation confirmed**: unique plugin IDs, `t.TempDir()`,
  ephemeral TCP port, matching `t.Cleanup` conventions already in the
  file.
- **Two recurring pipeline bug classes checked**: no file-write handler
  in this diff (N/A for missing `os.MkdirAll`); no cwd-relative path (the
  one file write in the new test path goes through the existing
  `writeFileWithParents` helper, which already does `MkdirAll`).
- **No secrets, no real client/shop name**: grepped the diff for
  credential-shaped literals; only test literals
  (`pushed-before-any-request`, `com.test.*`).
- **Scope creep confirmed absent**: exactly 6 files, all under
  `internal/plugins/`; no SQL, no UI, no i18n, no money, no
  `web/help/` topic — matches this being a plugin-runtime internal
  robustness fix, not a plugin-facing behavior/API change.

### Findings triaged

- **M1 (fixed)**: the doc note in `plugin-host-functions.md` originally
  claimed the "-4 never costs you data" guarantee for *any* buffer-ABI
  call, including `http_request` — false for `http_request`, whose retry
  re-issues the live request rather than re-fetching a cached response.
  Narrowed the wording to the two streaming calls this fix actually
  covers, and added an explicit warning against assuming it for
  `http_request`, with a pointer to the new follow-up card.
- **M2 (deferred, filed as universaltill/ut-docs#754)**: pre-existing,
  outside this card's scope — `hostHTTPRequest`'s buffer-ABI retry
  ("call again with a bigger buffer") re-issues the underlying HTTP
  request rather than re-fetching a cached response. For a non-idempotent
  request (a payment/ERP-connector plugin POSTing a charge or order with
  an undersized response buffer), the documented retry path can duplicate
  a real side effect. Same failure class as #614 (a non-replayable
  external source behind a retry-oriented ABI), different mechanism
  (ordinary undersized-buffer use per the documented ABI, not a malformed
  guest) — genuinely a different card, not a #614 regression.
- **N1–N3 (nits, not fixed)**: (1) the two new pre-checks call
  `m.Memory().Read` directly rather than the existing `readGuest` helper
  — equivalent here, arguably clearer as a direct destination-probe; (2)
  the `0xFFFFFF00` sentinel is a documented package const in the import
  guest fixture but a function-local const in the TCP guest fixture —
  minor cross-fixture inconsistency; (3) the new TCP fixture handler's
  `time.Sleep(500ms)` is unnecessary (the payload is written before the
  sleep and TCP delivers already-buffered bytes ahead of EOF) and leaves
  a goroutine running slightly past test end — cosmetic, no panic risk.
  Left as-is: none affect correctness, and process-depth guidance is to
  fix what matters rather than grind on nits.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l` on every changed file —
  clean (confirmed independently by both implementer and reviewer).
- `go test ./internal/plugins/... -run 'TestImportDispatch|TestHostTCP|TestTCPConnRegistry' -v`
  — all 14 tests pass, including the two new ones.
- Whole `internal/plugins` package (plus `marketplace`/`oauth`
  subpackages) — green, no regressions. Reviewer additionally ran the
  affected suites with `-count=2` and `-race` — clean.
- Whole-repo `go test ./...` (43 packages) — green.
- `bash scripts/ci/guard-data-access.sh` / `guard-kiosk-engine.sh` /
  `guard-plugin-menu-read.sh` — all pass (N/A scope confirmed: no SQL, no
  self-order/kiosk route, no plugin-menu read in this diff).

## N/A for this diff

No i18n strings, no money-type conversion, no plugin manifest change, no
self-order/kiosk route, no user-facing UI, no `web/help/` manual topic —
this is an internal plugin-runtime robustness fix; the wire-visible
`dstCap`/`dstPtr` semantics for a well-behaved guest are unchanged, only
the malformed-guest edge case's side effect improves.

## Safe to merge

Yes. M1 fixed in this same branch; M2 filed as a follow-up card
(universaltill/ut-docs#754); N1–N3 deferred as genuine nits.

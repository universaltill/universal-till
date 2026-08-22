# Code review: TCP transport small-fixes (live revocation, version-update leak, handle numbering, exact-boundary tests)

**Card:** universaltill/ut-docs#606 (p3, hardware, complexity: easy)
**Branch:** `fix/606-tcp-transport-small-fixes`
**Date:** 2026-08-22
**Complexity:** easy — Dev inline (Sonnet), Review via an independent
fresh-context Sonnet subagent (isolated worktree). One review round found
two non-blocking findings; both fixed in this same diff and re-verified —
not treated as earning a second full round (neither was blocker-class:
money/tax, data loss, or security).

## What shipped

Follow-up from ut-docs#542's independent review (2026-08-12), four items
deliberately left out of that diff to keep it reviewable:

1. **Live permission revocation.** `tcp_write`/`tcp_read` previously
   authorized purely via the already-open handle — a live `tcp:`
   revocation had no effect until the plugin was next disabled, contradicting
   `wasm_hostfns.go`'s own module-wide invariant ("every capability is
   permission-gated per call... denials are audited and grants are
   revocable live"). Both now re-check the plugin's grant on every call via
   a new `tcpAddrAuthorized` helper (exact `tcp:<host>:<port>`, else
   `tcp:*`), the same exact-then-wildcard pattern `tcp_open` already used.
2. **`CloseAll` on a plugin version update.** `WasmRuntime.Sync`'s
   module-drop loop only force-closed TCP handles for a plugin gone
   inactive; an updated-but-still-active plugin reaches `w.load`'s
   recompile branch instead, which never force-closed the OLD module's
   open sockets — leaked (bounded at 4 FDs/plugin) until the plugin was
   next disabled or the process exited. `tcpConns.CloseAll(pluginID)` is
   now called from that recompile branch too.
3. **Handle-counter reset across `CloseAll`.** Accepted, not fixed, per the
   card's own "fix or document" acceptance criterion — a disable→re-enable
   cycle (or now, a version reload) restarts a plugin's handle numbering at
   0; impact is academic since `byPlugin`/`next` are keyed strictly
   per-`pluginID`, so a plugin can only ever alias its own past
   connections. `tcpConnRegistry`'s and `CloseAll`'s doc comments now state
   this precisely (numbering is monotonic within one continuous
   registration lifetime, not across a `CloseAll`), replacing the previous
   comment's ambiguous "registry lifetime" wording the card flagged as
   self-contradicting.
4. **New regression tests**, at the host-function layer, not just the bare
   registry: `TestHostTCPExactPermissionBoundary` (a `tcp:<host>:<portA>`
   grant denies `<portB>` on the same host — real second fixture, asserts
   zero accepted connections on the ungranted port), and
   `TestHostTCPCrossPluginHandleRejected` (a different plugin's real
   `hostState`/context, not the registry directly, gets `hostErrNotFound`
   from `hostTCPWrite`/`hostTCPRead` and a no-op from `hostTCPClose` on a
   handle it doesn't own — plus confirms the owning plugin's handle is
   untouched).

Files: `internal/plugins/wasm_tcp.go`, `internal/plugins/wasm_runtime.go`,
`internal/plugins/wasm_tcp_test.go` (all in `universal-till`); doc updates
in `ut-docs/reference/plugin-host-functions.md` and
`ut-docs/architecture/wasm-runtime.md` (companion PR in that repo — the
version-update gap and the per-call live-revocation behavior those two docs
explicitly flagged as "not yet"/a known gap are now closed).

## Independent review (Sonnet, fresh context, isolated worktree)

Read the full diff cold (no dev reasoning), ran `go build`, `go vet`,
`gofmt -l`, `go test ./internal/plugins/... -run TCP -v -race` (all 19 TCP
tests), and a full-package `go test ./internal/plugins/...` (no `-race`,
beyond the assigned scope) as extra diligence. Independently reverted and
restored both behavioral fixes to confirm each regression test is
load-bearing.

### TDD re-verification (reviewer's own revert/restore, not taken on word)

- **Item 1**: reverted the `tcpAddrAuthorized` re-check in both
  `hostTCPWrite`/`hostTCPRead`. `TestHostTCPWriteReadDeniedAfterLiveRevocation`
  failed (`tcp_write after live revoke = 0, want -2`, then a nil-pointer
  panic in the read path falling through to the module it should never
  have reached). Restored: test passes (14.08s), diff empty after restore.
- **Item 2**: removed `tcpConns.CloseAll(pluginID)` from `w.load`'s
  recompile branch. `TestHostTCPCloseAllOnPluginVersionUpdate` failed
  (`registry still holds the old version's handle after a version update`,
  16.5s). Restored: passes (14.07s), diff empty after restore.

### Findings

**Non-blocking — FIXED in this diff.** `tcpAddrAuthorized`'s exact-then-
wildcard check ran on every `tcp_write`/`tcp_read`, not only `tcp_open` —
for a plugin holding only the documented common-case `tcp:*` wildcard grant
(configurable terminal plugins), the exact-address probe fails on every
single call of an active protocol exchange, and the auditing
`plugins.CheckPermission` unconditionally writes a `permission_denied`
`audit_log` row on that failure before falling through to the wildcard
success one line later — flooding the audit trail with false "denied" rows
for calls that actually succeeded. Fixed: `tcpAddrAuthorized` now probes
exact-then-wildcard via the repository's plain, non-auditing
`CheckPermission` (`granted, exists, err`) to decide authorization, and
only falls through to the auditing `plugins.CheckPermission` calls (both of
them, matching `tcp_open`'s pre-existing pattern) on a genuine denial —
still fully audited, just no longer noisy for the expected-successful case.

**Non-blocking — FIXED in this diff (cleanup, not a live bug).**
`hostTCPWrite`/`hostTCPRead` looked up a handle's connection and its
authorization address via two separately-locked registry calls
(`Get` then `Addr`), leaving a re-lock window. Analysis (confirmed by the
reviewer): a same-plugin race can't misdirect a call onto a different
connection — handle numbers are never reused within one registration
lifetime, so a closed handle just reports not-found. A same-plugin
cross-reload race (a `CloseAll` resetting the counter to 0 while a new
`tcp_open` claims the same number) could at worst produce a spurious
closed-connection I/O error, never data leakage or misdirected I/O onto the
new connection — the racing call's `conn` was already captured from the
old, closed socket before the reload happened. Not a real bug, but the
window is free to close: added `tcpConnRegistry.GetWithAddr`, a single
locked lookup returning both, and switched both call sites to it. The
now-unused two-call `Addr` accessor was removed rather than left as dead
API surface; `Get` stays (still used by tests and elsewhere in the
registry's own assertions).

**Confirmed correct, no changes needed:**

- Item 3's doc comments accurately describe the accepted gap — verified
  directly against `Open`/`Get`/`CloseAll`'s map access that `byPlugin`/
  `next` are strictly per-`pluginID`, so the "can only alias its own past
  connections" claim holds.
- Neither new test is tautological — both exercise the described scenario
  through the real host functions (not the bare registry), with concrete,
  falsifiable assertions (a second real fixture with a zero-accepted-
  connections check; a second plugin's real `hostState` denied by name).
- Both recurring bug classes this pipeline watches for: not applicable
  (`TestHostTCPCloseAllOnPluginVersionUpdate`'s new test file write
  correctly calls `os.MkdirAll` first; every path in the diff — test and
  production — is built via `filepath.Join(w.baseDir, ...)` from an
  explicit caller-supplied absolute base, no cwd-relative usage anywhere).
- `hostTCPRead`'s live re-check runs before the destination-buffer memory
  validation and before touching the socket — a denied call still costs
  the guest nothing, preserving the ut-docs#614 invariant it sits above.
- `revokePerm`'s use of `PluginRepo.SetPermission(..., false)` genuinely
  simulates a live admin revoke (matches `CheckPermission`'s "declared but
  not granted" denial path), not a different code path than a real revoke
  would exercise.
- No real client/shop name, no secret-shaped literal anywhere in the diff.

Two recurring bug classes this pipeline watches for, checked again after
the review fixes: not applicable — no new file-write path, no cwd-relative
path introduced by `GetWithAddr` or the non-auditing `CheckPermission`
calls (pure DB/in-memory lookups, no filesystem touched).

## Verified beyond the automated suite (this session)

- `gofmt -l internal/plugins/wasm_tcp.go internal/plugins/wasm_runtime.go internal/plugins/wasm_tcp_test.go` — clean.
- `go build ./...`, `go vet ./internal/plugins/...` — clean.
- `scripts/ci/guard-data-access.sh` — clean (the test file's raw SQL for
  seeding a second `plugin_catalog` version row is `_test.go`-exempt, and
  mirrors `seedPlugin`'s own existing seeding pattern).
- `go test ./internal/plugins/... -run TCP -v -race` — all 19 TCP tests
  pass, both before and after the review-finding fixes (196.5s / 192.3s).
- `go test ./internal/plugins/...` full package, no `-race` — passes
  (73.5s). With `-race`, the full package (much wider than this diff's
  scope — supervisor/IPC/network host-fn tests etc.) hit `go test`'s
  *default* 600s timeout on sheer volume plus race-instrumentation
  overhead, not a hang: reran with `-timeout 900s` and it passed clean at
  642.6s. **Not a CI risk** — `.github/workflows/ci.yml` already runs
  `internal/plugins` with `-timeout 20m` specifically for this reason
  (ut-docs#643/#753/#776), a fix that predates this diff.
- Both TDD claims (items 1 and 2) personally re-verified red-then-green,
  see above — not taken on the dev pass's word.
- No manual/help-topic work owed: this is a plugin-author-facing host
  function surface (`reference/plugin-host-functions.md`,
  `architecture/wasm-runtime.md`), not anything a shop owner sees or does.
  Both docs updated in the same session (companion `ut-docs` PR) to state
  the version-update gap is closed and the per-call live-revocation
  behavior now holds, matching what those files previously flagged as
  known gaps.

## Safe-to-merge verdict

**Yes.** Both behavioral fixes (items 1 and 2) are real, minimal, and
independently re-verified red-then-green. Item 3 is a deliberate,
correctly-reasoned accept-and-document per the card's own criterion. Item
4's tests genuinely close the coverage gap the card named, at the right
layer (host functions, not just the registry). The two findings the
independent review raised were real but non-blocking (an audit-log noise
issue for the documented common case, and a narrow, already-fails-safe
TOCTOU window) — both fixed cheaply in this same diff rather than deferred,
since neither needed a second review round on its own.

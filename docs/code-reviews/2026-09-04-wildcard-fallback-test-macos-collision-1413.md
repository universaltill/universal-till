# Code review — fix macOS-only failure of the wildcard-fallback test (ut-docs#1413)

- **Date**: 2026-09-04
- **Card**: ut-docs#1413 (`complexity:easy`), duplicate closed: ut-docs#1535
  (independent fresh repro of the identical failure, filed 2026-09-04 with a
  `security`/`p2` label — folded into this one rather than fixed twice)
- **Branch**: `fix/1413-wildcard-fallback-test-macos-collision`
- **Review**: independent subagent, fresh-context Sonnet (per this card's
  `complexity:easy` routing), read-only. One round, no findings that needed a
  code change.

## What shipped

`internal/server/listen_test.go`'s `TestListenWithFallback_WildcardHostFallsBackToLoopback`
(added in the ut-docs#1169 fix, `2026-08-27-no-wildcard-fallback-bind-1169.md`)
verifies that when the *configured* listen host is wildcard (`""`, `"0.0.0.0"`,
`"::"`, `"::0"`, `"0:0:0:0:0:0:0:0"`, `"::ffff:0.0.0.0"`) and the bind fails,
`listenWithFallback`'s fallback path degrades to loopback-only (`127.0.0.1`)
rather than repeating the wildcard bind — the security property that closed
ut-docs#1169 (an unconfigured, un-provisioned till otherwise being reachable
and pairable from the whole LAN).

To *drive* that fallback path, the test needs the initial bind to genuinely
fail. It did this by occupying `127.0.0.1:0` first, then attempting each
wildcard-host spelling on that same port. On Linux this reliably collides
(binding a wildcard host on a port already held on `127.0.0.1` fails there).
**On macOS it does not**: a dual-stack wildcard bind can succeed alongside an
already-bound `127.0.0.1:<port>` listener, so `net.Listen` on the wildcard
host just succeeds — no error, no fallback, and the loopback-degrade
assertion this test exists to enforce silently goes untested on darwin, while
Linux CI stays green throughout. Not a production bug: `listenWithFallback`
and `isWildcardHost` (`internal/server/server.go:361-405`) are unchanged and
were already believed correct — this is a test-portability bug only,
reproduced independently twice (ut-docs#1413 on 2026-09-02, ut-docs#1535 on
2026-09-04, both against a clean `main`, both confirming CI stays green).

**The fix**: occupy the port on the *same host spelling under test* (via
`net.JoinHostPort(host, "0")`) instead of a hardcoded `"127.0.0.1:0"`, then
retry the bind on that identical host+port string. Binding the exact same
address twice fails with `EADDRINUSE` on every OS Go supports — there is no
cross-address-family ambiguity left for a platform to resolve differently,
so this can't drift out of sync with macOS/Linux socket semantics the way the
original did.

## Independent review — no findings requiring a change

The reviewing subagent verified, rather than assumed:
- **The fix's cross-platform reasoning**, spelling-by-spelling: all six
  `net.JoinHostPort(host, "0")` outputs are valid addresses `net.Listen`
  accepts identically to before; the collision is now a literal identical
  bind, not a cross-address one.
- **No TOCTOU/isolation gap**: `busy.Close()` is deferred within a
  non-parallel subtest, so it can't race the `listenWithFallback` call —
  confirmed with `go test -race -count=5`, clean.
- **No sibling instance of the same bug elsewhere** — grepped
  `isWildcardHost`, `IsUnspecified`, and the `127.0.0.1:0`-occupy pattern
  across the repo; every other hit is a genuine loopback-only bind
  (`cmd/unitill-desktop`'s control socket, `internal/lanip`, unrelated
  fallback tests) with no wildcard-collision assumption riding on it.
- **Diff scope**: only `internal/server/listen_test.go` touched (16
  insertions, 6 deletions) — no production code, no unrelated files.
- `go build ./...`, `go vet ./internal/server/...`,
  `go test ./internal/server/... -run TestListenWithFallback -v -count=3`
  and an additional `-race -count=5` pass: all green.

## What was verified rather than taken on trust (this session)

- **Full gate**: `gofmt -l .` empty on the touched file; `go build ./...`
  clean; `go test ./...` — every package green (`internal/server` 1.8s,
  `internal/data` 19.9s, `internal/pages` 67.6s, `internal/plugins` 73.7s,
  no timeouts); `go vet ./...` clean; all 20 CI-blocking guard scripts in
  `ci.yml`'s `build` job pass locally.
- **No mac available in this sandbox** — the macOS symptom itself can't be
  directly re-reproduced here (same documented limitation as other
  darwin-only findings in this repo's review history). The fix's soundness
  rests on the platform-independent construction (identical address bound
  twice always collides), not on re-observing the original failure, and
  Linux stays green under the new version exactly as before, confirming no
  regression.
- **Secrets/PII**: none in the diff.
- **Scope**: test-only change; no `web/`/`internal/pages/` touched, so the
  i18n/UX/help-manual/compliance-wording guards don't apply — confirmed, not
  assumed (all still ran clean regardless, per the full gate above).

## Verdict

Safe to merge. Restores real macOS coverage of the ut-docs#1169
loopback-degrade security guarantee without weakening the assertion or
skipping it on darwin — matching this card's own explicit "prefer keeping the
guard meaningful on developer laptops" instruction. ut-docs#1535's duplicate
report is closed in favor of this one.

# Code review: mDNS noisy IPv6 bind logs on hosts without IPv6 support (ut-docs#272)

**Card:** universaltill/ut-docs#272 (p3, complexity: easy)
**Branch:** `fix/272-mdns-noisy-ipv6-logs`
**Date:** 2026-08-22
**Scope:** `internal/discovery/browse.go`, `internal/discovery/browse_test.go`

## What shipped

`github.com/hashicorp/mdns`'s client always tries a udp6 bind/multicast
join first, and logs two `[ERR]`-tagged lines straight to Go's global
stdlib `log` package (bypassing `internal/logging` entirely) before any
fallback runs. On a host that can't open IPv6 sockets at all — containers,
many Pi/kiosk images, IPv6-disabled sandboxes, including this very build
sandbox — this fires on every `discovery.Browse()` call, and the `[ERR]`
tag makes it look like a real fault in POS logs even though IPv4 discovery
works fine (no functional impact — cosmetic/observability only, per the
card).

Added `detectIPv6Support()`: a real `net.ListenUDP("udp6", ...)` bind+close
— the exact same condition mdns's own client bind fails on — behind a
package-var seam (`ipv6Supported`, mirroring the file's existing
`mdnsQuery` seam). `Browse()` now checks it upfront: when false, it skips
straight to the existing v4-only attempt instead of provoking the noisy
v4+v6 attempt first. On a host with real IPv6 support, behavior is
unchanged — the pre-existing v4+v6-first-then-v4-only-retry code path is
untouched, byte-for-byte.

`advertiser.go` (the mDNS *server*/advertise side) needed no change: it
already silently discards its own udp6 bind error (`ipv6List, _ :=
net.ListenMulticastUDP(...)`) and never logs it — only the *client*
(`Browse`) side produces the noisy lines this card is about.

Out of scope, and explicitly noted rather than hidden: mdns's
`[INFO] mdns: Closing client ...` line (from `client.Close()`, always
deferred in `mdns.Query`) still fires on every scan regardless of IPv6
support — it isn't IPv6-specific and isn't `[ERR]`-tagged, so it isn't the
"looks like a real fault" problem this card is about.

## Review

Independent review via a fresh-context Sonnet subagent (complexity: easy),
isolated worktree. Found one real issue, fixed in this same diff:

- `TestBrowse_ReturnsErrorWhenTheV4OnlyAttemptFailsOnAHostWithNoIPv6Support`
  didn't actually pin down the new fast path — its injected `mdnsQuery`
  failed unconditionally regardless of `DisableIPv6`, so the test's three
  assertions (`err != nil`, 0 candidates, error message substring) were
  satisfied identically by both the buggy (pre-fix, two-call retry) and
  fixed (one-call fast-path) code — the reviewer proved this by reverting
  just `Browse()`'s body to its pre-fix form and confirming the test still
  passed, 3/3 runs. Fixed by adding the same call-count/`DisableIPv6`
  tracking its sibling test
  (`TestBrowse_SkipsV4V6AttemptWhenHostHasNoIPv6Support`) already used,
  asserting exactly one call with `DisableIPv6=true`.

I independently re-verified this personally after the fix (not just
trusting the reviewer's or the diff's claim): reverted `Browse()`'s body to
its pre-fix form again (keeping the seam so it compiles), confirmed BOTH
new fast-path tests now fail against that pre-fix code, then restored the
real fix and confirmed the full `internal/discovery` package is green
again.

All eight pre-existing `Browse`-calling tests were updated to force
`ipv6Supported = true` via a new `forceIPv6Supported(t, bool)` helper —
required because this sandbox's real IPv6 support is false (confirmed:
`net.ListenUDP("udp6", ...)` genuinely fails here with "address family not
supported by protocol", the exact condition this card is about), and
several of those tests assert an exact `mdnsQuery` call sequence that is
only true on the v4+v6-first path. The reviewer verified each forced test
still exercises what its own doc comment says it does, and that forcing
the seam didn't silently narrow any of their existing coverage.

## Testing

- `gofmt -l internal/discovery/` — clean.
- `go build ./...`, `go vet ./internal/discovery/...` — clean.
- `go test ./internal/discovery/... -v -race` — all pass, including the
  full package (advertiser/browse/discovery tests together).
- `go test ./... -race` (full repo) — one apparent failure,
  `internal/plugins`, purely an artifact of the default 10-minute
  `go test` timeout; this package's own documented CI configuration runs
  it with `-timeout 20m` (per this repo's own CLAUDE.md, ut-docs#643/#753/
  #776) because its real runtime is a known, already-handled quantity.
  Re-ran `go test ./internal/plugins/... -timeout 20m` standalone: passes
  in 75.7s. Unrelated to this diff — `internal/plugins` isn't touched by
  it.
- `scripts/ci/guard-data-access.sh` — clean (no SQL touched; this change
  doesn't go near the data layer).

## Safe-to-merge verdict

Yes — minimal, correctly scoped to `complexity:easy`, existing behavior on
IPv6-capable hosts is provably unchanged, the new fast path is covered by
tests that fail without the fix and pass with it (personally re-verified),
and the one real review finding was fixed and re-verified in the same
diff.

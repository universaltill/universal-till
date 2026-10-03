# Review: netreach status-bar probe makes no request in demo mode (ut-docs#3588)

**Branch:** `fix/3588-netreach-demo-mode` (PR #1676, head `ecec93bb`) · **Design:** ADR-0113 §1.6; closes F1 of `2026-10-03-demo-mode-no-outbound-network-2795.md`
**Dev model:** Sonnet · **Review model:** Opus 5.5 (independent)

## What shipped

- `internal/netreach/netreach.go`:
  - `Monitor.Enabled()` now also requires `!netaccess.Demo()`, read live on
    every call (not only at construction). `Status()` already short-circuits
    on `!Enabled()`, so in demo mode it returns `Unknown` and never starts a
    probe goroutine: zero requests, and the light reads "Unknown" as it did
    before #3095.
  - `New()`'s fallback client is now `netaccess.NewClient(0)` instead of
    `http.DefaultClient`: same zero timeout, same `http.DefaultTransport`
    (resolved per request), but wrapped in `guardTransport`, so any request
    that got past `Enabled()` would still fail with `ErrDemoDenied`.
    Defence in depth, which is exactly what F1 recommended ("do both").
- New `internal/netreach/demo_test.go` (`package netreach`):
  - `TestStatusMakesNoRequestInDemoMode`: explicit client against an
    httptest server, demo on. Asserts `Enabled()==false`, three
    `Status()==Unknown` calls, and 0 server hits.
  - `TestDefaultClientDeniedInDemoMode`: no `Client` (the production path,
    `internal/pages/init.go`). Calls the default client directly and asserts
    `errors.Is(err, netaccess.ErrDemoDenied)`.

## Verified beyond automated tests

The review sandbox could not clone `universal-till` (ut-docs#3431/#2928), so
**I did not run `go test`, `go vet` or a live binary myself.** What I did:
read the full post-change `netreach.go`, `netreach_test.go`, `demo_test.go`
and `internal/netaccess/netaccess.go`, traced the code by hand, searched the
repo with GitHub code search, and checked the PR's CI results (9/9 green on
`ecec93bb`: build, compile, e2e, playwright, desktop-shell, contract,
simulator, authors, adr-taxonomy-guard).

- **TDD claims, checked by reasoning (not by a revert run):**
  - `TestStatusMakesNoRequestInDemoMode` would fail on the old code.
    `Enabled()` was just `probeURL != ""`, and the endpoint is valid with
    `allowLoopback`, so the first assertion fails at once. Even without that
    assertion, `Status()` would start a probe that hits the server.
  - `TestDefaultClientDeniedInDemoMode` would fail on the old code.
    `http.DefaultClient` has no demo guard, so it would try a real DNS lookup
    of `cloud.example.test`. The `.test` TLD never resolves, so that returns
    a `*url.Error` wrapping a DNS error, not `ErrDemoDenied`. With the new
    client, `guardTransport.RoundTrip` returns `ErrDemoDenied` before any
    dial. `http.Client` wraps that in a `*url.Error`, which has `Unwrap`, so
    `errors.Is` matches.
- **Compile check by hand:**
  - Imports in `demo_test.go` (`errors`, `net/http`, `httptest`,
    `sync/atomic`, `testing`, `time`, `netaccess`) are all used. The new
    `netaccess` import in `netreach.go` is used twice.
  - `demo_test.go` is `package netreach`, so reading `m.client` and setting
    `allowLoopback` is legal.
  - Signatures match `netaccess.go`: `NewClient(time.Duration) *http.Client`,
    `Demo() bool`, `SetDemo(bool)`, `var ErrDemoDenied error`.
    `atomic.Int32` needs Go 1.19+, and `netreach_test.go` already uses
    `atomic.Int64`. Formatting looks gofmt-clean. The green `build` and
    `compile` jobs agree.
- **Existing tests unaffected:** every `New(...)` call in `netreach_test.go`
  passes an explicit `Client`, so the new fallback never applies to them.
  None of them turns demo mode on, so the extra `Enabled()` condition is
  always true there. No test in the package calls `t.Parallel()`, so the
  process-global demo switch set by `demo_test.go` (and reset in
  `t.Cleanup`) cannot leak into a concurrently running test. Go runs each
  package's tests in its own process, so other packages' `SetDemo` calls
  can't interfere either.
- **Concurrency:** `netaccess.Demo()` is an `atomic.Bool` load, so calling it
  from `Enabled()` while `Status()` runs on other goroutines (or while a
  probe goroutine is in flight) is race-free.
- **No behaviour change with demo off:** the switch defaults to false and is
  set only by `SetDemo` (from `app.Run` with `cfg.Demo`, or from tests). With
  it off, `Enabled()` is unchanged. The new fallback client also behaves the
  same as `http.DefaultClient`: zero timeout, `http.DefaultTransport`
  resolved per request, no `CheckRedirect` or `Jar`.
  `CloseIdleConnections` is forwarded.
- **Other callers:** in production, `netreach.New` is called only in
  `internal/pages/init.go` (no `Client` passed, so it now gets the guarded
  client). `Monitor.Status` is called only by `internal/pages/net_status.go`.
  `Enabled()` has no callers outside the package. Nothing else is affected.
- **Scope (production `http.DefaultClient` left in `internal/`):** code
  search for `"http.DefaultClient"` finds, besides `netreach.go` itself (the
  index still shows `main`), only:
  - comments in `internal/updates/updates.go` and
    `internal/selfupdate/selfupdate.go`, which already use
    `netaccess.NewClient(0)`;
  - `_test.go` files in `internal/pages`, `internal/plugins` and
    `cmd/unitill-desktop`;
  - docs.

  After this PR, no non-test production file uses it.
- No UI template or help text changed. The light shows "Unknown" on the demo
  till, the existing pre-#3095 behaviour. That is not a new state for shop
  owners, so no help topic needs updating. No real shop names and no secrets
  appear in the test data (`cloud.example.test` is a reserved TLD).

## Findings

| # | Severity | Status | Summary |
|---|---|---|---|
| F1 | Info | No action needed | `TestStatusMakesNoRequestInDemoMode` ends with `time.Sleep(20ms)` "to let any stray probe goroutine land". Because `Enabled()` short-circuits, no goroutine is ever started, so the sleep only guards against a future regression. It cannot make the test flaky: it can only add hits, never hide them for long. This matches the existing `TestDisabledIsUnknownAndNeverProbes`. |
| F2 | Info | No action needed | The package doc comment in `netreach.go` doesn't mention demo mode. The `Enabled()` doc comment does, and that's where a reader would look. |
| F3 | Info | No action needed | The F1 row of `2026-10-03-demo-mode-no-outbound-network-2795.md` still says "Not fixed". Review records are point-in-time history, so it is left as-is. This record is where F1's closure is written down. |

## Verdict

**Safe to merge.** The change is the exact two-part fix F1 recommended:
zero requests in demo mode through `Enabled()`, plus a guarded fallback
client in case anything gets past it. It is minimal and race-free, and it
doesn't change behaviour when demo mode is off. By hand-tracing, both new
tests are real regression guards that would have failed on the old code.
CI is fully green on the head commit. The limitation is stated above: this
review did not run the tests or revert the fix itself, because the sandbox
could not clone the repo. Its confidence comes from reading the code and
from CI, not from a local mutation run.

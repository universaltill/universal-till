# Code review: internal/plugins -race timeout is margin, not a deadlock (ut-docs#2156)

**Date:** 2026-09-12
**Card:** universaltill/ut-docs#2156
**Branch:** `fix/2156-plugins-race-timeout-margin`
**Reviewed commit:** `5d07d115`
**Complexity:** medium (Dev: Sonnet; Review: independent Opus subagent, different
model from the author, isolated worktree)

## What shipped

The card, filed out of ut-docs#2143's own review, claimed
`go test ./internal/plugins/ -race` "never completes (hangs)" and named
`plugins/tax-tr/okc/sim`'s `select {}` as the surviving goroutine in the
`-race` timeout's dump. This change re-diagnoses that as a **timeout-budget
problem, not a deadlock**, and lands three things:

1. **`plugins/tax-tr/okc/sim/sim.go`** — the simulator's "Silent" failure mode
   (a deliberately unresponsive device) parked its per-connection `serve()`
   goroutine in `select {}`, leaking that goroutine and its TCP connection
   forever, even after `Server.Close()`. Replaced with a `closed chan struct{}`
   closed under a `sync.Once` in `Close()`; the Silent branch now does
   `<-s.closed; return`, so `serve`'s `defer conn.Close()` finally runs. New
   regression test `sim_test.go`'s `TestSilentServer_CloseReleasesConnection`.
2. **`internal/plugins/telemetry_client_test.go`** and
   **`internal/plugins/integration_test.go`** — `t.Cleanup` closes for two
   `*sql.DB` helpers that never closed their pool, each leaking a
   `database/sql.(*DB).connectionOpener` goroutine per test.
3. **`Makefile`** — a new `test-race-plugins` target
   (`go test -race -timeout 45m ./internal/plugins/...`) mirroring the existing
   `test-race-pages`, with a doc comment recording the measurement.

The author's stated evidence: `./plugins/tax-tr/okc/...` under `-race` passes in
~1.7s; the full package killed at a 900s timeout while still *legitimately*
loading a real wazero module (not blocked); and a clean full pass at 1078.827s
(~18min) at `-timeout 2400s`.

## Independent review (Opus subagent, isolated worktree)

Re-derived the root cause from the source rather than from the writeup, and
re-ran the TDD cycle, the probes and the gate for real.

**Verdict: PASS — safe to merge.** No blockers. Two Low findings fixed during
review, two Low findings flagged as out of scope.

### Root cause, re-derived

Confirmed the leak and its scope by reading the code, not by trusting the
diagnosis:

- `serve()` has `defer conn.Close()` (sim.go:132), so `return`ing after
  `<-s.closed` genuinely releases the connection. The fix is structurally real.
- **Why the old leak was permanent**, which the writeup does not spell out: the
  client's own `defer conn.Close()` cannot wake the server, because `serve` is
  parked in `select {}` and *not* in a read — so closing the client end never
  produced an error for `serve` to return on. That is exactly the reported
  symptom.
- **The Silent branch really was the only non-terminating path.**
  `BridgeDriver.roundTrip` (`plugins/tax-tr/okc/bridge.go:177`) does
  `defer conn.Close()` per request, so every non-Silent `serve` goroutine
  already exits on EOF. Correct, minimal scoping — nothing else needed
  releasing.
- **The leak is genuinely tiny**, as claimed: system-wide only two
  `Silent: true` call sites exist (`plugins/tax-tr/okc/bridge_test.go:121`,
  `internal/plugins/okc_plugin_test.go:166`), plus the `scripts/okc-sim` CLI
  flag. So ~2 leaked goroutines per test binary — real, but never plausibly
  the cause of an 18-minute runtime. Agreed: a red herring the dump surfaced.
- `internal/db.Open` starts **no goroutines of its own** beyond `sql.Open`'s
  pool, so `Close()` is the complete fix for (2), not a partial one.

**The diagnosis is right, and I can now put a number on it.** I ran both halves
myself: the package takes **106.506s** plain and **1108.046s** under `-race` — a
**10.4x** instrumentation multiplier. That is the entire story of this card in
one ratio. The plain runtime sits comfortably inside the default 600s
per-package budget (which is why `make test` and CI have never complained), but
10.4x of it lands at ~1108s, nearly double that default — so an unqualified
`go test ./internal/plugins/ -race` gets killed at 600s every single time, with
a goroutine dump that inevitably names whatever happened to be parked at that
instant. "Hangs" was a reasonable reading of that dump and an incorrect one.
There is no deadlock.

### TDD re-verification (exact output)

Reverted **only** the `serve()` Silent branch back to `select {}`, keeping the
new test, inside this isolated worktree:

```
--- FAIL: TestSilentServer_CloseReleasesConnection (2.05s)
    sim_test.go:52: connection still open 2s after Close() — the serve
    goroutine leaked: read tcp 127.0.0.1:55436->127.0.0.1:34951: i/o timeout
```

Restored, re-ran: `--- PASS (0.05s)` — and note it passes in 0.05s, two orders
of magnitude inside its own 2s deadline, which is itself evidence the server
releases immediately rather than squeaking under the wire.

**The test is not a tautology, and the `net.Error.Timeout()` check is
load-bearing, not decorative.** Two things establish this:

- The failing run above returns a **non-nil** error (`i/o timeout`). An
  assertion that only checked `err != nil` — the obvious way to write this
  test, and the way an earlier draft reportedly did — would have **passed with
  the bug present**. The `Timeout()` discrimination is the whole proof.
- Probed the "fix broken some other way" case directly: left `<-s.closed` in
  place but `continue`d the loop instead of returning, so the goroutine wakes
  on `Close()` but never releases the connection. The test **still fails**
  (`2.05s`, same timeout-shaped message). It is asserting the connection was
  released, not merely that the channel was closed.

Re-ran the same revert/restore cycle **after** my own change to the test
(finding 2), to confirm the added `t.Cleanup` doesn't soften it: still fails
red for the same reason (`sim_test.go:65`, 2.05s), still passes green when
restored. `sim.go` was byte-identical to `5d07d115` afterwards.

### Findings

- **(Low, found and fixed during review)** `setupIntegrationTestDB`'s new
  comment asserted a leak that the code **did not have**, on two counts.
  (a) The helper's only caller,
  `TestIntegration_EventDispatchCrashIsolation` (integration_test.go:21),
  already had `defer tmpDB.Close()` on the very handle the helper returns —
  `db.DB` embeds `*sql.DB`, so that is the same pool the cleanup closes. The
  DB was never leaked. (b) The whole file sits behind `//go:build integration`:
  `go list` puts `integration_test.go` in `IgnoredGoFiles` for a default build
  and only in `TestGoFiles` under `-tags=integration`, so it is compiled out of
  every default `go test ./internal/plugins/...` run and **could not have
  contributed a `connectionOpener` goroutine to this card's dump at all**. The
  `t.Cleanup` itself is harmless and mildly useful — the helper now owns its
  own teardown, and `*sql.DB.Close` is idempotent so the caller's `defer`
  getting there first is fine — so I kept it and rewrote the comment to state
  the accurate rationale. Doc-only.
  A side effect worth recording: because that file is tag-gated, **the standard
  gate never compiles the added line**. `go build ./...`, `go vet ./...` and
  `golangci-lint run ./...` all skip it. Verified separately:
  `go vet -tags=integration ./internal/plugins` and
  `golangci-lint run --build-tags=integration ./internal/plugins/...` both
  clean.
  The telemetry half is the opposite and **fully accurate**:
  `telemetry_client_test.go` carries no build tag, all five callers
  (lines 49/67/91/137/155) had no `db.Close()` of any kind, and the file's only
  other teardown is `defer srv.Close()` on httptest servers. That leak was
  real, on the default path, one per test.
- **(Low, found and fixed during review)** The new `sim_test.go` leaked the very
  thing it exists to test, on its own failure paths: between `Start` and the
  explicit `s.Close()` sit two `t.Fatalf` exits (dial, write) that would leave
  the listener and its `accept` goroutine running for the rest of the binary.
  Added `t.Cleanup(func() { _ = s.Close() })` immediately after `Start`,
  matching `startSim`/`startOKCSim`'s convention in the sibling packages. This
  also buys explicit regression cover for the `sync.Once`, which today is
  load-bearing **only** via `internal/plugins/okc_plugin_test.go`'s
  `"unreachable"` subtest (an explicit `s.Close()` at :179 on top of
  `startOKCSim`'s own `t.Cleanup` at :41) — a long way from sim.go, and a slow
  WASM-suite panic rather than a local failure if someone later drops the guard.
- **(Low, flagged — out of scope)** The 50 ms `time.Sleep` is the test's only
  synchronisation. If the sim hasn't reached the Silent branch in time the read
  times out and the test fails, so the race's direction is conservative (a
  false *failure*, never a false pass), and 30 consecutive `-race` runs were
  green here under concurrent load. There is, however, one theoretical
  spurious-*pass* path to be honest about: had `accept` not yet taken the
  connection, `ln.Close()` would RST the queued connection and the client would
  see a non-timeout error without the Silent branch ever running. `go s.accept()`
  starts in `Start` and the dial completes before the write, so 50 ms makes
  this effectively unreachable — but strictly, the test proves "the server let
  go of the connection", not specifically "the serve goroutine woke on
  `closed`". There is no hook on `Server` to make it deterministic; left as is.
- **(Low, flagged — out of scope)** Nothing measures this budget over time. The
  45m is anchored to two manual runs on one machine, and `test-race-pages` shows
  exactly how that erodes: it shipped at 30m (ut-docs#1003) and was later
  measured at 1531s, ~85% of budget (ut-docs#1034/#1119), only after a new
  WASM-plugin test class grew the package. `internal/plugins` is on the same
  trajectory (real wazero modules under `-race`), and because `-race` is
  deliberately never run in CI for this package, the next erosion will again
  surface as a confusing local "hang" rather than a red build. Worth a card to
  record the measured runtime where it can be compared; not a blocker.

### Checked and confirmed sound, no fix needed

- **`sync.Once` + channel is the right fix, and the `Once` is load-bearing —
  proved, not assumed.** Replacing `s.closeOnce.Do(func() { close(s.closed) })`
  with a bare `close(s.closed)` and running a throwaway double-`Close()` test
  (mirroring okc_plugin_test.go:179 + :41) panics:
  `panic: close of closed channel` at `sim.go:101`. Before this change `Close()`
  was naturally idempotent (`ln.Close()` twice merely returns an error), and
  the repo **already depends on that** — so the guard is precisely what keeps a
  pre-existing contract intact, not boilerplate.
- **The alternative (track live conns, close them directly in `Close()`) is
  worse here.** It needs a mutex-guarded conn set plus add/remove bookkeeping
  in `serve` — more shared mutable state in the very thing being made
  race-safe — for no extra coverage, since `roundTrip`'s per-request
  `defer conn.Close()` already retires every non-Silent goroutine. The
  channel broadcast is the simpler and safer of the two.
- **No race introduced by the fix** (which would have been a particularly
  unfortunate own-goal for this card). `closed` is assigned in `Start` *before*
  `go s.accept()`, establishing happens-before for every `serve` child; `close`
  and `<-` are synchronised; `closeOnce` is a `sync.Once`. Empirically:
  `go test -race ./plugins/tax-tr/...` green in **1.694s**, independently
  reproducing the author's ~1.7s, and the new test 30x under `-race` green.
- **No new leak window in `Close()`'s ordering.** It closes the channel *before*
  the listener, so a connection accepted in that window hits an already-closed
  `closed` and returns immediately instead of parking.
- **The behaviour the live test depends on is preserved.**
  `TestBridgeSale_SilentDeviceTimesOut` needs the sim to hang past the
  *client's* read deadline; `Close()` only arrives at cleanup, so it still
  does — and that test is green.
- `Server`'s zero value remains unusable (a nil `closed` would park forever and
  `close(nil)` would panic), but that was already true of its nil `ln` and nil
  `seen` map, and `Start` is the only constructor in-tree. Not a new hazard.
- **`t.Cleanup` ordering is correct.** In `setupIntegrationTestDB`, `os.Remove`
  is registered first and the Close second; cleanups run LIFO, so the pool
  closes before the temp file is removed. No double-close panic anywhere —
  `*sql.DB.Close` is documented-idempotent, which is what makes the overlap
  with the caller's `defer` safe.
- **No test relies on a DB staying open across the new cleanup boundary.** All
  five telemetry callers take the handle as their first statement and their only
  other teardown is `defer srv.Close()`; every `defer` in a test body runs
  before any `t.Cleanup` registered during that test. No background goroutine
  in `telemetry_client_test.go` touches the handle.
- **Makefile.** `.PHONY` updated. Package path `./internal/plugins/...` matches
  `test-race-pages`' `./internal/pages/...` convention, and since `-timeout` is
  per test binary the three light subpackages (`builtinlayouts`, `marketplace`,
  `oauth`) do not erode the heavy package's budget. The margin is consistent
  with the repo's own precedent: 45m / 1078.827s = **2.50x** ("~2.5x") on the
  author's measurement, **2.44x** on my own 1108.046s, against
  `test-race-pages`' 60m / 1531s = **2.35x** ("~2.4x"). The claimed shape holds
  either way. The cross-references
  check out too — ci.yml's internal/plugins step does say "this workflow never
  uses -race" and does run `-timeout 20m` against a runtime it records as
  83–93s.
- **No doc went stale.** Checked whether the new target needs listing anywhere:
  `test-race-pages`, its direct precedent, is referenced in no `README.md`,
  `CLAUDE.md` or workflow file — only in historical `docs/code-reviews/` records
  (9 of them). So there is no target inventory for `test-race-plugins` to be
  missing from, and the README makes no claim this change invalidates.
- **Conventions.** No SQL added anywhere (confirmed by grepping the diff for
  `SELECT/INSERT/UPDATE/DELETE/CREATE TABLE` additions — none — and by
  `guard-data-access.sh` green). No real client or shop name. No secret-shaped
  literal.
- **Explicitly not applicable** (stated rather than silently skipped): no UI
  surface (`web/ui/**` untouched), so no UX, kiosk-nav or RTL concern; no
  user-facing string and no `web/locales/**` change, so no i18n key and no
  `ut-plugin-language-{de,es}` follow-up; no `web/help/**` change and no new
  page route, so no manual topic and no `make docs-shots`; no monetary value
  handled, so no `money.Money` question; no architectural choice, so **no ADR
  needed** — this neither contradicts nor requires one, being test-infra plus a
  timeout budget. `android/**` and `mobile/**` untouched, so `android-ci.yml`
  does not apply; `CanonicalTypes` untouched, so `adr-taxonomy-guard` does not
  apply.
- **Do the two DB fixes mask a different still-there root cause? No.** The
  `connectionOpener` goroutines are idle and O(number of tests) — they inflate a
  goroutine dump but cost effectively no runtime — and `internal/db.Open` adds
  no others. The real cost is ~18 minutes of genuine wazero compile/instantiate
  work under `-race` instrumentation, which the Makefile target addresses
  honestly instead of papering over. The one thing I would temper in the
  narrative is the weight of the corroborating evidence: of the three leaks
  cited, the `integration_test.go` one cannot have been in that dump at all
  (finding 1), so it is really two.

## Verification

Everything below was run in this worktree; output is real, not assumed.

- `gofmt -l .` — **no output** (clean), before and after my two fixes.
- `go build ./...` — **exit 0**, no output.
- `go vet ./...` — **exit 0**, no output.
- `go vet -tags=integration ./internal/plugins` — **exit 0** (the only way the
  `integration_test.go` change is compiled at all).
- `go test -count=1 ./plugins/tax-tr/...`:
  ```
  ok  github.com/universaltill/universal-till/plugins/tax-tr/okc       0.668s
  ok  github.com/universaltill/universal-till/plugins/tax-tr/okc/sim   0.056s
  ```
- `go test -race -count=1 ./plugins/tax-tr/...`:
  ```
  ok  github.com/universaltill/universal-till/plugins/tax-tr/okc       1.694s
  ok  github.com/universaltill/universal-till/plugins/tax-tr/okc/sim   1.068s
  ```
- `go test -race -count=30 -run TestSilentServer_CloseReleasesConnection
  ./plugins/tax-tr/okc/sim/` — **ok, 2.553s** (30/30 green; the 50 ms-sleep
  flake check).
- `go test -count=1 ./internal/plugins/...` (plain, no `-race`):
  ```
  ok  .../internal/plugins                 106.506s
  ok  .../internal/plugins/builtinlayouts    1.563s
  ok  .../internal/plugins/marketplace       0.278s
  ok  .../internal/plugins/oauth             0.121s
  ```
- `go test -race -timeout 40m ./internal/plugins/...` — **run in full, not
  skipped**, and it **completed cleanly**:
  ```
  ok  .../internal/plugins                1108.046s
  ok  .../internal/plugins/builtinlayouts   37.348s
  ok  .../internal/plugins/marketplace       1.360s
  ok  .../internal/plugins/oauth             1.159s
  EXIT=0 WALL_SECONDS=1116
  ```
  This independently reproduces the author's 1078.827s — 1108.046s here, on a
  4-CPU box and with my own `go build`/`go vet`/`golangci-lint` runs competing
  for it, so the small excess is contention, not drift. **No deadlock, no
  failure, no data race reported.**
- `golangci-lint run ./plugins/tax-tr/... ./internal/plugins/...` —
  **0 issues.**
- `golangci-lint run --build-tags=integration ./internal/plugins/...` —
  **0 issues.**
- CI-blocking guards from `.github/workflows/ci.yml`'s `build` job, run
  locally — all green: `check-brand-assets`, `guard-android-external-links`,
  `guard-android-i18n`, `guard-android-manifest-features`,
  `guard-android-status-address`, `guard-autofill-suppression`,
  `guard-compliance-claims`, `guard-data-access`,
  `guard-docs-hub-checkout-repo`, `guard-docs-shots`,
  `guard-e2e-fixtures-import`, `guard-emoji-font`, `guard-help-drift`,
  `guard-help-topics`, `guard-htmx-loaded`, `guard-i18n`, `guard-kiosk-engine`,
  `guard-kiosk-launch-flags`, `guard-makefile-version`,
  `guard-migration-version-collision`, `guard-osk-loaded`,
  `guard-page-http-error`, `guard-plugin-menu-read`,
  `guard-plugin-settings-bump`, `guard-price-history-sync`,
  `guard-webkit-version`.
  Two could not run in this sandbox, both **environment gaps, not code
  issues**: `guard-deadcode-baseline` (no `gtk+-3.0`/`webkit2gtk-4.1` headers,
  so `deadcode` can't build the cgo packages — `pkg-config --exists gtk+-3.0`
  fails here) and `guard-shellcheck-version` (no `shellcheck` on PATH). Neither
  subject is touched by this diff: no shell script and no cgo file changed.
  CI's `ubuntu-latest` carries both.
- TDD claim independently re-verified by revert/restore in this isolated
  worktree, plus a deliberate "broken a different way" probe and a
  `sync.Once`-removal probe — see above. Never on a shared checkout.

**Safe to merge.** No blockers. No UI-visible change, no manual/help-topic
update required, no ADR required.

## Deferred / follow-up

- Record `internal/plugins`' measured `-race` runtime somewhere it can be
  compared release-to-release, so the 45m budget can't silently erode to ~85%
  the way `test-race-pages`' original 30m did (ut-docs#1003 → #1034/#1119).
  Because `-race` is never run in CI for this package, the next erosion
  otherwise surfaces as a local "hang" rather than a red build. (Finding 4.)
- `TestSilentServer_CloseReleasesConnection` still synchronises on a 50 ms
  sleep; a deterministic hook on `sim.Server` (e.g. an "accepted" signal) would
  remove both the conservative flake risk and the narrow spurious-pass path.
  (Finding 3.)

# internal/pages `-race` timeout margin (ut-docs#648)

**Card:** universaltill/ut-docs#648 — complexity:easy
**Branch:** `fix/pages-race-timeout-margin-648`
**Reviewer:** independent fresh-context Sonnet subagent (per easy-complexity routing)

## Problem

An earlier independent review found `go test ./internal/data/... ./internal/pages/... -race`
hitting the default 600s per-package `go test` timeout, with a goroutine
dump showing `TestAskTaxRateBP_OverflowAndConcurrency` mid-DB-query at the
timeout instant — not a real deadlock (re-running `./internal/pages/`
alone with `-timeout 30m` passed cleanly at 599.802s), but genuinely no
margin. The test does `taxAskCacheMax+1` (4097) sequential event-bus + DB
round-trips to exercise cache-overflow eviction, and `-race`
instrumentation multiplies that cost heavily.

## Fix

1. **`internal/pages/tax_hook.go`** — added a `cacheMax int` field to
   `pluginTaxRateAsker` (zero value = production behaviour, unchanged) and
   a `maxCache()` helper that falls back to the real `taxAskCacheMax`
   constant when `cacheMax` is unset. `AskTaxRateBP`'s overflow check now
   reads `a.maxCache()` instead of the bare constant.
2. **`internal/pages/tax_hook_cache_test.go`** — `TestAskTaxRateBP_
   OverflowAndConcurrency` now constructs its asker with `cacheMax: 8`
   instead of relying on the real 4096 bound, so it still crosses the
   eviction boundary by exactly one payload (same code path, same
   assertions, same 8×50 concurrency hammer afterward) but with ~512x
   fewer round-trips. Isolated runtime dropped from 9.49s to ~0.13-0.51s
   under `-race`.
3. **`Makefile`** — added a `test-race-pages` target
   (`go test -race -timeout 30m ./internal/pages/...`), mirroring the
   `internal/plugins`/ut-docs#643 precedent (an explicit wider timeout for
   one DB-heavy package, rather than raising the global default). CI
   (`ci.yml`) never runs `-race` at all today (confirmed — it's mentioned
   only in a comment), so there was no existing CI job to bump; this gives
   whoever runs `-race` on this package by hand (e.g. a Reviewer/Tester
   gate pass) a documented, safe invocation instead of the bare 600s
   default.

## Why (1)+(2) alone weren't enough

Measured on this session's own sandbox (4 cores): even after the test
fix, the *whole package's* `-race` runtime is 861-878s — well past the
600s default, because the package is broadly DB-heavy and `-race`
instrumentation taxes every test, not just the one named in the issue.
The test-side fix cuts the worst single contributor dramatically but
doesn't bring the aggregate under the bare default, so option (a) (an
explicit longer timeout wherever `-race` actually gets invoked) was
required too, per the issue's own "(c) both" option.

## Verification

- `go build ./...`, `go vet ./internal/pages/...`, `gofmt -l` — clean.
- `go test ./internal/pages/... -v -run TestAskTaxRateBP` — all 13
  subtests pass, `OverflowAndConcurrency` in ~0.13-0.51s (was 9.49s
  isolated / part of a whole-package run right at the 600s wall).
- `go test ./...` (full suite, no `-race`) — green, no regressions.
- `make test-race-pages` (`go test -race -timeout 30m ./internal/pages/...`)
  — run to completion **twice** independently (once by Dev, once by the
  reviewer subagent): 861.879s / 878.363s, no failures, no data races
  reported, comfortably inside the 1800s budget (~48-51% used).
- `scripts/ci/guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh` — all pass (no SQL/kiosk/plugin-menu
  surface touched).

## Independent review

Fresh-context Sonnet subagent, full report:

- Confirmed every `pluginTaxRateAsker{...}` construction site in the
  package (17 total) — only the rewritten test sets `cacheMax`; every
  other site, including production `init.go:133`, keeps the zero value
  and therefore the original 4096 bound. Production behaviour unchanged.
- Confirmed the rewritten test still exercises the identical overflow
  path (`len(a.cache) >= a.maxCache()`) and the same concurrency hammer —
  not a coverage-weakening false-pass, just a smaller bound.
- Confirmed `cacheMax` is set once at construction (before any goroutine
  touches the asker) and never mutated — race-free by construction.
- Verified the Makefile target's claims against `ci.yml` directly (no
  `-race` anywhere in that file) and the cited `internal/plugins`/#643
  precedent (real, at `ci.yml:96-126`).
- Re-ran the full gate independently, including a second full
  `make test-race-pages` run to completion (861.879s, 48% of budget).
- **Verdict: PASS.** One cosmetic-only nit (an unrelated stray blank-line
  whitespace diff in the Makefile context) — not worth a fix cycle, not
  blocking.

## Non-goals (per the card)

Not a rewrite of the tax-ask cache itself; `-race` was not newly added to
CI (`ci.yml` still doesn't run it — this fix targets the manual/gate
invocation path documented by the new Makefile target).

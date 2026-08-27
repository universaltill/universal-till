# internal/pages `-race` timeout margin, round 2 (ut-docs#1034, ut-docs#1119)

**Cards:** universaltill/ut-docs#1034, universaltill/ut-docs#1119 — complexity:easy
**Branch:** `fix/1034-pages-race-timeout-margin`
**Reviewer:** independent fresh-context Sonnet subagent (per easy-complexity routing)

## Problem

ut-docs#648 gave `internal/pages`'s `-race` run its own `make test-race-pages`
target (`go test -race -timeout 30m ./internal/pages/...`) after the bare
600s default proved to have no margin. Two more cards found that 30m budget
itself had eroded:

- ut-docs#1034 — reproduced the *bare* (no explicit timeout) 600s default
  timing out on clean `main`, and flagged that nobody currently has a
  documented safe way to get a clean `-race` signal on this package without
  either a longer timeout or a split run. (`make test-race-pages` already
  existed by the time this was filed — from the same PR that introduced
  ut-docs#1003 — but nothing pointed at it as the fix.)
- ut-docs#1119 — found the package's `-race` runtime creeping back up
  against the *existing* `make test-race-pages` 30m budget, this time
  driven by the `TestFiscalSignAsk_*` family (each subtest loads a real
  wazero WASM module).

Both cards propose the same fix shape: a longer explicit timeout for this
target (same pattern as `internal/plugins`/#643), and/or splitting the
slow WASM tests into their own run. This PR does the former — the cheaper,
mechanical fix — since it fully satisfies both cards' acceptance criteria
on its own.

## Measurement

`make test-race-pages` (old 30m budget), run to completion on this
session's sandbox (4 cores), clean `main`:

- **1531.189s (~25.5min) real**, i.e. ~85% of the 1800s/30m budget —
  confirms #1119's "creeping back up" finding; not comfortable margin.
- No `DATA RACE` reported.
- One failure: `TestAsyncPrintFailureIsRecordedWhenPrintCtxExpired` — this
  is the pre-existing, already-tracked flake (ut-docs#1018, open,
  unrelated to this timeout/margin question) — not a new regression from
  either #1034 or #1119's own changes (there are none; both cards are
  investigation-only).
- `internal/pages/catalog` (5.9s) and `internal/pages/common` (77.9s) —
  both green, well inside budget (they're smaller sub-packages, not part
  of the margin problem).

## Fix

**`Makefile`** — `test-race-pages` target's `-timeout` raised from `30m`
to `60m` (~2.4x the measured 1531s runtime), and its comment updated to
record this measurement and the reasoning, mirroring the generous-margin
shape of the `internal/plugins`/#643 precedent (that package's own
~85-90s measured runtime against a 20m/1200s budget — ~13-14x margin;
60m against ~25.5min gives less headroom than that but is proportionate
to this package's much larger, still-growing test surface without
ballooning the manual-run wall-clock unreasonably).

No production code or test code changed — both cards are explicit
non-goals on that (confirm-and-fix / test-runtime-only scope). CI
(`ci.yml`) still never runs `-race` at all (confirmed via `grep -n
'\-race' .github/workflows/ci.yml` — the only hit is the existing comment
noting this), so this change has zero CI behavioural effect; it only
affects the manual/Reviewer-Tester-gate invocation path this target
documents.

## Why not split the WASM tests instead (option (c))

Both cards offer splitting `TestFiscalSignAsk_*` into its own test
binary/package as an alternative. That's a more durable fix long-term
(keeps this package's own `-race` run from re-accumulating margin loss
every time a new WASM-plugin test is added) but is a real structural
change — a new (sub)package or build-tag split, updated imports, its own
Makefile target — out of proportion to two `complexity:easy` cards whose
stated non-goal is "not a rewrite," and it doesn't change today's
measured numbers enough to skip needing *some* timeout headroom anyway.
Logged as a follow-up worth a future card if the margin erodes again
before then (see Follow-up below), rather than done speculatively now.

## Verification

- `gofmt -l .` — clean.
- `go build ./...` — clean.
- `go test ./...` (full suite, no `-race`) — green, 55 packages, no
  regressions (`internal/pages` plain run: 105.251s).
- `make -n test-race-pages` — prints exactly
  `go test -race -timeout 60m ./internal/pages/...`.
- `make test-race-pages` (old 30m target) — run to completion as the
  measurement above: 1531.189s, no data races, only the pre-existing
  #1018 flake. (Re-running the new 60m target to completion would just
  re-measure the same ~1531s number a second time at ~2x the wait for no
  new information, so this PR relies on that one measurement plus the
  arithmetic — 1531/3600 ≈ 42.5% used, i.e. comfortably outside the "not
  within ~2% of the deadline" bar #1119's acceptance criteria set.)
- All 15 CI-blocking guards in `.github/workflows/ci.yml`'s `build` job
  (`guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-i18n.sh`,
  `guard-compliance-claims.sh`, `guard-help-topics.sh`,
  `guard-webkit-version.sh`, `guard-kiosk-launch-flags.sh`,
  `guard-android-status-address.sh`, `guard-android-i18n.sh`,
  `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `check-brand-assets.sh`,
  `guard-makefile-version.sh`) — all pass (no surface any of them govern
  was touched).

## Independent review

Fresh-context Sonnet subagent, full report:

- Confirmed `git diff main` touches only `Makefile` — the `-timeout`
  value and the comment, nothing else.
- Confirmed `make -n test-race-pages` prints exactly the expected new
  command; recipe line still uses a tab, not spaces.
- Re-derived the comment's arithmetic independently (1531/1800 ≈ 85.06%,
  3600/1531 ≈ 2.35x) and cross-checked the `internal/plugins` "83-93s"
  figure against `ci.yml`'s own comment — both correct, not fabricated.
- Searched the repo for other `test-race-pages`/`-timeout 30m` references
  outside the Makefile: only historical `docs/code-reviews/` entries
  logging a past run's own timing at the time — expected, not a stale
  reference to this target's current value.
- Confirmed via `ci.yml` grep that `-race` still isn't run in CI, so this
  is genuinely zero-CI-impact.
- **Verdict: PASS.** No blocking findings. Noted (non-blocking) that the
  WASM-test-split alternative is a legitimate design tradeoff, not a
  defect — the stated goal (restore comfortable margin, no CI impact) is
  fully met by the simpler timeout bump.

## Follow-up (not filed as a new card yet)

If a future cycle finds this same package's `-race` margin eroding again
before the WASM test family stabilises, the split-into-its-own-run option
(c) from ut-docs#1119 is the next lever, not another timeout bump —
raising the ceiling repeatedly just delays the same conversation rather
than fixing the accumulation.

## Non-goals (per both cards)

Not a rewrite of the fiscal-sign-ask flow or the tax-ask cache; `-race`
was not newly added to CI; the pre-existing #1018 flake is out of scope
here (tracked separately).

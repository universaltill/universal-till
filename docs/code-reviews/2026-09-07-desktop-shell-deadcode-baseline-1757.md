# Code review — baseline `blockingPaymentEvent` as test-only-reachable deadcode (ut-docs#1757)

**Date:** 2026-09-07
**Branch:** `fix/1757-desktop-shell-deadcode-baseline`
**Card:** ut-docs#1757 — `desktop-shell` CI (`guard-deadcode-baseline.sh`) red on `main`
**Reviewer:** independent fresh-context Sonnet subagent (no sight of the implementer's reasoning; `complexity:easy`, per the model-routing table)
**Verdict:** SAFE TO MERGE. No blocking or non-blocking findings.

## What shipped

`main`'s `desktop-shell` CI job was red: the whole-program `deadcode` scan
(roots `.`, `./cmd/unitill-desktop`, `./cmd/unitill-uninstall`, `-tags
desktop -test=false`) flagged `internal/pages/refund_page.go`'s
`blockingPaymentEvent` as newly unreachable, and it wasn't in
`scripts/ci/deadcode-baseline.txt`.

One line added to the baseline file, in its correct alphabetically-sorted
position:

```
internal/pages/refund_page.go: unreachable func: blockingPaymentEvent
```

## Investigation (not assumed — the issue's own stated hypothesis was checked and found imprecise)

The original card guessed the false positive was "reachable from the main
server binary's route registration, not the desktop shell." That doesn't
hold: this repo has no separate server binary distinct from
`cmd/unitill-desktop` (root `main.go` is itself one of the guard's three
scan roots), and a repo-wide grep for `blockingPaymentEvent(` found **zero
production callers in any of the three roots** — only
`internal/pages/payment_event_test.go`, which calls it directly across 4
cases (lines 48, 66, 70, 74).

The real mechanism is different, and it's one the guard script's own
header comment already names explicitly: `deadcode` runs with
`-test=false`, so a function reachable only from a `_test.go` file reads
as unreachable even though it's genuinely in use — the same shape as the
pre-existing baseline entry `ResetCacheForTests`. Both of `refund_page.go`'s
real production call sites (its own refund handler at line 797, and
`pos_api.go`'s tender-authorize gate at line 197) call the *different*
function `blockingPaymentEventWithResponse` directly — `blockingPaymentEvent`
is a thin `(err error)`-only wrapper around it, kept for symmetry and its
own direct unit coverage, not currently called by any production code path.

Given that, baselining (not deleting) is the correct call, per the guard's
own documented policy for this exact shape.

## Independent re-verification (fresh subagent, re-derived from scratch, not trusting the commit message)

1. **Unreachability** — re-grepped the whole repo; confirmed one
   definition, zero non-test callers, `blockingPaymentEventWithResponse`
   confirmed as the distinct function actually used in production.
2. **Baseline vs. delete** — confirmed this matches the guard's own
   documented test-only-reachable shape.
3. **Guard red→green** — reproduced red on `main` (exactly this one
   line flagged); confirmed green on this branch, with one unrelated
   informational note (`SigningDeviceCredentialStore.Exists` burned down
   elsewhere — pre-existing, not touched by this diff, not a blocker).
4. **Exact string match** — implied by #3 (the guard does its own string
   comparison against the baseline file).
5. **Sort order** — `sort scripts/ci/deadcode-baseline.txt | diff -
   scripts/ci/deadcode-baseline.txt` — no diff.
6. **Build/vet** — `go build -tags desktop ./cmd/unitill-desktop/...` and
   `go vet -tags desktop ./cmd/unitill-desktop/...` both clean.
7. **Scope** — `git diff main...fix/1757-desktop-shell-deadcode-baseline
   --stat` — exactly one file, one line.
8. **Commit message accuracy** — every factual claim re-checked against
   independent findings; no inaccuracies.

## Deferred (out of scope, deliberately not fixed here)

- `SigningDeviceCredentialStore.Exists` no longer needs its own baseline
  entry (burned down elsewhere) — informational only, not required by the
  guard, and unrelated to this card; left for a future baseline refresh
  rather than widening this diff.
- `lang-pack-drift` red on `main` (Turkey PR #750's new locale keys) is a
  separate, already-tracked failure (ut-docs#1758) — unrelated to this
  guard and this diff.

## Gate

| command | result |
| --- | --- |
| `bash scripts/ci/guard-deadcode-baseline.sh` (branch) | green |
| `bash scripts/ci/guard-deadcode-baseline.sh` (main, pre-fix) | red — exactly the one flagged line |
| `go build -tags desktop ./cmd/unitill-desktop/...` | clean |
| `go vet -tags desktop ./cmd/unitill-desktop/...` | clean |

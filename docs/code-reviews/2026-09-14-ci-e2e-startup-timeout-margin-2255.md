# 2026-09-14 — CI: e2e job's health-check budget had no margin against actual compile time (ut-docs#2255)

## What shipped

Found while driving universaltill/universal-till#1164 through CI, immediately
after universaltill/ut-docs#2252 (the IPv6 listen-address fix) landed —
`ci.yml`'s `e2e` job's "Start app" step failed a second, different way,
twice in one day on two unrelated PRs (universal-till#1165 and #1164):
the app bound cleanly (confirming #2252's fix works), but didn't finish
starting until ~31s into the step, one second past the health-check
loop's 30×1s budget. The day before, the same step passed in ~13s on two
separate `main` runs — today's compile time had more than doubled, with
zero margin left in the retry budget.

- **`.github/workflows/ci.yml`**: the `e2e` job's "Start app" step now
  runs `go build -o /tmp/ut-e2e-app .` as its own foreground command
  *before* backgrounding anything, then backgrounds the already-compiled
  binary and polls it. Previously it backgrounded `go run .` directly,
  which ties the health-check clock to compile time as well as actual
  process startup — a slow compile (cold cache, contended runner) could
  eat the whole 30s budget before the binary ever started listening.
  Splitting build from run means a slow compile only makes this CI step
  take longer overall; the retry loop now only ever waits on real
  process startup. Same pattern `e2e/run-till.sh` already uses in this
  repo (build once, run the binary, not `go run .`) — that script does
  it for a different original reason (CWD-tying to a fresh temp dir, see
  its own comment), but the same shape happens to fix this timing issue
  too.

No application code touched; no change to the 30×1s retry loop itself
(now genuinely sufficient, since it only measures startup, not startup +
compile).

## Independent review

Sonnet, fresh context.

**What it did:** confirmed the diff is exactly this one step; validated
`ci.yml`'s YAML; reproduced with a **genuinely cold** compile
(`go clean -cache` first) — `go build` took 63.4s in that worst case, yet
the health-check loop (timed separately, using the already-built binary)
detected the app up on its second attempt (~1.1s), directly confirming
the fix decouples the two clocks regardless of how slow the compile gets.
Checked the fixed `/tmp/ut-e2e-app` path for collision risk (none — each
GitHub Actions job gets a fresh VM, and this workflow already writes a
similar fixed `/tmp/...` path elsewhere, e.g. `/tmp/libpact_ffi.so.gz`).
Investigated the specific CWD-sensitivity concern `e2e/run-till.sh`'s own
comment raises about `go run` vs. a built binary
(`internal/paths.go`'s `migrateLegacyDB`, which only acts on a
CWD-relative `data/unitill-pos.db` *if it exists on disk*) and confirmed
it's structurally inapplicable here: that path is gitignored, can never
exist on a fresh CI checkout, and neither the old nor new step sets a
different working directory — so no CWD change was introduced by this
fix.

**Verdict: PASS, safe to merge. No blocking findings.**

**Nit, not fixed:** a `${{ runner.temp }}`-based path would be marginally
more idiomatic/consistent with the `build` job's own `GOCACHE`/
`GOMODCACHE` convention than a bare `/tmp/...` literal — not a defect,
not changed, since the fixed path is already proven safe on this
single-job-per-VM runner and matches existing precedent in the same
file.

## Verified beyond automated tests

- Reproduced the original failure symptom (not just assumed it): the
  `e2e` job failed identically on two separate PRs today, both times at
  the same ~30-31s boundary, both times with a clean bind (ruling out a
  repeat of #2252's issue) — established this as a real, recurring
  margin problem before writing a fix.
- Reproduced the fix under a worst-case cold-cache compile (63s+), not
  just the ~31s case actually observed — the health-check still
  succeeded in ~1s, proving genuine decoupling rather than a
  coincidentally-sufficient number bump.
- YAML re-validated after the edit.

## Safe to merge

Yes.

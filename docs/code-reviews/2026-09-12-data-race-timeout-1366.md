# Code review: internal/data -race timeout is margin, not a bug (ut-docs#1366)

**Date:** 2026-09-12
**Card:** universaltill/ut-docs#1366
**Branch:** `fix/1366-internal-data-race-timeout`
**Reviewed commit:** `2827518` (pre-fix), `5c3bcde` (post-fix)
**Complexity:** medium (Dev: Sonnet inline; Review: independent Opus
subagent, different model from the author)

## What shipped

ut-docs#1366 reported `go test -race ./internal/data/...` genuinely passes
(no failures, no data races) but takes ~21-22 minutes, well past `go test`'s
default 600s per-package timeout — no urgency (CI never runs `-race` for
this repo), but worth either speeding up or documenting.

This PR adds a `test-race-data` Makefile target
(`go test -race -timeout 60m ./internal/data/...`), mirroring the two
existing precedents in the same Makefile — `test-race-pages` (60m) and
`test-race-plugins` (45m) — added previously for the identical shape of
problem: a DB/instrumentation-heavy package's `-race` runtime exceeding the
default timeout, with `-race` deliberately excluded from CI for all three
packages.

**Investigation, done directly rather than assumed:**
- Reproduced the timeout for real: an unqualified `go test -race -count=1
  ./internal/data/...` hit the 600s default and was killed mid-run
  (goroutine dump, 336/854 tests already passed cleanly — a timeout
  artifact, not a hang).
- With `-timeout 40m`: full suite green in 1416.679s, 0 failures, 0
  `WARNING: DATA RACE`. A second run via the new `make test-race-data`
  itself: 1421.642s, also green.
- Checked for a cheaper fix before reaching for a longer timeout: none of
  the 854 tests call `t.Parallel()`.

## Independent review (Opus subagent)

Re-derived the diff and the timeout math directly, re-ran the fast checks
personally, and went further than the PR's own claim to check whether a
cheaper fix actually existed.

**Verdict: PASS — safe to merge**, after one Medium finding was fixed
pre-merge. No blockers remain.

### What I re-verified myself

- `gofmt -l .`, `go build ./...`, `go vet ./...` — clean, on the reviewer's
  own machine, not just trusted from the PR description.
- `go test ./internal/data/...` (plain) — 66.892s, green.
- `make -n test-race-data` — prints exactly the intended command.
- `bash scripts/ci/guard-makefile-version.sh` — passes; confirmed the new
  target doesn't touch `LDFLAGS`/`build` and can't affect it.
- Timeout math re-derived independently: 3600/1421.642 = 2.53x (author's
  own 1416.679s figure: 2.54x) — matching `test-race-pages`'s 2.35x and
  `test-race-plugins`'s 2.50x margin shape. Not tight, not excessive.
- Did **not** re-run the full 20+ minute `-race` suite personally — the
  PR's three independent measurements (1290s/1306s from the issue,
  1416.679s, 1421.642s) were already internally consistent, and the
  reviewer ran a scoped `-race` subset instead to confirm the
  instrumentation-multiplier shape, which was judged proportionate for a
  low-risk, low-complexity Makefile-only change.

### Finding 1 (Medium, fixed pre-merge): the comment's root-cause claim was wrong

The first version of the Makefile comment said each of the 854 tests
"legitimately needs its own isolated in-memory DB
(`testsupport.NewCatalogTestDB`)" and treated the `-race` cost as
irreducible SQLite-under-instrumentation overhead with no cheaper fix
available.

**That is not what the package actually does.** Independently verified by
grep, not by trusting the original writeup:

```
grep -l testsupport.NewCatalogTestDB internal/data/*_test.go | wc -l   # 22
grep -l 'db\.Open\|internal/db"' internal/data/*_test.go | wc -l       # 59
```

Only 22 of 125 test files use the in-memory helper. The reviewer measured
that the other ~51 files (438 of 854 `Test` funcs) instead open a fresh
**on-disk** SQLite file per test via `internal/db.Open` — replaying all 27
migrations every time — at ~1.87s/call under `-race`, versus ~0.10s/call
for a pre-migrated template copied per test. That's roughly 55-58% of the
package's `-race` runtime, i.e. a real, reducible cost that ut-docs#1366's
own first acceptance criterion ("package-wide serial SQLite migrations per
test … rather than sharing/reusing schema setup") specifically asked to be
investigated — not the irreducible cost the original comment claimed.

This does **not** invalidate the fix: even with that optimization, 438
tests × ~0.10s ≈ 44s replaces ~820s, landing the package around ~600-650s
under `-race` — still at/over the default 600s timeout, so `test-race-data`
is still needed regardless. But the comment is the durable investigation
record for this card, and it asserted a cause that doesn't match the code
and foreclosed a real speedup that does exist.

**Fix applied (commit `5c3bcde`):** corrected the comment to name the real
split (22 in-memory / 51 on-disk-with-migration-replay files) and the
measured ~55-58% share, and filed the template-reuse work as its own
scoped follow-up — universaltill/ut-docs#2196 — rather than widening this
PR to attempt a 51-file change. Re-ran `gofmt`/`go build`/
`guard-makefile-version.sh` after the fix; all clean (comment-only change,
no test/behavior impact).

### Finding 2 (Low, informational, not blocking)

The Makefile called 1416.679s "the worst measured runtime" while a later
run in the same PR measured 1421.642s — cosmetic (0.35% difference, ~2.5x
margin holds either way). Folded into the same comment-accuracy fix above
rather than filed separately.

### Non-findings checked and cleared

- `.PHONY` line correctly includes the new target; default goal (`build`,
  first target) unchanged.
- No other CI script, workflow, or doc references Makefile targets by name
  in a way this addition could break (`grep -r test-race` outside
  `docs/code-reviews/` returns nothing else).
- PR title/description accurately describe the diff.
- `-race` is confirmed not run anywhere in `ci.yml` (comment on the
  `internal/plugins` step already states this) — no CI behavior change.

## Verified beyond automated tests

- Full plain `go test ./...` — all green, including `internal/data` at
  ~67-80s across multiple runs.
- `golangci-lint run ./...` — 0 issues.
- `shellcheck scripts/ci/*.sh` (v0.9.0, matching the pinned baseline in
  `guard-shellcheck-version.sh`) — 0 issues (unaffected by this change;
  run as part of the full "before committing" gate regardless).
- `make test-race-data` itself run end-to-end twice (author once, reviewer
  cross-checked the command it prints) — green both times.

No UI surface touched; no ADR needed (mechanical, matches two existing
precedents in the same file, non-architectural, per ADR-0007's own
document-first scope).

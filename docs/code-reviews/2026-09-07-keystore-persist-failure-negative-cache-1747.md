# Code review: arm KeyStore negative-cache on a post-fetch persist failure (ut-docs#1747)

## What shipped

Closes another deferred should-fix from the ADR-0082/ut-docs#1739
implementation review: `internal/secrets.KeyStore.Load`'s ~30s
negative-cache/retry floor (`lastFail` + `fetchRetryFloor`) armed only when
the fetch from the primary itself failed — not when the fetch succeeded but
the subsequent persist-to-disk step failed (e.g. a wedged disk). That left
a replica till hammering its primary with a fresh HTTP fetch on every
`settings_get` WASM host call instead of being rate-limited exactly like a
fetch failure already is.

- `Load`'s post-fetch persist-error branch now sets `ks.lastFail =
  ks.now()` before returning, in the same place the wrong-length-key case
  a few lines above already does.
- One new regression test, `TestKeyStoreFetchSucceedsButPersistFailureArmsNegativeCache`,
  forces a deterministic, root-proof persist failure (a non-empty directory
  occupying the write-tmp-then-rename target, so `os.WriteFile` fails with
  EISDIR and persist's own `os.Remove` cleanup can't silently clear the
  obstruction on a non-empty directory) and checks: first `Load` fails and
  fetches once; a second `Load` inside the floor fails again WITHOUT a
  second fetch; once the floor elapses and the obstruction is removed, the
  next `Load` succeeds and fetches exactly once more.

## Independent review (fresh-context Sonnet subagent, per this card's `complexity:easy` routing)

Full review ran in an isolated worktree, with real command execution
(`go build`, `go vet`, `gofmt -l`, `go test ./internal/secrets/...` and the
full `go test ./...`, `golangci-lint run ./...`,
`scripts/ci/guard-data-access.sh`) and its own independent TDD
revert/restore, plus a standalone Go probe confirming the test's own
obstruction technique actually behaves as claimed (root-proof EISDIR on
write, `os.Remove` genuinely can't clear a non-empty directory, `ks.path`
itself never exists so the initial `readFile` check is unaffected).

**Verdict: SAFE TO MERGE. No blockers, no should-fix findings.**

- **Explicitly checked and correctly out of scope:** `generateLocked`
  (used by a primary/standalone till, no `ks.fetch`) has the same shape of
  gap — it also calls `ks.persist(key)` with no `lastFail` handling on
  failure. The reviewer traced `Load`'s branch structure and confirmed the
  `ks.fetch == nil` early return happens *before* the retry-floor check
  even exists in the code path, so a standalone till's `Load` never
  consults `lastFail` at all — adding it to `generateLocked` would be
  inert without a materially bigger change, and the impact profile is
  different besides (a standalone till has no primary to protect from
  network load; a persist failure there just means retrying its own local
  disk). Correct scope discipline, not a missed spot.
- **Recurring bug classes checked, neither applies:** no new file-write
  handler (missing `os.MkdirAll` — `persist()` itself is unmodified except
  for the one added line at its call site, and already has it); no path
  construction in this diff.
- **Scope check:** exactly what the ticket asked — one line in production
  code, one new regression test, nothing else.
- No real client/shop name or literal credential anywhere in the diff.

## What was verified beyond automated tests

- TDD claim personally re-verified before requesting review: reverted the
  `ks.lastFail = ks.now()` line, ran the new test, confirmed it fails with
  the exact expected symptom (`calls=2`, i.e. a second fetch happened
  inside the floor), restored the fix, confirmed green again.
- Independently re-verified a second time by the review subagent in its own
  isolated worktree, same method, same result, plus the extra standalone
  probe validating the test's obstruction technique itself (not just
  trusting its PASS/FAIL).
- Full gate run clean: `gofmt -l .` empty, `go build ./...` clean,
  `go vet ./...` clean, `go test ./...` fully green across the whole repo,
  `golangci-lint run ./...` 0 issues, `guard-data-access.sh` passes (no
  inline SQL — this diff doesn't touch the data layer at all).

## Safe-to-merge verdict

**Safe to merge.** Minimal, precisely-scoped fix; the one adjacent
question (whether `generateLocked` needs an equivalent guard) was
investigated and correctly found not to apply as-is.

## Explicitly deferred

None requiring a new card — the `generateLocked` question was answered as
"not applicable without a bigger, differently-shaped change," not left as
an open gap.

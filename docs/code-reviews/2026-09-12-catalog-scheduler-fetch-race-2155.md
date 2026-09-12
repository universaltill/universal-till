# Code review: `CatalogRepository.Fetch` scheduler/page-handler write race (ut-docs#2155)

**Date:** 2026-09-12
**Card:** universaltill/ut-docs#2155
**Branch:** `fix/2155-catalog-scheduler-fetch-race`
**Complexity:** hard (Dev: Fable subagent in an isolated worktree, Review: Opus subagent in a separate isolated worktree)

## What shipped

Filed as a deferred follow-up from ut-docs#2143's own review (Finding 9,
`docs/code-reviews/2026-09-12-plugin-catalog-background-refresh-2143.md`):
`internal/server/server.go`'s scheduler (`syncCatalog`) calls
`CatalogRepository.Fetch` directly, bypassing both `GetOrFetch`'s
`refreshing` flag and the `coldFetch` `singleflight.Group` — so a scheduler
tick can genuinely run concurrently with a page-handler-triggered
`refreshInBackground`/`coldFetch` fetch. Each call stamps its own
`FetchedAt: time.Now()` *before* acquiring `cr.mu` to commit the result, so
goroutine scheduling could let the fetch with the numerically **older**
`FetchedAt` win the lock race and land second — unconditionally overwriting
a chronologically newer, already-cached snapshot (both in memory and on
disk) with staler data. Rare, but real, and self-correcting only in the
sense that the cache goes stale again sooner.

`internal/plugins/marketplace/catalog_repository.go`, `Fetch`:

- Added a monotonic-write guard immediately after `cr.mu.Lock()`: if
  `cr.cached != nil && !snapshot.FetchedAt.After(cr.cached.FetchedAt)`, skip
  both the disk save and the `cr.cached = snapshot` assignment — the call
  still returns its own freshly-fetched `snapshot` either way, never a
  substituted value from `cr.cached`.
- Both writes (disk and memory) are guarded together deliberately: guarding
  only one would leave them disagreeing, and a restart would reload the
  older on-disk copy over the newer in-memory one.
- Deliberately out of scope: the single shared `cr.cached` slot regardless
  of `(locale, deviceArch)` — the card's own "suggested direction" chose
  this smaller, safer guard over redesigning per-key caching, and this
  review confirms the guard doesn't make that pre-existing design worse
  (see Finding 5 below).

Two new tests in `catalog_repository_test.go`:

1. `TestCatalogRepository_Fetch_NeverOverwritesCacheWithOlderResult` — the
   actual regression test. White-box seeds `cr.cached` with a `FetchedAt`
   one hour in the future (a different, distinguishable snapshot), then
   calls `Fetch` with different params; asserts the call's own return value
   is honest (its own request's data) while `cr.cached` and the on-disk
   snapshot remain completely untouched.
2. `TestCatalogRepository_Fetch_ConcurrentDifferentParamsEachCallerGetsOwnResult`
   — two concurrent `Fetch` calls with different `(locale, deviceArch)`
   pairs and a staggered server response (50ms), asserting each goroutine's
   own return value matches its own request. This guards AC2 (no
   cross-caller param mixing) but — per the independent review below —
   passes identically on the pre-fix code, since `Fetch`'s return value was
   never the thing that raced; only test 1 is a true TDD regression test.

No i18n/help/UI changes — backend-only cache-consistency fix, no new
user-facing string or screen.

## Independent review (Opus subagent, isolated worktree, detached from the WIP commit)

Read the full diff plus the surrounding file (`Get`, `GetOrFetch`,
`refreshInBackground`, `Filter`) and the scheduler in `server.go`; built,
vetted, ran `golangci-lint`; ran the marketplace suite under `-race
-count=8` (no flakes) and the whole repo's non-race suite; independently
reverted the guard and re-ran the primary regression test.

**Verdict: safe to merge** — no blocker-class (money/tax, data-loss,
security) issue found.

### Independent revert → red → restore → green (re-verified personally by this orchestrator too, separately, before handing to the reviewer)

Both the orchestrator (in the Dev worktree, before sending for review) and
the reviewer (independently, in its own isolated worktree) reverted just
the guard hunk and re-ran
`TestCatalogRepository_Fetch_NeverOverwritesCacheWithOlderResult`:

```
--- FAIL: TestCatalogRepository_Fetch_NeverOverwritesCacheWithOlderResult
    catalog_repository_test.go:766: Fetch overwrote a chronologically newer
        cached snapshot with an older result: cached=&{... en-US ...}
    catalog_repository_test.go:772: cached snapshot fields changed:
        locale="en-US" arch="linux/amd64" version=0
    catalog_repository_test.go:775: cached FetchedAt changed: ... != ...+1h
    catalog_repository_test.go:785: Fetch persisted an older result to disk
        despite a newer cached snapshot: &{...}
```

Both confirmed it fails for exactly the claimed reason (the seeded newer
entry — memory and disk — gets clobbered), then restored the guard and
confirmed both new tests pass again, `-race`, repeated runs, no flakes.

### Findings

1. **Guard condition correct, but relies on a real invariant worth
   documenting (accepted, comment added).** The comparison is a
   **monotonic**, not wall-clock, comparison, because both `time.Now()`
   values originate in-process. This holds today because `cr.cached` has
   exactly one production write site (this same function). If a future
   change ever warms `cr.cached` from `loadSnapshot()` (a disk round-trip),
   the monotonic reading is lost and a bad-RTC-dated snapshot could freeze
   the cache permanently instead of self-correcting. Documented inline as
   an `INVARIANT` comment above the guard so a future change to `Get()`'s
   cold-cache path doesn't reintroduce this silently.
2. **No deadlock from the early return under a deferred unlock** — verified
   empirically (test 1 calls `repo.Get()`, which takes `RLock`, immediately
   after the guarded `Fetch` returns; no hang, clean under `-race -count=8`).
3. **Return value never leaks another caller's data** — confirmed:
   `Fetch` always returns its own locally-built `snapshot`, on every path,
   never reading from `cr.cached`.
4. **Disk/memory consistency preserved** — `cr.cached` is only ever
   assigned immediately after a successful `saveSnapshot` of the same
   pointer, so skipping the guarded write never leaves disk and memory
   disagreeing.
5. **Does not worsen the pre-existing single-cache-slot design** — the
   guard only changes outcomes inside the sub-millisecond scheduling window
   already described; it introduces no systematic bias toward either
   caller's params winning the shared slot.
6. **Both recurring bug classes checked, N/A** — no new file-write path
   (`saveSnapshot`'s directory is already `os.MkdirAll`'d in
   `NewCatalogRepository`), no cwd-relative path (`cacheDir` is
   `paths.Plugins("cache")` in production, injected, not hardcoded).
7. **Test 2's doc comment overstated what it proves** — fixed: the comment
   now says explicitly that it doesn't reproduce the lock-acquisition race
   itself and passes on pre-fix code too; it exists for the AC2 "never
   swap a caller's own result" invariant, not as a second red→green proof.

### Accepted, not fixed (real, pre-existing, out of scope)

8. `saveSnapshot` writes via a bare `os.WriteFile`, no temp-file-plus-
   rename — a power loss mid-write can leave truncated JSON that
   `loadSnapshot` then fails to unmarshal. Pre-existing, unrelated to this
   diff (the guard sometimes causes `saveSnapshot` to be skipped, but never
   changes how it writes when it does run).
9. A future-dated on-disk `FetchedAt` also makes `Get()`'s staleness check
   report "never stale," so the scheduler would never fire a refresh at
   all. Pre-existing, independent of this change, and only reachable via a
   bad system clock at boot — same root cause as Finding 1's invariant note.

## Verified beyond automated tests

- `gofmt -l .`, `go build ./...`, `go vet ./...` — clean.
- `golangci-lint run ./internal/plugins/marketplace/...` (v2.5.0, CI's
  pinned version) — 0 issues.
- `go test ./internal/plugins/marketplace/... -race -count=8` — stable, no
  flakes (both before and after the post-review comment edits).
- `go test ./internal/pages/... -race -run
  'TestPluginsPage_|TestPluginStore|TestApplyPluginUpdate|TestHandleUpdatePlugin'`
  — green (all three `GetOrFetch`/`Fetch` call sites' own tests).
- Full `go test ./...` (whole repo, no `-race` — `internal/plugins/-race`
  has a separate, pre-existing hang unrelated to this change, tracked as
  ut-docs#2156) — green, exit 0.
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh`
  — both pass (no SQL, no user-facing strings touched).
- No real client/shop name; only credential-shaped literals are obvious
  test fixtures (`test-token`, `example.com`, `deadbeef`).

## Deferred / follow-up

- The single-cache-slot-regardless-of-params design remains a known,
  deliberately deferred limitation (see the card's own "suggested
  direction") — not reopened by this fix.
- `saveSnapshot`'s non-atomic disk write (Finding 8) and the future-dated-
  clock staleness interaction (Finding 9) are real but out of scope here;
  no new card filed since neither is newly introduced or newly exposed by
  this change, and both would need a real bad-RTC repro to prioritize.

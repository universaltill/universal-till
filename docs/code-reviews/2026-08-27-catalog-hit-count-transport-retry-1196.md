# 2026-08-27 — Eliminate net/http idle-connection-retry race in a catalog-hit-count test (ut-docs#1196)

## Summary

`TestSetupWizardListsInstallableCatalogLanguagesAndCachesFetch` asserted
an exact HTTP hit count across two wizard renders, proving the 5-minute
language-catalog TTL cache serves the second render without a real
network fetch. It failed reliably in CI — never in an isolated local
run — as the *second* of `ci.yml`'s two back-to-back
`go test ./internal/pages/...` invocations in one job
(`LANG=en_GB.UTF-8` then `LANG=de_DE.UTF-8`, see that step's own comment
for why the double-run exists).

## Background

Found while driving `universal-till#586` (the `ut-docs#1180` tax-plugin
prompt) to a green `build` check — this test's own file
(`setup_language_catalog.go`/`_test.go`) is untouched by that PR, so this
was a pre-existing, previously-unnoticed issue surfaced by chance, not a
regression from that work.

## Investigation

- CI failure: `"expected exactly one catalog fetch across two renders
  (TTL cache), got 2"` — 3/3 occurrences on the same PR's `build` check,
  every time on the *second* of the two locale runs in the job.
- Ruled out this PR's diff as the cause: neither `setup_language_catalog.go`
  nor its test file was touched.
- Ruled out a genuine caching bug in the app: `setupLanguageCatalogEntries`
  is a single mutex-guarded package-level cache with no `t.Parallel()`
  anywhere in `internal/pages` — nothing else in the package can race it.
- Reproduced locally, deterministically tied to the double-run pattern:
  - `LANG=en_GB.UTF-8 go test ./internal/pages/... -count=1` → clean,
    every time (5/5 isolated runs also all passed).
  - Immediately after, same shell, `LANG=de_DE.UTF-8 go test
    ./internal/pages/... -count=1` → the identical failure, reproduced
    1/1 before the fix.
- Root cause: `net/http.Transport`'s own documented behavior — it
  silently retries a GET once when the request was issued on a pooled
  idle connection the server had *just* closed, a legitimate race,
  invisible to the caller (one successful response either way). Under
  CPU/IO contention from the first `go test` invocation's teardown still
  finishing as the second starts, the fake marketplace's
  `httptest.Server` connection is measurably more likely to hit exactly
  that race — landing one logical fetch as two real HTTP requests, which
  is exactly what the test's hit counter observes.

## Fix

`internal/pages/setup_language_catalog_test.go`:
`mkt.server.Config.SetKeepAlivesEnabled(false)` on this test's own fake
marketplace server. Disabling keep-alives closes every connection after
one response, which removes the idle-connection-reuse race outright —
test-infrastructure-only, no production code touched, scoped to this
one test's own server instance (every other test sharing
`newFakeMarketplace` is unaffected, since each test gets its own
`*httptest.Server`).

**Rejected alternative: loosening the hit-count threshold.** With only
two renders in this test, a wider bound (e.g. `hits > 2`) couldn't
distinguish "one retried fetch" from "caching doesn't work at all" —
both would show `hits == 2`, silently defeating the assertion's actual
purpose. Fixing the race itself, rather than tolerating its symptom,
keeps the test able to catch a real regression.

## Verification

- Reproduced the exact CI failure locally before the fix (`en_GB` clean,
  `de_DE` immediately after fails identically) — confirms the fix target
  matches what CI actually hit, not a guess.
- After the fix, reproduced the same `en_GB`-then-`de_DE` sequence twice
  more, both clean (2/2).
- `gofmt -l .` clean, `go build ./...` / `go vet ./...` clean.
- `go test ./...` (repo-wide) green.
- `guard-i18n.sh`, `guard-compliance-claims.sh`, `guard-data-access.sh`,
  `guard-help-topics.sh` all pass (test-only change; included for
  completeness, none were expected to be affected).

## Verdict

**Safe to merge.** Test-only, root-cause fix (not a threshold loosening),
verified against the exact reproduction of what CI observed. No
production code path is touched.

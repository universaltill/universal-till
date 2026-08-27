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

## Correction (same day, while driving universal-till#586 to green)

The diagnosis above was **wrong about what CI was actually hitting**, found
while merging this fix into `universal-till#586`'s branch and re-running the
exact `en_GB`-then-`de_DE` double-run against that branch's own head: the
failure reproduced again, deterministically, 1/1 — not intermittently, and
not only as the *second* run in a pair. Isolating it further:

- `LANG=de_DE.UTF-8 go test ./internal/pages/... -run
  TestSetupWizardListsInstallableCatalogLanguagesAndCachesFetch` fails
  every time, alone, cold, no prior run in the same process — impossible
  for a connection-reuse race, which needs a pooled idle connection from a
  *prior* request to race against.
- The failing render's own debug log shows two DIFFERENT marketplace
  requests, not one retried twice: `capability=language` and
  `capability=tax`.
- Root cause: `universal-till#586` (this same PR, ut-docs#1180) added a
  tax-plugin tile to the setup wizard's step 3, fetched on every `GET
  /setup` render via `setupInstallableTaxPlugin(ctx, d, code)` — and `code`
  is `detectCountry(...)`, which falls back to the OS locale's own region
  when the timezone doesn't resolve one. Under CI's `LANG=de_DE.UTF-8`
  run, `detectCountry` resolves `"DE"` from the locale alone, which IS in
  `countryTaxLocale`, so the render fetches the tax catalog too — a second,
  legitimate `GET /v1/catalog/plugins?capability=tax` alongside the
  existing `capability=language` one. `catalogHits()` counts both
  indiscriminately, so a test asserting "exactly one fetch, proving the
  language cache works" started seeing 2 — correctly, given what the render
  now actually does — the moment this PR's own diff landed in the same
  branch as this test.
  The earlier investigation ran its reproduction on a fix branch created
  from `main` *before* `universal-till#586` merged into it, so
  `setupInstallableTaxPlugin` didn't exist there yet — the "1/1 reproduced,
  2/2 clean after the fix" result recorded above was real, but reproducing
  a different, genuine (if rarer) net/http race, not the one CI's `build`
  check was actually failing on for this PR.
- This was never a flake. It is 100% deterministic under
  `LANG=de_DE.UTF-8`, with or without a prior run in the same process,
  with or without `SetKeepAlivesEnabled(false)`.

**Real fix** (in `universal-till#586`, `internal/pages/sync_plugins_test.go`
+ `setup_language_catalog_test.go`): `fakeMarketplace` now tracks catalog
hits per `capability` query value (`catHitsByCapability` /
`catalogHitsFor(capability)`), and the four exact-count assertions in
`setup_language_catalog_test.go` that only ever meant to test the language
catalog now call `catalogHitsFor("language")` instead of the raw
`catalogHits()` total — immune to whatever *other* capability catalogs a
render also happens to browse, now or in the future. Verified: the exact
`en_GB`-then-`de_DE` double run, repeated 3× in a row plus two full
`go test ./internal/pages/...` passes under each locale, all clean.

`SetKeepAlivesEnabled(false)` is left in place — it is a real, defensible
guard against the connection-reuse race its own comment describes, costs
nothing, and does no harm — but it is no longer the load-bearing part of
why this test passes, and the file comment has been corrected to say so.

**Lesson**: "confirmed by local reproduction" is only as good as what the
reproduction branch actually contains. The original investigation reproduced
a real bug, just not the one CI's failure was pointing at, because the
repro branch was missing the very diff (`universal-till#586`'s own tax-catalog
fetch) that made the assertion wrong. Re-diagnosing against the actual
failing branch — not a same-shaped clean-room branch — surfaced this.

# Review — hermetic /setup tests against OS locale (ut-docs#662)

## Summary

`ut-docs#590`'s OS-language-detection redirect on `GET /setup` fires
whenever `detectLanguage()` finds an available language from the
process's real `LANG`/`LC_ALL` — which any real developer machine sets
and CI's runner container doesn't. Four tests requesting `/setup` with no
`?lang=`/cookie and expecting a `200` got a `303` instead, but only
outside CI: `TestFirstBootSetupThenLogin`, `TestSetupWizardRendersShopTypeStep`,
`TestSetupPageLoadsHTMXForTheJoinForm`, `TestSetupPageRendersDiscoveryAffordance`.

Fix: each now calls the existing `withOSLocale(t, "", "")` helper
(`setup_detect_test.go`, same package) to stub the detection seams
(`osLocaleEnv`/`osTimezoneName`) to "no locale," with `t.Cleanup`
restoring them — the tests state the environment they test instead of
inheriting the developer's. Also adds a second CI test run with
`LANG=en_GB.UTF-8`/`LC_ALL=en_GB.UTF-8` set, so CI's own empty-locale
runner stops hiding this class of bug (AC#3).

## BA scoping correction

The card originally named 6 failing tests. BA re-verification this cycle
found only 4 reproduce as locale-caused; the other 2
(`TestInventoryReplicaBannerNeverLinksAcrossDevices`,
`TestCatalogReplicaBannerNeverLinksAcrossDevices`) exercise `/inventory`,
which never calls `detectLanguage()` — confirmed no code path exists
(`detectLanguage`/`detectCountry`'s only callers are `setup_page.go:92,135`).
Reproduction attempted across 5 locales + unset, filtered + full suite,
with and without `-race`: both passed every time. Left unchanged rather
than guessing a fix with nothing to verify it against — the issue body
and a comment on the ticket record this narrowing.

## No production code changed

The diff touches only 4 `*_test.go` files and `.github/workflows/ci.yml` —
zero templates, `web/locales/`, `web/help/`, or README changes, so the
UX-guidelines/manual/help-topic checks in the reviewer skill don't apply
(confirmed, not silently skipped).

## Independent review

Opus, fresh context, worktree-isolated (complexity:medium → Opus per
model routing). Full report in the pipeline session; summarized here.

**Verdict: SAFE TO MERGE AFTER FIXES** — exactly one item, a process
artifact: this review record itself, which `CLAUDE.md` requires before
merge and was missing when the review ran. No code change requested.

**What it verified independently** (re-derived, not trusted):
- `withOSLocale` used correctly at all 4 call sites (right package,
  signature, placed before the request in every case); confirmed no
  `t.Parallel()` anywhere in the package, so mutating the package-level
  seam vars is race-free — corroborated by clean `-race` runs.
- `go build`/`go vet` clean; the 4 tests pass under `-race` with LANG
  unset and with `en_GB.UTF-8`; full `go test ./...` green under unset,
  `en_GB`, `de_DE`, `tr_TR`, `fa_IR`, `ar_SA`; full `internal/pages/...`
  green under `-race` both unset and `en_GB`.
- All three required guards green, plus i18n/htmx-loaded/help-topics
  (expected — nothing user-facing changed).
- Reproduced the root cause independently: reverted the 4 test files to
  `origin/main`, kept the new CI step, and confirmed it fails with
  `LANG=en_GB.UTF-8` (303 redirects) and passes with LANG unset — i.e.
  the new CI step would have caught this exact bug before the card was
  filed.
- Re-verified the TDD/false-pass claim personally with two independent
  induced breakages: forcing `GET /setup` to `500` (all 4 tests failed
  for the right reason, both with and without LANG), and removing the
  `htmx.min.js` `<script>` tag from `setup.html` (only
  `TestSetupPageLoadsHTMXForTheJoinForm` fired, its sibling correctly did
  not) — confirming the assertions have teeth beyond the status code.
  Both reverted; tree confirmed clean after.
- Independently confirmed the 2-tests-not-reproduced claim by grepping
  every caller of the locale-detection seams and finding no path from
  `/inventory`'s handlers.
- Checked the new CI YAML with a parser (not by eye): correctly placed as
  the last step in the `build` job, right after `Test`, same
  `GOMODCACHE`/`GOCACHE`, running the full suite (not scoped to one
  package). Also checked a real risk directly: `en_GB.UTF-8` is not a
  generated system locale in this environment (`locale -a` has no
  `en_GB.UTF-8`) and the suite still passed — confirming the step works
  purely by setting the env var, with no dependency on the runner having
  that locale installed.
- Commit message checked against the diff: accurate on every checkable
  claim.

**Non-blocking findings, triaged:**

1. **The new CI step only exercises one of `/setup`'s two detection
   branches** (language available → redirect; the reviewer showed the
   *unavailable* branch is actually worse — a nil-`Settings`-pointer
   panic on the test harness's minimal `newAuthTestMux` fixture, though
   confirmed **not** a production bug: `internal/pages/init.go` always
   wires a real `Settings` store; the panic is moot on this branch since
   the stub makes `detectLanguage` return `""`, never reaching that
   line). The CI step's own comment ("catches the whole class... bug")
   overclaims slightly — it covers one branch of two. **Accepted as a
   follow-up, not fixed here**: broadening the CI locale matrix
   (`de_DE.UTF-8` alongside `en_GB.UTF-8`) is a reasonable next step, but
   the harness fragility it would need to survive (`newAuthTestMux`'s nil
   `Settings`) is itself out of scope for a test-hermeticity card. Logged
   as ut-docs#672.
2. **`go test ./...` runs twice in the `build` job**, roughly doubling
   that job's wall-clock (measured ~79s per run locally); `e2e`/`contract`
   both depend on `build` completing. Accepted deliberately — the
   alternative (scoping the second run to `internal/pages`) would
   undercut AC#3's actual intent (catch this class of bug anywhere in the
   tree, not just where it's known to exist today).
3. Setting both `LANG` and `LC_ALL` in the new CI step means the
   `LANG`-only fallback path in `osLocaleEnv` is never itself exercised
   in CI. Trivial, not worth a second CI variant for.
4. Pre-existing `gofmt` drift on 6 unrelated files, unrelated to this
   change and not gated by CI today — noted, not this card's job to fix.
5. Call-order placement of `withOSLocale` (before deps construction in
   the 4 new sites vs. after, in the 8 pre-existing ones in
   `setup_page_test.go`) — checked directly, both orders are correct
   given `t.Cleanup`'s LIFO restore ordering; no change made.

## Verified beyond automated tests

No UI/runtime surface changed (test + CI infra only), so no driven run or
screenshot check applies — same conclusion the reviewer reached
independently.

## Safe-to-merge verdict

**Safe to merge.** Independent review found no code-level blocking issue;
the one blocking item (this record) is resolved by its own existence.
One follow-up backlog card filed (ut-docs#672) for the CI-locale-matrix
non-blocking finding.

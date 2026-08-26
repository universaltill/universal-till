# Setup wizard language catalog: two follow-up bug fixes (ut-docs#1110)

**Card:** universaltill/ut-docs#1110 — two real bugs found while independently
reviewing a duplicate implementation of ut-docs#1092 (universal-till#546,
closed unmerged after a concurrent pipeline lane's PR #545 merged the same
card 13 seconds earlier). #545's own review caught a different pair of bugs
(a lazy-registration/ADR-0015 leak and an offline-fetch-timeout bound) and
didn't happen to hit these two, which are still present in what actually
shipped to `main`.

**Complexity:** medium. Dev inline (Sonnet), review at Opus (fresh context,
isolated worktree).

## What shipped

1. **`GET /setup`'s "we don't have `de` yet — it's on the way" note no
   longer renders alongside a working `de` catalog-install tile.**
   `detectLanguage()`'s unavailable check only ever compared against
   `httpx.AvailableLocales()` (core-compiled locales), with zero
   cross-reference to the marketplace catalog fetch `renderWizard` performs
   a few lines later in the same request. On a real German Pi with the
   catalog reachable, step 1 showed the note directly above a working
   install tile — the exact field report ut-docs#1092 exists to fix,
   reproduced by a second mechanism. `setup_page.go`'s `GET /setup` handler
   now calls `setupInstallableLanguages` (a cache hit against the existing
   TTL cache, not a second network round-trip — verified, see below) before
   deciding whether to set `langUnavailableCode`, and suppresses both the
   note and the `setup.detected_lang_unavailable` telemetry
   (ut-docs#589 child 3's missing-language ticket-filer) whenever the
   detected language is already in the catalog.
2. **The country step's pending-plugin write now merges instead of
   wholesale-replacing.** `installBasePluginsForSetup` (the ut-docs#591
   country auto-install hook, untouched by #545) persisted its own specs via
   a bare `savePendingBasePlugins` both before its install attempt and
   after a partial success — silently dropping any spec the wizard's
   language step (`setup_language_catalog.go`, new in #545) had already
   queued. It now merges via a new `addPendingBasePlugins` helper
   (append-if-absent) before attempting, and removes only the specific spec
   that actually installs (`dismissPendingBasePlugin`, already existed) —
   never a wholesale replace.

## Independent review (Opus, fresh context, isolated worktree)

Verdict: **yes-with-fixes-first**. No blocker-class findings (nothing
money/data-loss/security-shaped) — everything was real-but-minor or
nitpick, so per the pipeline's process-depth rule a second full review round
was not run; the one explicitly-requested fix below was applied and
re-verified by me directly instead.

- **Applied before merge**: the "remove only the spec that installed" half
  of fix 2 had zero test coverage — a mutant reverting the new
  `dismissPendingBasePlugin` call back to the old wholesale-clobber shape
  passed the entire `internal/pages` package. Added
  `TestInstallBasePluginsForSetup_SuccessRemovesOnlyItsOwnSpecNotTheWholeList`
  (a real successful install via a fake marketplace + Ed25519 verification,
  with an unrelated spec seeded alongside) and a `len(pending) != 2` guard
  on the existing merge test (closes the reviewer's second, related gap —
  the dedup branch was also unable to catch a same-spec-added-twice
  regression). Confirmed the new test genuinely catches the mutant
  (re-injected it, watched the test fail with the exact symptom, restored,
  confirmed green) before considering this "applied," not just "written."
- **Accepted as real-but-minor, deferred**: a *previously* persisted
  `setup.detected_lang_unavailable` value is never cleared once the catalog
  starts covering that language (no consumer of that setting exists yet,
  so low impact today); the same wholesale-replace pattern this card fixes
  in `installBasePluginsForSetup` still exists in `basePluginRetryTick`
  (narrower window — first-boot-only — filed as ut-docs#1117); the new
  `code`-vs-catalog comparison in `setup_page.go` isn't `baseLang`-normalized
  (pre-existing exactness inherited from `detectLanguage`, vanishingly rare
  in practice on real hardware); two pre-existing tests
  (`setup_page_test.go`, `auth_page_test.go`) implicitly depend on the
  package-global catalog cache being cold without calling
  `resetLangCatalogForTest` themselves (works today only because every
  catalog-populating test cleans up after itself — worth hardening
  eventually, not now); the manual's step-6 prose ("isn't available yet")
  is now slightly less precise than it could be but not false.
- **Verified false-positive**: whether the new `setup_page.go` catalog check
  is a real cache hit (confirmed — exactly one marketplace request in a
  full request that calls `setupInstallableLanguages` twice); whether `code`
  (from OS-locale detection only) is spoofable by a request (confirmed
  not — no query/header/cookie/body path feeds it); scope (confirmed clean
  — exactly the 2 logic files + 1 test file + the required `make docs-shots`
  regeneration); the 4 existing `installBasePluginsForSetup` callers
  (`NoMappingIsNoOp`, `OfflineThenBackgroundRetryInstalls`,
  `DE_HappyPathInstallsBasePluginSynchronously`, `DE_OfflineCompletesAnd...`)
  still test what they claim after the merge-not-replace rewrite.

## Verification beyond automated tests

- Both TDD claims independently re-verified by the reviewer: reverted each
  fix, confirmed the paired test failed with the exact reported symptom
  (not just "any" failure), restored, confirmed green.
- The mutation-killing test added post-review was itself proven to catch
  its target mutant (injected, watched red, restored, watched green) —
  the same discipline applied to the original two fixes.
- Full `go build ./...`, `go vet ./...`, `gofmt -l .`, full `go test ./...`
  (twice — once before the post-review test addition, once after) — clean
  throughout.
- `go test ./internal/pages/... -race` on the touched area — clean, no data
  race on the shared package-level catalog cache.
- All CI-blocking guards from `universal-till/CLAUDE.md`'s "Before
  committing" list — clean, including `guard-docs-shots.sh` after
  `make docs-shots` regenerated the manifest for the two touched
  `internal/pages/**.go` files (no PNG content change beyond
  encoder-nondeterminism noise in one already-regenerated image).

## Not verified / accepted gap

- No new real-browser drive for this follow-up (the underlying feature's
  visual surfaces were already screenshotted/driven live as part of
  ut-docs#1092/#545's own verification, and this fix changes server-side
  branching logic only — no new markup, no new visible state beyond what
  `TestSetupWizardCatalogAvailableLanguageSuppressesComingSoonNote` already
  asserts on the rendered HTML).

## Safe-to-merge verdict

Yes, after applying the one requested pre-merge fix (mutation-killing test
for the success-path removal), re-verified, full gate green.

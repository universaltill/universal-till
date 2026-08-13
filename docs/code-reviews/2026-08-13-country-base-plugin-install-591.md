# Code review: auto-install a country's base plugins at setup (ut-docs#591)

## What shipped

Once a merchant confirms their country in the first-boot setup wizard, the
till auto-installs that country's free base plugins (today: the DE/ES
language packs) through the existing Ed25519-verified marketplace install
path — never a second install code path, and never blocking wizard
completion.

- New `internal/pages/setup_base_plugins.go`: the `setupBasePlugins`
  country→spec registry, `installBasePluginsForSetup` (the wizard hook),
  `resolveAndInstallBasePlugin` (catalog resolve + install),
  pending-list persistence under a new `common.KeyPendingBasePlugins`,
  `basePluginRetryTick`, and the `StartBasePluginRetry` background worker.
- `POST /api/setup` calls the hook best-effort, same posture as the
  restore-choice and demo-data steps already beside it.
- Settings → Data grows a per-pending-entry chip with a Remove button
  (`POST /api/settings/dismiss-pending-base-plugin`, manager-gated).
- i18n: `setup.base_plugins.{title,pending,dismiss_btn}` across all 4
  locales. Manual (`web/help/{en,ar,fa,tr}/display.md`) and README updated.
- 11 new tests in `internal/pages/setup_base_plugins_test.go`, plus a
  `setCatalog` extension to the existing `fakeMarketplace` test double so
  it can serve `GET /v1/catalog/plugins`.

Reviewed as commit `c51c118` on `feat/591-country-base-plugins`.

## Review

Independent review by a different-model subagent in an isolated git
worktree, reviewing the diff with fresh eyes rather than trusting the
Dev's self-report. **Verdict: ready to commit and open a PR**, with one
test-coverage gap found and fixed by the reviewer (below). No blocker-class
finding (nothing touching money, tax, data loss, or security), so no second
review round was earned.

### TDD verification — revert-then-restore, four mutations

The point of this step is to prove the new tests are load-bearing rather
than decorative. Each mutation was made to the *implementation* (never the
test), kept compiling (two first attempts produced only "imported and not
used" build errors — a build break is not proof a test detects a behaviour
change, so each was reworked into a genuine behavioural mutation), the test
was run, and the implementation was restored byte-for-byte from a pristine
copy afterwards.

1. **Idempotency** — removed the `PluginActive` early-return in
   `resolveAndInstallBasePlugin`.
   `TestResolveAndInstallBasePlugin_IdempotentWhenAlreadyActive` failed with
   `expected exactly one download-token request across both attempts, got 2`.
   This is the real proof of item 5: a second resolve does *not* re-hit the
   marketplace or double-install.
2. **Highest-semver selection** — inverted the comparison to
   `updates.Newer(best.Version, p.Version)` (lowest wins).
   `TestResolveAndInstallBasePlugin_PicksHighestSemverVersion` failed with
   `expected the higher-semver listing (1.2.0) to be installed`.
3. **Client-side filter** — deleted the `CanonicalType`/`Locale` guard, so
   the code trusts the server-side filter alone.
   `TestResolveAndInstallBasePlugin_FiltersNonMatchingEntriesClientSide`
   failed with `expected the matching language/de listing to be installed`.
4. **Offline tolerance** — removed the persist-before-network write.
   Both `TestInstallBasePluginsForSetup_OfflineThenBackgroundRetryInstalls`
   and `TestSetupWizardDE_OfflineCompletesAndLeavesPendingForRetry` failed
   with `expected the DE language spec still pending, got []`.

All four restored cleanly; all 11 new tests pass afterwards
(`git status` clean against `c51c118` at each restore).

### Finding — background worker had no lifecycle test (fixed)

`StartBasePluginRetry` is registered against `app.Run`'s drain WaitGroup,
but nothing tested that it actually returns on `ctx.Done()`. The reviewer
wrote a throwaway probe, confirmed the worker *does* shut down cleanly, then
proved the probe was load-bearing by removing the `ctx.Done()` arm from the
initial-delay `select` — the probe then failed with
`StartBasePluginRetry did not return on ctx.Done() — goroutine leak`
(it hung the full 3s budget). Without that arm a till would sit on the 30s
initial delay before it could finish shutting down.

The implementation was correct as written; the *test* was missing. **Fixed**
by promoting the probe into a permanent, documented
`TestStartBasePluginRetryShutsDownOnCtxDone` in
`internal/pages/setup_base_plugins_test.go`. This is the only change the
reviewer made to the diff.

### Checks that came back clean

- **ADR-0025 decision 4 boundary** — `setupBasePlugins` contains only
  `CanonicalType: "language"` entries (DE→de, ES→es). Nothing fiscal or
  tax-typed, and the `basePluginSpec` doc comment states the constraint and
  names ADR-0025 decision 4 explicitly, so the next person adding a row is
  told a fiscal entry needs a superseding ADR first. Verified against the
  ADR's primary text, which requires a fiscal plugin be *prompted*, never
  silently auto-installed.
- **Offline tolerance, end to end** — `TestSetupWizardDE_OfflineCompletes...`
  drives the real `POST /api/setup` against a closed listener and asserts
  `303 See Other` plus a session cookie, i.e. the wizard completes *and*
  signs the admin in with the marketplace fully unreachable.
- **Bounded wizard response** — the synchronous attempt is wrapped in
  `context.WithTimeout(ctx, setupBasePluginAttemptTimeout)` (5s), and
  independently the marketplace client carries its own
  `http.Client{Timeout: RequestTimeoutSec}`, so a *hung* (not merely
  refused) marketplace cannot stall the wizard's HTTP response. The
  background tick is covered by the same client-level timeout.
- **Retry cadence** — 30s initial delay, then a 5-minute ticker: not a
  tight loop, not effectively-never, and deliberately looser than the 30s
  plugin-sync tick. Retries indefinitely rather than capping attempts,
  which is right here — "offline" has no terminal failure to give up on.
  The rationale is written down in the const block.
- **Worker wiring** — `StartBasePluginRetry(bgCtx, dp, wg)` is wired in
  `internal/pages/init.go:283`, joined by `app.Run`'s drain.
- **The Dev's placement deviation is justified.** The Dev put the `Start`
  call in `internal/pages/init.go` rather than `internal/app/app.go` as the
  Architect suggested. Verified independently: every `Start` that needs the
  `*common.Deps` (`StartSyncPush`, `StartSyncPull`, `StartCloudSync`,
  `StartEODScheduler`, `StartAutoUpdateScheduler`) lives in `init.go`,
  where `dp` is built at line 187; `app.go`'s `Start` calls
  (`updates.Start`, `alerts.Start`, `discoveryAdvertiser.Start`) all take
  `cfg`/`*sql.DB` directly and need no Deps. `app.go`'s own comment at
  line 213 says it outright — "Init runs after this and needs mux routes
  registered before returning" — so `*common.Deps` does not yet exist at
  the point `app.go` starts its workers. The reasoning holds.
- **Recurring bug class (a), missing `os.MkdirAll`** — not applicable: the
  new code writes no files directly. Installs delegate to the pre-existing
  `cloudInstallPluginVersion` path.
- **Recurring bug class (b), cwd-relative paths** — none. No `paths.*`,
  `filepath.Join`, or `os.Create`/`WriteFile` calls in the new code at all;
  the only persistence is `d.Settings` (SQLite) plus the existing install
  path. Test runs left the repo tree clean (no untracked files).
- **Data access** — no raw SQL outside `internal/data`/`internal/db`.
  `resolveAndInstallBasePlugin` uses `data.NewPluginRepo(d.Db).PluginActive`.
  The one raw `SELECT COUNT(*)` is in `setup_base_plugins_test.go`, which
  the guard excludes (`grep -v '_test.go'`) and which CLAUDE.md scopes out
  as test-support.
- **Manual + README** — `web/help/{en,ar,fa,tr}/display.md` all gained a
  matching item 9; README's plugin section describes the new behaviour. No
  screenshot regeneration needed (no rendered layout change beyond a
  conditional chip).

## i18n

`guard-i18n.sh` passes: 998 template keys resolve, all locales match
en.json. The three new keys exist in all four locale files, each with the
`%s` placeholder preserved (the template renders them through `printf`, so
a dropped placeholder would surface as `%!(EXTRA string=DE)`).

The ar/fa/tr strings were read and sanity-checked individually — they are
genuine target-script sentences, not English copies, transliteration, or
gibberish: ar `إزالة`/`إضافات مجانية لبلدك`, fa `حذف`/`افزونه‌های رایگان برای کشور شما`
(correct ZWNJ usage), tr `Kaldır`/`Ülkeniz için ücretsiz eklentiler` (correct
diacritics). The pending-status sentence reads naturally in each.

**Disclosed deviation — translation provenance.** The homelab Ollama NAS
(`192.168.1.231:11434`) documented in `reference/translation.md` is
unreachable from this sandbox, so the ar/fa/tr strings were authored
directly rather than through the documented self-hosted flow. The reviewer
independently re-confirmed the unreachability this session (`curl -m 5`
returned `Connection timed out after 5002 milliseconds`) rather than
accepting the claim on trust. This is the same real, disclosed deviation
already recorded for ut-docs#268 and others — established precedent, not an
oversight, and the strings were independently checked for sense as above.

## Full gate

```
PASS: go build ./...
PASS: go vet ./...
ok   github.com/universaltill/universal-till/internal/pages          93.985s
ok   github.com/universaltill/universal-till/internal/pages/catalog   0.405s
ok   github.com/universaltill/universal-till/internal/pages/common   (cached)
ok   github.com/universaltill/universal-till/internal/updates        (cached)
ok   github.com/universaltill/universal-till/internal/app            (cached)
go test ./...  — full repo, zero failures
✓ data-access guard: no inline SQL outside internal/data / internal/db
✓ kiosk-engine guard: no self-order route handler references the cashier's Engine
✓ plugin-menu-read guard: no unlocked read of Pm.Installed / Pm.MenuPlugins / Menu under internal/pages
✓ i18n guard: 998 template keys resolve; all locales match en.json; no hardcoded Go-side response strings found; no hand-written hx-vals literals found; no hardcoded inline-JS status strings found
✓ help-topics guard: no route conflicts, every topic parses, all shipped locales complete, every page route has a claiming topic
```

The known pre-existing flaky race timeout in `internal/plugins`
(`TestPublish_NeverPanicsRacingManagerReload`) did not trigger this run —
the full-repo suite came back with zero failures, so it needed no
against-`main` comparison.

## Follow-ups (not blocking)

- `setupBasePlugins` maps a country to a *locale* only. A country with more
  than one official language (CH, BE, CA) will need either multiple rows or
  a locale-per-country-language notion; fine today with DE/ES, worth
  knowing before the table grows.
- The pending chip renders per spec with the locale upper-cased (`de`→`DE`),
  which reads as a country code but is really a language code. Identical
  for DE/ES; would diverge for a country whose code differs from its
  language's (e.g. AT→`DE`). Cosmetic, only visible once such a row exists.
</content>

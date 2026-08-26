# Code review — setup wizard: list + install marketplace language packs (ut-docs#1092)

**Date:** 2026-08-26
**Branch:** `feat/1092-wizard-language-catalog-install`
**Base:** `main` @ `b7dabf0`
**Reviewer:** independent Opus subagent, isolated `git worktree` (`review/1092-independent`, discarded after review)
**Card:** universaltill/ut-docs#1092, `complexity:hard` → Dev at Fable, Review at Opus

## What shipped

The setup wizard's language step (step 1 of 8) previously listed only the
4 compiled-in base locales (`en/tr/fa/ar`). German is complete and
shipping as a plugin (`ut-plugin-language-de`) but never appeared there —
a German shop owner's first interaction with the product was a language
picker missing their own language.

- New `internal/pages/setup_language_catalog.go`: a 5-minute TTL,
  mutex-guarded cache of the marketplace's `canonical_type=language`
  catalog listings, feeding extra tiles for languages available as a
  not-yet-installed plugin. A 3s fetch bound keeps `GET /setup` from
  hanging when the catalog is unreachable — bundled locales still render,
  with a "more available once connected" note (offline-first, ADR-0003).
- New `POST /api/setup/language`: installs the selected catalog language
  pack by reusing ut-docs#1055's `resolveAndInstallBasePlugin` in full
  (same `ListPlugins`/`localeInList`/`cloudInstallPluginVersion`
  Ed25519-verified path — no second install code path). A 20s foreground
  timeout (deliberately longer than #1055's 5s background best-effort,
  since this is a user-waited-on action). Success redirects to
  `/setup?lang=<locale>` — the exact same cookie-setting path
  (`httpx.ResolveLocale`) a bundled tile's link already uses. Failure or
  timeout joins the existing `savePendingBasePlugins`/
  `StartBasePluginRetry` queue rather than dropping silently, and the
  wizard says so via a new note.
- Template changes to `web/ui/pages/setup.html`: catalog tiles as plain
  POST forms (this wizard is server-rendered multi-page navigation
  throughout, deliberately not a new hx-post partial-swap for one
  button), a catalog-unavailable note, an install-pending note, a
  submit-disable/"Installing…" state.
- 4 new `setup.language.*` i18n keys in all 4 base locales.
- `web/help/*/users.md` (all 4 locales) + regenerated
  `web/help/img/manifest.json` + `README.md` updated.
- `internal/auth/middleware.go`: `/api/setup/language` added to the
  exact-path first-boot exempt list (see Blocker #1 below).

## Independent review — findings

Severity-ordered; all fixed except one accepted UX note and one
pre-existing-fixture gap noted for a follow-up card.

1. **BLOCKER (real live defect, found by Tester driving it in a browser,
   not by the Go suite) — `POST /api/setup/language` 401'd on every real
   click.** `internal/auth/middleware.go`'s `exempt()` uses a hardcoded
   exact-path allowlist for first-boot routes; the new route was missing
   from it, so a first-boot till (no operators, no session possible)
   rejected the request before the handler ever ran. Every Dev-written Go
   test was green because none of them ran through the real
   `auth.Middleware` — this codebase already has the precedent for this
   exact trap in `setup_pairing_test.go`'s own header comment. **Fixed**
   (commit `48c7efc`): route added to the exempt switch (exact-match, not
   a prefix — confirmed by reading `exempt()` directly), plus a
   regression test wrapping the real middleware
   (`TestSetupLanguageInstallExemptFromAuthMiddleware`) and a pin in
   `auth_test.go`'s `TestMiddlewareExemptsFirstBootPairingRoutes`.
   Re-verified independently: reverting the exempt-list line reproduces
   the exact 401 in both tests; restoring passes both again.

2. **BLOCKER — `GET /setup` silently registered the shop with the cloud
   (ADR-0015 violation).** The catalog-browse fetch built its marketplace
   client via `enroll.EnsureRegistered`, the same call the *install* path
   uses — but ADR-0015 (lazy store registration, still governing; ADR-0026's
   eager-registration alternative is explicitly "still proposed, not
   implemented") reserves creating a cloud store record for an actual
   plugin download/install or an explicit "Register now", specifically to
   stop "every download, demo, test boot, and CI run" from minting a
   store org. Rendering the wizard's very first screen is exactly that
   case. The codebase already draws this line correctly elsewhere
   (`plugins_store_page.go`'s browse path uses `enroll.Effective`, install
   uses `EnsureRegistered`) — this diff used the install-tier call for a
   browse. **Fixed**: browse path switched to `enroll.Effective(d.Cfg)`;
   install path (`resolveAndInstallBasePlugin`) is untouched and still the
   one legitimate trigger. Proven live against a stub marketplace:
   `register` hit count 1→0 for a `GET /setup` render, before vs. after.
   New regression test: `TestSetupWizardCatalogBrowseDoesNotRegisterStore`.

3. **HIGH — the 3s catalog-fetch offline-first bound didn't actually
   hold.** Same root cause as #2: `EnsureRegistered` takes `enroll`'s
   package-level `attemptMu`, held across the background enrolment retry
   loop's own 15s-timeout HTTP calls; `sync.Mutex.Lock` ignores the
   fetch's `context.Context`. Measured live against a black-holed
   (packet-dropped, not connection-refused) endpoint: up to 7.1s observed,
   worst case ~30s (two 15s calls) — 2.4x over the documented/tested
   bound. The existing test never caught this because it points at a
   *closed* local port (instant connection-refused) and only asserts
   `< 5s`. **Fixed by the same `enroll.Effective` change as #2** —
   `Effective` takes a short read-lock and never touches the network, so
   the fetch's own context timeout is the real bound again. Re-measured
   post-fix: six renders 35s apart, each exactly ~3.0s.

4. **MEDIUM — dead vertical space per catalog language.** The install
   `<form>`s are direct children of `.setup-card`
   (`display:flex; flex-direction:column; gap:.9rem`), so each empty form
   was a zero-height flex item still contributing a full `.9rem` gap —
   growing by one gap per catalog listing, which is the entire point of
   this card. **Fixed**: `hidden` on the forms (`display:none` doesn't
   affect submission — the visible button still owns its form via
   `form=`). Caveat stated honestly by the reviewer: this is CSS-derived,
   not pixel-measured — no browser was available in the review's isolated
   worktree environment to screenshot it; Tester's own live-browser pass
   (pre-fix) didn't have more than one catalog listing on screen at once
   to notice the compounding gap.

5. **LOW — catalog-supplied locale strings weren't shape-validated before
   landing in an HTML `id=`/`form=` and a JS `querySelector`.**
   `html/template` escaping means no injection was possible, but
   `CLAUDE.md`'s "validate all external input (users, plugins, devices)"
   applies to the catalog too, and an unvalidated value could still throw
   in `querySelector` (denial of that one button, not a security issue).
   **Fixed**: the same `isPlausibleLocale` check already applied to the
   `install_pending` query param is now applied to catalog-supplied
   locales too (function renamed from `isPlausibleLocaleParam`).
   Independently re-verified: removing the guard reproduces a live
   marketplace resolve for an arbitrary posted locale (visible in the
   debug log), confirming this wasn't just theoretical; restoring the
   guard blocks it again.

6. **LOW — accepted as-is.** The install-pending note renders raw locale
   codes ("Still installing **de** in the background…") while the tapped
   tile showed the native name ("Deutsch"). Consistent with the
   pre-existing bundled-locale tile row's own code/native-name mix — a
   UX-role call, not something this review changes unilaterally. Worth a
   UX pass; not filed as a new card since it's cosmetic and low-traffic
   (only shown on install failure/timeout).

7. **INFO — accepted, no fixture exists to prove it.**
   `TestSetupLanguageInstallHappyPath`'s fixture plugin ships no
   `locales/` directory, so no test proves the wizard actually *renders*
   in the newly-installed language post-install (only the `ut_lang`
   cookie is asserted). The mechanism (`cloudInstallPluginVersion` →
   `d.ReloadPlugins` → `Manager.syncLocales` → `I18n.SetOverlays`, all
   pre-existing and load-bearing for the real `ut-plugin-language-de`) is
   sound and unchanged by this diff; this is a pre-existing test-fixture
   gap shared with #1055's own tests, not something #1092 introduced.
   **New Backlog card recommended** (not filed as blocking this PR): a
   shared test fixture that actually ships a `locales/*.json` overlay, so
   this class of change can assert the rendered locale directly instead
   of only the cookie.

8. **INFO — CSRF, accepted, matches existing precedent.**
   `POST /api/setup/language` has no CSRF token, so a cross-site POST
   could reach a first-boot till on the LAN. Bounded impact (installing a
   signature-verified pack from the official catalog on an
   un-provisioned till) and identical posture to the pre-existing
   `POST /api/setup` — not worth diverging here.

9. **INFO — follow-up required, not blocking merge.** The 4 new
   `en.json` keys need matching entries in the external
   `ut-plugin-language-{de,es}` packs. `lang-pack-drift` CI is advisory
   on this PR (it touches `en.json`) and **blocking on push to `main`** —
   handled as the same-cycle follow-up this pipeline's lane-ownership
   rule requires for exactly this class of change (a core `en.json` merge
   implies a pack-repo follow-up on no board card of its own).

## Explicitly checked and clean

- **File-write / path-safety bug classes this pipeline repeatedly finds**
  (missing `os.MkdirAll`, cwd-relative path instead of `paths.Data(...)`):
  N/A — this diff adds no filesystem writes at all.
- **Repository pattern**: no raw SQL added outside `internal/data`/
  `internal/db`; `guard-data-access.sh` green.
- **i18n**: all 4 locale files carry exactly the same key count with the
  4 new keys present in each; `guard-i18n.sh` green; both `%s` verbs
  present in all 4 locales for `install_pending`.
- **RTL**: `dir="rtl"` correct for fa/ar; no literal `left`/`right`, no
  new hardcoded colors/spacing (reuses `.btn`, `.setup-langs`, `.muted`).
- **Manual**: `web/help/*/users.md` read in all 4 locales — same claim,
  consistently true, in each. The `users` topic ships no screenshots, so
  nothing needed regenerating; `guard-docs-shots.sh`/`guard-help-topics.sh`
  green.
- **No secret-shaped literals or real client/shop names** — test fixtures
  use only generic placeholders (`merchant-1`, `store-1`,
  `listing-lang-de`).
- **Live security probes** against a running first-boot till: an
  off-catalog locale, an already-bundled locale, and a path-traversal-
  shaped value all correctly rejected server-side (303 back to `/setup`,
  no install attempted); reflected `install_pending` values containing
  script/attribute-injection shapes were not emitted into the page at
  all, confirming the validation the diff already had (and #5 above
  tightened further).

## TDD re-verification (independently re-run, not taken on report)

Two revert→run→restore→run cycles, both in the reviewer's isolated
worktree:

1. Removed the `/api/setup/language` exempt-list line →
   `TestSetupLanguageInstallExemptFromAuthMiddleware` and
   `TestMiddlewareExemptsFirstBootPairingRoutes` both failed with the
   real 401 the fix claims to prevent. Restored → both pass.
2. Removed the server-side catalog-membership check from
   `setupLanguageInstallHandler` →
   `TestSetupLanguageInstallRejectsLocaleNotInCatalog` failed, and the
   debug log showed a live marketplace resolve for the unlisted locale —
   proof the guard is load-bearing, not decorative. Restored → all 8
   `ut-docs#1092`-related tests pass.

The reviewer's own new test (`TestSetupWizardCatalogBrowseDoesNotRegisterStore`)
was held to the same standard: written to fail against the pre-fix
`EnsureRegistered` call first (confirmed failing with a real assertion
message), then confirmed passing after the fix.

## Verified beyond automated tests

- Live browser drive (Tester phase, pre-review): catalog-available path
  against a real signed local marketplace (actual Ed25519-verified
  install completing, DB row confirmed `installed`/`active`),
  catalog-unavailable/offline path (no hang, correct fallback), fa/RTL
  rendering of the new strings, existing bundled-locale flow unaffected.
- Live security probes and live registration-count measurement (Review
  phase, described above) against a running till binary, not just
  `httptest`.
- Translation sanity check (Tester phase): tr/fa/ar judged grammatical
  and meaning-accurate for all 4 new keys and the manual addition; one
  pre-existing (not-introduced-by-this-PR) glossary inconsistency noted
  in `ar.json` (`الجهاز`/`الصندوق`), not a blocker.

## Gate (final, on the real feature-branch checkout, post-fix)

- `gofmt -l .` — clean.
- `go build ./...`, `go vet ./...` — clean.
- `go test ./...` (full repo) — all green, zero failures.
- `go test ./internal/pages/... ./internal/auth/... -race` — green (no
  data race on the new package-level TTL cache or anywhere else touched).
- All 17 CI-blocking `build`-job guard scripts (per `.github/workflows/ci.yml`,
  read fresh rather than trusted from memory) plus their own regression
  self-tests (`*_test.sh`) — all green.

## Deferred / explicitly out of scope (unchanged from BA/Architect)

- Persisting the wizard-chosen language as the shop's permanent
  `store.locale` default post-setup — tracked separately by
  ut-docs#1074/#1067.
- The pre-existing, not-introduced-here `PluginActive(best.ID)` inert
  idempotency check — tracked by ut-docs#1063.
- New e2e Playwright infra beyond what already exists.
- A UX pass on the install-pending note's code-vs-native-name mixing
  (finding #6 above) — cosmetic, low-traffic, not filed as blocking.
- A shared test fixture that ships real `locales/*.json` content
  (finding #7 above) — recommended as a new Backlog card.

## Verdict

**Safe to merge.** All findings from the independent review are either
fixed (with re-verified evidence, not just claimed) or explicitly
accepted with a stated reason. Full gate green on the real checkout.

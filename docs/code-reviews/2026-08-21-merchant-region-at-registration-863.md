# Code review — Derive merchant region from the shop's country at registration (ut-docs#863)

- **Date**: 2026-08-21
- **Repo**: universal-till (paired change; the closing half lands in `ut-cloud`)
- **Branch/PR**: `fix/863-merchant-region-at-registration`
- **Card**: universaltill/ut-docs#863
- **Author model**: Sonnet (built inline via subagent — contained, well-scoped change)
- **Reviewer model**: Opus, two independent fresh-context rounds (complexity:medium — different model per the reviewer skill)

## What shipped

ADR-0049's `de → germanywestcentral` residency mapping (already-accepted, already
mechanically wired end-to-end by ut-docs#829) was never actually triggered in
practice: nothing ever tagged a German merchant's `MerchantOrganization.region`
as `"de"` — every self-provisioned merchant stayed on the `"default"` sentinel
forever. This change closes the gap on the `universal-till` side: the shop's
already-configured country (`store.country`, the ADR-0026 setup-wizard field —
the same signal `internal/fiscal`'s TSE hard-gate already keys off) is read at
store registration and, when it's exactly `"DE"`, sent as `"region":"de"` in the
`POST /v1/stores/register` payload. Any other/unset country omits the field
entirely — byte-for-byte unchanged behavior otherwise. No new setup step for the
shop owner; the signal already exists.

- `internal/enroll/enroll.go` — `regionForCountry(country string) string`
  (pure, exact ISO-3166 alpha-2 match on `"DE"`, case-insensitive/trimmed,
  never a language/locale match); `register()` reads `store.country` via the
  already-passed `kv Settings` and adds `region` to the payload only when
  non-empty; a failed/absent settings read is treated as "no signal" (same
  tolerant pattern as this file's other best-effort settings reads).
  `StoreCountrySettingsKey` is exported and duplicates `internal/pages/common.
  KeyCountry` as a literal (same convention `internal/data/reset_archive_repo.go`
  already uses for this exact key — `internal/enroll` cannot import
  `internal/pages/common` without cycling through `internal/httpx`), with a
  drift guard (`TestEnrollStoreCountrySettingsKeyMatchesCommon`, in
  `internal/pages/common/state_test.go`, alongside the two existing guards of
  the same shape).

## Two review rounds — first found real blockers, second confirmed the fix

**Round 1 (Opus, fresh context)** — the first attempt derived region from
`cfg.DefaultLocale`, not `store.country`. Verdict: **NOT SAFE TO MERGE**,
3 blockers:

- **Blocker 1 (this repo).** `cfg.DefaultLocale` is the marketplace/plugin-
  catalog language (`UT_MARKETPLACE_LOCALE`), not the shop's locale —
  `internal/config/config_test.go`'s own comment warns the two are "easy to
  conflate". On a real German till it's `"en-US"`; no region would ever be
  sent. The base-language match (`"de-DE"`→`"de"`) also wrongly matched
  `de-AT`/`de-CH` — language, not jurisdiction.
- Blocker 2 and Blocker 3 were on the `ut-cloud` side (its own review record
  covers them in full: the residency guard rejected `"de"` in every deployed
  environment, and the acceptance criterion wasn't proven end-to-end).

**Fix**: switched the signal from locale to `store.country`, matched exactly
(not by base language) — see "What shipped" above. Independently mutation-
tested by the orchestrator before the second review round: reverting to a
base-language match makes `regionForCountry("DEU")` wrongly return `"de"`;
reverting to reading `cfg.DefaultLocale` makes the payload never carry a
region for a `de-DE`-configured till. Both fail as expected pre-fix, pass
post-fix.

**Round 2 (Opus, fresh context, scoped to the fixes — not a full re-review)**
— verdict: **SAFE TO MERGE**. Blocker 1 confirmed FIXED (with real evidence:
traced `store.country`'s persistence-before-first-registration ordering
through `internal/pages/setup_page.go`, confirmed no gap). One new Medium
finding on this repo:

- **NEW-1 (fixed) — the exported `StoreCountrySettingsKey` had no drift
  guard**, unlike the `reset_archive_repo.go` precedent it claimed to follow
  (whose whole point is the guard test). Mutation-proved: renaming the
  underlying literal to `"store.countryX"` left the entire `enroll` suite
  green, because every test seeds through the same constant — a silent,
  green-CI failure mode where the region hint simply stops being sent.
  **Fixed**: added `TestEnrollStoreCountrySettingsKeyMatchesCommon` in
  `internal/pages/common/state_test.go` (that package already transitively
  imports `internal/enroll` via `internal/httpx`, so no new cycle). Mutation-
  tested by the orchestrator: the same rename now fails this specific test
  with a clear message, while the rest of the suite stays green (isolating
  the fault to the drift guard, as intended).

## Verified beyond automated tests

- `strings.ToUpper(strings.TrimSpace(...))` exact-match logic: confirmed no
  panic on empty/garbage/unicode input, and confirmed `"DEU"` (3-letter code)
  and `" DE "` (whitespace) are handled correctly by direct unit cases.
- Confirmed `store.country`'s persistence ordering: `common.SaveState` in the
  setup-wizard handler persists the country before `installBasePluginsForSetup`
  (the first path that can trigger `EnsureRegistered`) runs — a fresh German
  till's first-ever registration call already has the country on hand.
- Full repo gate re-run twice (once per review round, independently by the
  orchestrator, not just trusted from the building subagent's own report):
  `go build ./...`, `go vet ./...`, `gofmt -l .`, full `go test ./...`
  (all packages), `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh` — all clean both times.

## Safe-to-merge verdict

Yes. Both review rounds' findings are fixed and mutation-verified; the second,
scoped round found nothing blocking.

## Explicitly deferred / out of scope

- **This repo's change alone does not close ut-docs#863** — it supplies the
  registration-time signal; `ut-cloud`'s companion PR (same branch name,
  `fix/863-merchant-region-at-registration`) is what actually validates and
  persists it, and closes the issue. Both PRs need to merge for the fix to
  take effect; see the `ut-cloud` PR body and its own review record for the
  cloud-side blockers/fixes.
- No backfill/migration: no real merchants exist in production yet
  (pre-launch), so there's no existing `region="default"` row that needs
  re-regioning this cycle.
- `internal/entitlements.Service.Acquire`/`ensureMerchant` (the self-serve
  plugin-download auto-provisioning path) still never forwards a region —
  no live signal exists there today; out of scope, unchanged from the
  original card.

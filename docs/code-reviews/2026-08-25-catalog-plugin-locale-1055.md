# 2026-08-25 — Independent review: base-plugin locale matched against ut-cloud's real wire shape (ut-docs#1055)

**Branch:** `fix/1055-base-plugin-locale-match` · **Card:**
[universaltill/ut-docs#1055](https://github.com/universaltill/ut-docs/issues/1055)
· **Base:** `f297de1` · **Reviewed commit:** `f417214`
· **Sibling (already reviewed/pushed separately):** `universaltill/ut-cloud`
branch `pipeline/1055-catalog-plugin-locale`

**Verdict: safe to merge**, with two blocking findings found and fixed on
this branch (`92e28ee`, `cb3bf85`) and two items deferred to follow-up cards.

## What shipped

`resolveAndInstallBasePlugin` (ut-docs#591's country base-plugin
auto-install) required `p.Locale == spec.Locale` to accept a catalog
candidate, but the real ut-cloud `ListPlugins` response never carried a
per-plugin `locale` field. `p.Locale` was therefore always `""` in
production, the match failed for every real listing, and the code fell
through to its "nothing published yet" no-op branch — no error, no retry, no
merchant-visible signal. A DE-provisioned test Pi ended up with zero plugins
installed despite a correct DE mapping.

- `internal/plugins/marketplace/client.go` — `PluginSummary` gains
  `AvailableLocales []string`, decoded camel-first
  (`availableLocales`, what the server really sends) with `available_locales`
  as fallback, mirroring the file's existing `IconURLCamel`/`PaidListing`
  dual-tag pattern. The legacy singular `Locale` is left in place.
- `internal/pages/setup_base_plugins.go` — match becomes
  `localeInList(p.AvailableLocales, spec.Locale)`.
- `internal/plugins/marketplace/catalog_contract_crossrepo_test.go` +
  `testdata/cloud_list_plugins_response.json` — new cross-repo contract test
  decoding a real captured protojson fixture through the production
  `json.Unmarshal(..., &ListPluginsResponse{})` path, following
  `marketplace_signature_crossrepo_test.go`'s precedent.
- `internal/pages/sync_plugins_test.go` — `fakeMarketplace` had been serving
  `PluginSummary`'s **own** snake_case tags (`locale`, `canonical_type`), a
  shape the real server never sends. Fixed to emit the real protojson wire
  shape.
- `internal/pages/setup_base_plugins_test.go` — existing behavioural tests
  updated to the new field shape.

The root-cause narrative in the commit message checks out, and the fake-double
fix is the genuinely important half of it — see the mutation evidence below.

## Findings

| # | Severity | Finding | Status |
|---|---|---|---|
| 1 | **Blocking** | `localeInList` compares **whole tags**; ut-cloud compares **base language subtags**. A pack published as `de-DE` passes the server filter, is returned, then dropped client-side — #1055's exact silent-no-match, one subtag down | **Fixed** `92e28ee` |
| 2 | **Blocking** | `guard-docs-shots` (CI-blocking) green on `main`, **red** on this branch | **Fixed** `cb3bf85` |
| 3 | Low | POS deliberately diverges from ut-cloud on empty-list / `en` catch-all | Accepted — documented + test-pinned |
| 4 | Low | Legacy `PluginSummary.Locale` left in place | Accepted — verified inert |
| 5 | Medium | `ID == ListingID` in production makes `PluginActive(best.ID)` inert; the fake still hides it | **Deferred** — follow-up card |

### 1 — Whole-tag match re-opens #1055 for region-suffixed tags (blocking, fixed)

The fix adopts the right *field* but not the field's *semantics*. ut-cloud is
the authority here, and `internal/catalog/service.go`'s `localeAvailable`
compares primary language subtags via `primaryLang`:

```go
al := primaryLang(a)
if al == wantLang || al == "en" { return true }
```

`availableLocales` is populated straight from the plugin author's manifest
(`internal/downloads/ingest.go`: `SetAvailableLocales(m.Locales)`), so whether
the German pack ships as `de` or `de-DE` is the author's choice, not a
contract. The POS asks the server for `locale=de`; a `de-DE` listing satisfies
the server filter and comes back — and was then dropped by the POS's stricter
`strings.EqualFold` re-filter. Silent no-match, no error, no signal: the same
failure mode the card exists to close. Region-suffixed tags are demonstrably
in circulation in this ecosystem (`de-DE`, `en-US`, `fa-IR` all appear in
config and test fixtures).

Fixed by matching on base language — which is also the rule the POS already
applies to its own locale lookups (`baseLang` in `plugin_page.go`,
`config.baseLang`'s region-tag fallback pinned by
`TestT_FallsBackFromRegionTagToBaseLanguage`).

### 2 — `guard-docs-shots` regression (blocking, fixed)

The guard hashes every non-test `internal/pages/*.go` that registers no mux
routes; `setup_base_plugins.go` registers none, so editing it changed the
app-surface hash and the screenshot manifest no longer matched the tree.
Verified this is *introduced by the branch*, not pre-existing: the guard fails
on `f417214` as committed and passes on `f297de1`'s tree. The before-committing
checklist was evidently not run to completion.

Resolved by evidence rather than assertion: a real `make docs-shots` run
captured all 92 screenshots (23 topics x 4 locales) and every PNG came back
**byte-identical**, so `cb3bf85` is the manifest surface hash alone with zero
pixel churn — empirically confirming the change has no rendered effect. (The
reused Chromium is 141 vs the 149 pin, which `resolve-chromium.sh` warns about;
byte-identical output means it did not matter here.)

### 3 — Deliberate divergence from ut-cloud's catch-all branches (accepted)

`localeAvailable`'s other two branches treat an **empty** `availableLocales`
list and an **`en`** entry as matching every requested locale. Those are right
for *browsing* the catalog (show the merchant everything they could read) and
wrong here, where the match decides plugin **identity**: an unrestricted or
English listing satisfying a `de` spec would silently auto-install the wrong
language pack. Note ut-cloud's `cloud.proto` documents `available_locales` as
"empty means global/all locales" and cites ut-docs#591 — so the divergence is
real and needed recording, not just implementing. The reasoning is now on
`localeInList` and pinned by
`TestResolveAndInstallBasePlugin_UnrestrictedOrForeignListingNeverMatches`, so
a future reader cannot "reconcile" the two repos in the harmful direction
without a failing test.

### 4 — Legacy `Locale` field (accepted, inert)

Grepped every `.Locale` in the repo and separated `marketplace.PluginSummary`
from the unrelated `basePluginSpec.Locale`, `config.Locales.Locale` and
`issuereport.Meta.Locale`. `marketplace.PluginSummary.Locale` is **written
once** (`client.go:386`) and **read nowhere** in production code. The two
near-misses are both false alarms: `tests/contract/marketplace_consumer_test.go:87`
sets `ListPluginsRequest.Locale` (a *request* field), and
`scripts/mock-marketplace/main.go:238` filters on a **locally defined**
`PluginSummary` struct of its own, not the marketplace package's. No live
footgun; no silent disagreement possible. Cleanup left out of scope as the
commit states.

Noted in passing (no action): the mock dev server serves singular `locale` and
no `availableLocales`, so it could not satisfy the new match — but its catalog
contains no `language`-type listing at all, so base-plugin auto-install would
never match there regardless. Genuinely N/A.

### 5 — `ID == ListingID`, and the fake that hides it (deferred)

The Dev's own flagged finding is real and the new contract test proves it:
against the real wire the fixture has no `listing_id`, and `UnmarshalJSON`'s
`firstNonEmptyStr(w.ID, w.ListingID)` fallback makes both fields the same
listing UUID (the test asserts exactly this). So
`PluginActive(best.ID)` looks up a listing UUID as a plugin id and is almost
certainly always false in production; idempotency rests on
`cloudInstallPluginVersion`'s own install-status handling.

Worth adding to that finding: **the fixed fake still emits a distinct
`listing_id` key** the real server never sends (deliberately, per its comment,
"so tests keep modeling the listing-id / plugin-id distinction"). That is the
one remaining fidelity gap, and it is precisely what keeps the idempotency
question unproven — the fake makes `ID != ListingID`, so
`TestResolveAndInstallBasePlugin_IdempotentWhenAlreadyActive` exercises a path
production never takes. Same *class* of defect as #1055 itself (a test double
diverging from the wire hiding a production no-op), so it belongs on the
follow-up card together with the ID fix rather than being papered over.

## TDD revert/restore, independently performed

Each mutation applied on disk in this isolated worktree, test run, then
restored and re-run green.

| Mutation | Test | Result |
|---|---|---|
| Fixture `availableLocales` `["de"]` → `["xx"]` | `TestCloudListPluginsResponseDecodes` | **FAIL** — `AvailableLocales = []string{"xx"}, want []string{"de"}` |
| `client.go`: camelCase decode wiring removed (snake_case only) | `TestCloudListPluginsResponseDecodes` | **FAIL** — `AvailableLocales = []string(nil), want []string{"de"}` |
| `localeInList` always returns `false` | `TestResolveAndInstallBasePlugin\|TestInstallBasePluginsForSetup\|TestSetupWizardDE` | **6 FAIL** (happy path, highest-semver, client-side filter, idempotency, offline retry, wizard e2e) |
| **Old `p.Locale != spec.Locale` restored** (the original #1055 bug) | same selection | **6 FAIL** — the decisive proof: with the fake now faithful, the original production code can no longer pass. The old tests were tautological *only* because of the fake |
| `localeInList` reverted to whole-tag `EqualFold` (this review's fix #1) | `..._MatchesRegionSuffixedCatalogLocale` | **FAIL** — `expected a listing published as "de-DE" to satisfy the bare "de" spec` |
| `localeInList` replaced with a **full mirror** of ut-cloud's `localeAvailable` | `..._UnrestrictedOrForeignListingNeverMatches` | **FAIL** — `a listing declaring no locale must not be auto-installed for a de spec` |

The last two confirm neither new test is tautological: one fails if the rule is
too strict, the other if it is too loose.

## Recurring bug classes checked

- **Missing `os.MkdirAll`** — N/A, confirmed by grep: no file-write handler in
  the diff (`os.WriteFile`/`os.Create`/`MkdirAll` all absent).
- **cwd-relative path instead of `paths.Data(...)`** — N/A, no path
  construction in the diff. The one new file read is
  `os.ReadFile("testdata/...")`, correct package-relative test convention.

## Cross-cutting checks

- **Repository pattern** — no SQL text in the diff; `guard-data-access.sh`
  clean.
- **Money** — no monetary amount touched.
- **i18n** — no user-facing string added or changed; `guard-i18n.sh` clean
  (1252 keys resolve, all locales match `en.json`).
- **Offline-first** — unchanged. The catalog call still returns
  `catalog unreachable` and leaves the spec pending for the background retry;
  the offline wizard test still passes.
- **Plugin trust** — install still goes through `cloudInstallPluginVersion`,
  the single Ed25519-verified path. No second install path introduced.
- **UX / manual** — confirmed rather than assumed: the diff touches **no**
  `web/**` and no template; the six changed paths are all `internal/**` Go
  plus one `testdata` JSON. `reference/ux-guidelines.md` and the `web/help/`
  manual requirement genuinely do not apply. (The `web/help/img/manifest.json`
  touch in `cb3bf85` is the guard's hash record, not manual content.)
- **Secrets / real customer data** — none. The fixture is synthetic
  (`German Language Pack`, vendor `universaltill`, a random UUID); the only
  grep hit for "token" is the protojson field `nextPageToken`. No real client
  or shop name.

## Gate

All run in this worktree at the final tree:

- `gofmt -l .` — clean (no output)
- `go build ./...` — OK
- `go vet ./...` — OK
- `go test ./...` — **41 packages ok, zero failures**, whole suite (incl.
  `internal/pages` 80.4s and `internal/plugins/marketplace`), run in full
  after the review fixes, not just the touched packages
- All **16** CI-blocking guards in `.github/workflows/ci.yml`'s `build` job —
  all PASS (`check-brand-assets`, `guard-android-i18n`,
  `guard-android-status-address`, `guard-autofill-suppression`,
  `guard-compliance-claims`, `guard-data-access`, `guard-docs-shots`,
  `guard-emoji-font`, `guard-help-topics`, `guard-htmx-loaded`, `guard-i18n`,
  `guard-kiosk-engine`, `guard-kiosk-launch-flags`, `guard-makefile-version`,
  `guard-plugin-menu-read`, `guard-webkit-version`)

## Deferred — follow-up card

1. **`ID == ListingID` makes the idempotency check inert** (medium). Against
   the real wire there is no separate manifest plugin id on this DTO, so
   `PluginActive(best.ID)` is effectively always false. Decide whether to
   resolve the installed plugin id from the manifest post-install or to drop
   the pre-check and lean on `cloudInstallPluginVersion` explicitly. **Bundle
   with it:** stop the `fakeMarketplace` emitting a `listing_id` key the real
   server never sends, so the test double stops hiding this — the same
   fake-diverges-from-wire class of defect as #1055 itself.
2. **Remove the dead `PluginSummary.Locale` field** (low). Verified unread;
   trivial but touches an exported DTO, so it belongs in its own change.

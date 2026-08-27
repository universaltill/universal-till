# 2026-08-27 — Setup wizard prompts to install the country tax plugin (ut-docs#1180)

## Summary

Reviewed `e9c5d62` on `feat/1180-tax-plugin-install-prompt`: a wizard-time
**install prompt** for a country's fiscal ("tax") plugin — `ut-plugin-tax-de`
today — surfaced on the Germany-only business-identity step and installed only
on an explicit tap.

The card as originally filed asked for a *silent auto-install* alongside the
language pack. That was correctly rejected during BA/Architect as contradicting
**ADR-0025 decision 4** (a fiscal/tax plugin is prompted, never silently
installed); `setup_base_plugins.go`'s own doc comment already said the same.
The diff implements the compliant alternative instead.

**Verdict: safe to merge**, after three fixes applied during this review (one
of them a user-visible defect in the feature's own happy path).

## What shipped in e9c5d62

- `internal/pages/setup_tax_catalog.go` (new, 272 lines) — a `"tax"`-capability
  marketplace catalog TTL cache plus `setupInstallableTaxPlugin`, mirroring
  `setup_language_catalog.go`'s existing prompt-then-click shape and reusing its
  helpers (`resolveAndInstallBasePlugin`, `localeInList`,
  `load`/`savePendingBasePlugins`) rather than a second install/retry mechanism.
  `countryTaxLocale` is DE-only.
- `setup_page.go` — registers `POST /api/setup/tax-plugin`, wires the tile into
  `renderWizard` off the same resolved country.
- `web/ui/pages/setup.html` — the install tile in step 3, using the same
  out-of-band-`<form>`/`form=` pattern as step 1's language tiles.
- `web/locales/{en,ar,fa,tr}.json` — four new `setup.tax_plugin.*` keys.
- `web/help/{en,ar,fa,tr}/users.md` + regenerated `web/help/img/manifest.json`,
  and a README paragraph.
- `setup_tax_catalog_test.go` — 13 tests.

## Independent verification performed

Run in an isolated worktree at `/tmp/utill-review-1180`, detached at `e9c5d62`.

| Check | Result |
|---|---|
| `gofmt -l .` | clean (no output) |
| `go build ./...` | clean |
| `go vet ./...` | clean |
| `go test ./...` (repo-wide) | **41 packages ok, no failures** |
| The 4 named tax tests, `-v -count=1` | all pass; log shows real marketplace round-trips, download-token issue and `[Verifier] Manifest signature verified for plugin ut-plugin-tax-de` |
| All 18 CI-blocking guards in `ci.yml`'s `build` job | all PASS |
| `-race` on just the tax tests (`-count=1`) | ok, 60.8s, no data race |

(The full-package `internal/pages` `-race` run was deliberately not attempted —
known to hang ~10min on unmodified `main` in this environment, pre-existing.)

### Test quality: not tautologies

Read every test body rather than trusting names. They assert real state, not
their own inputs: `data.NewPluginRepo(dp.Db).PluginActive(...)` against the
actual DB, `mkt.downloadTokenHits()` counts to prove a *rejected* request never
attempted an install, exact `Location` headers, and the contents of the
`ut-docs#591` pending list before and after a real `basePluginRetryTick`.

### TDD re-verification (mutation testing, performed personally)

Two mutations, each applied and reverted atomically in this worktree.

**1. Catalog-match filter** (a different assertion than the one the commit
message claims to have mutation-tested). Changed
`if e.CanonicalType != "tax"` → `!= "language"` in `setupInstallableTaxPlugin`:

```
--- FAIL: TestSetupInstallableTaxPlugin_DEMatchesCatalog (0.19s)
    setup_tax_catalog_test.go:53: expected a DE tax plugin match, got nil
--- FAIL: TestSetupInstallableTaxPlugin_IgnoresNonTaxListings (0.18s)
    setup_tax_catalog_test.go:140: a language listing must never match the tax
    prompt, got &{Country:DE ListingID:listing-lang-de}
```

Caught in **both** directions — the filter is genuinely pinned. Restored; `git
diff --stat` empty; tests green again.

**2. The "already active → don't prompt again" guard** (independently
confirming the commit's own claim). Changed `status.State ==
plugins.InstallStateActive` → `!=`:

```
--- FAIL: TestSetupInstallableTaxPlugin_AlreadyActiveReturnsNil (0.22s)
    setup_tax_catalog_test.go:99: expected nil once the listing is already
    active, got &{Country:DE ListingID:listing-tax-de}
--- FAIL: TestSetupWizardTaxPluginTileGoneAfterInstall (0.23s)
    setup_tax_catalog_test.go:374: the tax-plugin install tile must be gone
    once the plugin is actually installed
```

Restored; tests green again. The commit's TDD claim holds.

## Findings

### F1 — Install tap throws the operator back to step 1 of the wizard — **High, FIXED**

`POST /api/setup/tax-plugin` redirected to a bare `/setup` on every path. On the
following GET, `renderWizard` takes its GET branch: the template initialised
`step: {{ if .errKey }}{{ .errStep }}{{ else }}1{{ end }}`, and with no error key
that is **step 1**, while `code` falls back to `detectCountry(...)` — OS
detection. The wizard keeps no server-side draft state and no `sessionStorage`
(verified: no storage/`popstate` handling in `setup.html`), so:

- the operator is dumped at the *language* step, three steps back;
- anything typed into step 3's TSE fields is gone (those are only echoed on a
  POST re-render);
- worst case, a Pi imaged in English whose operator *hand-picked* DE has the
  country silently reverted to the detected one. Clicking straight through from
  there saves a different country, currency and tax rate — money-adjacent.

The language handler gets away with the identical redirect only because its
tiles are on step 1, where landing is correct. This tile is on step 3.

**Fix:** every redirect out of the handler now carries
`/setup?tax_country=<CC>` (and `&tax_plugin_pending=1` on the failure path);
`renderWizard` honours `tax_country` on GET *only* when the value is both
present in `countryTaxLocale` and a real wizard country code, sets `code` from
it, and resolves a new `startStep` (error → `errStep`, tax round-trip → 3,
otherwise 1) that the template now reads directly. No stored state — the same
query-param posture as the existing `install_pending`. The one genuinely forged
case (a country not in `countryTaxLocale` at all) still redirects to a bare
`/setup`, since there is nothing legitimate to resume onto.

Three tests added (`TestSetupGETResumesStep3ForTaxCountry`,
`TestSetupGETTaxCountryResumeIsNotForgeable`,
`TestSetupGETResumeShowsTaxPluginPendingNote`) and the three existing
`Location` assertions updated. The fix was itself mutation-checked: forcing the
resume branch to `startStep = 1` fails both new step-3 tests.

### F2 — The install tile had no CSS rule at all — **Low, FIXED**

`.setup-tax-plugin-prompt` appeared in no stylesheet
(`grep -rn … web/ --include=*.css` → nothing), unlike its analogue
`.setup-langs` (`app.css:1393`). Sections inside `.setup-card` are plain block
flow, so the prompt rendered as a bare, borderless block running straight into
the TSE inputs below it. Against `ux-guidelines.md`'s "reuse the existing design
tokens" / "reuse an existing pattern" rules.

**Fix:** a small rule next to `.setup-langs` using only `:root` tokens
(`--surface-2`, `--border`, `--radius`), **fully logical properties**
(`padding-block`/`padding-inline`/`margin-block` — no `left`/`right`, so the
fa/ar RTL locales are unaffected), rem sizing so it stays inside the fluid
touch-target scaling, and a full-width button because "Install" is three
letters in English and materially longer in de/tr.

### F3 — `<h2>` rendered before the section's own `<h1>` — **Low, FIXED**

The tile sat above `<h1>{{ T "setup.tse.title" }}</h1>`, so step 3 opened with a
subheading above its heading. **Fix:** moved the block below the step's `<h1>`
and hint. The manual copy ("this same step also offers to install it") stays
accurate.

### F4 — Dead template key `taxCatalogUnavailable` — **Low, FIXED**

`renderWizard` set `data["taxCatalogUnavailable"]`, which no template ever
reads — unlike `langCatalogUnavailable`, which `setup.html:94` genuinely uses
for the "more languages once connected" note. An unread key reads as though such
a note exists. **Fix:** dropped the assignment (the function's second return is
kept — it is documented and directly tested), with a comment pointing at F5.

### F5 — Offline first boot in Germany is never told the plugin exists — **Low, DEFERRED**

When the catalog is unreachable with nothing cached, `setupInstallableTaxPlugin`
correctly returns `(nil, true)` — but nothing renders. The language step shows a
note in exactly this situation; the tax step shows nothing, so a German merchant
setting up offline never learns a fiscal plugin exists. Recoverable (the plugin
catalog stays reachable later, and both the manual and README say so), so not a
blocker.

Deferred rather than fixed here because it is a **product** call, not a defect:
whether an unreachable catalog should say anything at all about a *fiscal*
plugin during setup is a wording decision with ADR-0040 implications, and it
needs copy in all four in-repo locales plus a `ut-plugin-language-{de,es}`
follow-up. Recommend a follow-up card.

### F6 — Two sequential 3s catalog fetches on a cold German first boot — **Low, DEFERRED**

`renderWizard` calls `setupInstallableLanguages` and then
`setupInstallableTaxPlugin` in sequence, each bounded by its own 3s fetch
timeout. On an offline DE till with a marketplace endpoint configured, the
first `GET /setup` can now pay up to ~6s instead of ~3s before painting. It is
first-boot-only (30s failure-retry throttle, 5min success TTL), never blocks
wizard *completion*, and non-DE tills are unaffected (the `countryTaxLocale`
lookup short-circuits before any fetch). Not fixed here: the clean fix is one
combined `Capability: ["language","tax"]` request or a concurrent pair, and
choosing between them needs ut-cloud's multi-capability filter semantics
(AND vs OR) confirmed first — out of scope for this card.

### F7 — `countryTaxLocale` and step 3's gating are coupled — **Informational, comment added**

The tile lives in step 3, which step 2 only routes to for `country === 'DE'`.
Adding e.g. `"FR"` to `countryTaxLocale` alone would resolve a listing no
operator could ever reach. Added a `NOTE when adding a country here:` comment on
the map so the next person moves the step gating too.

## Checks that came back clean (no finding)

- **No silent-auto-install regression.** `setup_base_plugins.go` is untouched by
  the commit (`git show e9c5d62 --stat -- internal/pages/setup_base_plugins.go`
  → empty), and its doc comment still states a fiscal/tax entry would contradict
  ADR-0025 D4. The compliant path is the only path.
- **Forged/stale POST cannot pick a listing.** The client posts only `country`.
  The handler re-derives the locale through `countryTaxLocale` and then
  re-resolves through the *same* `setupInstallableTaxPlugin` the tile renders
  from, so an arbitrary listing is unreachable from the request body. The
  `catalogUnavailable` path and the already-installed path both reject cleanly.
  `TestSetupTaxPluginInstallRejectsCountryWithNoMatch` proves zero download-token
  hits for `US`/`FR`. My new `tax_country` resume param is validated the same
  way (tax-mapped **and** a real wizard country) — covered by
  `TestSetupGETTaxCountryResumeIsNotForgeable`.
- **Auth window.** `POST /api/setup/tax-plugin` is gated on
  `svc.NeedsFirstBoot`, the same tier as `POST /api/setup/language`;
  `TestSetupTaxPluginInstallRefusedAfterFirstBoot` proves a 303 to `/login`
  after first boot.
- **Recurring bug class (a) — missing `os.MkdirAll` before a write.** Not
  applicable: the new file performs no filesystem writes at all
  (`grep -nE 'os\.(MkdirAll|WriteFile|Create|OpenFile)'` → nothing). Plugin
  bytes land through the existing `resolveAndInstallBasePlugin` path.
- **Recurring bug class (b) — cwd-relative path instead of `paths.*`.** No path
  construction anywhere in the new code.
- **Offline-first.** `POST /api/setup`'s success path ends in a plain
  `http.Redirect` and never touches the catalog; `renderWizard` is reached only
  on error re-renders. Wizard completion is never blocked on network. The
  catalog fetch is bounded, TTL-cached, serves a stale cache on refetch failure,
  and uses `enroll.Effective` rather than `EnsureRegistered` — preserving
  ADR-0015 (browsing must not mint the shop's cloud store identity).
- **Plugin trust.** Install goes through the existing verified path; the test
  logs show `[Verifier] Manifest signature verified` — ADR-0006 intact.
- **Repository pattern / money.** No SQL outside `internal/data`
  (`guard-data-access.sh` passes); no monetary amounts introduced.
- **i18n.** All four `setup.tax_plugin.*` keys exist in en/ar/fa/tr and
  `guard-i18n.sh` passes. Read the ar/fa/tr strings: genuine, idiomatic
  translations that match the English meaning — not untranslated English and not
  garbled machine output (e.g. fa correctly renders "§12 UStG" as
  «مادهٔ ۱۲ قانون مالیات بر ارزش‌افزوده», tr uses "orada yeme/paket servis"
  for dine-in/takeaway). Adding these keys means the usual
  `ut-plugin-language-{de,es}` follow-up; `lang-pack-drift` is advisory on the
  PR by design.
- **Compliance wording (ADR-0040).** Read `guard-compliance-claims.sh`'s actual
  denylist (`gobd-compliant`, `gobd-konform`, `kassensichv-*`,
  `finanzamtskonform`, `audit-proof`, `revisionssicher`, `certified by the
  finanzamt`, `approved by the tax office`, `you are compliant`, `fully
  compliant`, the `§146a` filing claims). The new copy is nowhere near the
  boundary: it states only what the software *does* — "Applies Germany's
  dine-in/takeaway VAT rates (§12 UStG) and signs each sale with your configured
  TSE" — with no legal-outcome promise anywhere, in any locale.
- **Manual.** Read the new sentence in all four `web/help/*/users.md`. It
  accurately describes the shipped behaviour (optional, prompted, never
  self-installing, disappears once installed) — not aspirational. README
  paragraph likewise correct, and explicitly states the offline-first reason a
  fiscal plugin is never a silent hard dependency.
- **Secrets / real names.** Swept the whole change for credential-shaped
  strings and real client/shop names — nothing. Test fixtures use
  `listing-tax-de` / `store-1` / `merchant-1`.

## Post-fix re-verification

Re-ran the whole gate after the fixes: `gofmt -l .` clean, `go build ./...`,
`go vet ./...`, `go test ./...` **41 packages ok / no failures**, all **18**
CI-blocking guards PASS, and the tax + resume tests green under `-race`.

`guard-docs-shots.sh` failed after the UI edits (expected — the surface hash
covers `web/ui/**`, `web/public/**` and non-test `internal/pages/**.go`).
Regenerated with `bash e2e/scripts/docs-shots.sh` — 92 Playwright shots passed,
new surface `c7b0580c75ef…`, guard green. Only `manifest.json` changed; no PNG
moved, since the tile needs a live catalog listing that the docs-shots harness
does not serve.

Test count in `setup_tax_catalog_test.go`: 13 → **16**.

## Verdict

**Safe to merge.** The architectural call is right — this is the ADR-0025 D4
compliant shape, and the "never silently install a fiscal plugin" invariant is
preserved and now additionally protected by a comment on `countryTaxLocale`.
The shipped tests are genuine (mutation-verified in two independent places).
F1 was a real user-visible defect in the feature's own happy path and is fixed
and test-pinned; F2–F4 and F7 are fixed; F5 and F6 are noted for follow-up
cards and neither blocks this change.

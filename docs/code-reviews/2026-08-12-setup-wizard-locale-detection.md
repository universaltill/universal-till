# Code review: setup wizard — offline system language + country detection

**Card:** universaltill/ut-docs#590 (child 1 of epic ut-docs#589, zero-touch setup)
**Branch:** `feat/590-setup-wizard-detect-locale-country`
**Date:** 2026-08-12
**Reviewer:** independent Opus subagent, fresh context — did not write the
implementation. Reviewed the staged diff (`git diff --cached`) plus the current
content of every changed file, re-ran every claim personally rather than
trusting the Dev's report, and mutation-tested both the Dev's tests and my own
fixes.

## What shipped

The first-boot wizard's language and country steps now arrive pre-filled from
the device's own OS locale and timezone, entirely offline — never IP
geolocation (ADR-0003; a shop's WiFi typically isn't configured yet at exactly
the moment the wizard runs).

- `internal/pages/setup_detect.go` (new) — `osLocaleEnv` / `osTimezoneName`
  swappable seams, an IANA-zone → `setupCountries` code map, POSIX locale-value
  parsing, and the "is this language one we actually ship" check against
  `httpx.AvailableLocales()`.
- `internal/pages/setup_page.go` — `renderWizard` gained the detected
  country/currency/tax + detected-unavailable-language inputs; `GET /setup`
  redirects through the existing `?lang=` mechanism for an available detected
  language and otherwise records `setup.detected_lang_unavailable` (for #589's
  child 3) and renders a coming-soon note.
- `web/ui/pages/setup.html` — `x-data` initial values from detection; the
  coming-soon note in step 1.
- `web/locales/{en,ar,fa,tr}.json`, `web/help/{en,ar,fa,tr}/users.md` — one new
  key and one new manual bullet, in every shipped locale.

## Verified personally (not taken on trust)

- `go build ./...`, `go vet ./...` — clean.
- `go test ./internal/pages/... -run 'Setup|Detect|Timezone|Locale' -count=1` —
  green (all detection, redirect, prefill and pre-existing wizard tests).
- `go test ./... -count=1` (whole module) — every package green.
- `guard-i18n.sh`, `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-help-topics.sh` — all five green.
  Also ran `guard-htmx-loaded.sh` (green) and `check-lang-pack-drift.sh`
  (fails — see finding N1).
- `gofmt -l` on all four changed Go files — clean.
- **Zero network in the detection path**, confirmed by reading rather than
  assuming: `setup_detect.go` imports only `os`, `strings`, `time` and
  `internal/httpx`; there is no `net/http` and no outbound call anywhere in
  `detectCountry` / `detectLanguage` / `parseLocaleEnv`, and
  `httpx.AvailableLocales()` reads the already-loaded in-memory translator. No
  IP geolocation anywhere. The offline-first non-negotiable holds.
- **Redirect-vs-render logic cannot loop.** The redirect target always carries
  `?lang=`, and any `lang` key in the query (including `?lang=` with an empty
  value, and a bare `?lang`) sets `hasQueryLang`, so the second hop always
  renders. `httpx.ResolveLocale` sets the `ut_lang` cookie on that hop, so the
  visit after it is cookie-bound and detection stops for good. The redirect
  value is always drawn from `httpx.AvailableLocales()`, so there is no
  open-redirect or injection surface. Detection sits behind the existing
  `NeedsFirstBoot` gate, so it cannot fire on a configured till.
- **`detectedTaxInclusive` does not regress the pre-#590 "on" default.** Read
  the code, not the comment: the map literal seeds `true` unconditionally and
  it is overwritten only inside the matched-country branch, so with nothing
  detected the template still emits `taxinc: 'on'` — the pre-#590 literal. The
  sibling keys are *absent* from the map when nothing is detected;
  `html/template`'s `stringify` skips untyped-nil args, so they render as empty
  strings (`country: ''`), not `<no value>`. Confirmed against a real rendered
  response, not reasoned about.
- **i18n placeholder handling is correct and matches the house pattern.**
  `{{ printf (T "key") .arg }}` is the same shape already used in
  `receipt.html`, `plugin_content.html` and `settings.html`. `httpx`'s template
  `T` returns a plain `string`, so the interpolated value is still HTML-escaped
  — verified in the real render: `We don&#39;t have de yet — it&#39;s on the
  way. Choose one of these for now:`. The key exists with exactly one `%s` in
  all four shipped locales and `guard-i18n.sh` confirms parity with `en.json`.
- **RTL is safe.** The note is a `<p class="muted">`; `.muted` in `app.css` is
  `color:` only — no `left`/`right` anywhere in the new markup, and the
  document `dir` still derives from the locale. Long-locale wrapping is a
  non-issue for the German case specifically, since `de` being unavailable is
  exactly why the page renders in English there.
- Every step keeps its visible way back (steps 2–6 and the join branch all
  retain their `setup.back` button); step 1 is the entry point.
- All card ACs are met: detection is a default and never a lock, country
  prefills currency + tax from the existing `setupCountries` table (no second
  list), the unavailable case defaults to English while showing the available
  languages and promising no date, and the detected-but-unavailable locale is
  recorded as `setup.detected_lang_unavailable` for #589's child 3. No scope
  creep — the diff is tightly bounded.

## Findings fixed in this pass

**F1 (HIGH). The timezone half of detection was a silent no-op on a real
till.** `osTimezoneName` was `time.Local.String()`. Go only preserves the IANA
zone name when `TZ` is exported; with `TZ` unset — the normal case for a
systemd-managed till, and for Debian/Raspberry Pi OS generally, where the zone
is configured through `/etc/localtime` + `/etc/timezone` and nothing exports
`TZ` — `time.initLocal` deliberately renames the loaded zone to the literal
`"Local"`, which matches no key in `setupTimezoneCountry`. Confirmed
empirically in this environment (`TZ` unset → `"Local"`; `TZ=Europe/Berlin` →
`"Europe/Berlin"`). Effect: the *primary* detection signal — and half of the
card's title — would never have fired in the field. Detection would have
degraded silently to the locale region alone, and would have detected nothing
whatsoever on the common `LANG=C.UTF-8` appliance image, which is precisely the
zero-touch case this epic exists for. **Fixed**: new `systemTimezoneName()`
falls back to `timezoneNameFromFiles()` — `/etc/timezone` verbatim
(Debian/Ubuntu/Raspberry Pi OS), then the `/etc/localtime` symlink target
(Fedora/Arch/Alpine) — when Go hands back the placeholder. Both are plain local
file reads; still zero network. An explicitly exported `TZ` still wins.
Covered by `TestTimezoneNameFromFiles` (all three sources, including the
neither-available case) and `TestSystemTimezoneNameFallsBackToDiskWhenTZUnset`,
written to be deterministic whether or not the test process itself has `TZ`.

**F2 (HIGH). A failed PIN silently rewrote the operator's country, currency
and tax rate.** `renderWizard` re-ran `detectCountry()` on the POST error path
as well as on GET. Reproduced before fixing: submitting `country=FR` /
`tax_rate_pct=20` with a mismatched PIN re-rendered with `country: 'DE' … tax:
'19'`. Because the country step's hidden `currency` / `tax_rate_pct` /
`tax_inclusive` inputs are bound to that same `x-data`, the operator's retry —
they are only retyping a mistyped PIN, and the wizard drops them straight on
step 5 — would have persisted Germany at 19% for a shop that deliberately chose
France at 20%, without ever showing the country step again. Pre-#590 the same
path reset to blank, which at least fell through to existing state defaults
rather than to a confidently wrong country, so this is a real regression
introduced by the change, on a tax rate. **Fixed**: GET prefills from
detection; a POST re-render echoes the operator's own submitted country and
derives currency/tax from the same `setupCountries` row (exactly what the
select's `@change` handler does), and never re-detects. Covered by
`TestSetupWizardPINErrorRerenderKeepsOperatorCountryNotDetected` and
`TestSetupWizardPINErrorRerenderWithNoCountryStaysBlank` (the no-country-
submitted case must stay blank with `taxinc: 'on'`, i.e. the pre-#590 default).

**F3 (MEDIUM, test quality). Two of the five new handler tests contained
assertions that could not fail.** `strings.Contains(body, "de")` is vacuously
true on this page — `name="code"` contains the substring — proven by probe, and
the German test still passed with the coming-soon note deleted from the
template entirely. Separately, both "must not show the note" assertions checked
for the literal locale key `setup.language.detected_coming_soon`, which `T`
resolves, so it never appears in rendered output regardless of what the handler
does. **Fixed**: the note carries a `data-detected-lang="{{ .detectedLangCode
}}"` hook (the repo's existing `data-*` test-hook convention, already used in
18 places) and all four assertions now target it. Mutation-confirmed: with the
note's `{{ if }}` forced false, both tests now fail, where before they passed.

All three fixes were mutation-tested individually — each reverted in turn, the
corresponding test confirmed failing with the exact wrong value, then restored
and confirmed green. My own first attempt at the F1 test was itself too weak
(it survived the mutation) and was strengthened before being accepted.

## Findings NOT fixed (out of this tree's reach, or out of scope)

**N1 (must be tracked, not merge-blocking). `check-lang-pack-drift.sh` fails
on this branch**: the new `setup.language.detected_coming_soon` key is missing
from `ut-plugin-language-de` and `ut-plugin-language-es`. Confirmed the drift
is genuinely new — deleting the key from `en.json` makes the guard pass. This
does not block the PR by explicit design: `.github/workflows/lang-pack-drift.yml`
is deliberately `push: branches: [main]` + `workflow_dispatch` only and never
`pull_request`, so a lagging externally-maintained pack cannot block an
unrelated core feature. But it *will* turn `lang-pack-drift` red on `main`
within minutes of merge, which is exactly what that guard is for. The key needs
adding in both pack repos; neither is reachable from this working tree. Worth
naming the irony: the feature whose entire point is "your language is coming
soon" is the one that puts the German pack out of sync.

**N2 (nit, own card).** The note names the raw code — "We don't have **de**
yet" — rather than the language's endonym ("Deutsch"). This is consistent with
the existing step-1 language buttons, which already render bare codes
(`en`/`ar`/`fa`/`tr`), so it is neither a regression nor out of place here. But
the card's "simple and professional" bar would be better served by endonym
labels across the whole language step. Doing it properly means a new key set
plus changing the existing button rendering — real scope creep for #590, and a
clean small card of its own.

**N3 (housekeeping, pre-existing).** Three gofmt-dirty files in the repo, all
untouched by this branch: `internal/pages/common/state.go`,
`internal/pages/external_api_test.go`, `internal/pages/import_bkp_page_test.go`.
No CI gofmt gate exists, so not blocking; recorded only so they are not
mistaken for this branch's doing.

**N4 (working-tree hygiene — raised during the review, resolved cleanly).**
Mid-review, `internal/catimport/bkp.go` and `internal/catimport/bkp_test.go`
were modified-but-unstaged in this shared working tree, belonging to a
*different* card (ut-docs#594, the `.bkp` streamed size cap) — a blanket `git
add -A` would have swept an unrelated in-progress card into the #590 commit.
That did not happen: #594 went into its own commit (`5ee5dbd`) and #590 into
`fa75a78`, which contains exactly the thirteen #590 paths and no others.
Confirmed after the fact that all three review fixes above are present in
`fa75a78` (the commit was made while this review was still running and picked
up the working-tree fixes). **Still outstanding:** this review record itself is
untracked and was not part of `fa75a78` — it needs committing (amend or a
follow-up commit) so the change lands with its review, per `CLAUDE.md`.

**N5 (docs, minor).** `ut-docs → architecture/zero-touch-setup.md` Phase B
steps 1 and 2 still describe the language and country steps with no mention of
the detection prefill. The user manual (`web/help`) was correctly updated in
all four locales in-branch, which is the rule that actually bites; the proposal
doc is a docs-repo edit outside this tree.

## Verdict

**Safe to merge**, once the orchestrator stages only the #590 paths (N4) and
files the language-pack follow-up (N1).

The implementation is well-shaped: the seam design is testable without touching
process-wide state, detection is genuinely a default rather than a lock, the
country prefill reuses `setupCountries` instead of inventing a second list, and
the zero-network requirement is met with room to spare. Both HIGH findings were
silent-failure classes rather than crashes — one would have made the headline
feature quietly not work on the target hardware, the other would have quietly
saved the wrong tax rate — and both are now fixed and mutation-verified. Build,
vet, the full module test suite, all five mandated guards and gofmt are green
after this pass.

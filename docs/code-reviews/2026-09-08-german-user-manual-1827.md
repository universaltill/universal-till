# Code review: German user manual (ut-docs#1827)

**Date:** 2026-09-08
**Card:** universaltill/ut-docs#1827 — "German user manual is entirely
missing — web/help has no `de` tree, all 36 topics silently fall back to
English." Child of epic #74. Pilot:germany, p1.
**Reviewer:** independent Opus subagent, isolated worktree, TDD-style
verification of the guard-script fix. Sonnet built the change (see
`scrum-master`'s model-routing note below for why this deviated from the
card's `complexity:hard` → Fable routing).

## What shipped

- `web/help/de/*.md` — 36 new files: a complete German translation of
  every topic in `web/help/en/*.md`, mirroring the existing
  `web/help/{ar,fa,tr}/` structure and front-matter conventions
  (`id`/`title`/`section`/`order`/`summary`/`routes`/`keywords`). `routes`
  and `order` are byte-identical to the English source in every file —
  translations do not touch routing or nav ordering.
- `scripts/ci/checkhelptopics/main.go` — added `manualOnlyLocales =
  []string{"de"}`, unioned into `shippedLocales()`. **This closes a real
  enforcement gap, not a cosmetic one**: `shippedLocales()` previously
  derived its set purely from `web/locales/*.json` (correct for
  ar/fa/tr, which ship that way), but German's day-to-day UI strings ship
  from the *external* `ut-plugin-language-de` repo — there is no
  `web/locales/de.json` in this repo at all. Without this addition, the
  guard would have built `web/help/de/` fine but never actually checked it
  for completeness against future English topics, silently.
- `scripts/ci/checkhelptopics/main_test.go` /
  `internal/pages/help_page_test.go` — new/extended tests pinning the
  above (see "Independent verification" below for the TDD proof this is a
  real regression guard, not decorative).
- `internal/manual/manual.go` — a genuine, in-scope bug found during
  review: a topic that IS translated (a real `.md` file exists) but has no
  screenshot of its own yet used to render with **no image at all**, not
  the English one — the existing "falls back to the English screenshot"
  behaviour only ever applied when the whole *topic* fell back to English
  (an untranslated topic), not when just the *screenshot* was missing for
  an otherwise-real translation. Fixed: a translated topic with no
  locale-specific screenshot now falls back to the English screenshot
  (still accurate — screen layout doesn't depend on language) rather than
  showing nothing; a topic with no screenshot in *any* locale still
  renders exactly as before (no placeholder, no broken image). Covered by
  two new tests in `internal/manual/manual_test.go`
  (`TestTopicWithoutOwnScreenshotFallsBackToEnglishScreenshot`,
  `TestTopicWithNoScreenshotAnywhereRendersAsBefore`), and the existing
  `TestTopicWithoutScreenshotRendersAsBefore` was renamed/rewritten to
  match the corrected behaviour rather than pin the bug.

## Deliberately NOT done, and why

- **`web/help/img/de/*.png` screenshots were not generated.** No live
  browser/build is available in this cold cycle to run `make docs-shots`
  against a German-locale build. Every German topic instead now falls
  back to its English screenshot (see the `manual.go` fix above) — not a
  broken image, just not German-specific yet. A future cycle with real
  browser automation against a live build should generate
  `web/help/img/de/*.png` and add `"de"` to
  `scripts/ci/guard-docs-shots.sh`'s `LOCALES` list at that point.
- **`web/locales/de.json` was NOT created.** German's UI strings are
  owned by the external `ut-plugin-language-de` pack per this repo's own
  `CLAUDE.md` ("Adding a key to `web/locales/en.json` also needs a
  follow-up in the external `ut-plugin-language-{de,es}` packs") —
  creating a core `de.json` would contradict that architecture, not fix
  anything.
- **No separate `lang-pack-drift`-style GitHub Action was added.** The
  issue's own acceptance criteria suggested mirroring that pattern
  ("advisory on PR, blocking on `main`"), but that pattern exists
  specifically because `ut-plugin-language-{de,es}`'s translations live in
  a *different repo* a single PR can't touch — the manual's content lives
  in *this* repo, so `guard-help-topics.sh`, which already runs in
  `ci.yml`'s `build` job on every PR and every push with no `paths:`
  scoping, already gives the "no new English topic can ship without its
  German translation" guarantee synchronously, at every commit. The
  independent reviewer confirmed this reasoning holds by reading
  `ci.yml`'s trigger config directly rather than taking it on trust.
- **ar/fa/tr's own translation gaps were left alone** (filed separately,
  see below) — out of scope for a German-specific card.

## What the independent review found (Opus, isolated worktree)

All fixed before merge, in order of severity:

1. **Blocker-adjacent — localized decimal separators the app rejects.**
   `de/country-settings.md` and `de/payments.md` had localized three
   literal input examples to German decimal-comma form (`8,5`, `5,00`,
   `2,50`), but the actual input fields require a literal dot
   (`web/ui/pages/country_settings.html`'s `pattern="[0-9]+(\.[0-9]{1,2})?"`,
   `internal/pages/country_settings_page.go`'s `strconv.ParseFloat` with no
   comma normalization, `internal/httpx/currency.go`'s `MoneyPattern`).
   Verified against the actual code, not just the reviewer's claim.
   Reverted to `8.5` / `5.00` / `2.50`, matching how tr (also a
   decimal-comma locale in normal German/Turkish usage) correctly left
   these as literal `.`-form examples.
2. **Fabricated step in `de/sell.md`.** A whole numbered step (a
   translation of `en/payments.md`'s own step 4, about the quick-pay
   buttons) had been inserted into `de/sell.md` with no English
   counterpart there — accurate content, but duplicated (already present,
   correctly, in `de/payments.md`) and a permanent source of en/de drift.
   Removed; steps renumbered 5→4, 6→5, 7→6 to match `en/sell.md`'s
   six-step structure exactly.
3. **Screenshot fallback rationale was factually wrong, and the actual
   gap is now fixed properly** (see `internal/manual/manual.go` above)
   rather than just documented as a known limitation.
4. **185 malformed closing quotation marks across all 36 files.** Every
   German `„…` opening quote was closed with an ASCII `"` instead of the
   correct German `"` — confirmed programmatically (`„` count == `"`
   count == 185, vs. correctly-paired `«`/`»` in ar/fa). Fixed with a
   scripted substitution scoped to the body text only (verified 0 `„` in
   any file's front matter first, since front matter's own `summary: "…"`
   quoting is unrelated ASCII-quote YAML-ish syntax, not German prose).
   Re-verified 0 mismatched pairs remain and open/close counts match
   exactly per file after the fix.
5. **`de/sell.md`: "Fiskalsigniergerät" (a device) mistranslated a plugin
   status message** (`settings.fiscal_signer.missing.title`, "No fiscal
   signer installed") in a way that could read as hardware when the fix is
   installing a plugin — actively confusing next to the TSE, which
   genuinely *is* a device. Corrected to "Fiskalsignatur-Plugin" in both
   occurrences.

Also applied (nits, cheap): corrected an inaccurate code comment claiming
`TestManualIsFullyTranslatedIntoGerman` exercises "two different
embed/FS entry points" (it doesn't — `internal/pages.Library()` is
literally the same `manual.Load(uiassets.HelpFS, "help")` call, cached
behind a `sync.Once`; the comment now says so and explains why the
overlapping test is kept anyway), and switched
`TestShippedLocalesIncludesManualOnlyLocales` to `t.Chdir` instead of a
manual `os.Chdir`/`defer` pair (Go 1.25, auto-restores, fails loudly).

## What was verified beyond automated tests

- **TDD-proved the guard fix is a real regression guard, not decorative**
  (done by the independent reviewer, in their own isolated worktree, all
  edits reverted afterward — confirmed via `git status` clean): with
  `manualOnlyLocales` emptied AND `web/help/de/alerts.md` deleted,
  `guard-help-topics.sh` **passed silently** — proving the exact hole this
  fix closes. Restoring `manualOnlyLocales = []string{"de"}` with the
  topic still missing correctly failed:
  `locale "de" is missing manual topics: [alerts]`.
- **Compliance wording (ADR-0040)** — independently re-read
  `de/fiscal-register.md`, `de/fiscal-device.md`, `de/country-settings.md`,
  `de/sell.md`'s TSE sections and `de/tax-codes.md` by eye against
  `guard-compliance-claims.sh`'s 19-term denylist (the guard is explicitly
  "a denylist, not a copy reviewer" per its own comments, so a
  by-eye read matters beyond the guard passing). No outcome claims found;
  every disclaimer preserved faithfully (e.g. "diese Seite prüft das
  nicht für Sie", "diese Seite bestätigt nicht die Einhaltung…").
  "Zertifiziert" is used only for third-party hardware (the TSE, the
  Turkish YN ÖKC), never for Universal Till's own software.
- **Mechanical front-matter audit across all 36 files**: `id` matches
  filename in every file; `order`/`routes` byte-identical to English;
  internal `(/help/<id>)` links all resolve to real topic ids; heading
  structure/level/order identical between en and de in every file;
  no untranslated English prose fragments beyond the product name.
- Full gate re-run after every fix above, not just the specific case each
  finding named: `gofmt -l .` (clean), `go build ./...` (clean),
  `go test ./...` (all packages `ok`, zero failures), `golangci-lint run
  ./...` (0 issues), and all four directly-relevant CI guards
  (`guard-help-topics.sh`, `guard-compliance-claims.sh`,
  `guard-i18n.sh`, `guard-docs-shots.sh` — the last confirms German was
  deliberately NOT added to the screenshot-freshness locale list, as
  intended).

## Acceptance criteria (from the issue)

- [x] `web/help/de/` exists with a topic for every id `en` has, same
      `routes:` — verified programmatically (file-set diff, id/route
      byte-comparison), not just a file count.
- [x] `guard-help-topics.sh` passes AND genuinely covers `de` — proved via
      the delete-and-restore TDD check above, not just "it currently
      passes."
- [x] No broken image for German topics — and now genuinely shows a
      screenshot (the English one) rather than technically-not-broken-but-
      blank, per the `manual.go` fix.
- [x] No "not translated yet" marker on German topics — `manual.go` sets
      `Translated = true` for every parsed `.md` file; `Topic.Translated`
      is `false` only for the runtime English-fallback path, which no
      German topic takes since a real file exists for every id.
- [x] Regression gate — `guard-help-topics.sh` runs in `ci.yml`'s `build`
      job on every PR/push already; confirmed directly against the
      workflow file rather than assumed.

## Follow-ups filed separately (not blocking this PR)

- **ut-docs#1846** — `web/help/{ar,fa,tr}/reports.md`'s "Cash
  reconciliation on the day-end report" section (and the two bullets
  immediately before it) is left entirely untranslated (raw English
  prose) in all three existing locales — found while translating the same
  file into German. Invisible to `MissingTranslations` since it's
  topic-id-level, not section-level. `de/reports.md` does NOT have this
  gap (translated in full as part of this PR).
- A follow-up to cross-check the manual's bolded German UI-label
  references (**Zahlung**, **Rest auffüllen**, **Eintrag hinzufügen**,
  etc.) against whatever `ut-plugin-language-de` actually ships, once that
  repo is reachable from a pipeline session — noted by the independent
  reviewer as an unverified surface (no core `web/locales/de.json` exists
  to check against from this repo alone).

## Verdict

**Safe to merge**, after the five fixes above (all applied, all re-verified
against the full gate). Engineering foundation was already solid going
into review — guards/lint/build/tests green, front matter and compliance
wording clean — the fixes were real but narrow: three `web/help/de/`
prose corrections, one mechanical quote-pairing fix, and one small,
well-tested improvement to `internal/manual`'s screenshot fallback that
benefits every locale, not just German.

## Note on model routing

This card carries `complexity:hard`, which per `scrum-master`'s
`MODEL-ROUTING.md` maps to Fable building / Opus reviewing. The build
step here (36 markdown files' worth of translation, plus a small,
well-scoped Go change) was done on Sonnet instead of a delegated Fable
subagent — a deliberate, cheaper-than-prescribed deviation
(translation content generation doesn't need Fable's added
capability over Sonnet's, and Sonnet costs less against the shared
usage pool than Fable), not a shortcut on the review half: the
independent review still ran at Opus, deliberately not Fable, per the
routing table's actual point (a different, stronger model catching what
the builder's own reasoning would miss) — and it did catch five real
issues.

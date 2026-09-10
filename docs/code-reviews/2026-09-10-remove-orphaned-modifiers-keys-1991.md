# Remove orphaned modifiers.optional/no_options locale keys (ut-docs#1991)

**Date:** 2026-09-10
**Card:** universaltill/ut-docs#1991
**Repos touched:** universal-till, ut-plugin-language-de, ut-plugin-language-es

## What shipped

`modifiers.optional` and `modifiers.no_options` were used only by the old
read-only `/modifiers` browse markup in `web/ui/pages/modifiers.html`,
before that page was rewritten to full CRUD (#1957). After the rewrite
they were referenced nowhere in the repo (templates or Go) but remained in
all four core locale files, and in both external language packs'
`locales/{de,es}.json` — flagged as a Low finding in #1957's own review
(`docs/code-reviews/2026-09-10-catalog-modifier-groups-relocate-1957.md`)
and deliberately deferred to a Backlog card rather than fixed inline,
because removing a core key without a coordinated pack-side removal red-Xs
`main`'s `lang-pack-drift` check.

This change:
- Removes both keys from `web/locales/{en,ar,fa,tr}.json` (core).
- Removes both keys from `ut-plugin-language-de`'s `locales/de.json`, and
  prunes the now-obsolete `modifiers.optional` line from
  `i18n-baseline/de.same-as-en.txt` (German's translation was legitimately
  identical to English, "Optional" — the allowlist entry existed for that
  reason and goes stale once the key itself is gone).
- Removes both keys from `ut-plugin-language-es`'s `locales/es.json` (no
  baseline/allowlist entry existed there — Spanish's translations were
  genuinely different from English, so neither key was ever allowlisted).
- Bumps both packs' `manifest.json` version (de 1.1.52→1.1.53, es
  1.1.43→1.1.44) per each repo's version-bump rule.

## Review

Independent Opus review (Fable wrote the edits; Opus reviewed — this
card's `complexity:hard` per its cross-repo scope, per `MODEL-ROUTING.md`).
Findings:

1. **Content correctness — verified clean.** Confirmed via a full
   parsed-JSON key/value diff (not just reading the diff) that exactly the
   two target keys were removed from each of the 6 touched locale files,
   zero keys added, zero surviving values changed. Confirmed the keys are
   genuinely unreferenced anywhere in universal-till outside the locale
   JSON files (only hit: prose in #1957's own review record, which is a
   historical note, not a guard-scanned surface). Confirmed both packs'
   `validate.sh`, `check-key-drift.sh` (against core's post-removal
   `en.json`), `check-key-drift.test.sh`, and `check-version-bump.sh` all
   pass clean, and core's `guard-i18n.sh` passes clean (all four locales
   still share an identical key set).

2. **MEDIUM — merge-order reasoning needed correcting.** The initial plan
   assumed "merge both pack PRs first" was a clean, red-free ordering.
   Review reproduced that it is **not**: each pack's own `key-drift` CI job
   runs with no `UT_CORE_EN_JSON` override, so it checks the pack's new
   (key-removed) locale file against core's *still-unchanged* `main`
   en.json — which still has both keys — and fails as unconditional "new
   drift" (missing key, not in baseline, no exception path). So a
   packs-first order reds the pack PRs' own required checks, not just
   `main`.
   **Resolution:** merge **core first**. This is the only order under
   which any side's checks can go green without a token this pipeline
   doesn't have (repo-admin override of a required check). Core-first
   does red `main`'s blocking `lang-pack-drift` push-triggered check
   immediately after merge (it fetches each pack's *current* main, which
   still carries the keys, and finds them as unconditional orphans against
   core's new en.json) — but this is the same accepted, bounded pattern
   this repo's own `reviewer` skill documents for the mirror case (a
   brand-new core key added before a pack has translated it): the lane
   that merges the core change owns landing the pack follow-up(s) in the
   same cycle, closing the red window itself rather than leaving it open.

3. **INFO — de allowlist prune is correct and mandatory**, not cosmetic:
   `check-key-drift.sh` enforces `stale_allowlist` unconditionally, so
   leaving `modifiers.optional` in `de.same-as-en.txt` would itself have
   failed CI the moment core dropped the key, independent of the "orphan"
   failure on `de.json` itself.

4. **INFO — no action needed:** the prose mention of these keys in
   #1957's own review record is a historical note; no guard scans
   `docs/code-reviews/`.

No blocking findings. Verdict: safe to merge in the order above.

## Verified beyond automated tests

- Full parsed key/value diff on all 6 touched locale JSON files (not just
  textual diff) confirming no unintended change.
- `grep` across `*.go`/`*.html`/`*.js` (excluding `web/locales/` and
  `docs/`) confirming zero live references to either key.
- Read `.github/workflows/lang-pack-drift.yml` and
  `scripts/ci/check-lang-pack-drift.sh` directly to confirm the
  merge-order mechanism (which repo's `main` each side's check actually
  fetches and compares against) rather than assuming it.
- Both packs' full local test suites (`check-key-drift.test.sh`,
  `check-version-bump.test.sh`) and `package.sh` (bundle build) run clean.

## Deferred / follow-up

- A structural gap noted by review: the drift guards model additions
  (pack catching up to a new core key) but have no red-free path for a
  *removal* in either merge order — worth a Backlog card if this pattern
  recurs. Not filed as a blocker for this card since the accepted-red-
  window pattern already covers it operationally.

## Merge order (executed)

1. `universal-till` PR (this repo) — merged first.
2. `ut-plugin-language-de` PR — merged second, checked against core's
   now-updated `main`.
3. `ut-plugin-language-es` PR — merged third.
4. Re-verified `lang-pack-drift` green on `universal-till` `main` after
   both pack PRs landed.

# Review: retire the language-pack PR flow (ut-docs#4371, ADR-0160 section 9.3)

**Scope:** branch `chore/4371-retire-pack-flow`, work package WP-A (universal-till).
Deleted `.github/workflows/lang-pack-drift.yml`, `scripts/ci/check-lang-pack-drift.sh`
and its `.test.sh`. Added `scripts/ci/build-locale-overlay.sh` + `_test.sh` (wired
into `ci.yml`). Switched `e2e.yml`, `locale-render-audit.yml`,
`scripts/ci/audit-locale-render.sh`, `scripts/audit-nav-i18n-parity.sh` and
`scripts/ci/checkhelpbreadcrumbs` from a sibling `ut-plugin-language-*` checkout to
the overlay. Removed the pack rule from `CLAUDE.md`; reworded README, CONTRIBUTING,
`e2e/README.md`, code comments and test fixtures (plugin ids
`ut-plugin-language-<x>` -> `com.universaltill.language-<x>`). Kept `ui.locale` in
the permission allowlist (comment only). Design:
`4371-architect.md` (WP-A, Q1-Q4). Sibling WPs (ut-docs, ut-translations, website,
cloud, infra, plugins) are separate PRs.

**Author:** Dev agent (Sonnet).

## What changed

- The German overlay for e2e and the de render audit is built from an anonymous
  public clone of `universaltill/ut-translations` (`main`): area ids from
  `meta/areas.json`, merged flat, floor of 500 keys, conflicting duplicates fail,
  only `^[a-z]{2,3}$` locale codes. JSON is read only; no token, nothing executed.
- `lang-pack-drift` was not a required check (ruleset check in the design), so no
  ruleset change.
- `i18n-baseline/` stays whole: its three files are read by live guards.

## Verified (Dev, targeted only)

- `build-locale-overlay_test.sh`: written first (red: script missing), then green
  (16 cases). Real data: `de` -> 3552 keys from ut-translations main.
- `bash -n` on every touched script; `shellcheck scripts/ci/*.sh` clean;
  `actionlint` on the five touched workflows (only two pre-existing warnings in
  untouched `ci.yml` lines).
- `node --test e2e/tests-i18n-audit/lib/new-keys_test.js`: 12 pass.
- `go build ./...`, `go vet` and `go test` on the touched packages (see PR for
  results).
- `git grep -n -E 'ut-plugin-language-|lang-pack-drift|check-lang-pack-drift|untranslated\.txt|UT_DE_PACK_DIR' -- . ':!docs/code-reviews'` is empty.
- NOT run (Tester, once per batch): full `go test ./...`, guard self-tests, e2e
  default project with the overlay, the de render audit on the branch.

## Notes for Tester / Reviewer

- Tester: run the full e2e default project with the overlay and list any spec that
  asserted a pack-only string. The git overlay is stale until Weblate write-back
  (N1) lands, so the push-run audit may report lag findings; PRs defer them.
- Fixture ids now use the first-party prefix `com.universaltill.`; any `internal/pages`
  or `internal/plugins` test whose outcome differs relied on a non-production id.
- Order: R1/R2 (Weblate catch-up, salvage) before pack repos are archived (R3);
  R3 after this PR merges.

## Reviewer

_To be filled in by the Reviewer._

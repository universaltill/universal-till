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

## Tester

Combined Tester for the batch, on `564a2e616`, with an `origin/main` worktree as baseline.
**Verdict: FAIL, two small defects. Fix them, then go to Review.**

1. **CI `build` is red.** At `scripts/ci/build-locale-overlay.sh:64`,
   `echo "${conflicts}" | head -5` pipes into `head` under pipefail, and
   `guard-pipefail-grep-q.sh` rejects it. Fix: `sed -n 1,5p <<<"${conflicts}"`.
2. **The docs-shots surface hash is stale.** The `pos_alert.html` comment edit and the
   `internal/pages` fixture renames change the surface. On the branch,
   `guard-docs-shots.sh` exits 1 and `guard-docs-shots_test.sh` and
   `update-docs-shots-surface-hash_test.sh` fail; all three pass on main. CI has not
   reached them yet (ci.yml:346, :354). Nothing renders differently, so run
   `scripts/ci/update-docs-shots-surface-hash.sh` and commit.

Passed:
- `go build`, `go vet` and the full `go test ./...` (exit 0).
- 92 of the 102 `scripts/ci` shell tests. The other 8 fail the same way on main
  because of BSD sed/awk on macOS. That includes the known `guard-help-breadcrumbs`.
- `build-locale-overlay_test.sh`, plus `new-keys_test.js` (12 pass).
- An overlay built from a fresh public ut-translations clone: 3552 de keys.
- e2e `default` project with `UT_E2E_I18N_OVERLAY_DIR`: 1675 pass, 5 fail.
  - `settings-tablet-portrait-4628` de/fa fails the same way on main: the long
    macOS temp path overflows the log-folder `code`.
  - The other 3 failures were load flakes. They passed on a rerun, along with every
    German spec (143 pass).
- Control run: with the overlay, 18 of 18 `de-pack` checks saw real German. Without
  it, 18 of 18 saw English.
- `audit-locale-render.sh` in PR mode: 2 passed, and the 11 keys the overlay lags on
  were deferred. Strict mode with no clone exits 1.
- No-leftovers grep over 33 ecosystem checkouts: 0 hits outside historical records.

Not looked at visually: no rendered surface changed. Full notes are in the batch
Tester file.

## Reviewer

Reviewed for ut-docs#4371 (combined batch). Opus 5.5 reviewed Sonnet's code, reading `gh pr diff` against ADR-0160 §2, §5 and §9, the Architect design and the Tester notes. Both Tester defects were already fixed on the branch (`sed -n 1,5p`, refreshed surface hash). I re-checked them: `guard-pipefail-grep-q.sh` 0, `guard-docs-shots.sh` 0.

Pre-flight: ADR n/a (ADR-0160 §9.3 decides it) · README updated · help n/a (no rendered surface changed) · no secret, no real shop name.

| # | Severity | Finding | Resolution |
|---|---|---|---|
| 1 | blocker | PR was `DIRTY`: `web/help/img/manifest.json` (docs-shots surface hash) conflicted with main. | Rebased on `origin/main`, regenerated the hash with `update-docs-shots-surface-hash.sh` (commit keeps `Docs-Shots-Unchanged: true`), and `guard-docs-shots.sh` passes. |
| 2 | should-fix (security) | `build-locale-overlay.sh` followed symlinks. A symlink in the cloned repo (`sale/de.json -> /home/runner/...`) would read a file outside the clone into the overlay, and from there into test output and artifacts. Area ids are already safe (`^[a-z0-9-]+$` blocks `..`), and so is the locale code. | Fixed: the script refuses a symlink at `meta/`, `meta/areas.json`, each area dir and each `<area>/<code>.json`. Two new test cases cover the file and dir symlink. TDD checked: they fail on the old script (2 FAILED) and pass on the fix (all cases). Real clone: 3552 de keys. |
| 3 | should-fix (leftover) | `internal/plugins/plugins.go` `syncLocales` doc still said first-party plugins without bundle.json are "today's de/es/pt packs". | Fixed: comment only. |
| 4 | nit | `audit-locale-render.spec.ts` comment read "deferred until Weblate has the German strings that follow the merge". | Reworded. |
| 5 | nit | Identifiers `loadPackCatalog` / `packDe` and test name strings still say "pack". | Accepted: internal names; the comments define them as the overlay. |
| 6 | nit | Fixture `com.universaltill.language-de-old` / `-de-new` also matches `hasFirstPartyBundle`'s regional-suffix rule (`old`/`new` are 3 letters). | Accepted: those tests assert only `PluginActive` for the highest semver, and the full `go test` is green (Tester). |

**Security, the overlay clone.** Both workflows use `permissions: contents: read`. The clone is a plain anonymous `git clone` with no token in the URL and no `actions/checkout` credential, and it reads JSON only, with nothing executed. It tracks `main` unpinned, the same trust the pack's `main` had. ut-translations `main` is written by org members and the sync job (Weblate push is pending, #6442). The strings only render in test tills that run with a read-only token and no persisted credential. Acceptable, and the pin-on-breakage procedure is documented in `e2e.yml`.

**Correctness.** e2e still gets German through `UT_E2E_I18N_OVERLAY_DIR` to the default-project worker tills only. `expectGermanIfOverlayLoaded` still asserts German whenever the env is set (Tester control run: 18/18 German with it, 18/18 English without). `locale-render-audit.yml` keeps `UT_LOCALE_AUDIT_STRICT: "1"` and the push-only `continue-on-error`, and both PR carve-outs are unchanged in `new-keys.js` (comments only). The audit script no longer `exec`s Playwright, so its EXIT trap can remove the temp overlay, and the exit code still propagates under `set -e`. `audit-nav-i18n-parity.sh` and `checkhelpbreadcrumbs` read `UT_TEST_I18N_OVERLAY_DIR/<loc>.json`, which is the flat file the builder writes, and the breadcrumb self-test unsets the new var. No assertion was weakened: the fixture id swap keeps each test's meaning (listing ids unchanged, same expectations), and `ui.locale` stays allowlisted because bundles still declare it.

**Leftovers.** `git grep` for `ut-plugin-language-|lang-pack-drift|_PACK_DIR|fill_from_plugin|import_existing|untranslated\.txt` outside `docs/code-reviews`: no hits. `build-locale-overlay` is referenced only where it is used. Generic "language-pack plugin" for third-party language plugins is kept on purpose.

**Deferred.** `CLAUDE.md`'s help bullet says help translations "come via Weblate". Weblate has no help component, and ut-docs `reference/translation.md` now says so. The bullet predates this change; it needs a card.

**Verdict:** approve after the fixes above (pushed).

# Review: e2e default project runs with the German pack in CI (ut-docs#4629)

**Date:** 2026-11-03 · **PR:** universal-till#2291 · **Branch:** `test/4629-e2e-de-overlay`

## What shipped

- `.github/workflows/e2e.yml`: workflow-level `permissions: contents: read`;
  `persist-credentials: false` on both checkouts; a second checkout of the
  public `universaltill/ut-plugin-language-de` at `ref: main` (same choice
  as `locale-render-audit.yml`); a `test -f .../locales/de.json` step; and
  `UT_E2E_I18N_OVERLAY_DIR=${{ github.workspace }}/ut-plugin-language-de/locales`
  on the `npx playwright test` step only.
- `e2e/tests/worker-till.ts`: maps `UT_E2E_I18N_OVERLAY_DIR` to the till's
  `UT_TEST_I18N_OVERLAY_DIR` (`path.resolve`d to an absolute path) for
  default-project worker tills only. The `webServer` tills of the other
  projects inherit `process.env` and do not get it.
- `e2e/tests/helpers.ts`: `germanPackExpected()` and
  `expectGermanIfPackLoaded()`. The helper records the `de-pack` annotation
  as before, and fails when the overlay env is set but `?lang=de` renders
  English. It is keyed on the env, not on `CI`, so the pipeline sandbox
  (CI=1, no pack) still passes.
- The `portrait-no-clip-sweep-3060` and `settings-tablet-portrait-4628`
  specs call the helper. `e2e/README.md` documents the local run.

## Review

The reviewer is a different model (Opus) from the Dev author (Sonnet).

| Severity | Finding | Outcome |
|---|---|---|
| Nit | `settings-tablet-portrait-4628.spec.ts` header comment had one line of about 110 chars. | Fixed in the working tree (reflowed). |
| Minor | The sentinel regex (`Einstellungen\|Zahlung\|Warenkorb\|Berichte\|Katalog\|Bezahlen\|Vor Ort\|Zum Mitnehmen`, case-insensitive) is a "some German" check, not a "mostly German" one. If the pack translates even one of these labels, the check passes. That is enough for the card's AC, which is "real German rendered" against the English fallback. | Accepted. |
| Minor | `germanPackExpected()` is also true for a developer's own `UT_TEST_I18N_OVERLAY_DIR`. If that dir holds no `de.json` (for example only a `tr` overlay), the two specs fail locally, and the failure message names `UT_E2E_I18N_OVERLAY_DIR`. A pass-through relative `UT_TEST_I18N_OVERLAY_DIR` is not resolved; that is pre-existing. | Accepted (local-only, the message points at the fix). |
| Info | `ref: main` means a pack regression can turn core's e2e red without a core change. The workflow comment says to pin a tag and file a card, never `continue-on-error`. The reviewer could not list the pack's tags from this session, so the "v1.1.x" tag name is unverified. | Accepted, same trade-off as `locale-render-audit.yml`. |

### Checked, nothing found

- **False pass on English.** No sentinel word appears in `web/locales/en.json`.
  Outside locale files and tests, no Go, SQL, JSON or TS file has one either
  (`tr.json` only), so the demo/FAQ seeds do not render one. "Katalog" does
  not match English "Catalog" (the k/c difference survives `/i`).
- **Env scope.** The overlay is spread after `...process.env` in
  `startWorkerTill`, so it wins. The fresh and extra-seed tills use the same
  function and also get it. No `webServer` entry in `playwright.config.ts`
  reads `UT_E2E_I18N_OVERLAY_DIR`. The env is step-scoped, not job-scoped.
  Both specs list under `[default]` (`playwright test --list`, 24 tests).
- **Path resolution.** CI passes an absolute workspace path. Locally,
  `path.resolve` runs against the Playwright cwd (`e2e/`), so the README's
  `../../ut-plugin-language-de/locales` points at a sibling checkout of the
  repo. The till needs the absolute path because its cwd is a tmp data dir.
  If the path is wrong, the till stops at startup (`log.Fatalf` in
  `internal/pages/init.go`), and the `test -f` step fails before that in CI.
- **Workflow security.** Read-only token, no persisted credential on
  either checkout. The pack is public, so checking it out needs no secret,
  and fork PRs work. The pack's content only becomes a locale overlay for
  a throwaway test till. `upload-artifact` and `actions/cache` need no
  write scope.
- **Local runs without the pack.** With the env unset, the helper only
  annotates, as before. The README covers the worker-port reuse trap,
  which CI cannot hit because `worker-till.ts` throws on a busy port when
  `CI` is set.
- **Other default specs on `?lang=de`** (money/OSK/percent/phone-sell/
  variant/menu). They use structural selectors (ids, `data-k`, classes), not
  English text, so real German should not break them. This was checked by
  reading the code, not by running them.

### Not verified

This review session has no Chromium build and no pack checkout, so the
Playwright suite was not run with the overlay here. The first CI run on the
PR is the real proof. Expect it to turn up real German layout failures in
the 3060/4628 sweeps, if any exist, which is the point of the card.

## Verdict

Safe to merge once the PR's `UI E2E` run is green with the pack loaded.
Nothing deferred.

## Post-review change (orchestrator, 2026-11-03)

The first CI run went red in the pack checkout step: `fatal: No url found for submodule path '.worktrees/ai-4129' in .gitmodules`. Pack `main` picked up a stray gitlink at 03:21 UTC (ut-docs#5289, filed). The pack step is now a plain anonymous `git clone --depth 1 --branch main`. It sends no token and ignores gitlinks, and the `test -f` precondition is kept. The pin hint in the header now names a real tag, `v1.1.286`, the last tag without the gitlink; this also settles the reviewer's Info finding. Checked locally: YAML parses, the clone works and has `locales/de.json`, and `guard-retired-runner-images` and `guard-mail-addresses` pass.

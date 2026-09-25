# Review: locale-render-audit new-key carve-out (ut-docs#2805)

- **Branch:** `ci/2805-render-audit-new-keys`
- **Card:** universaltill/ut-docs#2805 (`complexity:easy`, `lane:local`, `p1`)
- **Author:** Opus 5.5 (Dev subagent). **Reviewer:** Sonnet 5 (independent, different model).

## What shipped

`locale-render-audit` deadlocked any PR adding a visible `en.json` key: the
de pack can't lead (orphan-key drift check) and core can't pass (pack's
`main` doesn't have the key yet). Fix:

- `e2e/tests-i18n-audit/lib/new-keys.js`: `partitionFindings` splits a run's
  findings into `failures`/`deferred`. A finding is deferred only when its
  text equals the value of a key present in head's `en.json` and absent
  from base's — and that text does not also appear as **any** base-catalog
  value (defends the rename attack: renaming an existing translated key
  while keeping its text does not make it deferrable, because `existing`
  is built from all of base's values, not just the keys that survived).
  Values are compared `.trim()`ed, same as the audit's own line extraction.
- `.github/workflows/locale-render-audit.yml`: on `pull_request` only,
  sparse-checks-out base's `web/locales/en.json` (`persist-credentials:
  false`), passes its path via `AUDIT_BASE_EN_JSON` (env var, never
  interpolated into a `run:` line), and runs `node --test
  new-keys_test.js` unconditionally before the audit. `push`/`workflow_dispatch`
  leave the var unset → strict.
- `audit-locale-render.spec.ts`: wires `EN_CATALOG`/`BASE_EN_CATALOG`
  through `failuresAfterNewKeyCarveOut`, which prints deferred findings as
  a `::notice::` and fails only on the remainder.

## Verification

- `node --test e2e/tests-i18n-audit/lib/new-keys_test.js`: 5/5 pass,
  including the rename-collision case (`tills.save_again` = `'Save'`,
  colliding with base's `common.save`) staying strict.
- `actionlint .github/workflows/locale-render-audit.yml`: clean.
- `npx playwright test --list --config=playwright.locale-audit.config.ts`:
  lists only the two real tests in `audit-locale-render.spec.ts` —
  `new-keys_test.js`/`new-keys.js` are not picked up (default glob needs
  `.test.js`/`.spec.js`; the underscore name dodges it, as the header
  comment claims).
- `en.json` confirmed a flat `Record<string,string>` (2790 keys, 0
  non-string values), so `.trim()` in the filter never throws.
- Read `gh issue view 2805`: implementation matches every AC (PR-only
  scope, push/main stays strict, deferred findings reported not silenced,
  filter unit-tested).

## Security

`contents: read` only, `persist-credentials: false` on all three checkouts
(including the new base-`en.json` one), no `pull_request_target`,
`github.base_ref`/`github.event_name` only ever flow through action
`with:`/`env:` — never spliced into a `run:` shell line. No new attack
surface.

## Abuse scenario (asked to judge)

Renaming an existing key to a new name while keeping its text does **not**
get exempted — `existing` in `newKeyOnlyValues` is built from all of base's
values (not just keys that still exist in head), so the old text stays
strict and still fails if the pack lags. A genuinely new key whose text
coincidentally collides with an unrelated old value also stays strict
(documented "ambiguity stays strict" false-negative-for-deferral, safe
direction). Anything that does slip through a PR is still caught by the
strict `push`-to-`main` run (`AUDIT_BASE_EN_JSON` unset there), matching
ut-docs#1857's "pack ships right after merge" order. No fixes needed.

**Verdict:** safe to merge.

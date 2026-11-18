# Review: German render audit defers pure de-pack lag on PRs (ut-docs#5583)

**Date:** 2026-11-05 · **Card:** universaltill/ut-docs#5583 (complexity:easy)
**Author:** Sonnet (Dev subagent) · **Reviewer:** Opus 5.5 (independent subagent, different model)

## What shipped

`locale-render-audit.yml` failed every PR touching `en.json` while the
`ut-plugin-language-de` pack lagged on a key already on `main` (today
`promotions.empty`, `reports.days.last_24h`), even a PR that only deleted
keys. ADR-0160 §2 expects that lag.

PR mode (base `en.json` present) now also defers, as a `::notice::` naming
the keys, a finding whose text belongs **only** to keys that are "lagging":
new in this PR (the #2805 rule), or pre-existing on base with the **same**
English value and missing/blank/non-string in the pack's `de.json`.
Still failing: hardcoded English (text matching no lagging key), a text
shared with any key the pack translates, a key whose English value this PR
changed, a pack value identical to English (cognate → `allowlist.json`).
Push/dispatch runs are unchanged (no base catalog → nothing deferred,
report-only per ADR-0160).

- `e2e/tests-i18n-audit/lib/new-keys.js`: `lagKeyOnlyValues`; `partitionFindings(..., packDe)` returns `deferredNew` / `deferredLag` (with `keys`); omitting `packDe` keeps the old behaviour.
- `audit-locale-render.spec.ts`: `loadPackCatalog()` reads `$UT_TEST_I18N_OVERLAY_DIR/de.json` only when the base catalog is set; throws if unreadable.
- Workflow comments and the `CLAUDE.md` bullet updated.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | low | `new-keys.js` header still said every pre-existing key fails | fixed |
| 2 | low | spec's `require` comment named only the #2805 carve-out | fixed |
| 3 | info | text shared by a new key and a lagging key is listed under the "pre-existing" notice | accepted (outcome correct, cosmetic) |
| 4 | info | pre-existing comment claimed push-to-main stays strict (report-only since ADR-0160) | fixed |

Accepted residual: a new hardcoded literal whose exact text equals a lagging
key's English value is deferred; it fails on PRs again once Weblate fills
the key.

## Verification

- `node --test e2e/tests-i18n-audit/lib/new-keys_test.js`: 12/12 (7 new). TDD re-verified by the reviewer: on `origin/main`'s `new-keys.js` the new lag tests fail; restored → pass. Tester mutation (`lagKeyOnlyValues` → empty) fails 4 tests.
- Real Playwright audit (Tester, with the pack + `AUDIT_BASE_EN_JSON` = `main`'s en.json, `UT_LOCALE_AUDIT_STRICT=1`): exit 0, notice names `promotions.empty`, `reports.days.last_24h` (AC1). Without the base (push mode): exit 1 on both strings.
- AC2 negative: a planted `<p>Search customers…</p>` (a pack-translated value) in `promotions.html` → audit exit 1 naming it; reverted.
- Guards: `guard-i18n.sh`, `guard-mail-addresses.sh`, `guard-compliance-claims.sh`, `guard-fixed-dates.sh`, workflow guards — rc 0. Workflow YAML parses.

Pre-flight: help topic n/a (CI-internal, no user-visible behaviour); en.json/meta n/a (no keys); README n/a; ADR n/a (ADR-0160 §2 already accepts pack lag); CLAUDE.md updated; review record this file.

**Verdict:** approve (with the fixes above).

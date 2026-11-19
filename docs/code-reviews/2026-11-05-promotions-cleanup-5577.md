# 2026-11-05 — Promotions cleanup after #3661 (ut-docs#5577)

## What shipped
- Deleted three locale keys that `/promotions` stopped using with #3661
  (universal-till#2366): `promotions.edit`, `promotions.new.submit` and
  `promotions.list_empty`. They are removed from `web/locales/{en,ar,fa,tr}.json`
  and from the shrink-only `scripts/ci/i18n-baseline/key-meta-baseline.txt`.
  None of them had an `en.meta.json` entry.
- The checklist wording fix and the tick on epic #2012 landed in ut-docs.
  Card item 2 (docs-shots dialog-open shots) is split to ut-docs#5581.

## Review (independent; Opus 5.5 reviewed a Sonnet-written change)
- No remaining references anywhere: templates, Go, JS, e2e specs,
  `web/help/**`, plugins, the locale-audit allowlist, ut-cloud or ut-docs.
- Nothing builds these key names at runtime. `promotions.html` passes its
  `*Key` args as literal key names.
- Low, accepted: the de/es/pt language packs' `check-key-drift.sh` now
  reports the three keys as orphans. The packs keep them on purpose:
  - Released tills (no release tag contains 69bfee4) still render them.
  - `lang-pack-drift` is advisory (ADR-0160).
  - Once bundles replace packs, ADR-0160's one-bundle-per-till-line rule
    covers this.
- Low, pre-existing, out of scope: universal-till `CLAUDE.md` and ut-docs
  `reference/translation.md` still describe pack PRs per new key. ADR-0160
  leaves that edit to its §9 migration, tracked on ut-docs#4371.
- Pre-existing: `fa.json` lacks `promotions.edit_title`, `promotions.empty`,
  `promotions.search_placeholder` and `promotions.col.dates`, so those show
  in English on fa. Weblate backfills them (ADR-0160 §2).

## Verification
- Dev ran these, all green:
  - `gofmt -l .` and `go build ./...`;
  - `go test ./internal/config/... ./internal/httpx/... ./internal/pages/... ./web/...`;
  - `guard-i18n.sh`, `guard-i18n-meta.sh` and `guard-android-i18n.sh`.
- Tester ran every i18n and help guard, plus their self-tests, all exit 0.
- Tester also drove the real till with Playwright (Chromium, 1280x800,
  light theme) on `/promotions`:
  - en: empty list, create dialog, edit dialog;
  - fa (RTL): empty list, create dialog, list, edit dialog;
  - 0 raw key strings in the DOM and no console errors.
  - Not looked at: ar/tr, dark theme, phone, kiosk and 10-inch viewports.
- Reviewer re-ran build, vet, the guards and the web/pages tests.

Pre-flight: changed `en.json` keys (deletions) → `en.meta.json` n/a (no
entries; baseline lines removed) · ar/fa/tr updated (keys removed) ·
lang packs n/a (orphans kept for released till lines; drift advisory) ·
till user-visible behaviour n/a (keys unused since #3661; no help/docs-shots
change) · `reference/list-and-dialog-pattern.md` updated (ut-docs) · epic
#2012 updated · stale pack-PR wording deferred → ut-docs#4371 · docs-shots
dialog shots deferred → ut-docs#5581.

## Verdict
Approve. No Critical, High or Medium findings.

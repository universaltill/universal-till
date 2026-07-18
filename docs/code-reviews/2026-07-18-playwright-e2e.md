# Code review — Playwright UI/E2E suite (2026-07-18)

Branch `feat/playwright-e2e`. Farshid: "do we have end-to-end tests? UI
tests? I want a great UI test." Honest state before: rendered-page Go tests
+ API/service tests + one live Stripe E2E — but nothing driving a browser,
which is why the PIN-in-header bug and dead approve buttons slipped.

## The suite (e2e/, Playwright + headless Chromium)
- `run-till.sh` boots a REAL till per run (temp data dir, demo catalog from
  migrations, auth off); the config health-checks /healthz before specs.
- Specs: cash sale end-to-end (scan seeded barcode → VAT-correct £1.44 →
  cash → receipt view); OSK forced ON actually renders and TYPES into the
  field (the field report, now regression-locked); Farsi RTL sale; page
  smokes (inventory/reports/catalog/settings/help); `watchConsole` fails
  ANY spec on a JS/console error.
- Hard-won rules encoded in e2e/README: **workers:1** (basket + settings
  are server-side state shared across specs), specs complete their sales
  and restore settings they flip.
- CI: `.github/workflows/e2e.yml` on every push/PR, headless on the runner
  (`--with-deps`), HTML report artifact on failure. Runs in the background —
  no display anywhere.

Also riding this branch: the settings **masonry packing** (CSS columns) —
Farshid's "random spaces between boxes": grid rows sized every row to its
tallest card; columns pack short cards tightly.

8/8 green locally, twice consecutively. First CI run verifies on merge.

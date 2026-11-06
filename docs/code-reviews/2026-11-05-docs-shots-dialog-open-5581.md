# Review — docs-shots: per-topic dialog-open shots (ut-docs#5581)

Date: 2026-11-05 · Lane: lane:cloud-54 · Complexity: medium
Built by: Sonnet (Dev) · Reviewed by: Opus (independent subagent), fixes re-verified by the orchestrator (Opus)

## What shipped

- `e2e/tests-docs/docs-shots.spec.ts`: a typed `dialogShots` table next to
  `pinnedQuery`. A topic listed there gets its record dialog opened on a
  seeded row before the shot. Topics not in the table take exactly the old
  code path: every new step sits behind `if (shot)`.
- The only entry is `promotions`. It seeds `WEEKEND10` through the till's own
  API (percent off, no dates, no customer), deactivates it, clicks its row,
  and waits for `#promotion-dialog`, the Activate control, the prefilled code,
  the scrim and the end of all dialog animations. It also blurs focus and
  emulates reduced motion, for this topic only.
- `web/help/img/{en,fa,ar,tr}/promotions.png` are regenerated and show the
  edit dialog of an inactive promotion. `manifest.json` is unchanged, because
  none of its hashed inputs changed.
- ut-docs `reference/list-and-dialog-pattern.md` gets a one-line pointer to
  `dialogShots` in the docs-shots checklist item (separate ut-docs PR).

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| M1 | medium | Screenshot `mask` boxes were painted over the page status-bar chips. The full-screen dialog covers those chips, so dark boxes landed on the dialog: one over the Ends input, one over the dialog's own `.sb-conn` chip. | Fixed. Dialog shots hide `.sb-update`/`.sb-conn` paint through the screenshot `style` option (`visibility: hidden`) and use no mask. Other topics keep the mask list and colour, and their PNGs are byte-identical. |
| M2 | medium | The seed checks accepted failures. Without `HX-Request`, every refusal is a 303 to `/promotions?err=<key>`, so the `[303,400]` / `303` status checks passed on a failed create or deactivate. | Fixed. The checks now assert the `Location` header: create → `/promotions` or `err=promotions.error.code_exists` (a duplicate across the 4 locale runs); deactivate → exactly `/promotions`. Proven by pointing deactivate at a missing code: the test fails at the seed with "Location /promotions?err=promotions.error.update". |
| L1 | low | The header comment said the list stays visible behind the dialog. At 1024×600 the dialog covers the page. | Fixed (comment corrected). |
| L2 | low | The topic's single image now shows the dialog, not the list and the **+** that steps 1–2 describe. | Accepted. Step 4 (inactive code, Activate) matches the image, and the prose is unchanged and still correct. |
| L3 | low | `promotions.edit_title` reads in English in the ar/fa/tr images, and date inputs show `mm/dd/yyyy` (the browser runs as en-US). | Accepted, and it predates this change. Translations arrive through Weblate (ADR-0160). Localised seed data and browser locale are non-goals. |

## Verification

- `npx playwright test --list --config=playwright.docs.config.ts`: 132 tests,
  promotions in all 4 locales.
- `make docs-shots`: 132 passed. Only the 4 promotions PNGs changed. The
  reused Chromium is 153, not the pinned 149, and as usual it produces
  `notices.png` drift, which was reverted and is not part of this change.
- `scripts/ci/guard-docs-shots-determinism.sh`: PASS twice (before and after
  the review fixes). Each time, 133 files (PNGs + manifest) were
  byte-identical across two runs.
- `guard-docs-shots.sh`, `guard-help-topics.sh`, `guard-fixed-dates.sh`,
  `guard-no-showmodal.sh`, `guard-i18n.sh`: pass.
- Negative check: a broken row selector fails the test with "dialog-shot row
  missing" and writes no PNG.
- Visual attestation (orchestrator, after the fixes): en and ar images read
  by eye. The edit dialog is open on WEEKEND10 with Activate, there are no
  mask boxes, no focus ring and no caret, and ar is mirrored RTL. The Tester
  attested all 4 locales before the fixes.

Pre-flight: review record added; help prose n/a (unchanged; step 4 already describes the inactive code and Activate, see L2); `make docs-shots` regenerated (promotions ×4 locales); en.json n/a (no strings); reference updated (`list-and-dialog-pattern.md`, ut-docs PR); README n/a.

## Verdict

Approve after fixes. No ADR needed: this is a harness extension in the
same style as `pinnedQuery`.

## Deferred

None. Other list/dialog screens can adopt `dialogShots` as part of their own
per-screen cards under epic #2012.

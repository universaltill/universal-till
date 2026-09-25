# Review — running-out chip: UI font + real plural (ut-docs#2648)

Date: 2026-09-25 · Lane: `lane:cloud-24` · complexity:easy (built by Sonnet, reviewed by Opus 5.5 in a fresh context)

## What shipped

- `web/public/app.css`: a new `.chip-warn` rule. `.chip` is the monospace code chip, and `.chip-warn` had no rule of its own, so the
  "⚠ N item(s) predicted to run out within a week" chip on Reports and Inventory was monospace. On Inventory it also picked up
  the global `h2` upper-casing. It now uses `font-family: inherit`, `text-transform: none`, `letter-spacing: normal` and
  `white-space: normal` (a long German or Persian sentence can wrap on a phone), with the warning tint as its background and
  border. `a.chip-warn { text-decoration: none }` replaces the inline style on the Reports link.
- The `inventory.running_out` key ("item(s)") is split into `inventory.running_out_one` / `_other` in en/ar/fa/tr. The
  templates pick one with `eq .RunningOut 1`, the same as `sync_chip.html`. That applies in `reports.html`, `inventory.html` and
  the `stock_table.html` HTMX refresh partial.
- `web/help/img/manifest.json`: only `surface_sha256` is refreshed, via `update-docs-shots-surface-hash.sh`
  (`Docs-Shots-Unchanged: true`). No manual screenshot shows the chip: the docs till has no completed sales, so `RunningOut`
  is 0 (`e2e/tests-docs/docs-shots.spec.ts:28-29`). The reviewer confirmed this.
- Language packs: follow-up PRs in `ut-plugin-language-de` and `-es` add the two new keys, in the same cycle.

## Tests (TDD; the reviewer re-verified them personally)

- `TestChipWarnRule_OverridesMonospaceAndUppercase` fails when `app.css` is reverted.
- `TestInventoryPredictsDaysLeft` (singular), `TestInventoryRunningOutChip_PluralWording` (plural, on the full page and on
  `/ui/inventory/stock-table`) and `TestReportsPage_RunningOutChipSingularWording` all fail when the templates and locales are
  reverted. I confirmed the new partial assertion by reverting only `stock_table.html`.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `--warning` text on `--warning-tint` is about 2.6–2.8:1 in the light themes, below WCAG AA for .78rem text. The old chip used `--text`. | **Fixed:** the text stays `var(--text)`, and the tint, border and ⚠ carry the warning. The same low-contrast pairing in `.tag.warn`, `.sync-banner` and `.journal-replica-notice` is out of scope and filed as a Backlog card. |
| 2 | nit | The `stock_table.html` partial had no test. | **Fixed:** an assertion on `/ui/inventory/stock-table`. |
| 3 | nit | The CSS test found the rule with the first `.chip-warn` substring, which is fragile. | **Fixed:** it now anchors on `.chip-warn {`. |
| 4 | nit | Arabic has a dual form, and one/other simplifies it. | Accepted. This matches the existing one/other precedent. Turkish and Persian use a singular noun after a numeral, which is correct. |

## Verified beyond the automated tests

I ran a real till (demo seed plus 2 fast sellers at 6 on hand) and drove it with Playwright:

- Checked: Inventory and Reports at 1024×600 in en, Inventory in fa (RTL), and Inventory at 360×740 in en.
- Computed style of the chip: the UI sans-serif stack, `text-transform: none`. On the phone the sentence wraps inside the card.
- Not checked: the dark theme, which is a till setting and not `prefers-color-scheme`. The reviewer computed its contrast
  instead (4.8:1 or better).

Gate: `go build`, `go vet`, `golangci-lint` (0 issues), `go test ./...`, and every `scripts/ci/guard-*.sh` in the ci.yml
build job are green. The exception is `guard-shellcheck-version`: this container has no `shellcheck` binary, and no shell file
changed.

**Verdict: safe to merge.**

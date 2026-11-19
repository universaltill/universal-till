# 2026-11-04 — German help: "Bestellung", not "Vorgang", for open orders (ut-docs#3635)

## What shipped
- `web/help/de/sell.md` step 4/5, `web/help/de/open-orders.md` steps 1-2 and
  `web/help/de/age-restricted-sales.md:50` now quote the sale-screen button
  and list as **Offene Bestellungen** and call a held order "eine Bestellung"
  (was **Offene Vorgänge** / "Vorgang"). This matches the de pack change
  shipped with it (ut-plugin-language-de 1.1.291: `open_orders.title`,
  `page.title.open_orders`, `tour.parked.title` and the other `open_orders.*`
  keys now say "Bestellung"), the topic title "Offene Bestellungen" and the
  newer `open_orders.cancel.*` keys.
- `open-orders.md` front matter keeps `offene vorgänge` as a search keyword
  (synonym for people who learned the old label).
- No Go, template, locale or baseline change. de docs-shots not regenerated:
  this change only alters wording, and the de screenshots come from the
  installed pack.

## Process
Independent review by Claude Opus 5.5 (the author was a Claude Sonnet Dev
subagent). The reviewer only read the code and edited no source.
Diff: uncommitted worktree on `fix/3635-bestellung-term` vs `origin/main`
(93b87c778). Labels checked against the de pack (`locales/de.json`) and the
templates that render them (`web/ui/pages/index.html:169,520,523` →
`open_orders.title`; `web/ui/partials/parked_orders.html` →
`open_orders.tabs_label`, `open_orders.resume_row`).

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | medium | `web/help/de/tables.md:26` (twice) and `:29` still say **Offene Vorgänge**: once for the sale-screen list, once in "siehe **Offene Vorgänge**" (the topic, whose title is "Offene Bestellungen") and once for the counted button. Line 31 of the same page already says **Offene Bestellungen**, and the UI label (`open_orders.title`) now does too. Replace all three with **Offene Bestellungen**. No grammar change is needed, because each one is a bold label. | fixed |
| 2 | low | `web/help/de/sell.md:20`, end of step 4: "sie verschwindet, wenn keiner wartet." The masculine "keiner" agreed with "Vorgang"; the noun is now "Bestellungen" (feminine), so it should read "wenn keine wartet." | fixed |
| 3 | low | Merge order: these pages now quote "Offene Bestellungen", which the till only shows once de pack 1.1.291 is installed. Land the pack PR in the same cycle (it has no core dependency). | note |
| 4 | nit, accepted | "Bestellung" is now used both for held sales (Offene Bestellungen) and for the Orders page (`orders.*`, "Bestellungen"). en uses "Open orders" / "Orders" the same way, and `hold.toast.*` / `open_orders.cancel.*` already said Bestellung. | accepted (mirrors en) |

Checked and found correct: "eine geparkte Bestellung fragt beim Fortsetzen
erneut" (article/adjective agreement; matches en "a parked (held) order");
"eine Bestellung antippen", "wie viele Bestellungen warten"; sell.md:21 list
label changed too; remaining "Vorgang" hits in `web/help/de/` (multitill,
my-reports, claim, sell.md:143) use the generic sense "process/ticket" and
are correct as they are; no "Offene Vorgänge" in `e2e/` or templates.

## Verification
- `scripts/ci/guard-help-drift.sh`, `guard-help-topics.sh`,
  `guard-help-terms.sh`, `guard-help-breadcrumbs.sh`,
  `guard-compliance-claims.sh`, `guard-competitor-naming.sh`: all exit 0
  (reviewer re-ran them). No baseline entries added.
- `grep -rn "Offene Vorg" web/` → only `web/help/de/tables.md` (finding 1).

Pre-flight: Till user-visible behaviour: `web/help/` updated (de: sell,
open-orders, age-restricted-sales; tables → finding 1), docs-shots n/a
(wording-only, pack-sourced), `reference/` n/a (no behaviour change),
README n/a; `en.json` key n/a (no core key added; pack catch-up for
`elevation.summary.reduce_line_qty` from 94f605e21 lands in the de pack PR);
review record: this file.

## Verdict
Findings 1 and 2 fixed by the orchestrator; all help guards re-run green after the fixes. Safe to merge. Merge in the same cycle as
ut-plugin-language-de `fix/3635-bestellung-term`.

## Deferred
None.

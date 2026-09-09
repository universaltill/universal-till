# Code review: translate the cash-reconciliation help section (ut-docs#1846)

**Date:** 2026-09-09
**Author:** pipeline (lane:cloud-54), Dev at Sonnet
**Reviewer:** independent Opus subagent (complexity:medium → Opus per model-routing)
**Branch:** `fix/1846-untranslated-cash-reconciliation-help`

## What shipped

`web/help/en/reports.md`'s "Cash reconciliation on the day-end report"
section, plus the "Skim to safe" / "Denomination count" bullets in the
preceding "Counting the drawer at close" section, were left as raw
untranslated English in all three existing locale manuals (`tr`, `ar`,
`fa`) — invisible to `guard-help-topics.sh` because that check is
topic-id-level, not section-level. Translated both sections' headings and
prose into Turkish, Arabic and Farsi, reusing each locale's existing
terminology from elsewhere in the same file / `web/locales/*.json`
`shifts.*` keys (variance, drawer, till, shift, skim, payout). "Safe" (the
physical safe, distinct from the till/register) got a locale-appropriate
term since no prior in-app term existed for it: "çelik kasa" (tr),
"الخزنة" (ar), "گاوصندوق" (fa).

## What the independent review found

The Opus reviewer ran the guards itself, checked all ~25 factual clauses
from the English source against each locale individually, cross-checked
terminology against each file's own prior usage and the `shifts.*` /
`reports.eod.*` locale keys, and checked markdown/RTL/scope/compliance.
Verdict: **translation content itself was accurate and complete — nothing
in the prose needed re-translating** — but found 3 real blockers, all
fixed before merge:

1. **`guard-docs-shots.sh` failure.** The guard hashes each locale's
   `reports.md` directly; editing the prose invalidated the stored hash
   in `web/help/img/manifest.json`. Fixed by running `make docs-shots`
   (104 screenshots across 26 topics × 4 locales, one worker, ~2.1 min) —
   confirmed by the same reviewer's re-run of the guard. Only 2 unrelated
   PNGs (`ar/multitill.png`, `ar/till-designer.png`) changed by a couple
   of bytes each, consistent with normal re-render noise (a reused,
   version-mismatched Chromium per the script's own printed warning, not
   a content regression) — `reports.md`'s own screenshots are pixel-
   identical because the translated section sits below the captured
   viewport.
2. **Printed-report labels were translated when the file's own
   established convention (see `GUTSCHEINE`, `BY METHOD & VAT RATE`
   elsewhere in the same three files) is to keep a literal printed-label
   quote verbatim.** `internal/pages/eod_api.go` builds the printed
   CASH RECONCILIATION block from hardcoded English strings deliberately
   NOT routed through `T()` (its own comment: "an auditor reads the
   printed report regardless of the till's operator-UI locale"). Fixed
   the two bolded/quoted literal-label mentions in each locale
   (`**CASH RECONCILIATION**` and the quoted `"Tips held out"` line) to
   read `**CASH RECONCILIATION** (nakit mutabakatı)` / `(التسوية النقدية)`
   / `(تطبیق نقدی)` and similarly for the quoted line name — verbatim
   English label plus a parenthetical gloss. The plain descriptive list
   of line items in flowing prose (opening float, cash sales, calculated,
   counted, variance, …) was left translated, since the English source
   itself presents those as description, not as quoted print labels.
3. **The underlying UI strings this section describes were themselves
   still raw English** in `tr.json`/`ar.json`/`fa.json`: `shifts.skim`,
   `shifts.skim_hint`, `shifts.count_protocol`,
   `shifts.count_protocol_hint`, `shifts.opening_carried`,
   `shifts.skim_formula`, `shifts.skim_manager_pin_hint`,
   `reports.eod.variance_flag` — all 8 keys byte-identical to `en.json`
   in all three locales (a pre-existing gap `guard-i18n.sh` can't catch,
   since it only checks key-set parity, not translation content).
   Shipping the manual translation alone would have made it *less*
   usable than before for these three locales, confidently naming
   controls that render in English on the actual close-shift form.
   Translated all 8 keys (24 short strings) in the same branch — small
   and directly load-bearing for what this card ships; no new keys were
   added to `en.json`, so no `ut-plugin-language-{de,es}` follow-up
   applies (`lang-pack-drift` is unaffected).

Two non-blocking prose nits from the same review were also applied:
`web/help/fa/reports.md`'s heading tense (`بسته شدن` → `بستن`, matching
the body's own verb form two lines later) and an elliptical clause
(`… تا آن را تأیید کنید نه دوباره تایپ.` →
`… نه اینکه دوباره تایپ کنید.`). Two further nits were left as noted,
pre-existing and out of this card's scope:
- `tr.json`'s shift-close toast literally says `Yeni kasa: %s` ("new
  till") for what should read "new float" — a pre-existing mistranslation
  in that toast string, not introduced by this diff; the manual's own
  "yeni bakiye" wording is correct and was left as the better term.
- `ar`/`fa`'s existing overlap between the word for "payout" and the word
  for "skim" (both root from withdrawal/برداشت) was already present
  before this diff touches the same list; noted, not touching it further
  to avoid widening scope into `shifts.payout`'s own established wording.

## Verified beyond automated tests

- `gofmt -l .`: clean (no `.go` files touched).
- `go build ./...`: succeeds (sanity check; no Go code in this diff).
- `guard-i18n.sh`, `guard-help-topics.sh`, `guard-compliance-claims.sh`,
  `guard-docs-shots.sh`: all pass, re-run after every fix.
- All three edited locale JSON files parse as valid JSON.
- Manually diffed all 25 factual clauses of the English source against
  each of tr/ar/fa (both by the implementer and independently by the
  Opus reviewer) — nothing dropped, nothing added, all in the original
  order.
- Regenerated screenshots via `make docs-shots` against a real running
  server (two throwaway tills, 104 Playwright specs, 1 worker) — not
  just a guard-script hash check.

## Scope

`web/help/{tr,ar,fa}/reports.md`, `web/locales/{tr,ar,fa}.json`,
`web/help/img/manifest.json` + 2 incidentally-refreshed screenshots.
Nothing else touched. Three further gaps in the same file family were
identified but deliberately left out of scope, each already tracked or
now flagged:
- `tr`/`ar`/`fa` `reports.md` are still missing three whole sections
  `en` has ("Cancellations, and who ran the close", "Dine in / takeaway
  breakdown", "Reconciling a card payment") — pre-existing, not part of
  this card's stated gap; flagging for a follow-up card.
- The pre-existing `tr.json`/`ar.json`/`fa.json` wording overlaps noted
  above (new float vs. till; payout vs. skim root collision).

## Safe-to-merge verdict

Safe to merge. All guards green, translation content verified accurate
and complete by an independent model, and all three real blockers the
review found are fixed and re-verified.

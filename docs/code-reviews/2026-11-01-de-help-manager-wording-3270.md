# Review: German help, Settings → Shop name says "Führungskraft" (ut-docs#3270)

- Date: 2026-11-01
- Lane: lane:cloud-41 (routine `:41`)
- Card: universaltill/ut-docs#3270 (complexity:easy)
- Author model: Opus 5.5; reviewer: Sonnet 5.5 (fresh context, different model)

## Change

`web/help/de/display.md` step 9 used "Leitung" for the manager role twice
("dazu ist eine Leitung nötig", "sieht eine Leitung außerdem"). Both now say
"Führungskraft", and "Manager-PIN" becomes "PIN einer Führungskraft". That is
the wording `backups.md`, `open-orders.md` and step 16 of the same file
already use. The meaning matches `web/help/en/display.md` step 9.

German is not a docs-shots locale, so `web/help/img/manifest.json` has no
`de` hash to refresh. The card asked for one; it isn't needed.

## Findings

- Low (accepted, out of scope): the on-screen PIN prompt (`de.json`) says
  "Manager-PIN", and the manual mixes "Manager" and "Führungskraft". This
  step now matches the other PIN steps in the manual. Making the whole
  manual match the UI would be a separate card.
- No other "Leitung" is left in `web/help/de/`. The structure is unchanged
  (one line, same list item), so `guard-help-drift` is unaffected.

## Verified

- `guard-help-topics`, `guard-help-drift`, `guard-help-breadcrumbs`,
  `guard-docs-shots`, `guard-compliance-claims`, `guard-competitor-naming`: pass.
- `go test ./internal/manual/ ./internal/httpx/ ./internal/pages/`: pass.

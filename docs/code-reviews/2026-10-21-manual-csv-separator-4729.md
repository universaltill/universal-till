# Review — manual: CSV separator note in the catalog topic (ut-docs#4729)

Scope: one "CSV separator" sub-bullet in `web/help/{en,de,tr}/catalog.md`,
help-drift baseline entries for `catalog`, refreshed `web/help/img/manifest.json`
(`make docs-shots` on Linux; PNGs byte-identical, only the topic hash moved).

Reviewer: independent different-model pass (Opus).

Findings
1. Issue-number mismatch (detection code cites ut-docs#4726, baseline cites
   #4729) — not a defect: #4726 is the feature, #4729 is this manual card.
2. "needs no conversion" overclaimed: German Excel's default CSV is
   Windows-1252 and the importer does not transcode it. Fixed — wording is now
   "needs no separator change" in en/de/tr; screenshots/manifest regenerated.

Verified: de/tr wording accurate; baseline JSON valid and consistent
(de/tr in step with English, ar/fa one sub-bullet behind);
`guard-docs-shots`, `guard-help-topics`, `guard-help-drift`,
`guard-compliance-claims` pass.

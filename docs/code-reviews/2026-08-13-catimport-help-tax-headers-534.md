# Review — catimport help topic names the accepted tax column headers (ut-docs#534)

## Summary

`web/help/{en,ar,fa,tr}/catalog.md`'s Import step said a catalog file
"can also carry a tax rate column (and a takeaway tax rate where it
differs)" without ever naming the actual accepted CSV headers. A merchant
building a CSV by hand had no way to know what to name the column.
Found during independent review of ut-docs#512
(universaltill/universal-till#285).

## Changed here

- All four locale files' Import prose now name the literal headers —
  `` `Tax rate` `` and `` `Takeaway tax` `` — as backtick-quoted English
  tokens embedded in the translated sentence (CSV headers aren't
  translated, so the tokens stay in English the same way `` `.bkp` ``
  already does in the surrounding prose). One line changed per file,
  prose otherwise unchanged.

## Verification

- Confirmed the header text is correct against the actual code, not
  just asserted: `internal/pages/import_page.go`'s `writeCatalogCSV`
  writes the literal header `"Tax rate", "Takeaway tax"`, and
  `internal/catimport/catimport.go`'s `columnSynonyms` map (`"tax"` /
  `"takeaway_tax"` entries) accepts `"tax rate"` / `"takeaway tax"`
  case-insensitively — so the documented names are both what the export
  writes and what import recognises.
- `bash scripts/ci/guard-help-topics.sh` — green.
- Prose-only, no route/code/`web/locales/*.json` change — `guard-i18n.sh`
  doesn't apply here (it governs `web/locales/`, not the `web/help/`
  manual tree); `guard-help-topics.sh` is the correct guard.

## Independent review (Sonnet, fresh context, isolated worktree)

PASS, no findings — blocker or non-blocker. Reviewer independently
re-derived the header text from `writeCatalogCSV`/`columnSynonyms`
rather than trusting the commit message, confirmed all four locales
carry the equivalent addition with no locale left behind, confirmed
balanced backticks/no bidi issue in the ar/fa RTL sentences, and reran
`guard-help-topics.sh` against the actual post-fix files. No real
client/shop name or secret introduced (`speedy kasse` / `pepperm
cashbox` are pre-existing generic third-party references, unmodified by
this diff).

## Verdict

Safe to merge. No deferred items — this was a fully self-contained doc
fix with no code behaviour change.

# Legacy CSV import: name rule shown in the preview and at commit (ut-docs#5908)

Branch `fix/5908-import-name-rule`. Independent Fable 5.2 batch review (with #5770 #5776 #5831 #5852).

## What shipped
The legacy CSV import preview flags item, department and category names the single-line rule
refuses (line break, tab, invisible formatting character) with a translated reason; commit maps
`ErrNameInvalidCharacters` to translated row reasons instead of a raw error; the corrected-name
"Import anyway" path re-checks the typed name. Help topic `catalog.md` updated.

## Findings
| # | Finding | Severity | Result |
|---|---|---|---|
| 10 | `ensureCategoryUnderExec` now also rejects for catalog sync | n/a | no regression: catsync already applies the same rule to its rows (`catsync/row.go:99`) |

No blocker/major. No file write, cwd-relative path, float money; new keys in `en.json` + `en.meta.json` only.

## Verification
Build/vet clean; `internal/data`, `internal/catimport`, `internal/customerimport`, `internal/stockupload`,
`internal/i18nmeta`, `web/...` and full `internal/pages` (batch branch) green; tests range over the name cases.
guards data-access / i18n green. docs-shots regenerated in the ut-docshots container (manifest; no catalog PNG
changed; unrelated plugin PNG noise discarded).
Pre-flight: help topic updated; en.meta.json updated; README/ADR n/a.

## Verdict
Approve.

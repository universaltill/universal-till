# 2026-08-06 — CSV exports: formula-injection defusing (audit log + invoice export)

Card: [ut-docs#195](https://github.com/universaltill/ut-docs/issues/195) (p3, security)
Branch: `fix/195-csv-formula-injection`

## What shipped

A shared `csvSafe(field string) string` helper
(`internal/pages/csv_export.go`) that prefixes a leading `'` when a field
starts with `=`, `+`, `-`, `@`, a tab, or a carriage return — the standard
CSV/formula-injection mitigation. Excel/LibreOffice would otherwise
interpret such a field as a live formula on open (e.g.
`=cmd|'/c calc'!A1`); the leading apostrophe defuses it to inert text
without being visible in the rendered cell, and `encoding/csv` round-trips
it as an ordinary character (verified — see below), so nothing downstream
that reads the file as data is affected.

Applied at both call sites the ticket named, to exactly the free-typed
fields it identified — **not** blanket-applied to every column in the row:

- `internal/pages/invoice_page.go`'s `GET /api/invoices/export` —
  `CustomerName` and `CustomerVATNo` (free-typed by whoever issues an
  invoice).
- `internal/pages/audit_page.go`'s `GET /api/audit/export` — `Actor` (a
  manager's own free-typed `display_name`), `Entity ID`, and `Action`
  (plugin code can supply an arbitrary string via
  `PluginRepo.InsertAuditRaw`). `Details`/`DataJSON` is always JSON
  starting with `{`, so it's left alone, matching the ticket's own
  analysis.

## Why not sanitize the whole row

First pass wrapped every field in each row through `csvSafe` uniformly —
`go test` immediately caught a real regression:
`TestGetInvoicesExport_CSVHasCreditNoteAsNegative` failed because a credit
note's gross total is a **legitimate negative number** (`-1.20`), and
`csvSafe` doesn't distinguish "starts with `-` because it's a formula"
from "starts with `-` because it's a signed amount" — it can't, by
design, that's exactly what makes the field ambiguous to a spreadsheet
app too. Blanket-sanitizing would have silently corrupted every credit
note's exported amounts into text. Fixed by sanitizing only the specific
fields the ticket's own analysis named as free-typed/attacker-reachable,
leaving system-generated fields (dates, IDs, kind literals, signed
amounts) untouched. This is the reason `csvSafe` stayed a single-field
function rather than a row-wrapping one.

## Verification

- `go build ./... && go vet ./...`, `bash scripts/ci/guard-data-access.sh`,
  `bash scripts/ci/guard-i18n.sh` — all clean (no template/i18n surface
  touched; this fix is Go-side CSV writer behaviour, not operator-facing
  copy).
- `go test ./internal/pages/...` — full package green, including the
  pre-existing `TestGetInvoicesExport_CSVHasCreditNoteAsNegative`
  (proves the fix does NOT touch signed amounts) and two new regression
  tests:
  - `TestGetInvoicesExport_CustomerFieldsAreCSVFormulaSafe` — issues an
    invoice with a `=cmd|'/c calc'!A1`-shaped customer name/VAT number,
    asserts both come back defused in the exported CSV, and asserts the
    same row's `Gross` is untouched (`1.20`, not `'1.20`).
  - `TestAuditExport_FormulaShapedFieldsAreCSVSafe` — seeds an audit
    entry with a formula-shaped actor display name, entity ID, and
    action, asserts all three come back defused.
  - `TestCSVSafeRoundTripsThroughRealCSVWriter` — round-trips a
    formula-shaped value containing a comma and an embedded quote through
    the actual `encoding/csv` writer/reader, proving the mitigation
    survives real CSV quoting/escaping rather than being a string-prefix
    check in isolation.
- `go test ./...` — green except the pre-existing, unrelated
  `internal/issuereport` `TestSaveCleansUpDirectoryOnWriteFailure`
  (ut-docs#258, sandbox root-run quirk, untouched by this change).

## Acceptance criteria (from the ticket)

- A crafted `=cmd|'/c calc'!A1`-shaped value in any exported field opens
  as inert text in Excel/LibreOffice, not as a formula, for both
  `/api/invoices/export` and `/api/audit/export` — covered by the two new
  handler-level regression tests above plus the round-trip test.
- Regression test proving round-trip through `encoding/csv` + the
  sanitizer — `TestCSVSafeRoundTripsThroughRealCSVWriter`.

## Safe-to-merge verdict

Pending — an independent review (Opus, fresh context, per this card's
`complexity:medium` label) is running against this branch. This record
will be updated with its findings (fixed vs. accepted) before the PR
opens; nothing here merges until that pass completes.

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

## Independent review (Opus, fresh context)

Ran build/vet/guards/the affected test suite itself, and re-verified the
TDD claim by temporarily reducing `csvSafe` to a no-op, confirming both
new regression tests failed with the crafted payload undefused, then
restoring the real implementation and confirming they passed again
(repo left byte-identical throughout — verified by SHA).

**2 MEDIUM findings, both fixed and re-verified:**

- **`invoice_page.go`'s own comment claimed `DisplayNo`/`ReceiptNo` are
  "system-generated" — factually wrong.** Both embed
  `sync.receipt_prefix`, a setting writable as free text via the generic
  `/api/settings/upsert` with no allowlist (`settings_page.go`'s generic
  upsert only `TrimSpace`s key/value). A manager can set that prefix to a
  formula-shaped value and every subsequent invoice's `Invoice`/`Receipt`
  columns go out undefused — the exact same privilege level as the
  `display_name` vector this ticket already treats as in scope, and the
  audit export already defuses this identical string when it shows up as
  `Entity ID`. Fixed: `csvSafe(it.DisplayNo)` / `csvSafe(it.ReceiptNo)`
  alongside the customer fields; comment corrected. New regression test
  `TestGetInvoicesExport_DisplayNoAndReceiptNoAreCSVFormulaSafe` sets a
  malicious `sync.receipt_prefix`, issues a real invoice through it, and
  asserts both columns come back defused.
- **The audit export's ubiquitous `"-"` "no entity ID" sentinel (used at
  roughly a dozen `InsertAudit` call sites) started coming out as `"'-"`.**
  `csvSafe("-")` doesn't distinguish a lone system sentinel from a
  formula-shaped value — a real behavioural regression on the majority of
  rows in a fiscal/compliance export, and not caught by the original test
  suite (it only ever seeded non-`"-"` entity IDs). Fixed: `csvSafe` now
  exempts the literal `"-"` explicitly (a lone hyphen also isn't a live
  formula to Excel/LibreOffice on its own, independent of the sentinel
  reasoning). New table cases in `TestCSVSafe` cover both the exempted
  sentinel and a longer dash-prefixed value that must still be defused.

**1 LOW finding, fixed:** `csvSafe`'s doc comment claimed the mitigation
is "invisible to any consumer reading the CSV as plain data" — directly
contradicted by the very next sentence (and by
`TestCSVSafeRoundTripsThroughRealCSVWriter`'s own assertion) that
`encoding/csv` round-trips the leading apostrophe as a literal rune.
Reworded so a future maintainer doesn't assume it's a no-op to anything
but Excel/LibreOffice — the invoice export is an accountant handoff, and
a downstream system exact-matching a customer name or VAT number would
see the added `'`.

**1 MEDIUM finding, deferred (filed as universaltill/ut-docs#321):**
`/api/catalog/export` (and `-save`) writes `Name`/`SKU`/`Barcode`/
`Category`/`Description` verbatim — all free-typed, and reachable via an
*uploaded CSV* through `/api/import`, arguably a stronger vector than
either field this ticket named since it doesn't require manager-level
typing at all. Out of scope for this ticket (which named exactly the
invoice and audit exports) and genuinely harder than a drop-in `csvSafe`
call: the catalog CSV is a documented round-trip format with the
importer (`TestExportCSVRoundTripsThroughImporter`), so defusing on
export without teaching the importer to strip the same prefix back off
would silently corrupt re-imported names/SKUs/barcodes on every round
trip. Needs its own design, not a same-session bolt-on.

## Safe-to-merge verdict

Yes. Independent review found 2 MEDIUM + 1 LOW issue in-scope, all fixed
and re-verified; 1 MEDIUM found out-of-scope, deferred to
universaltill/ut-docs#321 rather than silently left unmentioned.

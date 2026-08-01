# 2026-08-01 — Audit log CSV export

## Context
`ut-docs#58` ("Change/audit log export", from the "speedy" competitor-
parity sweep, `germany-pos-parity-backlog.md`). The audit log itself
already existed end-to-end — persistence (`InsertAudit`/`InsertAuditRaw`)
and a browse/filter page (`/audit`, `docs/code-reviews/
2026-07-24-audit-trail-page.md`) — what was missing was purely the export
capability. This scope was chosen deliberately narrow: date-ranged CSV
export following this codebase's existing export pattern
(`internal/pages/invoice_page.go`'s `GET /api/invoices/export`), not a
new concept.

## What shipped
- **`internal/data/pos_repo.go`**: the WHERE-clause building shared by
  filtering (`AuditFilters` → SQL) was extracted out of `ListAudit` into
  a private `buildAuditWhere` helper, so the historical bare-date `Until`
  fix (`endOfDayIfBareDate`, from the 2026-07-24 review) can't drift
  between the paginated-browse path and a new bulk-export path. New
  `ListAuditForExport` reuses that helper, ignores pagination (`Limit`/
  `Offset`), and enforces a hard `auditExportCeiling = 10000` row ceiling
  (fetch ceiling+1, truncate to ceiling if exceeded) so a wide-open filter
  can't exhaust memory against the live SQLite file.
- **`internal/pages/audit_page.go`**: new `GET /api/audit/export`,
  manager-gated (`isManagerOrAuthOff`, first statement), reads the same
  `entity_type`/`actor_id`/`action`/`since`/`until` query params as
  `/audit`, streams CSV via `encoding/csv` directly to the response
  (`When, Actor, Entity Type, Entity ID, Action, Details`), and writes its
  own `audit_log` row afterward (`audit_exported`) — same shape as
  `invoice_page.go`'s `invoices_exported`.
- **`web/ui/pages/audit.html`**: an export link next to the filter form,
  carrying the current filters — same shape as `invoices.html`'s existing
  export link.
- **`web/locales/{en,ar,fa,tr}.json`**: `audit.export_btn`, reusing the
  exact translated text already vetted for `invoice.export_btn`.

## Independent review
`general-purpose` subagent, `claude-opus-5` (a different model from this
pipeline run). Given the exact diff, the relevant `CLAUDE.md` rules, and
told explicitly to re-derive every claim against the real code rather
than trust the implementer's summary — including running the build/test/
guard suite itself and mutation-testing the new tests.

**Mutation testing (3 mutations, each confirmed to break a real, meaningful
assertion, then restored):**
1. `ceiling+1` → `ceiling` in the export query's LIMIT →
   `TestPOSRepo_ListAuditForExport_TruncatesAtCeilingAndSignalsIt` failed
   with a real assertion error, not a compile error. Confirms the off-by-
   one (fetch N+1, `len(out) > ceiling` ⇒ truncated) is correct.
2. Dropped `endOfDayIfBareDate` from the shared `buildAuditWhere` helper →
   **two** tests failed from one edit — `TestPOSRepo_ListAudit_
   FiltersAndOrdersNewestFirst` AND `TestPOSRepo_ListAuditForExport_
   BareUntilDateIncludesWholeDay`. Direct, empirical proof the refactor's
   anti-drift goal actually holds: the browse and export paths are
   genuinely sharing one filter implementation, not two that happen to
   agree today.
3. Manager gate replaced with `if false` → `TestAuditExport_ManagerOnly`
   failed (`no session = 200, want 403`), confirming the gate is real and
   load-bearing, not just present.

**Verified correct (re-derived independently, not taken on trust):**
- Manager gate is the literal first statement, before any DB access.
- SQL injection: every filter binds via `?`; `ceiling+1` is a bound
  parameter, not concatenated text. `guard-data-access.sh` clean.
- No disk writes anywhere in the new handler (grepped for
  `os.`/`paths.`/`MkdirAll`/`Create`/`WriteFile` — none hit) — CSV streams
  straight to the `ResponseWriter`, so neither of this pipeline's two
  recurring file-write bug classes (missing `MkdirAll`, cwd-relative path
  instead of `paths.Data`) applies.
- XSS: the export link's filter-value interpolation in `audit.html` was
  executed against hostile inputs (`" onclick="alert(1)`, `'"><img
  src=x onerror=alert(1)>`) — `html/template`'s URL-context escaper
  percent-encodes both fully; no attribute breakout.
- CSV structural safety: commas, embedded quotes, and embedded newlines
  round-tripped through `encoding/csv` correctly — a crafted
  action/details string cannot break the row after it.
- `InsertAudit` call signature/argument order matches the `invoices_
  exported` precedent exactly.
- i18n: `audit.export_btn` present in all 4 locales with genuine
  (non-pasted) translations; `guard-i18n.sh` clean (784 keys).

**Findings, triaged:**
1. **Fixed — silent truncation was under-signalled for a compliance
   artifact.** The original design signaled truncation only via an
   `X-Export-Truncated` response header, which a normal browser download
   never surfaces — an operator exporting a date range for an accountant
   would silently receive an incomplete file. Worse: since the query is
   newest-first, truncation drops the *oldest* rows in the selected
   range — the ones a date-bounded request is usually asking for. At
   realistic volume (61 `InsertAudit` call sites across sales, shifts,
   logins, settings, plugin events — a shop doing 300-500 sales/day
   generates roughly 500-1000 audit rows/day), the 10,000-row ceiling is
   only 2-4 weeks of one till, not a rare edge case. **Fix applied**: the
   downloaded filename itself now carries `-TRUNCATED-first-N` when
   truncated (`auditExportFilename`, `internal/pages/audit_page.go`), and
   the CSV body gets a final visible `TRUNCATED` row explaining the
   range was cut. New test: `TestAuditExportFilename_
   TruncationIsVisibleNotJustAHeader`.
2. **Fixed — orphaned doc comment.** The `buildAuditWhere` refactor left
   `ListAudit`'s doc comment sitting above the new helper instead of
   `ListAudit` itself (godoc would have rendered it on the wrong
   function). Moved back.
3. **Deferred, not blocking — CSV formula injection.** `encoding/csv`
   writes fields verbatim; a value starting with `=`/`+`/`-`/`@` is
   interpreted as a formula by Excel/LibreOffice. Reachable via `Actor`
   (a manager's own `display_name`), `Entity ID`, and `Action` (plugin
   code can supply arbitrary strings via `InsertAuditRaw`). Not a
   regression this change introduced — `invoice_page.go`'s existing CSV
   export has the same exposure with worse reach (free-typed
   `CustomerName`/`CustomerVATNo`) and already shipped that way. Filed as
   a shared fix across both exports: **ut-docs#195**.
4. **Accepted as-is — CSV headers/"system" literal aren't localized.**
   Matches `invoice_page.go`'s existing precedent exactly (that CSV's
   headers are hardcoded English too) — internally consistent with how
   this codebase already treats machine/accountant-facing CSV output
   versus UI strings.

## Verification
- `go build ./...`, `go vet ./...`, `gofmt -l` on every changed file —
  clean.
- `go test ./internal/data/... ./internal/pages/...` (targeted, `-run
  Audit`) — all pass, including two tests added during review triage
  (`TestAuditExportFilename_TruncationIsVisibleNotJustAHeader`,
  `TestAuditExport_NullActorFallsBackToSystemLiteral` — the latter added
  by Tester after noticing the live drive-through couldn't exercise the
  NULL-actor fallback, since `UT_AUTH=off` resolves to a real seeded
  `system` user row rather than a genuinely NULL `actor_id`).
- `go test ./...` (full repo) — one pre-existing, unrelated failure:
  `TestSaveCleansUpDirectoryOnWriteFailure` in `internal/issuereport`,
  confirmed via `git stash` to fail identically on the base commit before
  this change (the test assumes a read-only directory blocks a write;
  this environment runs as root, which defeats that assumption). Not
  caused by, or fixed by, this change.
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh`
  — both clean.
- **Real running app**: built and ran the actual `unitill-pos` binary
  against a real SQLite dev database (`UT_STORE=sqlite`, `UT_AUTH=off`),
  hit `/audit` and confirmed the export link renders with the correct
  filter query string, triggered a real `POST /api/backup/now` action,
  and confirmed the resulting `backup_created` audit row round-tripped
  through a real downloaded CSV with correct headers and correct
  CSV-escaping of the JSON `Details` field. Server cleanly killed
  afterward.

## Explicitly not done (by scope)
- No Email/Share delivery mechanism — no such integration exists
  anywhere in this codebase; not invented here.
- No `.xlsx` format — CSV is this codebase's established Excel-compatible
  export format everywhere (invoices, and now audit); no `.xlsx` library
  exists in `go.mod`.
- The five sibling "speedy"-parity export cards this pattern was
  explicitly built to be reusable for (#55 full DB backup, #56
  per-category config export/import, #57 Endabrechnungen Z-report, #59
  stock level export, #60 time-tracking export) were left untouched, as
  intended — confirmed during review that no scope crept into them.
- CSV formula-injection hardening — filed as ut-docs#195, covering both
  this export and the pre-existing invoice export together.

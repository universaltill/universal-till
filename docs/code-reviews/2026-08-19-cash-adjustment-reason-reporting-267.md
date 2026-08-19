# Code review: cash adjustments by reason reporting (ut-docs#267)

**Date:** 2026-08-19
**Author:** Universal Till autonomous SDLC pipeline (Sonnet build, Opus independent review)
**Card:** universaltill/ut-docs#267
**Repos touched:** `universal-till` (code + tests + user manual), companion
translation PRs in `ut-plugin-language-{de,es}`

## What shipped

`ut-docs#267` found that nothing read the `reason` field back out of
`cash_adjustment` audit-log entries in grouped form: `SumShiftAdjustments`
returns one net total per shift with no reason breakdown, so the only way
to see e.g. "total Pfandrückgabe paid out this week" was the raw
`data_json` blob on `/audit`.

This change:

- Adds `POSRepo.CashAdjustmentsByReason(ctx, from, to)` and
  `CashAdjustmentReasonTotal` (`internal/data/pos_repo.go`), grouping
  `audit_log` rows (`entity_type='shift'`, `action='cash_adjustment'`) by
  `reason` within a window, window semantics matching the file's
  established `windowArgs`/`datetime(...)` convention exactly (no drift
  from `PaymentBreakdown`/`SalesByDay`).
- Wires it into the `/reports` page's Payments & channels tab
  (`internal/pages/reports_page.go`) as a new "Cash adjustments by
  reason" card (`web/ui/partials/reports_tab_payments.html`), shown only
  when there's at least one adjustment in the window — mirrors the
  existing Tills card's hide-when-not-applicable pattern.
- i18n key `reports.by_cash_adjustment_reason` added to all four locales
  (en/fa/ar/tr), real translations. Help manual
  (`web/help/{en,fa,ar,tr}/reports.md`) updated in the same branch.

## Independent review (Opus, fresh context)

Ran the full gate itself (build/vet/test/all guards) plus mutation testing
against the new repo test, and a live probe of SQLite's `json_extract`
behavior on malformed/missing data. Verdict: REQUEST CHANGES. Findings and
what was done about each:

1. **Blocker — `guard-docs-shots.sh` failed.** The new card is on a
   screenshotted screen (`/reports`) and the topic markdown changed.
   Fixed: ran `make docs-shots`, committed the regenerated
   `web/help/img/**/reports.png` (in practice byte-identical — the docs
   fixture seeds no cash-adjustment data, so the card never renders in
   that screenshot — but the manifest hash needed refreshing regardless)
   and `manifest.json`. Two unrelated screenshots (`alerts`, `designer`)
   also came out with different bytes on this run from rendering
   nondeterminism unrelated to this change (their topic hashes in the
   manifest did **not** change, confirming their source didn't) — reverted
   those two to avoid unrelated churn in this diff.

2. **Should-fix — `ut-plugin-language-{de,es}` lang-pack drift.**
   `lang-pack-drift` is blocking on push to `main`. Fixed: opened
   companion PRs adding real translations of the new key —
   universaltill/ut-plugin-language-de#51 (`"Bargeld-Korrekturen nach
   Grund"`, matching the pack's existing `shifts.adjustment`/`shifts.reason`
   terminology) and universaltill/ut-plugin-language-es#49
   (`"Ajustes de efectivo por motivo"`). Both packs' `validate.sh` and
   `check-key-drift.sh` pass clean against the updated core `en.json`.
   These must merge before (or in the same push as) this PR reaches
   `main`, or `lang-pack-drift` goes red.

3. **Should-fix — `GROUP BY json_extract(...)` vs. `SELECT
   COALESCE(json_extract(...), '')` mismatch.** `GROUP BY` on the raw
   expression put a `NULL` reason (missing field, or `NULL` `data_json`)
   in its own bucket separate from an explicit empty-string reason, so
   the two could render as two identically-blank-labelled rows. Fixed:
   `GROUP BY 1` (groups on the `SELECT` list's already-COALESCEd first
   column), plus a template fallback to `reports.uncategorized` matching
   the sibling Departments card's `{{ if .Department }}…{{ else }}…{{
   end }}` pattern. Unreachable via `RecordCashAdjustment` today (reason
   is required, non-empty) but the code already anticipated blank
   reasons via the `COALESCE`, so handling them consistently end-to-end
   is the honest fix rather than a half-measure.

4. **Should-fix — the card was ungated.** The only other surface for this
   data, `/audit`, is manager/admin-only (`canPerform(d, r, "audit")` —
   "this reads system-wide history", `audit_page.go`); `cash_adjustment`
   itself is a permission-gated action. A reason is staff free text
   (e.g. "cash short – Anna's till"), so a reporting shortcut surfacing
   it to any cashier with `reports` access would be a real, if modest,
   widening of who sees manager-only bookkeeping — not something to
   decide silently in a reporting card. Fixed: gated the repo call
   behind `canPerform(d, r, "audit")`, the same action `/audit` itself
   requires (currently granted to manager/admin/super_admin, identically
   to `reports` — so no behavior change today, but it now tracks the
   right permission if the two ever diverge). New handler test
   (`TestReportsPage_CashAdjustmentsByReasonGatedOnAuditPermission`)
   confirms a cashier session doesn't see the card while a manager
   session does.

5. **Should-fix — the repo test didn't falsify what it claimed to.**
   Mutation-testing `TestCashAdjustmentsByReason` showed `ORDER BY
   ABS(net) DESC → ORDER BY net DESC` and dropping the `entity_type =
   'shift'` filter both left the test green. Fixed: reseeded the
   Pfandrückgabe payout at a larger magnitude than the top-up (net
   -5300 vs. +2000) so only the `ABS` ordering produces the observed row
   order, and added a `cash_adjustment` row on a non-`'shift'` entity
   that must not be picked up. Re-verified by mutation: both mutations
   now fail the test.

6. **Nits, accepted as-is / follow-up, not blocking:** malformed
   `data_json` on a matching row surfaces as a discarded error (card
   silently disappears) — precedent-consistent with every sibling call
   in this switch (`Methods`/`Departments`/`Tills`) and with
   `SumShiftAdjustments`'s identical exposure; added an explicit `,
   COUNT(*) DESC, 1` tiebreaker for equal-`ABS(net)` rows (cosmetic,
   matches `PaymentBreakdown`'s own unsettled tie order otherwise);
   `audit_log`'s unbounded scan on this reporting query is identical in
   shape to every other report query in this file and not this card's
   problem — a fair follow-up if `audit_log` reporting ever gets an
   index pass.

Everything the reviewer checked and found correct (window/timezone
semantics, repository-pattern compliance, money handling at the DB
boundary, template nil/panic-safety, i18n key-set-per-locale and
genuine-translation checks, ADR-0008/ADR-0040 alignment, help-manual
consistency) is unchanged from the original diff.

## Verification

- `go build ./...`, `go vet ./...` — clean.
- Full `go test ./...` — clean.
- `go test ./internal/data/... ./internal/pages/... -race` — clean (the
  first `-race` run hit the default 10-minute per-package `go test`
  timeout on the large `internal/pages` package, not an actual data
  race; re-ran with `-timeout 30m` and it passed clean at ~1057s).
- All repo guards: `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-compliance-claims.sh`, `guard-docs-shots.sh` — all pass.
- Manually drove the real app (a throwaway till, `UT_AUTH=off`, seeded
  `cash_adjustment` audit rows directly) and looked at the rendered
  result: light theme (default), the "slate" curated theme, and fa/RTL.
  Card aligns with its sibling tables in all three, negative amounts
  render with a leading "-" via the existing `money` template func, RTL
  mirrors correctly (logical `text-align:end`, matching the sibling
  rows). Did not check the app's other curated themes (amber, fresh) or
  ar/tr locales visually beyond the docs-shots screenshots (the card
  doesn't render in those since the docs fixture seeds no adjustments) —
  the card reuses the exact same table markup/CSS as the three sibling
  cards above it on the same tab, so this is treated as low incremental
  risk.

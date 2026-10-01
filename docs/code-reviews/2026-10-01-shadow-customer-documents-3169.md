# Review: PT shadow-mode customer-document gate (ut-docs#3169, ADR-0124 card 1/2)

- **Date**: 2026-10-01
- **Branch**: `feat/3169-shadow-customer-documents`
- **Card**: ut-docs#3169 (Portugal track), `lane:local-mahshid76`, `complexity:hard`
- **Author model**: Claude Opus 5.5 (Dev and Tester subagents, plus the
  orchestrator's review fixes). **Reviewer**: Claude Fable 5.1 (independent
  subagent, separate worktree).
- **Spec**: ADR-0124 (Accepted 2026-09-28); BA, Architect and UX notes on the
  card. #3173/#3174 were closed as duplicates of #3169/#3170.

## What shipped

A market whose shipped `country_settings` data says
`shadow_customer_documents = 'forbidden'` (only PT today) records every sale
and issues no customer document. The market is data and the enforcement is
generic core code; core Go never tests `"PT"` (`guard-core-neutral`).

- **Data.** Migration `055_shadow_customer_documents.sql` (renumbered from 054 after #1572 took 054; CHECK
  `allowed|forbidden`, PT set to `forbidden`, checksum pinned).
  `builtinCountryDefaults` PT is `forbidden`. That value is a **floor**:
  `BuiltinShadowDocumentsForbidden` needs no I/O, so a missing, pruned or
  crafted row cannot lift it.
  - `Upsert` never takes the column from the caller.
  - `Delete` restores the shipped value.
  - Admin sync ratchets an incoming `allowed` back to `forbidden` for a
    builtin-forbidden code.
- **Decision.** `fiscal.CustomerDocuments` follows ADR-0124 §2: normalise the
  code, apply the builtin floor first, then the stored row; a read error in
  an allowed market stays Allowed and is logged. There is no lift:
  `system_of_record` and the signing-device keys are never read.
- **Surfaces.** One helper, `customerDocumentsSuppressed`, is used on all of
  them:
  - The effective receipt policy is forced to `never` after the plugin clamp
    (amends ADR-0089).
  - `printReceipt` (the choke point) returns `errCustomerDocumentsSuppressed`.
  - Reprint and the designer test print answer **451** with localized text.
    The receipt partial's fetch fallbacks never call `window.print()` on 451.
  - The tender renders a staff "Sale recorded" card with no lines and no
    totals.
  - A `pos-notice info` banner shows on the sell screen.
  - The Country settings page has a read-only column.
  - The async print path does not count a withheld receipt as a print
    failure.
- **Added in review (M1):** `POST /api/invoices/issue` → 451. The invoice's
  thermal print and the automatic credit note after a refund are skipped.
  Rendering an older invoice, self-order and the route-inventory test stay
  #3170 (noted on that card).
- **Docs.** Help topics `country-settings` and `printing` (en, de, ar, fa,
  tr) are updated, the help-drift baseline is re-recorded in lockstep, and
  docs-shots are regenerated.
- **Shared CSS:** `.pos-notice.info` text is now `var(--text)` instead of
  `var(--accent)`. A green-accent theme put green on the blue tint at about
  2:1. This applies to every info notice (catalog autofill,
  `fiscal_register`, pay-panel status). Every theme's `--text` was checked:
  contrast improves everywhere.

## Findings (Fable round 1)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| M1 | major | Banner, refusal and help say "no receipts or invoices", but invoices could still be issued (and thermal-printed) and credit notes auto-issued | **Fixed**: issue → 451, print skipped, credit note skipped; tests `TestInvoiceIssue_ShadowMarketIs451AndIssuesNothing`, `TestMaybeIssueCreditNote_ShadowMarketIssuesNone` |
| m1 | minor | `TestCustomerDocuments_SystemOfRecordAndPostureKeyDoNotLift` (fiscal) cannot fail, because its settings fixture is never passed | **Fixed**: `TestTender_ShadowMarketSystemOfRecordAndPostureKeyDoNotLift` drives the real settings store and tender (suppressed, 0 print jobs) |
| m2 | minor | Settings → Printer rendered the forced `never`; saving any printer setting would overwrite the shop's stored policy | **Fixed**: the page renders `printerConfigForSettings` (no shadow override) and shows a note; `TestSettingsPage_ShadowMarketShowsStoredPolicyAndExplains` |
| m3 | minor | A settings-read error in a PT shop recorded a false `print_failed` and set the `/orders` flag | **Fixed**: `printReceiptAsync` returns before the settings read when suppressed; `TestPrintReceiptAsync_ShadowMarketSettingsReadErrorIsNotAPrintFailure` |
| n1 | nit | "Shadow mode" label is shown even if the shop declares `system_of_record` | Accepted: this is ADR-0124 §4 wording, and no lift exists by design |
| n2 | nit | The CSS change restyles every info notice | Accepted, called out above and in the PR |
| n3 | nit | `Delete` makes two writes outside a transaction | Accepted: the builtin floor covers PT anyway |
| n4 | nit | One extra `SELECT` per `printerConfig` call | Accepted: negligible |
| n5 | nit | A value outside the CHECK in a sync bundle aborts `ApplyAdmin` | Accepted: the primary is trusted, same as other columns |
| n6 | nit | Tender JSON branch still returns total and payments | Accepted: no client uses it, and it is listed for #3170's route inventory |
| n7 | process | Language packs must land in the same cycle | Done: `ut-plugin-language-{de,es,pt}` branches `i18n/3169-shadow-documents` |

No fail-open hole was found on any receipt surface in scope, and there is
no fail-closed harm to GB or DE. DE with a plugin-forced `always` still
prints. An old-schema sync bundle gets DEFAULT `allowed`, and the floor
still suppresses.

A second Fable round was **not** run: round 1 found nothing blocker-class
(MODEL-ROUTING). Each fix has a test that failed first with the real error.

## Verification

- **TDD, Dev:** every production change was preceded by a failing test. The
  pages tests were run against a stub helper that always returned false
  first, and they failed for real (for example
  `printer received 2 jobs after sale+refund, want 0`).
- **Reviewer re-verification** (Fable, own worktree): four production lines
  were reverted and each targeted test failed:
  - `printerConfigChecked` override: `PT: policy="always" AutoPrint=true, want never/false`
  - `printReceipt` choke point: `printReceipt err = <nil>, want errCustomerDocumentsSuppressed`
  - async sentinel branch: `got 1 print_failed rows for a withheld document, want 0`
  - sync ratchet: `shadow_customer_documents = "allowed" after sync, want "forbidden"`

  The lines were then restored and the tests passed.
- **Review fixes:** four new tests failed first, for example
  `invoice issue status = 200, want 451` and
  `a credit note was issued in a shadow market (found=true err=<nil>)`, then
  passed after the fix.
- **Tester (driven run, real app in WSL, fake TCP printer counting jobs):**
  70/70 checks.
  - GB control: the auto-print reached the printer, and a dead printer port
    made the `window.print` fallback fire, so the counter works.
  - PT: the printer got 0 jobs on tender, reprint, refund and the designer
    test, and `window.print` was never called on a 451.
  - `/api/print/test` still prints.
- **Playwright e2e:** 61 passed (receipt auto-reset, sale, sale screen, phone
  sell, country settings, RTL, pos-notice, tender panel).
- **Full gate (Linux, `main` + this diff, after the review fixes):**
  `go build`, `go vet`, `gofmt -l` clean; `go test ./... -count=1`:
  exit 0, 76 packages ok, 0 FAIL. Every `guard-*` in the CI build job that runs locally
  passes, except `guard-deadcode-baseline`. It fails only on
  `internal/logging/file.go` desktop-tag entries, which fail identically on
  untouched `main` because WSL lacks the GTK headers; CI runs it with the
  dependencies.
- **`-race`:** not run locally (no gcc in WSL). CI runs it.
- **docs-shots:** 120/120. `country-settings` PNGs (en, fa, ar, tr) show the
  new column. The CSS change alters no screenshot: old and new
  `fiscal-register` are identical, so only `surface_sha256` moved.
- **Looked at:**
  - PT sell screen with the banner, and the Sale recorded card, at 1024×600
    and 360×740 in en and fa (RTL mirrors correctly), plus dark at 1024×600;
  - GB receipt at 1024×600;
  - PT journal reprint refusal in en and fa;
  - Country settings at 1366 in en and fa.
- **Not looked at:**
  - de, pt and tr renders of the banner and card (strings come from the
    packs);
  - dark theme in fa or at 360;
  - real touch hardware (emulated only; the change adds no gesture code).

## Verdict

**Safe to merge.** The language packs (de 1.1.155, es 1.1.146, pt 1.0.10)
merge in the same cycle so `lang-pack-drift` stays green on `main`.

## Deferred

- #3170: invoice re-render, self-order, the route-inventory test (including
  the tender JSON branch), and hiding journal Reprint and invoice controls
  when suppressed.

# Code review: wire printer/invoice settings onto manager-override elevation (ut-docs#866)

**Branch:** `feat/866-wire-remaining-settings-elevation`
**Reviewer:** independent Opus subagent, isolated worktree (`complexity:medium`
tier — see `scrum-master` skill's "Model routing by complexity"; build ran
inline on Sonnet)
**Verdict:** No blockers. 5 non-blocking nits found; all fixed before merge.

## What changed

ut-docs#867 (merged as universal-till#419) un-gated most `checkOrElevate`-backed
forms in `web/ui/pages/settings.html` so a blocked cashier's PIN elevation
dialog (ADR-0052, `internal/pages/elevation.go`) is actually reachable
through the shipped UI. It deliberately left the printer and invoice cards
gated, because their **backend** handlers weren't wired onto
`checkOrElevate` yet.

This card:

- `internal/pages/print_api.go`: `POST /api/settings/printer` converted
  from a flat `canPerform` 403-deny to `checkOrElevate` — mutating +
  now audit-writing (this endpoint had **no** audit write at all before).
- `internal/pages/invoice_page.go`: `POST /api/settings/invoice` converted
  the same way (already had an audit write; now elevation-aware via
  `settingsAudit`).
- `web/ui/pages/settings.html`: removed the `{{ if .isManager }}` wrappers
  around the printer and invoice cards, added
  `#printer-settings-msg`/`#invoice-settings-msg` spans as the elevation
  dialog's retry `hx-target`, switched both forms' `hx-on::after-request`
  reload guard to check `Content-Type` (mirrors #867/#796's fix — the
  elevation prompt is itself a 2xx and must not trigger the destructive
  reload).
- `internal/pages/menu_page.go`: comment-only — audited the nav-tile
  visibility gate and confirmed it's out of `checkOrElevate`'s scope (not a
  mutating+audit-writing handler; each destination page is independently
  gated already).
- Deliberately **not** converted: `/api/receipt-designer/logo` (file
  upload — can't replay through `elevationHiddenField`'s hidden-`<input>`
  mechanism) and the ephemeral, non-audit-writing sites
  (`/api/receipt-designer/preview`, `/api/receipt-designer/test`,
  `/api/print/test`).
- New backlog card ut-docs#870 for `GET /receipt-designer`'s own
  page-level gate (a full-page redirect, not an HTMX form — can't reuse
  the inline dialog mechanism as-is).
- i18n: `elevation.summary.printer_settings` / `elevation.summary.invoice_seller`
  added to all 4 locale files.

## Independent review

Opus subagent, isolated git worktree (`git worktree add --detach`, since
this session type has no native worktree-isolation hook — manual fallback
per the `reviewer` skill), full read-only pass over the diff plus the
relevant precedent files (`elevation.go`, `settings_page.go`'s
`settingsAudit`/`settingsRespondSaved` helpers).

**No blockers.** 5 non-blocking nits found, all fixed in follow-up commits
before merge:

1. **Test-coverage gap vs. the established slice convention.** Neither new
   site had a test for the `elevated` outcome (valid approver PIN) or a
   dual-attribution audit assertion, and there was no cashier+bad-input
   "validation runs before elevation" case for printer (the existing
   `TestPostSettingsPrinter_ValidatesModeAndCharset` only exercised an
   *allowed* session, which can't distinguish "validated first" from
   "elevation happens to also allow it"). The reviewer independently wrote
   and ran a throwaway test proving the elevated path, dual attribution,
   and ordering were all already correct — this was a coverage gap, not a
   defect. **Fixed**: added `TestPostSettingsPrinter_ElevationFlow` and
   `TestPostSettingsInvoice_ElevationFlow` to `settings_elevation_test.go`,
   mirroring `TestSettingsTillRegister_ElevationFlow`'s exact shape (deny/
   no-pin → deny/wrong-pin → elevate/valid-pin, with
   `assertElevatedAudit`).
2. **Factually wrong comment.** A new comment in
   `receipt_print_invoice_manager_gate_test.go` claimed the excluded sites
   stay flat-denied because they're "ephemeral or not audit-writing,"
   naming `designer-save` — which is neither (it persists 8 settings keys
   and writes an audit row, the same shape as the two sites this card
   converted). **Fixed**: corrected the comment to state the real reason
   (deferred as one unit with the page-level gate, ut-docs#870) and added
   a matching explanatory comment in `receipt_designer.go` itself.
3. **Real regression: un-gating the printer card newly exposed two
   blocked controls to a cashier** — the "Test print" button (still flat
   403'd) and the "Receipt designer" link (still a 303 redirect). Before
   this diff a cashier never saw either; clicking "Test print" would now
   fall through to `app.js`'s generic server-error banner, misreporting a
   permission denial as a server fault — a fresh instance of exactly the
   "visible but silently blocked" bug class #867/#866 exist to remove.
   **Fixed**: wrapped just those two elements (not the whole card/form) in
   a local `{{ if .isManager }}`, matching the precedent already used for
   the Data-management card's per-element guards. Added
   `TestSettingsPage_PrinterCardHidesTestPrintAndDesignerLinkFromCashier`,
   TDD-verified (reverted the template change, confirmed the test fails
   with the exact leak, restored, confirmed green).
4. **Partial audit payload, undocumented.** The new printer audit write
   records `mode`/`charset`/`auto_print` but not `address`/`device`/
   `kitchenAddr` (LAN identifiers), with no comment explaining why.
   **Fixed**: added a one-line comment.
5. **Informational only, no fix**: printer LAN address/device path and
   invoice seller identity now render to any session that can load
   `/settings`, cashiers included — consistent with the already-accepted
   #867 pattern (real config values are shown, real *business content*
   like backup files/GDPR search stays gated).

Also flagged for follow-up (not fixed in this diff, deliberately deferred):
`/api/receipt-designer/save` and the logo `remove=1` branch are both
mutating+audit-writing but stay excluded — correct sequencing (they're
only reachable from the still-flat-denied `GET /receipt-designer` page),
but the reasoning wasn't documented until this review's fix #2. ut-docs#870
should explicitly name both in its scope; noted in this review, left to
whoever picks up #870 to formalize on that card.

## Verification

- `go build ./...`, `go vet ./...` — clean.
- `gofmt -l .` — clean.
- `go test ./...` — full repo, all packages green (twice: once before the
  review's fixes, once after).
- `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-i18n.sh`, `guard-compliance-claims.sh`,
  `guard-help-topics.sh`, `guard-docs-shots.sh` — all green.
- TDD re-verified personally, twice: the original fix (reverted
  `print_api.go`/`invoice_page.go`, confirmed
  `TestPostSettingsPrinter_BlockedSessionGetsElevationPrompt`/
  `TestPostSettingsInvoice_BlockedSessionGetsElevationPrompt` fail with
  the specific pre-fix `403 … "manager or admin required"` message,
  restored, confirmed green) and the review's own N3 fix (reverted the
  `settings.html` guard, confirmed
  `TestSettingsPage_PrinterCardHidesTestPrintAndDesignerLinkFromCashier`
  fails with the exact leak message, restored, confirmed green).
- Independent reviewer separately re-verified the original TDD claim from
  scratch in its own isolated worktree, including the role-matrix
  (`TestReceiptPrintInvoiceEndpoints_RealSessionGatesByRole`) and
  template-visibility (`TestSettingsPage_ElevationWiredFormsVisibleToCashier`)
  tests, and wrote its own throwaway elevated-path test to settle nit #1
  before recommending the fix.

## Non-goals

`GET /receipt-designer`'s page-level gate, `/api/receipt-designer/save`,
and the logo upload/remove endpoints — tracked on ut-docs#870. `menu_page.go`'s
nav-tile visibility gate — audited, confirmed out of scope, no follow-up
needed (each destination page is independently gated; un-gating tile
visibility grants no capability, only invites the same silently-blocked
click #870 already tracks for one specific link).

# Code review: Auth `Can()` sweep — Receipt / print / invoice pages (ut-docs#712)

**Date:** 2026-08-14
**Author (Dev):** Sonnet (subagent, fresh context)
**Reviewer (independent):** Opus (subagent, fresh context, worktree-isolated)
**Card:** universaltill/ut-docs#712, split of #708, successor of #555, same
established mechanism as #554/#706/#707/#709.

## What shipped

Replaced all `isManagerOrAuthOff(r)` gate call sites in
`internal/pages/receipt_designer.go` (5), `internal/pages/print_api.go`
(2), and `internal/pages/invoice_page.go` (3) — 10 sites total — with
`canPerform(d, r, <action>)`. `POST /api/invoices/issue` correctly kept
its pre-existing no-gate behaviour (any cashier can invoice their own
completed sale) — locked in by a new regression test.

Deviated from the card's own suggestion (a possible new `invoicing`/
`print_customization` action): 8 of the 10 sites use the existing
`settings` action (receipt designer x5, printer settings, print test,
invoice seller identity — each is `/api/settings/*`-namespaced or a
manager-only config page, several already audit-logging under category
`"settings"`). **The other 2 — `GET /invoices` (invoice register) and
`GET /api/invoices/export` — use `"reports"` instead**, per the
independent review below; no new migration was needed for either
decision.

## Independent review findings

- **Fixed — blocker:** `GET /invoices` and `GET /api/invoices/export`
  were originally mapped to `"settings"`. Independent review found this
  contradicts an already-merged sibling gate on the *same feature*:
  `journal_page.go`'s `InvoicingOn` flag (landed in #709) already gates
  the "🧾 Invoices" nav button's visibility on
  `canPerform(d, r, "reports")`. Gating the destination page on
  `"settings"` while the entry point uses `"reports"` is inert today
  (both actions grant identically — manager/admin/super_admin), but the
  first role split where they diverge (e.g. a future bookkeeper role
  granted `reports` without `settings`) would hit a dead nav link and,
  worse, a live 403 if they typed the URL directly. The invoice-export
  audit log already categorizes the action as `"invoice"`, not
  `"settings"`, further undercutting the original settings-audit
  rationale for these two specific sites. **Fix applied:** both sites
  repointed to `canPerform(d, r, "reports")` (migration `042`, already
  granted manager/admin/super_admin, denied cashier — identical grant
  shape to `settings` today, so no test churn beyond updating the header
  comment). The 8 genuinely-config sites keep `settings`.
- **Fixed — nit:** test file's ordering-dependency comment
  (`receipt_print_invoice_manager_gate_test.go`) overstated its own
  blast radius ("the two invoices cases" would spuriously redirect —
  only `/invoices` is affected; `/api/invoices/export` has no
  `sellerConfig` check). Corrected the comment; reviewer independently
  verified the real ordering dependency by moving the case earlier and
  confirming the claimed failure actually occurs.
- **Noted, not fixed (accepted nit):** the case-ordering dependency
  itself (must run last) is real and currently enforced only by a
  comment + the corrected note above, not structurally. Reviewer
  confirmed the failure mode is a loud, spurious *failure* if the table
  is reordered, never a silent false pass — so it's a maintenance nit,
  not a blocker. Left as-is; a future hardening could reset seller state
  per-subtest instead.
- **Noted, not fixed:** `TestInvoicesIssue_StillHasNoManagerGate` asserts
  only `!= 403`, so a redirect-style gate would technically slip through
  undetected. Consistent with this file's existing `/api/*` 403
  convention; not tightened.
- **No finding** on: call-site completeness (re-grepped independently,
  zero `isManagerOrAuthOff` remaining in the 3 files), the 4 pre-existing
  ungated routes (`/api/print/labels`, `/api/print/receipt/{receiptNo}`,
  `/api/invoices/issue`, `/invoice/{display_no}` — confirmed unchanged),
  `os.MkdirAll` on the logo-upload path (present, untouched),
  `paths.Data(...)` usage (correct, no cwd-relative path introduced),
  money/SQL/kiosk-engine/i18n (none touched), secrets/real client names
  (none present).

## Verified beyond automated tests (independent re-verification, not just Dev's word)

Reviewer performed its own revert-then-restore TDD checks, in an isolated
git worktree, across all 3 files (not just the site the Dev's report
covered):

- `POST /api/receipt-designer/save` (receipt_designer.go) — reverted →
  `receipt_designer_save/super_admin_past_gate` failed as expected.
- `POST /api/settings/printer` (print_api.go) — reverted →
  `printer_settings/super_admin_past_gate` failed as expected.
- `GET /api/invoices/export` (invoice_page.go) — reverted →
  `invoices_export/super_admin_past_gate` failed as expected.

Each restored individually (atomic revert → run → restore); worktree
confirmed clean and green afterward.

Orchestrator (this session) independently re-ran, in addition to the
above, on the final `"reports"`-fixed diff: `go build ./...`,
`go vet ./...`, full `go test ./... -count=1` (all ~40 packages green),
`guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-i18n.sh` — all
green — and its own separate revert-then-restore spot check
(`POST /api/settings/printer`) confirming the same failure/recovery
signature.

## Safe-to-merge verdict

**Yes**, with the blocker fix (settings→reports on the 2 register/export
sites) applied before merge, as recorded above. No security or
data-integrity regression; behaviour preserved for cashier/manager/admin;
`super_admin` broadening is the one accepted, documented, currently-inert
change, consistent with the rest of this sweep.

## Deferred / follow-up items

None new — the ordering-dependency nit and the loose `!= 403` assertion
are both judged acceptable to carry as-is rather than file separately.

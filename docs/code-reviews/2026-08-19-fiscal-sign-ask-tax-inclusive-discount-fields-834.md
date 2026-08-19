# Code review: fiscal.sign.ask payload gap — tax_inclusive, sale_discount, service_charge (ut-docs#834)

**Date:** 2026-08-19
**Author:** Universal Till autonomous SDLC pipeline (Sonnet build, Opus independent review)
**Card:** universaltill/ut-docs#834
**Repos touched:** `universal-till` (code + tests), `ut-docs` (contract doc)

## What shipped

`ut-docs#834` found that the `fiscal.sign.ask` request payload gave a TSE
signer no reliable way to build a correct Beleg: `vat_breakdown`'s
`net`/`tax` fields meant different things depending on the till's pricing
mode (exclusive vs. inclusive/German-norm), and the payload carried no
`tax_inclusive` flag to disambiguate; separately, sale-level discount and
service charge were folded into `total` but never exposed, so a signer
could not reconcile `vat_breakdown` against `total` on such a sale.

This change (contract → **1.2.0**):

- Adds `tax_inclusive` (bool), `sale_discount` and `service_charge`
  (integer minor units, `omitempty`) to `fiscalSignAskPayload` /
  `buildFiscalSignPayload` (`internal/pages/fiscal_sign_hook.go`), mirrored
  verbatim from `pos.SaleInput`.
- Documents the three fields in `ut-docs/reference/contracts/fiscal-sign-ask.md`
  1.2.0, including the recommended `sale_discount` apportionment method and
  why `service_charge` is *not* apportioned.
- Fixes a related gap found during independent review (see below): a
  background-retry entry queued by a pre-1.2.0 build would otherwise replay
  with a **confidently wrong** `tax_inclusive: false` after upgrade, worse
  than the pre-1.2.0 behaviour of carrying no flag at all. Added
  `pendingFiscalSignRetry.ContractVersion` and a refresh-from-current-config
  step in `fiscalSignRetryTick` for legacy (pre-1.2.0) entries.
- Does **not** touch response-status handling — `ut-docs#835`'s proposed
  `cannot-sign` status is a separate, not-yet-implemented change to the
  same contract, deliberately out of scope here.

## Independent review (Opus, isolated worktree)

Spawned per the `reviewer` skill (medium complexity → Opus review of Sonnet
work), briefed with the exact diff scope across both repos and told to
actually build/vet/test, verify money/tax claims against the real code (not
just the diff), and check the doc's factual claims for accuracy — this is a
compliance-relevant (German TSE) contract change.

**First pass verdict: NOT safe to merge as written.** Findings, all
addressed before merge:

1. **Blocker** — the doc's original claim that "core itself does not
   apportion sale-level discount/service-charge per rate anywhere" was
   **false**: `internal/pages/invoice_page.go`'s `vatBreakdown` (the VAT
   invoice engine, for the *same* sale) already does exactly this — gross-
   share proration with largest-remainder rounding for `sale_discount`,
   and deliberately does **not** put `service_charge` into any VAT band at
   all (ut-docs#72: "a service charge is not itself a VAT-rated line").
   **Fixed**: rewrote the apportionment section to describe that real,
   existing, already-reviewed convention instead of an invented one, with
   the exact algorithm (gross-share proration, largest-remainder to the
   highest rate, exclusive vs. inclusive re-derivation) and an explicit
   note that `service_charge` needs no proration step at all.
2. **Blocker** — the request-payload example set `"tax_inclusive": true`
   without changing the `vat_breakdown` `net`/`tax` figures, which were
   written for exclusive-mode math (`net × rate = tax`) — a directly
   self-contradicting example in the very section teaching how to read the
   flag; `total` didn't reconcile under either reading. **Fixed**:
   rebuilt the example as a fully self-consistent exclusive-mode sale
   (`total = (500+700) − 100 + 50 + (35+133) = 1318`, matching
   `computeSaleTotals`'s own formula) and added a line stating the formula
   explicitly.
3. **Should-fix** — a retry-queue entry persisted by an older build has no
   `tax_inclusive` in its JSON, so it unmarshals to `false` — not a real
   "no data" gap but a false-but-confident `false`, worse than pre-1.2.0.
   **Fixed in code**: `pendingFiscalSignRetry.ContractVersion`, stamped on
   enqueue; `fiscalSignRetryTick` refreshes `TaxInclusive` from the
   *current* configured pricing mode for any entry whose version doesn't
   match, with a regression test
   (`TestFiscalSignRetry_LegacyEntryRefreshesTaxInclusiveFromCurrentConfig`)
   confirming a legacy entry gets refreshed while a current-version entry's
   genuine `false` is left untouched.
4. **Should-fix** — the doc recommended apportioning `service_charge`
   across VAT rates, which assigns it VAT and directly contradicts core's
   own invoice engine. **Fixed** as part of finding 1's rewrite.
5. **Should-fix** — "Known consumers" cited ADR-0044 Decision 3's
   fiskaly-signer-split framing, which ADR-0055 (accepted the same day)
   explicitly amends away — signing stays inside `ut-plugin-tax-de`, no
   separate plugin. **Fixed**: re-cited ADR-0055 in "Known consumers" and
   the 1.2.0 changelog row; left the *historical* 1.0.0/1.1.0 changelog
   rows as dated snapshots, per the reviewer's own note.
6. **Should-fix** — the new tests asserted the Go struct fields but not
   the actual wire JSON tags for the two money fields; a mutation test
   (renaming the tags) still passed. **Fixed**: added a marshalled-shape
   assertion for `sale_discount`/`service_charge` on the non-zero case.
7. **Nit** — the `total` formula note read as if exclusive-mode tax were
   computed on the discounted base; it isn't (tax is on the undiscounted
   line net). **Fixed**: split into an explicit exclusive/inclusive table.
8. **Nit** — the doc referenced `cannot-sign` (ut-docs#835) as if
   available. **Fixed**: marked it explicitly "proposed, not implemented".
9. **Nit** — no test pinned the `total` reconciliation the doc promises.
   **Fixed**: `TestFiscalSignPayload_SaleDiscountAndServiceChargeBreakout`
   now asserts `Total == 1140` for its exclusive-mode fixture.
10. Pre-existing `gofmt` finding on an unrelated paragraph in the same file
    (a Go 1.19 doc-comment blank-line rule) — confirmed via `git stash` to
    predate this change; left untouched, out of scope, no CI gate on it.

**Verified independently, not just re-stated from the diff:** `go build
./...`, `go vet ./internal/pages/...`, and the full `internal/pages` +
`internal/pos` suites all pass; a mutation check (temporarily removing the
three new field assignments from `buildFiscalSignPayload`, run only inside
the review's isolated worktree) made the new tests fail with the expected
messages, confirming they're not false-passes; `omitempty` safety verified
against `SaleDiscount`/`ServiceCharge`'s actual non-negative invariants in
`internal/pos`; the doc's `total` formulas cross-checked line-by-line
against `internal/pos/sales.go`'s `computeSaleTotals`.

## Verified beyond automated tests

- `go build ./...`, full `go test ./...` (all packages, not just the
  touched ones), `go vet ./internal/pages/...` — clean.
- `scripts/ci/guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh` — clean (no data-access/kiosk-isolation
  concerns; this change touches no SQL and no self-order routes).
- No real client/shop name; no literal secrets; all new JSON tags
  snake_case per `CLAUDE.md`.
- Not a UI-surface change (no `web/`, no `internal/pages` HTML/template
  touched) and not a page a shop owner sees, so the UX-guidelines checklist
  and the `web/help/` manual-parity rule don't apply.

## Safe-to-merge verdict

**Safe to merge.** All blockers and should-fix findings from the
independent review were fixed and re-verified; only the pre-existing,
out-of-scope `gofmt` nit remains, deliberately untouched.

## Explicitly deferred

- **`ut-docs#835`** (no `cannot-sign` status) — separate card, not solved
  here; the doc now says so explicitly rather than implying availability.
- **`ut-docs#833`** — the open accountant question on tip/discount
  treatment on the TSE receipt; this change's apportionment recommendation
  mirrors the existing VAT-invoice convention for consistency, but is
  explicitly flagged in the doc as not itself a guarantee of the correct
  legal treatment for a TSE Beleg specifically.
- **`ut-plugin-tax-de` itself** is not touched by this change (it lives in
  a separate repo outside this pipeline's current scope) — it can migrate
  off its interim tax_inclusive-by-reconciliation inference to reading the
  new field directly as a follow-up; filed as a new Backlog card rather
  than attempted here, per the changelog row's "consumer action" column.

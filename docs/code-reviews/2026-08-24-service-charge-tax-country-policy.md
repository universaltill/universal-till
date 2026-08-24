# 2026-08-24 — Service-charge tax + tip-recipient country policy, core mechanism (ut-docs#961)

**Date:** 2026-08-24 · **Branch:** `feat/961-service-charge-tax-country-policy` ·
**Card:** [universaltill/ut-docs#961](https://github.com/universaltill/ut-docs/issues/961)
(complexity:hard) · **Base:** `b22e4b7` (already contains ut-docs#962) ·
**Design:** [ADR-0060](https://github.com/universaltill/ut-docs/blob/main/adr/0060-service-charge-tax-and-tip-recipient-country-policy.md)

**Verdict: safe to merge**, with one **blocking cross-repo action** the reviewer
could not perform from an isolated worktree — see *Required before merge* below.

## Scope

Core mechanism only, per the BA/Architect scoping on ut-docs#961. Explicit
non-goals, each already its own card and deliberately **not** flagged as missing
here: `ut-plugin-tax-{de,uk}` actually answering the hook, Turkey's hard-disable
(ut-docs#962, shipped separately in PR #500 and present in this branch's base),
the GCC multi-charge model (ut-docs#963), UK Allocation-of-Tips-Act records
(ut-docs#964), and any settings-page UI beyond the minimum.

## What shipped

Two facts about a till line — *is a service charge lawful and taxed*, and
*whose money is a tip* — are country law, and both were hard-coded and
country-blind in core. `computeSaleTotals` added the charge to the total and
never taxed it; no market researched for ut-docs#961 defaults to an untaxed
charge, so the old behaviour under-declared tax in every one of them.

- **`charge.policy.ask`** (`internal/pages/charge_hook.go`) — a new
  non-exclusive, best-effort `EventBus.Ask` hook, registered exactly like
  `tax.rate.ask` and governed by its ADR rather than a `reference/contracts/`
  doc (ADR-0035's precedent, which ADR-0060 Decision 1 explicitly follows).
  Empty payload; a plugin may answer `service_charge_permitted`,
  `service_charge_default_rate_bp`, `service_charge_tax_basis_bp`,
  `tip_default_recipient`, `fiscal_business_case`. Answers are validated at
  the boundary (negative rates clamp to 0; an unknown recipient clamps to
  no-opinion; an **absent** `service_charge_permitted` reads as permitted, not
  as a silent `false`), memoized per bus generation, and errors/garbage are
  deliberately *not* cached.
- **`internal/pos/charge_policy.go`** — `ChargePolicy`, `ChargePolicyAsker`,
  `Service.SetChargePolicyAsker`/`ChargePolicy()`. `internal/pos` keeps no
  dependency on the plugin subsystem. One shared asker instance serves both the
  cashier and kiosk engines (the answer is store-level), matching `taxAsker`.
- **`internal/pos/service_charge_tax.go`** — `ApportionServiceChargeTax`, the
  single shared apportionment ADR-0060 Decision 2 requires, plus the
  `ServiceChargeTax` sum and `ChargeTaxLinesFromSale` conversion.
- **Tip recipient** — `PaymentInput.TipRecipient`, migration 061
  (`payments.tip_recipient`, `NOT NULL DEFAULT 'employee'`, mirrored onto
  `payments_archive`), threaded through `InsertPayment`/`GetSaleDetail`,
  defaulted from the hook in `completeTender`, re-validated and re-defaulted at
  persistence, and replayed verbatim through the LAN-sync journal.

### Reviewer additions

- **Migration 062** — `sales.service_charge_tax_basis_bp` (+ `sales_archive`
  mirror), threaded through `InsertSale`, `SaleDetail`, `GetSaleDetail`,
  `reset_archive_repo`'s column list and `applyJournal`. Closes the replay gap
  below.
- **`saleIsTaxInclusive`** now subtracts the service charge from its
  totals-shape inference.
- **`vatBreakdown`** now apportions the charge into the invoice's VAT bands
  through the same shared function; `issueInvoice`'s `GrossTotal` derives from
  the bands instead of adding the charge on as an untaxed lump.
- README's tip and service-charge bullets, which had gone stale on both points.
- `make docs-shots` regenerated (the guard was failing on the branch as pushed).

## Verification the reviewer performed independently

Everything below was re-derived from the code and re-run in a clean worktree —
no claim in a diff comment was taken at face value, per the known concurrent-edit
hazard on this branch's authoring session.

### 1. The apportionment is genuinely *not* `invoice_page.go`'s discount proration

Read side by side. `vatBreakdown`'s pre-existing discount proration divides by
each band's **gross** share (`share = d * out[i].Gross / grossSum`), correct for
taking a discount *off* a gross figure. `ApportionServiceChargeTax` weights by
each band's **true tax-exclusive net** — and under inclusive pricing it derives
that net itself (`trueNet = net − ComputeTaxBasisPoints(net, rate, true)`)
rather than weighing raw values, which would skew shares toward higher-rate
bands. Only the largest-remainder rounding *shape* is shared, exactly as the ADR
specifies. Rounding uses **floor** for every band but the last (floors can never
over-allocate, so `remaining` stays non-negative), with the minor-unit remainder
landing on the highest band because `rates` is sorted ascending — the
conservative direction. Confirmed integer-only: no float appears anywhere in the
money path.

### 2. Both mandated call sites use the one function; no third derivation exists

`computeSaleTotals` and `buildFiscalSignPayload` both call it. Grepped every
remaining `ServiceCharge` reference in non-test `internal/` code: the only other
consumers are display (`print_api.go`'s receipt line), the flat passthrough
field on the sign payload, and the invoice path — which the reviewer moved onto
the same shared function rather than leaving it to derive anything of its own.
No second tax-on-charge computation exists.

### 3. Fail-closed is a real proof, not a populated field

`TestCompleteSale_ServiceChargeTaxedByDefault_UntaxedPathUnreachable` asserts
that a payment at the **old untaxed total is rejected as underpayment**, and
that the persisted `tax_total`/`total` carry the charge's tax. The
tender-path test asserts the same through the HTTP handler and the persisted
row. Both are genuine. Revert-verified below.

### 4. The Turkey backstop and the charge policy compose

No test covered this; the reviewer added
`TestTenderHandler_TurkeyBackstopComposesWithChargePolicy`. The two mechanisms
meet in `pos_api.go` and neither knows about the other:
`EffectiveServiceChargeRateBP` zeroes the charge *before* the policy consult,
and `ServiceChargeTax(0, …)` is a clean no-op —
`ApportionServiceChargeTax` returns `nil` on a non-positive charge before the
flat-basis branch, so even a plugin answering *permitted, flat 19%* cannot
resurrect a charge or invent a phantom tax band on a TR till. Verified: charge 0,
`tax_total` 20 (line tax only), total 120, sale completes.

## Findings

### F1 — Replay of a plugin-answered flat tax basis was rejected outright (fixed)

**This was the review's central question, and the Dev's own caveat understated
it.** `SaleInput.ServiceChargeTaxBasisBP` changes what `computeSaleTotals`
derives, but nothing persisted it, and `applyJournal` rebuilds a `SaleInput`
from the journal and re-runs `CompleteSale`. So a sale tendered while a country
plugin answered a flat basis replayed under the primary's *apportioned* default.

It is **not** merely an AC-7 amount divergence. Proven with a probe against the
real `applyJournal`: a sale stored as tax 21 / total 131 (charge 10 taxed flat
at 7%) re-derives 132 on the primary and dies on

```
payments (131) do not cover total (132)
```

— a `422` that never succeeds, so that sale could **never** replicate; the
replica's cursor stalls on it as a poison entry. Latent only because no shipped
plugin answers the hook yet, but this PR is what makes the hook answerable, by
any plugin, including third-party ones.

Fixed by making the basis travel with the sale, which is the letter of ADR-0060
Decision 4 ("replay is exact"): migration 062 persists it on `sales` (+ archive
mirror), `GetSaleDetail` reads it, `buildJournal` carries it, `applyJournal`
threads it back. Additive and degrading: absent/0 *is* what a pre-ADR-0060 peer
computed. Pinned by `TestApplyJournal_ServiceChargeFlatBasisReplaysExactly` and
`TestBuildJournal_CarriesServiceChargeTaxBasis`.

The `basisBP == 0` path — every sale today — was already exact, and the Dev's
`TestApplyJournal_ServiceChargeReplayNeverReAsksChargePolicy` correctly proves
replay never re-asks the hook. That half was sound; only the non-default half
was broken.

### F2 — Issued invoices under-declared the charge's VAT (fixed)

`vatBreakdown` aggregates lines only, and `issueInvoice` added the service
charge on as an untaxed lump (`gross := net + tax + sale.ServiceCharge`) —
correct while the charge was untaxed, wrong the moment this PR taxes it. An
exclusive-priced sale of 100 @20% with a 10 charge stores `tax_total` 22 /
`total` 132, but its invoice declared VAT 20 and gross 130: a legal document
understating both the VAT charged and the amount paid. A regression this PR
introduces, so fixed here rather than deferred. The charge is now apportioned
into the bands via the same shared function, and gross derives from the bands
(`gross := net + tax`) so it cannot double-count. Pinned by
`TestVATBreakdownApportionsServiceCharge` (exclusive, inclusive, and flat-basis),
which asserts both standing invariants: every band's `Net+Tax == Gross`, and the
bands sum to the sale's own `Total` **and** `TaxTotal`.

### F3 — An inclusive sale carrying a service charge was misread as exclusive (fixed)

Pre-existing since ut-docs#72, but load-bearing for this PR.
`saleIsTaxInclusive` inferred the mode as `Total == Subtotal − DiscountTotal`,
which a service charge (added to the total in *both* modes) breaks. Probed and
confirmed: an inclusive sale with a charge returned `false`. It feeds the
invoice VAT breakdown, the refund math, and — since ADR-0060 taxes the charge
*by pricing mode* — a journal replay's recomputed totals, so it now corrupts
money in a new way. Fixed as a one-liner that adds `+ d.ServiceCharge` to the
comparison; it reduces to the original expression exactly when there is no
charge, so no pre-service-charge sale changes reading. Covered by the inclusive
subtest of F2.

### F4 — `guard-docs-shots` was failing on the branch as pushed (fixed)

A CI-blocking guard outside the four re-run by the orchestrator. This PR changes
non-test `internal/pages/**.go` that registers screenshotted routes, so the
manual's screenshot manifest went stale. `make docs-shots` regenerated (92
captures, 23 topics × 4 locales); guard now passes.

### F5 — README had gone stale on both halves (fixed)

Per this repo's standing rule. The service-charge bullet claimed only that the
charge is "added to the sale total" with no mention of tax — materially
incomplete now — and the tips bullet predated the recipient dimension. Both
updated factually (capability, not compliance outcome); `guard-compliance-claims`
re-run clean.

### F6 — A positive charge on a line-less sale returns no bands (accepted)

`ApportionServiceChargeTax` returns `nil` when there are no lines, so such a
charge would go untaxed. Unreachable: `CompleteSale` rejects a sale with no
lines before totals are computed. Noted, not fixed — inventing a rate out of
nothing would be worse than the unreachable branch.

### F7 — `AskChargePolicy` asks with `context.Background()` (accepted, no action)

No caller-side deadline, so a hanging handler would in principle sit on the
tender path — an offline-first concern. Checked before flagging: it mirrors
`pluginTaxRateAsker` exactly (the precedent ADR-0060 Decision 1 mandates), and
`wasm_runtime.go` applies its own per-event-class deadline, so a wasm plugin
cannot hang a checkout. Not a regression and not this PR's to change; diverging
from `tax.rate.ask` here would be the actual defect.

## Cross-cutting checks

- **Money** — `money.Money` throughout; `int64` only at the DB/DTO boundary via
  `FromMinor`/`Minor`. Basis-point rates stay `int`. No float in any money path
  (the two `float64`s in `invoice_page.go` are pre-existing *rate percentage*
  and *quantity* display formatting).
- **i18n** — this scope adds no user-facing string; `guard-i18n` clean, all
  four locales still match `en.json`.
- **Offline-first** — nothing here can block a sale. No answer, a broken
  plugin, a transport error and unparseable JSON all fall through to the safe
  default; a forbidden charge omits the line and completes the sale. Confirmed
  by `TestTenderHandler_ChargePolicyNotPermittedSuppressesCharge` and the TR
  composition test, both of which assert `200`.
- **Recurring bug classes** — N/A as expected: the diff writes no files, so
  there is no missing `os.MkdirAll` and no cwd-relative path that should be
  `paths.Data(…)`. Verified, not assumed.
- **Repository pattern** — all new SQL is in `internal/data` / `internal/db`;
  `guard-data-access` clean. Migrations are append-only (061, 062 both new).
- **Test data** — no real shop or client names, no secret-shaped literals.

## TDD revert/restore

Each fix was reverted individually and the test re-run; all three failed on a
**real assertion**, never a compile error, and passed again on restore.

| Reverted | Test | Failure observed |
|---|---|---|
| `taxTotal = taxTotal.Add(chargeTax)` in `computeSaleTotals` | `TestCompleteSale_ServiceChargeTaxedByDefault_UntaxedPathUnreachable` | `payment at the old untaxed total must be rejected as underpayment, got err=<nil>` |
| same | `TestTenderHandler_ServiceChargeTaxedAtBlendedRates` | `expected tax_total 22 (20 line + 2 on the charge), got 20` |
| `in.ServiceChargeTaxBasisBP = j.Sale.…` in `applyJournal` | `TestApplyJournal_ServiceChargeFlatBasisReplaysExactly` | `replay rejected a sale the replica already completed: payments (131) do not cover total (132)` |
| `EffectiveServiceChargeRateBP` → raw field in `pos_api.go` | `TestTenderHandler_TurkeyBackstopComposesWithChargePolicy` | `a permitting plugin must not resurrect a charge the TR backstop zeroed, got service_charge_amount 13` |

## Gate

`gofmt -l .` clean · `go build ./...` clean · `go vet ./...` clean ·
`go test ./... -count=1` **all packages ok** · every CI-blocking guard in
`.github/workflows/ci.yml`'s `build` job run individually and passing:
`guard-data-access`, `guard-kiosk-engine`, `guard-plugin-menu-read`,
`guard-i18n`, `guard-compliance-claims`, `guard-docs-shots`,
`guard-help-topics`, `guard-webkit-version`, `guard-kiosk-launch-flags`,
`guard-android-status-address`, `guard-android-i18n`, `guard-emoji-font`,
`guard-htmx-loaded`, `guard-autofill-suppression`, `check-brand-assets`,
`guard-makefile-version`.

## Required before merge (cross-repo, reviewer could not perform)

`reference/contracts/pos-lan-sync-journal.md` in **ut-docs** documents
`tip_recipient` at 1.2.0 but predates fix F1, so it does not yet document
`service_charge_tax_basis_bp` on the sale object. The reviewer worked in an
isolated `universal-till` worktree and cannot edit the `ut-docs` checkout. Add
to that contract's sale-field list, inside the same unreleased 1.2.0:

> - `service_charge_tax_basis_bp` (int, optional, 1.2.0, ADR-0060 Decision 4) —
>   the flat rate the ORIGINATING till taxed its service charge at, when an
>   installed country plugin's `charge.policy.ask` answer fixed one. `0` or
>   absent means the default: the charge's tax is apportioned across the sale's
>   own per-line rate bands. It has to travel with the sale because the primary
>   re-derives the charge's tax through the same `computeSaleTotals` the replica
>   ran — replaying a flat-basis sale under the primary's apportioned default
>   computes a different tax, stores totals that disagree with the replica's,
>   and, when the re-derived total lands above the original, is rejected
>   outright as underpayment (`422`), so that sale could never replicate. Absent
>   is exactly what a pre-1.2.0 replica computed, so it degrades correctly.

`reference/contracts/fiscal-sign-ask.md`'s 1.5.0 bump was checked against the
code and is **accurate as written** — the changelog states the behaviour change,
names the shared function and the rounding rule, keeps `service_charge` as
display-only, and gives the explicit consumer action (a signer apportioning it
per the 1.2.0 recommendation must stop or it double-counts). No change needed
there.

## Suggested follow-up cards (not blocking, not built here)

1. `ut-plugin-tax-de` / `ut-plugin-tax-uk` answering `charge.policy.ask` with
   the ut-docs#961 table's defaults — ADR-0060 names these as follow-ups but
   they do not appear to be filed yet. Worth filing now that the hook is real.
2. `InsertSale`'s positional signature is now 25 arguments and grows with every
   sale column. Not this PR's to fix, but it is one transposed pair away from a
   silent money bug; an options struct would remove the class.

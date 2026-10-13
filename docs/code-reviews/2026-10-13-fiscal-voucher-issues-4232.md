# Review: vouchers in the `fiscal.sign.ask` payload — contract 1.14.0 (ut-docs#4232)

**Date:** 2026-10-13 · **Lane:** `lane:cloud-41` · **Author model:** Opus 5.5 · **Reviewer:** independent Fable subagent (different model)

## What shipped (three repos, one change)

- **universal-till** (`fix/4232-fiscal-voucher-issues`): `buildFiscalSignPayload`
  now counts the vouchers a sale issues exactly as `computeSaleTotals` does.
  - A single-purpose voucher is taxed like a line: it goes into its `vat_breakdown` band, into subtotal/tax, and into the charge-apportionment basis (same content and order).
  - A multi-purpose voucher's face value is added to `total` after the negative clamp and never goes into a band.
  - New `voucher_issues: [{purpose, amount, rate_bp}]` field (omitempty, purpose normalised to `multi_purpose`/`single_purpose`).
  - Before this, a €25 voucher sale was signed as `total: 0` against payments of 2500. `ut-plugin-tax-de` answered `cannot-sign`, and since ADR-0136 that refuses the tender at the till.
- **ut-plugin-tax-de** v0.12.0 (`fix/4232-multi-purpose-voucher`): `BuildReceipt` reads `voucher_issues`.
  - Multi-purpose amounts are taken out of the total before the inclusive/exclusive inference, the discount apportionment and the gross reconciliation. They are then signed in `NULL` (DSFinV-K MehrzweckgutscheinKauf, USt-Schlüssel 5) and never weight a business tip.
  - Single-purpose entries are informational, because they are already in `vat_breakdown`.
  - An unknown purpose, a non-positive amount or an overflowing sum → `cannot-sign`.
- **ut-docs**: `reference/contracts/fiscal-sign-ask.md` 1.14.0. Adds the `voucher_issues` section, the reconciliation `max(Σgross − sale_discount, 0) + Σmulti = total`, a changelog row and the known-consumer note.

## Findings and their outcome

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | The documented reconciliation identity fails when core's negative clamp fires (over-discount + voucher). | Fixed: the formula is now `max(…, 0)`, and the doc says signers refuse a negative goods part. Core doc comment updated too. |
| 2 | should-fix | The contract's top-level example gained a voucher that its `total` doesn't include. | Fixed: the voucher was removed from the main example and now appears only in its own, self-consistent section. |
| 3 | should-fix | Two plugin tests passed against the unfixed plugin. | Fixed: the malformed-voucher cases now have goods that reconcile on their own (the old code signs them; the new code refuses). Added a test where a single-purpose voucher is not counted twice. |
| 4 | nit | No int64-overflow guard on the plugin's voucher sum. | Fixed: overflow → `cannot-sign`, plus a test. Not reachable from core. |
| 5 | nit | Core reads an unknown purpose as multi-purpose. | Comment added explaining why that is safe: the tender handler rejects unknown purposes and runs `computeSaleTotals` before dispatch. |
| 6 | info | A voucher-only sale with a discount or a flat charge is still `cannot-sign`. | Accepted: these are degenerate inputs that were already refused before this change. Not a regression. |

## Verified beyond automated tests

- **TDD, reviewer re-verified in throwaway worktrees.** All 5 new core tests fail on the old logic for the right reason. Examples: voucher-only total 0, not 2500; the tender-path spy saw signed 300 against 1800 persisted. All the plugin voucher tests and the wasm-driven `TestFiscalSignAsk_MultiPurposeVoucherSignsInNullBucket` fail on the old `receipt.go` (`cannot-sign`, "gross per rate sums to 290, total is 5290"). After fix 3 the malformed-voucher and overflow tests also fail on the old code.
- **Probe across 20 sale shapes** (both pricing modes; single/multi/mixed vouchers with discount, charge, over-discount, 0% and business tip). For every shape the core payload's `total` equals `computeSaleTotals`: `CheckPaymentCoverage` accepts `total` and refuses `total − 1`.
- **The same 20 real payloads fed into the new `BuildReceipt`:** 14 signed with balanced halves; the 6 refused are the degenerate shapes in row 6.
- **Gates.**
  - universal-till: gofmt, build, vet and golangci-lint are clean. `go test ./...` is green; `internal/plugins` needed `-timeout 1500s` under CPU contention, and nothing in it changed. Every CI build-job guard passes except two that couldn't run locally: `guard-deadcode-baseline` (its deadcode binary was built with go1.26) and `guard-shellcheck-version` (no shellcheck installed). No shell script or Go symbol set changed; CI runs both.
  - ut-plugin-tax-de: wasip1 vet, `go test ./...` (wasmrun included), build, validate, i18n guard and version-bump check are clean.
- **No UI surface.** No strings, templates or help topics changed, so UX was skipped.

## Not verified (follow-up cards)

- A live sign of a voucher sale against the fiskaly TEST sandbox. It needs the KV credentials, which a cloud lane can't reach (`blocked:env`).
- DSFinV-K export `GV_TYP` mapping for voucher sales. The export is still a skeleton.

## Verdict

Safe to merge, in any order: each side alone keeps today's refusal. Suggested order: ut-docs → universal-till → ut-plugin-tax-de (release v0.12.0).

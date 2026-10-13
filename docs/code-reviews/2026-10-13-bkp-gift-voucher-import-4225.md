# Review: .bkp voucher product imports as a gift voucher item (ut-docs#4225)

**Date:** 2026-10-13 · **Branch:** feat/4225-bkp-gift-voucher · **Reviewer:** Fable 5.1 subagent (built by Opus 5.5)

## What shipped
- `internal/catimport/bkp.go`: `ProductType=2` (the old till's voucher product, pilot "Gutscheine" €0) → `ItemType gift_voucher`; every other row is `standard`. A NULL price on a voucher row is accepted as 0, the same as the CSV path.
- `internal/pages/import_page.go`: a created gift voucher row reads "created (Gift voucher)" (existing key `catalog.item_type.gift_voucher`, no new keys).
- Help `web/help/{en,de,tr,ar,fa}/catalog.md`: one bullet, which also says how to convert an already-imported voucher product (Catalog → Type). Drift baselines for `catalog` re-recorded (+1 sub-bullet, +1 bold lead-in on both sides).
- Scope: card items 1 and 2 (item 2 needs no code: the catalog editor's Type select converts). Item 3 split to ut-docs#4259.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Low | German help said **Typ**; the de pack labels the field **Art** | Fixed |
| 2 | Low | NULL-price voucher branch untested | Fixed: fixture row with `SalesPrice: nil`; fails with `bad_price` when the branch is reverted |
| 3 | Low | Preview grid gives no type signal | Deferred → ut-docs#4262 |

Reviewer checked and found no problem: Status=3 / ProductType=4 / missing name / bad price still block as before; PLU de-dup and image path unchanged; nothing parses `rows[i].Status`; the correction path keeps `ItemType`.

## Verified
- TDD re-verified by the orchestrator: parser test fails with `type "standard"` with the mapping reverted; NULL-price case fails with `bad_price` with its branch reverted; both pass restored. Handler test failed on the missing "(Gift voucher)" before the status change.
- `go build ./...`, `go vet`, `go test ./internal/catimport/ ./internal/pages/`, `guard-data-access`, `guard-i18n`, `guard-help-drift`, `guard-help-topics` green. Local golangci-lint can't run (built with go1.26, repo targets 1.27.1): CI's lint is the gate.
- Not run: Playwright e2e and screenshots (the only visible change is result-row text).

## Verdict
Safe to merge.

# Review: erasing a customer switches off their targeted promotions (ut-docs#5591)

Date: 2026-11-05 · Branch: `fix/5591-erase-customer-promo` · Lane: `lane:cloud-24`
Built by: Sonnet (Dev) · Tested by: Sonnet (fresh agent) · Reviewed by: Opus (fresh agent)

## What shipped

- `internal/data/pos_repo.go` `EraseCustomer`: the promotions unlink is now
  `UPDATE promotions SET is_active = 0, customer_id = NULL WHERE customer_id = ?`.
  A NULL `customer_id` means "any customer" (`activePromoWhere`), so the old
  unlink-only statement turned a customer-only code into a code for everyone
  the moment that customer was erased. Same rule as migration 097 and the
  sync-apply repair (ut-docs#5590). One statement, inside the erasure tx; the
  023 trigger bumps `sync_admin_version`, so LAN replicas receive the
  inactive row.
- Tests: `internal/data/erase_customer_promo_5591_test.go` (repo level:
  bound active + bound inactive codes resolve for nobody via
  `FindActivePromo` and `ScanCodeKinds`, rows end `(NULL, 0)`, a global code
  and another customer's code untouched) and
  `internal/pages/erase_customer_promo_5591_test.go` (real mux, migrated DB,
  `POST /api/data/customers/erase` with manager PIN).
- Help: `web/help/en/promotions.md` step 3 says an erased customer's codes are
  switched off and must get the intended customer before re-activation;
  docs-shots manifest topic hash updated (prose only, no pixels change).

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | low | Help sentence said "re-activate after … removing the customer" — after erasure there is no customer, so this read as "switch it back on", i.e. the widening the fix prevents. | Fixed: reworded to match step 6 ("choose the customer it is meant for; otherwise any customer can redeem it"). |
| 2 | low | No replica-level test that a satellite ends with the code unlinked and inactive. | Accepted: verified by reading (023 trigger + full-row apply); the #5590 sync tests cover the apply path. |

Other paths checked, none widen a code: `EraseCustomer` is the only customer
delete (one caller, `data_api.go:424`); the FK has no ON DELETE action;
demo-seed removal never deletes a targeted customer; sync apply already
repairs per #5590; customer import / ADR-0168 tombstones never touch
promotions; the operator's × clear is deliberate. Erasure stays audited once
(`customer_erased`); no per-code audit row, consistent with 097.

## Verification

- TDD re-verified independently by Tester and Reviewer: SQL reverted → both
  tests red (`BOUNDACTIVE resolved for customer "" after erasure`;
  `erased customer's promo still applies for ""`); restored → green.
- `go build ./...`, `go vet`, `gofmt -l`, `golangci-lint` (data, pages): clean.
- `go test ./internal/data/ ./internal/pages/ ./internal/customerimport/...`: ok.
- Guards: data-access, i18n, help-topics, help-drift, help-breadcrumbs,
  help-terms, fixed-dates, docs-shots: pass.
- Not looked at visually: no rendered surface changed (backend + help prose).

Pre-flight: web/help topic updated (promotions.md + docs-shots topic hash; no screenshots change); reference/ n/a (no reference page covers erasure of targeted promos; behaviour follows 097/#5590); README n/a (not stale); en.json/i18n-meta n/a (no new keys); plan/platform/ADR/plugin rows n/a; review record this file; follow-ups none filed (finding 2 accepted).

Verdict: approve.

# Review — additional till refuses to void a voucher sale (ut-docs#5133)

Date: 2026-10-28 · Branch `fix/5133-replica-void-voucher-sale` · Built by
Sonnet from an Opus Architect brief; reviewed by Fable (money path,
high-risk tier).

## What shipped

- `pos.UpdateSaleStatus` gains an `additionalTill` parameter (tests pass
  `false`; a separate wrapper tripped the desktop-shell deadcode guard). On an additional till (`sync.primary_url` set) a void of a
  sale that issued or topped up a gift voucher returns
  `ErrVoidVoucherSaleOnAdditionalTill` inside the transaction, before any
  write. `POST /api/pos/sale/status` maps it to 409
  `pos.error.void_voucher_additional_till`.
- `POSRepo.SaleHasVoucherIssueOrTopUp` (SQL stays in `internal/data`).
- Why refuse rather than proxy or journal the void: the sales journal pushes
  only completed sales and the main till ignores a sale id it already has, so
  a local void never reaches it; the additional till cannot tell whether a
  sale was pushed (push cursor + 5 s settle window, lost 200s). Refusing
  writes nothing, so the main till's voucher balance and Z keep matching what
  the customer holds. No wire change, so `pos-lan-sync-journal` stays 1.13.0.
- Key in en/ar/fa/tr + `en.meta.json`; help `vouchers.md` (en/de/ar/fa/tr)
  "What can go wrong" bullet and the day-end sentence; help-drift baseline
  `top_bullets` 26→27 for ar/fa/tr (lockstep with English); README voucher
  bullet; docs-shots manifest refreshed.
- Language packs de/es/pt get the key in follow-up PRs after this merges.

## Findings (Fable)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `additionalTill` is the till's current role, not the sale's provenance; a till whose `sync.primary_url` were cleared would void old voucher sales again. No code clears it today. | Doc comment states it; no code change. |
| 2 | minor | The pos test's "issue" case reused the topped-up voucher, so a main till would refuse it too (weak); map order random. | Fixed: separate untouched voucher, slice, plus main-till voids of both. Re-verified: without the refusal it fails `issue sale: err = <nil>`. |
| 3 | nit | Help says "the till says so" but the void route has no UI caller (API-only). | Accepted — the existing pattern. |
| 4 | nit | Help could say the correction must happen on the main till. | Accepted; out of scope. |

Reviewer checked: the only production caller is `pos_api.go`, refunds never
touch vouchers, no other `voided` writer, the transaction rolls back on refusal,
and the error mapping has no shadowing. It also judged the ar/fa/tr wording
consistent with neighbouring keys.

## Verified

- TDD re-verified by the reviewer in a separate worktree (refusal removed →
  `TestVoidSale_ReplicaRefusesTopUpSale` / `...IssueSale` 204 instead of
  409, pos test fails; restored → pass) and by the orchestrator for the fixed
  pos test.
- `go build ./...`, `go vet`, `go test` for pos/data/pages and the full
  module except `internal/plugins` (it timed out at the local default 10 min;
  it's untouched, and CI runs it in its own leg). Every `ci.yml` build-job guard
  passes, except shellcheck-version (no local binary). docs-shots guard is
  fresh after `make docs-shots`.
- No visual surface changed (API-only route), so no screenshots were checked.

## CI follow-up

`desktop-shell`'s deadcode guard flagged the first push: the old
`UpdateSaleStatus` wrapper was only reached from tests. The wrapper is gone;
the one function takes `additionalTill`.

## Verdict

Safe to merge.

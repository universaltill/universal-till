# Review — the till adopts a my.-born shop's currency, or flags the difference (ut-docs#4539)

- **Date:** 2026-10-16 · **Lane:** lane:cloud-54 · **Built by:** Opus 5.5 · **Reviewed by:** Fable 5.1 (independent subagent; one review covering this PR, ut-cloud's and the ADR-0163 amendment)
- **Design:** ADR-0163 amendment (2026-10-16), "the shop's currency reaches the till". Cloud side: ut-cloud `storeEntitlement` sends `currency` / `currency_decimals` for a my.-born store.
- **Scope:**
  - `internal/entitlement`: the block's raw `currency` / `currency_decimals`, cached as `entitlement.shop_currency(_decimals)`; absent or malformed caches "" and never rejects the block;
  - `internal/cloudsync`: `Hooks.ShopCurrency`, run after a sync the cloud answered (a 402 carrying a block included);
  - `internal/pages/shop_currency.go`: the main till adopts the shop currency once per shop value, only with no completed/voided sale and no sale in progress (basket, kiosk, self-order, parked); otherwise a keyed problem (reaches my.) and a manager-only status chip `GET /ui/shop-currency-chip` on the main till;
  - 2 new `status.shop_currency_mismatch.*` keys in en/ar/fa/tr + meta; help topic `claim` in all five languages; `docs/data-model.md`.

## Verified
- gofmt, `go build ./...`, vet, golangci-lint 0 issues; full `go test ./...` rc=0 before the review fixes; after them, entitlement, cloudsync, pages, data and auth packages all ok.
- Guards: i18n, i18n-meta, help-topics, help-drift, rtl-logical-css, data-access, no-showmodal, docs-shots, settings-write (`TestSettingsWriteGuard`), and 116 of 117 build-job guards (shellcheck-version can't run here: no shellcheck binary; no shell script changed).
- TDD re-verified by the reviewer: forcing the "has sales" branch made `TestReconcileShopCurrency_NoSalesAdopts` fail (`store.currency = "GBP", want EUR`); restored, it passes. H1/M1/M2 tests failed against the pre-fix file.
- `-race`: the only failure is the pre-existing `TestPluginIdentify_JobCarriesReadOnlyOperator_4444` data race (#4517); the new tests pass under `-race` on their own.
- No visual change to any captured surface (the chip mount is empty unless mismatched), so docs-shots' surface hash was refreshed without new screenshots. Not looked at in a browser: the chip itself (kiosk/RTL/dark). It reuses the disk-space chip's `sb-item` pattern and logical CSS.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| H1 | High | Adoption could fire mid-basket and stamp the first sale with the new currency | Fixed: skipped while any basket, kiosk basket, self-order session or parked sale exists; test |
| M1 | Medium | A manager's deliberate pre-first-sale choice was reverted every tick | Fixed: adopt once per shop value (`entitlement.shop_currency_adopted`); later differences get chip + problem; test |
| M2 | Medium | Chip on a replica pointed at a card locked there | Fixed: chip empty on a till that follows a main till; test |
| M3 | Medium | A lapsed store (402) cached the block but never ran the hook | Fixed: hook runs after a 402 that carried a block; tests |
| M4 | Medium | Concurrent Settings save could leave DB and runtime currency apart | Fixed: persist under the state write lock, runtime set only on success; regression test (failed-write trigger) |
| L1 | Low | Cited ADR-0114 for the replica settings pull | Fixed: ADR-0011 §5 (code and ADR text) |
| L2 | Low | `reportedReadOnlyTillSettingKeys` says currency is never remote-writable | Fixed: cross-reference to the amendment |
| L3 | Low | New keys/audit action undocumented | Fixed: `docs/data-model.md` |
| L5 | Low | Adoption also marks the currency confirmed (#970 import gate) | Intended; now stated in the ADR |
| L6 | Low | No review record | This file |
| L7 | Low | Test globals not reset | Fixed: reset in `resetProcessGlobals` |

Accepted residual: a basket started in the instant between the in-progress check and the write is not caught (the engines don't take the state lock). The window is one settings write on a till that has never sold, and the first sale then carries the shop's own currency. The chip's hint ("change it in Settings") fits an unknown-code case less well; the help topic covers it.

**Follow-up:** the two new keys need `ut-plugin-language-*` pack PRs (`lang-pack-drift`).

**Verdict:** safe to merge after the ADR amendment and the ut-cloud PR.

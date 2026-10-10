# Review — core read view `shop.context.v1` (ut-docs#4034)

**Date:** 2026-10-10 · **Lane:** cloud-54 · **Author model:** Opus 5.5 · **Reviewer model:** Fable (independent, fresh context, separate worktree)

## What shipped

- `shop.context.v1` (`view:sales`, no arguments): one row with
  `store_name`, `till_name`, `currency_code`, `currency_decimals` and
  `locale`. A plugin had no way to learn the currency's decimals, so the AI
  plugin assumed 2. That is wrong for JPY, IRR, IRT, IQD and AFN, which
  have 0.
- `data` cannot import `httpx` (which holds the currency list) or `enroll`
  (which works out this till's own name), because both import `data`. So
  the row comes from a provider that `internal/pages` installs in an
  `init()` (`shop_context_view.go`), through `data.SetCoreViewShopContext`.
  With no provider installed the view fails (`-3`) rather than guess.
- Currency: the shop's stored `store.currency`, read through the list the
  till formats with (`httpx.CurrencyByCode`). When none is stored, the live
  value. Names are `""` when unset, never English placeholder text.
- `till_name` comes from `enroll.DeviceName`: `sync.till_name` on a joined
  till, `till.name` on the main till.
- Why `view:sales` and not a new class: the view is what makes the sales
  views' minor units readable, and `sales.receipts.v1` already returns
  `currency` under `view:sales`. Store and till names are printed on every
  receipt. Nothing here is a secret or customer PII (ADR-0121 §5).
- Docs: the README view list; ut-docs `reference/contracts/plugin-views.md`
  (1.2.0) and `reference/plugin-host-functions.md` (ut-docs PR).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | A price-only plugin that holds only `view:inventory` (for `catalog.items.v1`'s `price_minor`) must also request ★ `view:sales` to learn the decimals. That over-grants. | **Accepted for now, documented.** The contract says so. A narrower class needs an ADR-0121 §5 amendment and is filed as ut-docs#4045. |
| 2 | minor | The provider read `httpx.ActiveCurrency()`. Before `pages.Init` publishes the currency (a schedule tick at boot), that is still the GBP/2 default, the one path where the view guessed. | **Fixed.** The stored `store.currency` wins; the live value is the fallback. The test failed first: it got `GBP`/2 instead of `JPY`/0. |
| 3 | minor | A currency code the list doesn't know reports 2 decimals. | **Accepted, documented.** That matches what the till itself shows. |
| 4 | nit | `store.name` read errors fail the view, while `DeviceName` swallows its errors and returns `""`. | **Accepted.** `""` is a documented value; a code comment says so. |
| 5 | nit | `SetCoreViewShopContext(nil)` in a test cleanup would remove the real provider for later tests in `pages`. | **Fixed.** The setter returns the previous provider; every test restores it. |
| 6 | nit | `plugin-host-functions.md`'s view list still omits `users.list.v1` and `sales.receipts.v1`. | **Out of scope.** ut-docs PR #4010 (ut-docs#3976) adds them. That PR also uses contract version 1.2.0, so whichever merges second takes 1.3.0. |

## Verified beyond the automated tests

- Reviewer re-ran both TDD claims. With the `init()` removed, the pages
  test fails with "no shop context provider installed". With `cur.Decimals`
  forced to 2, it fails on `"currency_decimals":2`. The author re-ran
  finding 2's claim (revert → GBP/2 failure → restore → pass).
- Every binary that runs plugins links `internal/pages`: the root `main`
  (via `internal/app`) and `mobile/`. `cmd/unitill-desktop` and
  `cmd/unitill-uninstall` don't run plugins.
- `TestViewQueryShopContext` drives a real wasip1 guest. It gets the row
  with `view:sales`, and `-2` (audited) without it.
- No file writes, no paths, no SQL outside `internal/data`, no
  user-facing strings.
- The surface hash was refreshed with no screenshot change, because the
  new `internal/pages` file renders nothing (`Docs-Shots-Unchanged`).

## Verdict

Safe to merge.

## Deferred

- ut-docs#4043: the AI plugin builds its Ask prompt from this view
  (`blocked:dep` on #4034).
- ut-docs#4045: a narrower permission class for the view.

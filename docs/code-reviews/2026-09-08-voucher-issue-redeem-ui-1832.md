# 2026-09-08 — Cashier UI for voucher issue and redeem (ut-docs#1832)

Branch: `fix/1832-voucher-ui` → `main`. Reviewer: independent pass over the
real diff, not the prior pipeline steps' summaries.

## What shipped

The voucher backend was already complete and tested before this card
(`internal/pages/voucher_tender_test.go` drives issue + redeem through the
real HTTP handler and `pos.CompleteSale`). ut-docs#1832 is the missing
cashier-facing half — the capability existed only for something speaking
HTTP directly. This branch adds, and only adds, the UI:

- **`internal/db/migrations/013_voucher_payment_method.sql`** — seeds the
  built-in `voucher` payment method. `pos.CompleteSale` only honours a
  payment's `voucher_id` when its `MethodID` is literally `"voucher"`
  (`internal/pos/sales.go:542`); 001_init's `gift` row is `type='voucher'`
  but `id='gift'`, so it never satisfied that check and the tender select
  had nothing to offer.
- **`internal/data/pos_repo.go`** — `PaymentMethod` gains `Type`, selected
  by both `ListActivePaymentMethods` / `ListActiveNonCashPaymentMethods`.
- **`internal/pages/index_page.go`** — two differently-filtered views: the
  Split `<select>` keeps the full list; the Pay grid and the ⚡ quick-pay
  button exclude every voucher-type method.
- **Split-tender panel** (`web/ui/pages/index.html`, `web/public/app.{js,css}`)
  — "Sell a voucher" (amount / optional preprinted code / optional holder,
  folded into the amount due and sent as `issue_vouchers` on the same
  `/api/pos/tender` POST) and a voucher-code field with a best-effort
  balance check for redemption.
- **Receipt** — `renderReceipt` takes `issuedVouchers`; the payment line
  shows the redeemed voucher's code and a new "Vouchers issued" block
  shows every issued code.
- **i18n** (en/ar/fa/tr, 19 keys each), **help topic** `vouchers` in every
  shipped locale, README updated.

No backend, day-close, or voucher-repository code was touched — confirmed
by reading the diff, not by trusting the claim.

## What I verified independently (beyond re-reading)

**Migration safety — traced, not accepted.** `001_init.sql` is genuinely
absent from the diff. I read `internal/db/db.go`'s `verifyAppliedMigrations`
directly: on any name/checksum drift for a version at or below the ledger
watermark it returns a hard error ("delete the data directory and start
again") *unless* the version is in `idempotentRerunVersions` — and that map
is literally `map[int]bool{}`, so version 1 is not allowlisted. Editing
001_init's `payment_methods` seed instead would therefore have bricked
every already-migrated device, including the pilot install. The additive
013 file is `INSERT OR IGNORE`, so it is idempotent and safe to re-run.
`guard-migration-version-collision.sh` confirms 013 is a unique version.

One residual, accepted: `payment_methods.name` is `UNIQUE`, so on a till
that already has some *other* row named `Voucher`, `INSERT OR IGNORE`
no-ops on the name conflict and the built-in is not seeded. It fails safe
(no crash, no data change) and the shop can rename; not worth blocking on.

**The `gift` vs `voucher` design.** Read `sales.go:542` myself: the check is
`strings.ToLower(strings.TrimSpace(p.MethodID)) != "voucher"`, so `voucher`
is exactly the right id and `gift` genuinely could never work. Leaving the
legacy `gift` row untouched (still a generic, untracked voucher-type
tender) is the same stance ut-docs#1008 took and does not reinterpret
historical `gift` payments.

**Pay-grid exclusion, edge cases probed.** The filter excludes *both*
`gift` and `voucher` (both are `type='voucher'`), and the Split select
still offers both — pinned by the new tests and re-verified by breaking
the code (below). Edge cases I chased:

- *Zero active methods*: unchanged from before — `methods` falls back to
  `["cash","card"]`, `defaultPayMethod` stays nil, and the template's
  existing hardcoded-cash `{{ else }}` branch renders.
- *All active methods are voucher-type* (a shop that deactivated cash and
  card): `gridMethods` is empty, so quick-pay/Pay-grid fall back to the
  same hardcoded-cash branch while the Split select still preselects a
  voucher-type method. Accepted, not fixed: it needs a shop that cannot
  take ordinary money at all, and the outcome is a fallback, not a wrong
  tender. Worth knowing it exists.
- *`defaultPayMethod` interaction*: the preferred-method reorder runs on
  `payMethods` **before** the filter, so a shop whose
  `payments.default_method` is `voucher` still gets a real one-tap method
  at the head — pinned by `TestIndexVoucherMethods_DefaultNeverVoucher`.

**Receipt call sites.** All 24 `renderReceipt` references checked: one
production call site (`pos_api.go:1679`, passes `issuedVouchers`) and the
rest tests. The new parameter's type (`[]receiptVoucherIssue`) follows a
`string`, so a missed or transposed argument is a compile error, not a
silent mismatch.

**Are the receipt's codes the FINAL codes?** This is the subtlest claim in
the branch and it is correct, for a non-obvious reason: `CompleteSale`
takes `SaleInput` **by value**, but `in.VoucherIssues` is a slice, so
`in.VoucherIssues[i].VoucherID = id` (sales.go:635, where a blank code
becomes a uuid) writes through the shared backing array into the handler's
own `voucherIssues`. The receipt therefore shows post-normalization codes.
It is also *fragile* — a future defensive copy would silently blank
generated codes on the receipt — which is exactly what the new test pins.

## TDD claims re-verified by breaking the code

Not taken on trust. In this worktree I reverted each fix, re-ran the test,
confirmed a real assertion failure (not a compile error), then restored:

1. **Pay-grid exclusion** — replaced the filter predicate with `if true`.
   `TestIndexVoucherMethods_PayGridExcludesVoucherTypes` and
   `..._DefaultNeverVoucher` both failed, printing the actual rendered HTML
   containing `data-method="gift"`, `data-method="voucher"` and a
   `⚡ Voucher` quick-pay button. Restored → pass.
2. **Receipt voucher codes** — simulated the exact future refactor the
   design is vulnerable to, `in.VoucherIssues = append([]VoucherIssueInput(nil),
   in.VoucherIssues...)` at the top of `CompleteSale`.
   `TestPOSTender_ReceiptShowsIssuedVoucherCodes` failed with *"receipt does
   not show the server-generated voucher code
   \"8e355048-…\""*. Restored → pass. The test cross-checks against the
   `vouchers` row that actually landed, so it is a genuine end-to-end pin,
   not a tautology.

## Findings

### 1. `de` help topic missing — would have turned `main` red (BLOCKING; fixed)

`origin/main` moved ahead of this branch's fork point while the card was in
flight and now ships a **complete 36-topic German help locale**
(`web/help/de/**`). This branch adds a 37th topic (`vouchers`) to
en/ar/fa/tr only. I merged `origin/main` into the branch and ran the guard,
which failed for real:

```
guard-help-topics: locale "de" is missing manual topics: [vouchers]
```

No earlier pipeline step could have seen this — the `de` locale did not
exist when the branch forked. Beyond the red build, German is *the* pilot
locale for this exact feature (the merchant's word is **Gutschein**), so an
English-only voucher manual would have missed its actual audience.

**Fixed**: added `web/help/de/vouchers.md` (full translation, matching the
existing `de` topics' terminology — Aufteilen / Zahlung hinzufügen / Rest
auffüllen / Verkauf abschließen) and the matching **Gutscheine** cross-link
section in `web/help/de/payments.md`, mirroring what the branch already did
for the other four locales. Guard now passes.

### 2. New built-in omitted from the plugin-hijack repair (LOW; fixed)

`PluginRepo.SyncPluginPaymentMethods` re-asserts, every run, that "the
seeded built-ins are never plugin-owned" — but enumerates them literally as
`id IN ('cash', 'card', 'gift')`. This card adds a *fourth* seeded built-in,
so that statement's own stated invariant no longer matched its code.

Not exploitable today, and I checked both vectors rather than assuming:
the upsert's `ON CONFLICT(id) DO UPDATE ... WHERE payment_methods.plugin_id
= excluded.plugin_id` can never match a `plugin_id IS NULL` row, and
`syncAdminTables` gives `payment_methods` `skipCols: ["plugin_id"]`
specifically so the LAN path cannot import an ownership claim (ADR-0031).
But a hijacked-then-deactivated `voucher` row is precisely the regression
that silently removes tracked redemption from the Split tab again, and the
repair is the one place that would have disagreed about what "built-in"
means.

**Fixed**: added `'voucher'` to the list, with
`TestPluginRepo_SyncPluginPaymentMethods_RepairsHijackedBuiltIns` covering
all four. Verified the test fails on `voucher` alone before the fix.

### 3. Pay-grid type check was case-sensitive (LOW; fixed)

The exclusion compared `m.Type != "voucher"` exactly. Built-in rows are
lowercase, but a **plugin-provided** method's type is lifted verbatim from
its manifest — `SyncPluginPaymentMethods` does
`json_extract(pe.config_json, '$.method_type')` with no canonicalization —
so a manifest declaring `"Voucher"` would have bought a one-tap
full-amount button that records an untracked tender debiting no voucher.
This also disagreed with the house convention established the same day by
ut-docs#1795 (payment-method case canonicalization) and with `sales.go`'s
own `ToLower(TrimSpace(...))`.

**Fixed**: `!strings.EqualFold(strings.TrimSpace(m.Type), "voucher")`, with
`TestIndexVoucherMethods_PayGridExcludesMixedCaseVoucherType`. Verified the
test fails before the fix.

### 4. Observations, examined and accepted (not fixed)

- Raw SQL in `internal/pages/voucher_tender_test.go`'s cross-check is
  within the rule: `guard-data-access.sh` excludes `_test.go` by design,
  and CLAUDE.md scopes the rule to the guard script.
- The only `float64` in the diff is in *test-only* receipt template stubs
  (`receipt_test.go`, `fiscal_device_hook_test.go`), a pre-existing pattern
  in those files. No production monetary value is a float.
- The two duplicate-voucher error paths reuse `pos.toast.voucher_invalid`;
  the comment update correctly notes the duplicate-leg case is now
  reachable from the Split tab and that the generic wording still fits.

## Non-negotiables checked

- **Money**: every new amount is `money.Money` on the Go side, converted at
  the DTO boundary only (`v.Amount.Minor()`, `p.VoucherID`); the new
  `receiptVoucherIssue.Amount int64` matches the existing `receiptPayment`
  convention for template rendering. No float anywhere in production code.
- **Offline-first**: no new endpoint and no new blocking dependency. Issue
  and redeem both ride the existing `/api/pos/tender` POST, which still
  sends `offline: offlineOverrideEnabled() || !navigator.onLine`. The one
  new network call (`GET /api/vouchers/{id}` behind "Check balance") is
  explicitly best-effort: it is a button the operator chooses to press, its
  `catch` reports and returns, and it never gates **Add Payment** — the
  real validation happens on the tender POST. Checkout is not blocked.
- **i18n**: all four locale files carry an identical 2087-key set
  (verified by set comparison, not by eye); the new strings are real
  translations, not English copies; `%s` is the only verb used, matching
  `app.js`'s non-Sprintf `fmt` helper. `guard-i18n.sh` passes.
- **RTL**: no `left`/`right` in any new CSS; the new rules use
  `grid-column`, `flex`, `justify-content` only.
- **Repository pattern**: no SQL added outside `internal/data` /
  `internal/db`. My own fix stayed inside `internal/data`.
- **Recurring bug classes**: no new file writes at all in this diff, so no
  missing `os.MkdirAll` and no cwd-relative path that should be
  `paths.Data(...)`. Checked explicitly.
- **Secrets / client names**: none. The only long hex string in the diff is
  `web/help/img/manifest.json`'s screenshot surface hash.
- **Help topic**: `web/help/en/vouchers.md` read in full against the
  shipped UI — the issue/redeem steps, the "value is added to what the
  customer owes", the ✕ removal, the 50-voucher cap, the change-box
  disappearing, and the "older `gift` method doesn't track a balance"
  note all match the code. No `routes:` field in any locale (correct — it
  documents a section of the already-claimed `/` page), and
  `{{ helpLink "vouchers" }}` appears twice next to the new UI.

## Gate re-run (this exact HEAD, by me, post-merge with `origin/main`)

| Check | Result |
|---|---|
| `gofmt -l .` | empty |
| `go build ./...` | OK |
| `go vet ./...` | OK |
| `go test ./...` (full suite) | all packages pass |
| `golangci-lint run ./...` | `0 issues.` |
| `guard-data-access.sh` | ✓ no inline SQL outside internal/data / internal/db |
| `guard-i18n.sh` | ✓ 1521 keys resolve; all locales match en.json |
| `guard-help-topics.sh` | ✓ (after finding 1 fixed) |
| `guard-compliance-claims.sh` | ✓ 292 files, no forbidden claims |
| `guard-docs-shots.sh` | ✓ 26 topics × 4 locales, surface `a4c61f1b6a62…` |
| `guard-kiosk-engine.sh` / `guard-page-http-error.sh` | ✓ |
| `guard-autofill-suppression.sh` / `guard-htmx-loaded.sh` | ✓ |
| `guard-migration-version-collision.sh` | ✓ unique versions |
| `guard-plugin-menu-read.sh` / `guard-price-history-sync.sh` / `guard-android-i18n.sh` | ✓ |

Screenshots were regenerated (`make docs-shots`) because my `index_page.go`
fix changed the surface hash the guard tracks.

## Verdict

**Safe to merge.** The design decision at the centre of this card (a
distinct `voucher` payment-method id, seeded additively, kept out of the
one-tap grid) is correct and well-defended by tests I broke and restored
myself. Three defects found: one blocking (a `de` manual gap created by
main moving underneath the branch) and two low-severity hardening issues,
all three fixed here with regression tests. Nothing was forced through.

## Deferred / owed follow-ups

- **ut-plugin-language-{de,es}**: the 19 new `en.json` keys are owed to
  both external packs. `lang-pack-drift` is advisory on the PR and
  **blocking on push to `main`**, so this must be filed and done promptly.
  The German help topic added here uses Gutschein / Gutscheinwert /
  Gutscheincode / Guthaben prüfen / Ausgegebene Gutscheine — the `de` pack
  should match that wording so manual and UI agree.
- **ut-docs#1833** — barcode scanning of a voucher code (the pilot
  merchant prints the number as a barcode); deliberately out of this card.
- **ut-docs#1037** — single-purpose vouchers.
- **ut-docs#1036** — DATEV posting for vouchers.
- Not filed, noted only: the all-voucher-methods-active edge case above,
  and the `payment_methods.name` UNIQUE collision that can no-op the 013
  seed. Neither warrants a card on its own today.

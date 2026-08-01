# Review: service charge field on sales, distinct from tip (ut-docs#72)

**Date:** 2026-08-01 · **Branch:** `feat/service-charge-72` · **Card:** universaltill/ut-docs#72 (p2, restaurant-vertical backlog: a till-set service charge, automatically added, must be modeled separately from a customer-discretionary tip)

## What shipped

1. **Domain model** (`internal/pos/sales.go`): `SaleInput.ServiceCharge
   money.Money` — an already-computed amount, same shape as
   `SaleDiscount`, deliberately NOT a rate (see "Independent review"
   below for why). `computeSaleTotals` adds it to `total` (opposite of
   `SaleDiscount`, which subtracts), so `netPayments`'s payment-
   sufficiency check requires it be covered — unlike
   `PaymentInput.TipAmount`, which stays deliberately excluded from that
   check.
2. **Persistence**: migration `024_sale_service_charge.sql` —
   `sales.service_charge_amount` (integer minor units, `NOT NULL DEFAULT
   0`). `InsertSale`/`GetSaleDetail` (`internal/data/pos_repo.go`)
   round-trip it.
3. **Live checkout** (`internal/pages/pos_api.go`): the tender handler
   computes the amount from the till's configured rate
   (`store.service_charge_rate_pct`, off the post-discount pre-tax
   subtotal, same base `ComputeTaxBasisPoints` uses for tax) and passes
   it straight through to `SaleInput.ServiceCharge`.
4. **Live basket display** (`internal/pos/service.go`): `Config` gained
   `ServiceChargeRateBasisPoints` (display-only, current-config-driven);
   `Basket.ServiceCharge` + `recomputeTotals` fold it into the on-screen
   `Total` the same way, so what a cashier sees before tender matches
   what `CompleteSale` will demand.
5. **Multi-till sync** (`internal/pages/sync_sales.go`): `applyJournal`
   passes the synced sale's OWN `ServiceCharge` amount through unchanged,
   the same way it already does for `SaleDiscount` — never recomputed
   from whatever rate the primary happens to have configured at replay
   time.
6. **Settings**: `RuntimeState.ServiceChargeRatePct` +
   `store.service_charge_rate_pct`, wired through the existing generic
   manager-gated `POST /api/settings/upsert` (no dedicated settings-page
   form field — verified `TaxRatePct` itself has none either, only the
   setup wizard + this same generic endpoint, so this matches real
   precedent rather than inventing new UI scope).
7. **Receipt** (`internal/pages/print_api.go`) and **journal detail**
   (`web/ui/pages/journal_detail.html`, all 4 `web/locales/*.json`) both
   show a distinct "Service Charge" line when non-zero. The receipt label
   is a plain Go string literal, matching every other label in that exact
   function (`Tip`/`Change`/`Discount`/`Tax`/`TOTAL` are all plain
   literals too — `guard-i18n.sh`'s Go-string check only flags
   `w.Write`/`fmt.Fprintf` argument literals, confirmed by reading the
   guard script, not assumed). The journal line IS a real `{{ T "key" }}`
   template key, added to en/ar/fa/tr.
8. **Invoices** (`internal/pages/invoice_page.go`): `GrossTotal` now
   includes the service charge so an issued invoice matches what the
   customer actually paid.
9. Two **pre-existing** migration-upgrade-simulation tests
   (`internal/db/barcode_seed_test.go`, `internal/db/dead_seed_test.go`)
   needed a fix unrelated to this feature's logic: they rewind
   `schema_migrations` and replay migrations to test an old till
   upgrading. Migration 024 (`ALTER TABLE ADD COLUMN`) isn't idempotent,
   so replaying it after their rewind hit "duplicate column" — fixed by
   also dropping the column before replay so the simulated pre-upgrade
   DB state is physically accurate (same shape as the fix their own
   comments say was "just fixed" for an analogous 023 case). Their actual
   assertions are untouched.
10. README: "Service charge" moved from "Confirmed not built yet" to
    "Shipped".

## Independent review (Opus subagent) — findings and resolutions

**First-pass verdict: NOT SAFE TO MERGE — 4 blocking findings.** All four
were real, confirmed by re-deriving the failure independently (not just
trusting the subagent's report), and fixed:

- **BLOCKING — checkout broke entirely once the rate was set.** The
  original design put a *rate* on `SaleInput`
  (`ServiceChargeRateBasisPoints`) and let `computeSaleTotals` compute
  the amount internally. But `pos_api.go`'s tender handler computes its
  *own* local `total` (used to fill in every quick-tender button's
  `amount=0` default — confirmed every shipped tender button posts
  `amount=0`, `web/ui/pages/index.html`) without the service charge, so
  `CompleteSale` then computed a *higher* total internally and rejected
  the payment. Reproduced live: `POST /api/pos/tender` with
  `method=cash&amount=0` against a 10%-configured till returned `400
  payments (120) do not cover total (130)` — the feature could not be
  turned on at all. **Fixed** by moving the rate→amount computation to
  the tender handler itself (single source of truth for the live total)
  and changing `SaleInput` to carry the already-computed amount, not a
  rate — added `TestTenderHandler_QuickTenderCoversServiceCharge`
  (`internal/pages/pos_api_test.go`), which posts the exact real-button
  form shape and asserts 200 + correct persisted total; reproduced the
  original failure by temporarily reverting the fix (400, matching the
  live repro) before restoring it.
- **BLOCKING — the on-screen basket total didn't match what checkout
  would demand.** `pos.Config`/`Engine.recomputeTotals` never knew about
  the rate at all, so the sale-screen total shown to the cashier (and
  customer-facing display) was the PRE-service-charge amount — same root
  cause as above, one layer up. **Fixed**: `Config` gained
  `ServiceChargeRateBasisPoints` (display-only), `Basket.ServiceCharge` +
  `recomputeTotals` fold it into `Basket.Total` using the identical base
  the tender handler uses. Verified live: scanning an item now shows
  `£1.56`, not the pre-fix `£1.44`, immediately after setting the rate.
- **BLOCKING — multi-till sync would silently under-record synced
  sales.** A rate-shaped `SaleInput` field gave `sync_sales.go` no way to
  replay a synced sale's *historical* service charge — the primary would
  recompute from its own currently-configured rate (possibly 0, possibly
  different), so a replica's £110 sale (£100 + £10 service charge) would
  land on the primary as a £100 sale with a stray £10 in the recorded
  payment. **Fixed** by the same rate→amount refactor: `SaleInput.
  ServiceCharge` is a fixed amount, so `applyJournal` passes the synced
  sale's own `ServiceCharge` straight through, exactly like it already
  does for `SaleDiscount`.
- **BLOCKING — issued invoices understated what was charged.**
  `issueInvoice`'s `GrossTotal` was `net + tax` only (per-line VAT bands
  only) — a sale with a service charge would issue a legal invoice whose
  total is strictly less than what the customer paid. **Fixed**:
  `GrossTotal` now adds `sale.ServiceCharge`.

**Confirmed correct, not a gap** (subagent checked, I independently
re-checked the reasoning): the settings-page-form-field question (no
dedicated field exists for `TaxRatePct` either — real precedent, not a
gap); the receipt/journal label choice (verified against the actual
`guard-i18n.sh` regex, not assumed); the seed-test `DROP COLUMN` fix
doesn't weaken what `barcode_seed_test.go`/`dead_seed_test.go` actually
assert.

## Deferred (real follow-up work, not silently dropped)

Three Backlog cards opened from non-blocking review findings — none of
these make the shipped feature incorrect, but each is a real gap for a
shop that actually turns the rate on:

- **universaltill/ut-docs#242** — whether a *mandatory* service charge
  (this product's stated intent: "automatically added") should attract
  VAT like it does in the UK/Germany is a business/legal call, not an
  engineering one; left `needs-info` for the product owner. Current
  behavior (zero-tax) is correct for a discretionary charge and is what's
  documented; not a defect, but an explicit unresolved scope question.
- **universaltill/ut-docs#243** — a full refund doesn't return the
  original sale's service charge (lines-only `computeRefundTotal`); a
  product-policy call on whether/how to prorate.
- **universaltill/ut-docs#244** — `ServiceChargeRatePct` is whole-percent
  only, so the UK's standard 12.5% restaurant rate can't be configured
  (matches `TaxRatePct`'s own existing limitation, not a regression).

Also noted, not filed as a separate card (low-risk, self-order legitimately
not charging a dine-in service charge is plausible product behavior, not
an obvious bug): the self-order kiosk path
(`internal/pages/self_order_shop.go`) leaves the rate at zero.

## Verified beyond automated tests

Real driven runs against the actual compiled binary + real SQLite +
real migrations (`UT_AUTH=off`, killed and cleaned up after each run):

- Set the rate via `POST /api/settings/upsert`, scanned a real seeded
  item, confirmed the live basket total inflates correctly.
- Reproduced the original blocking bug exactly (quick-tender `amount=0`
  → 400) before the fix, then confirmed the identical request succeeds
  (200, correct total) after.
- Confirmed `/journal/<receipt>` (English and Arabic/RTL) shows a
  distinct, correctly-amounted, correctly-translated Service Charge line
  and that Subtotal + Tax + Service Charge now visibly sums to Total.
- `go build`, `go vet`, `gofmt -l`, `guard-data-access.sh`,
  `guard-i18n.sh` all clean. Full `go test ./...` green except one
  **pre-existing, unrelated** failure
  (`TestSaveCleansUpDirectoryOnWriteFailure`,
  `internal/issuereport/bundle_test.go`) — confirmed via `git stash`
  that it fails identically on unmodified `origin/main` (root user in
  this sandbox bypasses the read-only-directory check the test relies
  on).

## Verdict

**Safe to merge.** All four blocking findings from independent review are
fixed and each has a regression test proving the specific failure mode
they describe (not just re-asserting the happy path). Three genuine,
non-blocking follow-ups are tracked as Backlog cards rather than silently
dropped.

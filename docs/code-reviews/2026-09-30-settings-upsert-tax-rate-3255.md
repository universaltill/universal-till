# Review — settings upsert: store.tax_rate reaches the engines, validated (ut-docs#3255)

- **Date:** 2026-09-30
- **Branch:** `fix/3255-settings-upsert-tax-rate`
- **Author:** Opus 5.5 (lane:cloud-54); **reviewer:** Fable, fresh context, read-only.

## What shipped

`POST /api/settings/upsert` with `key=store.tax_rate`:

- is validated before the elevation gate (ut-docs#557 order): a whole
  percent `0..100`, else 400 with the existing localized
  `taxcodes.err.invalid_rate` message and nothing persisted. The accepted
  value is stored normalised (`"025"` → `"25"`).
- now joins `KeyTaxInclusive / KeyServiceChargeRate / KeyCountry` in the
  engine-apply case, so `applyEngineConfig` pushes the new rate to the
  cashier `Engine`, `KioskEngine` and live self-order sessions immediately
  (previously only `RuntimeState` changed until the next other save, sync
  pull or restart).

Test: `internal/pages/settings_upsert_tax_rate_test.go` — upsert 25 leaves
all three engines on 2500 bp; `-1`, `abc`, `101`, `12.5`, `""` → 400 with the
DB untouched; a cashier gets the 400 before any elevation prompt; `0`/`100`
accepted; `025` stored as `25`.

## TDD

Test written first and run against unchanged `main` code: failed with all
three engines on 0 bp and `-1` accepted (204). Passes after the fix
(also with `-race`).

## Findings

1. **Minor — fixed.** The comment claimed the tax-code editor's range; that
   editor accepts two decimals. Comment now says whole percent (the
   `RuntimeState.TaxRatePct` int, same as the setup wizard). Fractional
   default rates are a pre-existing core limit, outside this card.
2. **Nit — fixed.** `"+25"`/`"025"` were stored verbatim; now normalised
   with `strconv.Itoa`.
3. **Nit — accepted.** The refusal loop checks the DB but not live
   state/engines; validation returns before `UpdateState`, so it holds by
   construction.

Reviewer also confirmed: cloud `set_setting` writes the store directly and
re-derives (not routed through upsert, so unaffected); the wizard and
`/api/settings/save` keep their own guards; the shared engine-apply case has
no extra side effect for this key (`countryChanged` is false, locale re-set
is a no-op); key present in every `web/locales/*.json`.

## Verified beyond automated tests

No UI ships this key through upsert (API/raw-table only), so no help topic
or screenshot changes; no reference doc describes upsert per-key rules.

## Verdict

Safe to merge.

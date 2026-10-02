# Code review — locale strings no longer hard-code the £ sign (ut-docs#3192)

- **Date:** 2026-10-02
- **Ticket:** ut-docs#3192 (`complexity:easy`): "English locale strings
  hard-code the £ sign".
- **Branch:** `fix/3192-currency-symbol-locale-strings`
- **Author:** Sonnet (this cycle's build model, `complexity:easy`).
- **Reviewer:** an independent Opus 5.5 pass in an isolated worktree, per
  `MODEL-ROUTING.md`'s easy tier.
- **Verdict: SAFE TO MERGE.** Two test-quality findings were fixed, and one
  rule on cross-repo merge order is logged below.

## The gap

`inventory.cost_optional` ("Cost (£, optional)") and `catalog.price_major`
("Price (£)") had a literal currency symbol inside the locale string. Every
shop saw a pound sign on the stock-cost field whatever its configured
currency was. `tr.json` had the same problem with ₺, and `ar.json`/`fa.json`
with £.

## What shipped

- `web/locales/{en,tr,ar,fa}.json`: in both keys, the hard-coded symbol is
  replaced by a `%s` placeholder.
- `web/ui/pages/inventory.html`: the one live call site is now
  `{{ printf (T "inventory.cost_optional") currency.Display }}`. That shape
  is already used at `web/ui/partials/catalog_variants.html:254`
  (`catalog.routing.inherited`).
- `catalog.price_major` has no call site. The reviewer grepped `.go`, `.html`,
  `.js` and `.ts` files and checked for dynamically built keys (`"catalog." +`,
  template literals): the only reference outside the locale files is an old
  review record. The key is still fixed so that all locales keep the same
  placeholder count.
- `internal/pages/inventory_cost_currency_test.go`: a whole-body test and a
  test scoped to the `<label>` (GBP and EUR).
- Sibling language packs, which are separate repos and PRs:
  `ut-plugin-language-{de,es,pt}` `locales/<code>.json` get the same
  symbol→`%s` change for both keys. Each pack's `check-key-drift.sh` checks
  that its placeholder count matches core's `en.json`.

## Review findings

1. **LOW (fixed): the fmt-error guard in the label-scoped test was too
   narrow.** It rejected only `%!s`. Reviewer reproduction: put back the
   pre-fix `en.json` (no verb) but keep the new template. The label then
   renders `Cost (£, optional)%!(EXTRA string=£)`. Under GBP that string still
   contains the wanted `Cost (£, optional)`, so the GBP subtest **passed**.
   This failure mode is real: any locale or language pack whose string lacks
   the verb produces it. **Fix:** the guard now rejects any `%!`. After the
   fix, the GBP subtest fails on that reproduction with "leaked a Go fmt verb
   error".
2. **INFO (accepted, claim corrected): only one of the two tests fails when
   the template change is reverted.** With only the `inventory.html` line
   reverted, the label-scoped test fails, but the whole-body test still
   passes. Its own comment explains why: `base.html` stamps
   `data-currency-display` on `<body>`. The whole-body test is not useless,
   though. It goes red when the *locale* half of the fix is reverted (£ comes
   back on an EUR page), so it guards the other half of the change. Both tests
   are kept.
3. **INFO (accepted): unknown-currency fallback renders a trailing space.**
   For a code missing from the currency table, `httpx` falls back to
   `Display: code + " "`, so the label reads "Cost (XYZ , optional)".
   `catalog.html`'s `({{ currency.Display }})` already behaves the same way.
   It is cosmetic and only reachable with an unlisted currency code. No
   follow-up needed.

## Checked and dismissed

- **Placeholder convention:** `%s` matches the codebase.
  `catalog.labels.price_per_unit` is `"%s per %s"` and
  `catalog.routing.inherited` is `"From category: %s"`. No `%[1]s` or
  template tokens are used in `en.json`. `guard-i18n.sh`'s
  format-verb-mismatch check passes across all 4 core locales.
- **Arabic comma:** decoded the edited `ar.json` and `fa.json` values. The
  comma is still U+060C `،`, not a Latin `,`.
- **`currency.Display` safety:** every `CurrencyInfo.Display` in
  `internal/httpx/currency.go` is a plain symbol or word. It is also passed as
  a printf *argument*, never as the format, so even a `%` in it would print
  literally. `html/template` escapes the result. RTL suffix symbols
  (د.إ, ر.س, ریال) render inside the RTL label as normal.
- **Same bug elsewhere:** grepped `web/locales/*.json` for `£ ₺ € $ ¥ ₹ ₽ ₩ ₪`
  and for ISO codes. The only remaining hits are the
  `settings.printer.charset_*` labels ("CP858 — €/£"). Those correctly
  describe which glyphs a printer code page supports. They are not a price
  label.
- **Recurring Dev bugs** (file write without `os.MkdirAll`, cwd-relative path
  instead of `paths.Data`): not applicable. The diff writes no files and uses
  no paths.
- **Manual (#324):** no `web/help/` topic describes the stock-cost field's
  label. The £ hits in `web/help/**` are generic example amounts
  (`reports.md` tips line, `printing.md` "£1.50 per kg") or printer
  code-page explanations (`printing.md` step 8). No help update is needed.

## Cross-repo merge order (not a code finding, an obligation)

This changes the placeholder count of two *existing* keys, so core and the
three packs depend on each other:

- Core's `lang-pack-drift` runs each pack's `main` `check-key-drift.sh`
  against core's local `en.json`. It is red on the core PR until the packs
  land.
- At runtime, a till running new core with an old pack renders
  `Einkaufspreis (£, optional)%!(EXTRA string=€)` for that pack's locale.

Follow reviewer rule 4 as for a new key: merge core first (expect red on
`lang-pack-drift`), then land the de/es/pt pack PRs **in the same cycle**.
Do not ship a release between the two.

## Verified beyond the automated tests

- **TDD re-verified in a separate worktree.** With only the `inventory.html`
  line reverted to `{{ T "inventory.cost_optional" }}`,
  `TestInventoryPage_CostLabelInterpolatesActiveCurrency_ScopedToLabel`
  failed for both subtests with the real assertion:
  `[GBP] stock-cost label = "<label>Cost (%s, optional) <input ...", want it to contain "Cost (£, optional)"`
  (EUR failed the same way). After restoring the line, both tests passed.
- The reviewer also reverted the locale half (`en.json`) on its own.
  Both tests went red, and with finding 1 fixed, so did GBP.
- Sibling packs: confirmed the de/es/pt values are `Preis (%s)` /
  `Einkaufspreis (%s, optional)`, `Precio (%s)` / `Coste (%s, opcional)`,
  `Preço (%s)` / `Custo (%s, opcional)`. The pt pack has a 19-key translation
  gap that predates this change and is out of scope.
- Gate: `go build ./...`, `go vet ./...`, `gofmt -l` (clean), `go test ./...`
  (whole repo), `scripts/ci/guard-i18n.sh` (clean, including format-verb
  parity and duplicate keys), `scripts/ci/guard-data-access.sh` (clean; no
  SQL touched). All green.

## Deferred

None.

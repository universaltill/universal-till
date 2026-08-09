# Code review: catalog import loses dine-in/takeaway VAT — carry tax codes through catimport

**Card:** universaltill/ut-docs#512
**Date:** 2026-08-09
**Complexity:** hard — Dev via a Fable subagent, review via an independent
Opus subagent (isolated worktree). One review round found one blocker
(money/tax-correctness class), fixed inline and re-verified; no second
independent round — the fix was small, mechanical, and precisely scoped
to the finding (a parser input-validation gap), not a design change.

## What shipped

`internal/catimport`'s CSV parser (`Parse`) and the speedy-kasse `.bkp`
parser (`ParseBkp`, ut-docs#511) both now carry an optional tax rate (and
takeaway rate, for Germany's §12 UStG dine-in/takeaway split) per
`ImportItem` — `TaxRateBP`/`HasTax`/`TakeawayRateBP`/`HasTakeaway`, plus
non-blocking `TaxIssue`/`TakeawayTaxIssue` reason codes for a present-but-
unparseable cell (compliance-sensitive, so unlike a missing stock column,
a bad tax cell is warned about, never silently dropped).

`internal/data.CatalogRepo.FindOrCreateTaxCode(ctx, rateBP, takeawayBP *int)`
groups items onto `tax_codes` rows by their (dine-in, takeaway) pair —
idempotent, and the two distinct 19% groups from the issue's own café data
(19%/7% needs a `tax.rate.ask` override, 19%/19% doesn't) correctly land on
two different tax codes despite sharing a dine-in rate. `internal/pages/
import_page.go`'s commit path wires this in per row, then once after the
loop, best-effort merges any genuine override pairs into
`ut-plugin-tax-de`'s `takeaway_rate_overrides` setting via the existing
generic `PluginRepo.GetPluginSetting`/`UpsertPluginSetting` — add-only
(never clobbers a merchant's hand-set override), silently skipped if the
plugin isn't installed, and a failure here warns in the summary line but
never fails the already-committed item rows. Catalog export gains matching
`Tax rate`/`Takeaway tax` CSV columns so export → import round-trips the
pairing. New i18n keys in all four locales; the `web/help/*/catalog.md`
import topic updated in all four locales.

**No ADR needed** (checked explicitly against ADR-0035, "tax-rate
switching logic is a plugin hook"): this diff adds zero switching logic to
`internal/pos` — it only creates tax-code config rows (always core's job)
and writes a plugin's own pre-existing generic settings API. Confirmed by
both the implementer and, independently, the reviewer (`grep` for any
`OrderType`/`order_type` touch in the diff: zero hits).

## Mid-implementation discovery: `.bkp` wiring was missing entirely

While merging this branch onto `main` (which had moved — ut-docs#511's
`.bkp` speedy-kasse importer landed while this branch was in flight), it
became clear `catimport.ParseBkp` didn't read the source's
`TaxPercentage`/`TaxPercentage2` columns at all — meaning the *actual*
real-world motivating case in the issue's own text (a real café's catalog
conversion, which comes in via `.bkp`, not CSV) would have shipped with
this card's own gap unclosed, even though the literal CSV-scoped
acceptance criteria would have been satisfied. Folded into this same
change rather than deferred: `internal/data.ReadBkpProducts` now selects
the two tax columns (falling back to the pre-existing query, same
`no such column` fallback shape `CatalogRepo.ReadLookup` already uses, so
an older `backup.db` predating them still imports cleanly), and
`catimport.ParseBkp` maps them through the exact same `ParseTaxRateBP` /
grouping / override-merge machinery already built for the CSV path — no
new mechanism, one shared `ImportItem` shape regardless of source parser.
TDD throughout (failing compile → failing assertion → pass, at both the
`internal/data` and `internal/catimport`/`internal/pages` layers).

## Independent review (Opus, isolated worktree)

Ran the full gate itself (build, vet, full `go test ./...`, all five
guards), did genuine mutation-based TDD re-verification (three separate
mutations to `FindOrCreateTaxCode`'s NULL-safe lookup, the two-distinct-
19%-groups grouping, and `mergeTakeawayOverrides`'s add-only guard — each
caught with a precise, specific failing assertion, then restored), and
checked ADR-0035, the two recurring bug classes (missing `MkdirAll`,
cwd-relative paths — neither applies, no file writes in this diff),
money/basis-points discipline, i18n completeness and translation quality
in all four locales, and the help-topic accuracy.

**Verdict at first pass: not safe to merge as-is.** One blocking finding.

### Finding B1 — HIGH (tax-correctness defect), fixed

`ParseTaxRateBP`'s guard was `if err != nil || f < 0`. `strconv.ParseFloat`
accepts `"NaN"`, `"Inf"`, `"infinity"` — none of which are `< 0` — so
`int(math.Round(f * 100))` on a `NaN`/`Inf` input silently rounded to
`math.MinInt64` (`-9223372036854775808`). Driven end-to-end through the
real `/api/import` handler: HTTP 200, **zero warnings**, a `tax_codes` row
persisted with that rate, named `Imported --92233720368547758.-8%`,
assigned to the item. A merchant catalog stringified from pandas/a
spreadsheet pipeline that renders a missing value as `"nan"` (a genuinely
common shape) would hit this. `internal/pos.ComputeTaxBasisPoints` would
then compute tax on every sale of that item against a corrupted rate.
`"1900"`/`"1e3"` (a merchant typing basis points, or scientific notation,
where a percentage was expected) similarly sailed through and silently
created a 1900%/1000% tax code.

Fixed: `ParseTaxRateBP` now rejects `math.IsNaN(f)`, `math.IsInf(f, 0)`,
and `f > 100` explicitly (checked *before* the `< 0` comparison, since
`NaN < 0` and `+Inf < 0` are both `false` and would otherwise mask the
bug). Regression coverage added for `NaN`/`nan`/`Inf`/`+Inf`/`-Inf`/
`infinity`/`1900`/`1e3`/`101` (all rejected) and `100` (the legitimate
boundary, still accepted) — reproduced the exact failing output the
reviewer reported before the fix, confirmed the fix rejects all of them,
confirmed `100` itself still passes.

### Finding N1 — folded into the same fix (cheap, same defect class)

A row with a parseable takeaway-tax cell but a *blank* dine-in cell never
reached `FindOrCreateTaxCode` (gated on `it.HasTax`) — the item silently
landed on the till's default rate with no warning, the same silent-VAT-
loss shape this card exists to prevent, via an odd column combination.
Fixed: this case now warns (`import.status.tax_takeaway_only`, new key in
all four locales), with a regression test.

### Deferred, not fixed here — six follow-ups filed as separate Backlog cards

- **N2** — a plugin that's installed but *disabled* silently skips
  populating overrides, with no warning (only "not installed" was
  designed to be silent).
- **N3** — `mergeTakeawayOverrides` is an unguarded read-modify-write;
  two concurrent imports can lose an override entry, last-write-wins.
- **N4** — a failed row can still leave its tax-code id in the overrides
  map, merging an inert entry for a tax code no item actually uses.
- **N5** — `bpToPercent` (`import_page.go`) and `bpToPercentString`
  (`catalog_repo.go`) are byte-identical duplicates across two packages
  that can't share a helper without a new package (`internal/pages`
  imports both `internal/data` and `internal/catimport`; `internal/data`
  can't depend on `internal/catimport`, which already imports `internal/
  data`, without a cycle) — a real structural cost, not a one-line fix,
  so deferred rather than rushed.
- **N6** — a hand-created tax code with an *equal* dine-in/takeaway pair
  (e.g. 19/19, set manually rather than via import) round-trips through
  export → import as a *different* tax-code row than the original
  (semantically identical, no VAT impact, but churns the item's tax-code
  id).
- **N8** — the help topic says a tax/takeaway column is recognised but
  doesn't name the exact accepted headers (`Tax rate`/`Takeaway tax`),
  which a merchant needs to actually build a compliant CSV.

N7 (comma-decimal tax cells warn rather than silently misparse, unlike
price) was reviewed and deliberately left as-is — for this card's target
market, warn-and-default is the *safer* failure mode than guessing.

## Verified beyond the automated suite

- **Mutation-based TDD re-verification, independently, by the reviewer**:
  three separate reverts of the core grouping/merge logic, each caught by
  a specific, real assertion failure (not a generic panic), each restored
  and re-confirmed green.
- **B1/N1 fixes verified in both directions personally** (not just
  claimed): each new test confirmed failing with the reviewer's exact
  reported symptom before the fix, passing after.
- **ADR-0035 boundary check**: `internal/pos` untouched by the diff;
  `grep` for any `OrderType`/`order_type` touch returns zero hits — core
  adds no switching logic.
- **The two recurring bug classes** (missing `os.MkdirAll`, a cwd-relative
  path where `paths.Data(...)` belongs): confirmed not applicable — no
  file writes anywhere in this diff (only `filepath.Join(t.TempDir(), …)`
  in tests), checked by grep, not assumed.
- **Money discipline**: no `money.Money` usage anywhere in the diff —
  basis-point rates stay plain `int`, per this repo's own rule.
- **i18n**: all new keys (`tax_unparseable`, `tax_code_failed`,
  `tax_overrides_not_saved`, `tax_takeaway_only`) present in all four
  locales; `ar`/`fa`/`tr` are genuine idiomatic translations (verified by
  the reviewer against surrounding terminology already in each file), not
  pasted English — Ollama at the homelab NAS was unreachable from this
  sandbox, so translated by hand.
- **No real client/shop name introduced by this diff** — new fixtures are
  all fictitious (Latte, Cappuccino, Espresso, Gift Voucher, …). The
  pre-existing, unrelated `"Haaft 1"` leak (`internal/pages/hold_api.go`)
  is already tracked as ut-docs#521, found independently during #511's own
  review — not re-flagged here, and confirmed this diff adds nothing of
  that kind.
- **Manual**: `web/help/{en,ar,fa,tr}/catalog.md` updated in the same
  branch, merged with #511's own concurrent addition (the `.bkp` mention)
  into one coherent sentence per locale rather than losing either side.
  No screenshot regen needed — no visual/route change, backend column
  recognition only.
- Full `go build ./...`, `go vet ./...`, full `go test ./...` (zero
  failures, run after the #511 merge and again after the B1/N1 fixes) and
  all five guards (`guard-data-access`, `guard-i18n`, `guard-kiosk-engine`,
  `guard-plugin-menu-read`, `guard-help-topics`) green.

## Safe-to-merge verdict

Yes, with the reviewer's blocking finding (B1, `NaN`/`Inf`/out-of-range
tax rates silently corrupting a persisted tax code) fixed and the cheap
N1 follow-on folded in, both independently re-verified. N2–N6, N8 tracked
as new Backlog cards rather than expanded into this diff.

# Code review: §146a Abs. 4 AO notification export (ut-docs#937)

**Date:** 2026-09-19
**Card:** ut-docs#937 (export half of #665's core register)
**Repos touched:** `universal-till` (this PR), `ut-plugin-tax-de` (companion PR), `ut-docs` (contract doc)
**Complexity:** medium — Sonnet built, Opus reviewed

## What shipped

Wires Germany's §146a Abs. 4 AO till/TSE notification register
(`data.FiscalRegisterDEStore`, built in #665) into the existing
`export.requested.ask` plugin-dispatch mechanism already used for DSFinV-K
and DATEV exports:

- **Core** (`internal/pages/data_api.go`): a new `fiscal_register_de` ledger
  on `exportRequestPayload`, gated exactly like `tax_codes`/`items` (entry
  must declare the entity **and** hold `fiscal_register_de:read`). Always
  read from the fixed German tax plugin's own storage namespace
  (`taxDePluginID`), never the requesting entry's own plugin id, since the
  register is one plugin's ADR-0072-owned data, not generic core data.
- **Plugin** (`ut-plugin-tax-de`): a third export entry, `paragraph146a-de`,
  and a new host-independent package `src/paragraph146a` that renders a
  human-readable, German-labelled text summary — grouped by business
  location (gross method), mirroring the pilot's incumbent (certified)
  vendor's own field list/order/labels (ut-docs#665's 2026-08-14 research
  comment). No ELSTER XML (unverified schema, deliberately deferred) and no
  filing on the shop's behalf — data-capture aid only, same as #665.
- **Docs** (`ut-docs`): `reference/plugin-manifest.md` gains the
  `fiscal_register_de` contract section, cross-checked against the actual
  code by the independent review below.

## Independent review

Opus subagent, worktree-isolated (complexity:medium → Opus review). Full
findings in the review's own report; summary and disposition below.

### Blocker — fixed

**B1: the per-sale gather and `maxExportSalesRows` cap wrongly applied to
this entry.** `ut-plugin-tax-de`'s real manifest declares `sales:read` (for
TSE signing) in addition to `fiscal_register_de:read`, so `hasSales` was
true for its `paragraph146a-de` entry — and unlike `eod_closes`, the new
gating block didn't skip the sales gather/cap for it. Proven by the
reviewer with a live probe: a shop with more sales in the selected range
than the cap allows got a flat 400 ("narrow the date range") trying to run
an export that has nothing to do with sale volume, and every shop below the
cap paid to gather+marshal the full sales ledger for nothing.

**Fix:** `wantsFiscalRegisterDE` is now resolved ahead of the sales gather
(same position `wantsEODCloses` already occupies) and the gather condition
extended to `hasSales && !wantsEODCloses && !wantsFiscalRegisterDE`.
TDD-verified: reverted the fix, confirmed
`TestExportDispatch_FiscalRegisterDEEntrySkipsSalesGatherAndCap` fails with
the exact 400 the reviewer predicted, restored the fix, confirmed green.

### Should-fix — all fixed

- **S1** (help manual now false): `web/help/{en,de,tr,ar,fa}/fiscal-register.md`
  said "it has no export yet" — updated in all five locales (same
  structural shape, a new bullet added symmetrically so `guard-help-drift`
  stays honest; the pre-existing `fa`/`tr` baseline drift entry updated to
  its new absolute counts, gap itself unchanged). `make docs-shots`
  re-run — the register page itself didn't change, but the guard hashes
  topic markdown, so the manual-content edit alone required it.
- **S2** (nil vs `[]` collapsed): `paragraph146a.Build` treated a `nil`
  payload (not declared/granted) identically to a genuinely empty register,
  sending an operator with a working install to "add tills" when the real
  fix was a permission grant. `handleParagraph146aExport` now branches on
  `rows == nil` before calling `Build`, with its own distinct error.
- **S3** (in-use count wrong): "Gesamtanzahl der genutzten eAs" counted an
  uncommissioned till as "in use". Now excludes both uncommissioned
  (`CommissionedOn == nil`) and decommissioned rows — both still listed
  individually with their own placeholders, per §146a's own "verwendeten"
  wording.
- **S4** (test gaps): added `TestExportDispatch_FiscalRegisterDEFieldPresentButEmptyInRange`
  (pins the `[]`-not-`null` contract, mirroring `eod_closes`' own test) and
  the B1 regression test above. The `Row`↔`FiscalRegisterDE` tag
  correspondence the reviewer checked by hand remains unverified by an
  automated test — same accepted gap `src/wasmrun` already has for DATEV/
  DSFinV-K (no export-dispatch wasmrun coverage exists for any of the three
  export entries), not newly introduced here.
- **S5** (unassigned registers silently merge): more than one till with no
  assigned location folds into one "(keiner Betriebsstätte zugeordnet)"
  block, understating the real Betriebsstätte count. Now emits an explicit
  warning in that block when it holds more than one entry.

### Nits — addressed where cheap

- Error message now carries the `paragraph146a export: ` prefix `src/datev`
  uses.
- `manifest.json`'s description mentions the new export entry.
- `ut-docs/reference/feature-catalogue.md`'s §146a row updated from "in
  progress (#665)" to reflect what's actually shipped.
- Left as-is (accepted, not newly introduced by this card): `now.UTC()` for
  the "Erstellt am" date/filename (same convention as the rest of this
  plugin); the plugin's own failure message is un-i18n'd (same as DATEV/
  DSFinV-K's own error paths — outside `guard-i18n.sh`'s scope, which only
  scans `universal-till`); the PIN/PUK substring test is a belt-and-braces
  regression guard, the real protection is structural (no such field on
  `Row` at all).

### Confirmed correct as originally written (no change needed)

- **Security claim**: no TSE-PIN/PUK anywhere in the payload, structurally
  (no such field exists), not by redaction.
- **`taxDePluginID` vs `entry.PluginID`**: correct — the register lives in
  one fixed plugin's storage namespace; using the fixed constant is what
  makes a hypothetical second export plugin read the real register instead
  of its own always-empty namespace.
- **JSON tags added to `data.FiscalRegisterDE`**: change no existing wire
  format — the only other consumer is the html/template view in
  `fiscal_register_page.go`, which ignores struct tags entirely.
- Repository pattern, no file writes, i18n reasoning for the exported
  document's own German field labels (ELSTER's own form vocabulary, same
  precedent as DATEV's fixed accounting headers), plugin signing/
  verification untouched.

## Verified beyond automated tests

- Full `go test ./...` (both repos) green, including `ut-plugin-tax-de`'s
  `src/wasmrun` (compiles and runs the real `plugin.wasm`).
- `golangci-lint run ./internal/pages/... ./internal/data/...` — 0 issues.
- Every CI-blocking guard re-run locally and green: `guard-data-access.sh`,
  `guard-i18n.sh`, `guard-compliance-claims.sh`, `guard-help-topics.sh`,
  `guard-help-drift.sh` (baseline updated for the fa/tr fiscal-register
  entry), `guard-docs-shots.sh` (screenshots regenerated via
  `make docs-shots`, 124 shots, all passed).
- `ut-plugin-tax-de`: `scripts/build.sh`, `scripts/validate.sh`,
  `scripts/guard-plugin-i18n.sh`, `scripts/package.sh` all green at v0.6.0.
- TDD re-verification (B1): revert→red→restore→green, as above.

## Safe-to-merge verdict

Yes.

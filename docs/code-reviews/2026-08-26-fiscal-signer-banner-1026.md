# Code review: missing-fiscal-signer Settings banner (ut-docs#1026)

**Branch:** `feature/fiscal-signer-banner`
**Author:** autonomous SDLC pipeline (BA/Architect/Dev/Tester: Sonnet,
inline session + subagents; Review: Opus subagent, independent, no prior
context on the change)
**Scope shipped here:** the Settings-page visibility fix only — narrowed
from #1026's original bundle by this cycle's BA/Architect pass (see the
issue's own comments for that scoping). The core→plugin migration of
`fiscal_register_de`/the §146a AO register is split to #1106; real Pi
hardware verification is split to #1107. `#1026` closes with this PR.

## The gap this closes

A German shop can have a real, cloud-provisioned TSE credential
(`fiscal.tse_configured`/`fiscal.system_of_record` true, via the
ADR-0053 reseller-provisioning flow) while having **zero plugins
installed** — nothing subscribed to `fiscal.sign.ask`, so nothing is
actually TSE-signing sales. Sales still complete (proceed-and-declare:
journal marker, receipt outage notice, operator alert), but nothing in
the UI gave a **persistent, unmissable** signal that the shop wasn't
fiscally signing anything. This is exactly the state the product owner
hit on the test till (ut-docs#1026's original report).

Verified against the real code before scoping (not assumed): the
"auto-install never ran" framing in the original report was wrong —
`setupBasePlugins` deliberately excludes `tax`-type plugins per ADR-0025
Decision 4 (fiscal plugins are prompted, never silently auto-installed).
`ut-plugin-tax-de` is also far more real than ADR-0050 (2026-08-14)
describes — since v0.4.0 it's the till's actual `fiscal.sign.ask`
subscriber, confirmed against a live fiskaly sandbox. The real,
still-open gap was narrower than the original report: nothing connects
TSE credential provisioning to plugin install, and nothing tells the
merchant. Full research trail is in the BA/Architect comments on
ut-docs#1026.

## What shipped

- **`internal/pages/fiscal_signer_banner.go`** (new) —
  `missingFiscalSigner(ctx, d, country)`: read-only detection, never
  touches `EvaluateGate`/`KeyTSEFailingSince`/override state. Returns
  true when `fiscal.RequiresHardGate(country)` and
  `fiscal.IsSystemOfRecord` and (`!ActiveHookOwner(fiscal.sign.ask)` OR
  `HasBrokenActivePluginForEvent(fiscal.sign.ask)`).
- **`internal/fiscal/fiscal.go`** — added exported `IsSystemOfRecord`,
  a thin wrapper over the existing unexported `boolSetting`/
  `parseBoolSetting` used by `EvaluateGate` itself, so this banner and
  the gate can never parse the same setting two different ways. Purely
  additive; `EvaluateGate` itself is byte-for-byte unchanged.
- **`internal/pages/settings_page.go`** — wires the check in next to the
  existing `tseProvisioning` load, reading country via
  `d.CurrentState().Country` (the same source `evaluateFiscalGate` uses,
  not a second settings read), and logs on error rather than swallowing
  it silently.
- **`web/ui/pages/settings.html`** — a non-dismissable warning block
  (reusing the existing `.notice-block-warn`/`.row-warn-icon` component
  from `import_page.go`'s currency-confirmation prompt, not a new CSS
  pattern) right after the `tseProvisioning` chip, with a `helpLink
  "sell"` hint and a link to `/plugins`. Deliberately no dismiss control
  — this describes a live compliance gap, not a transient in-flight
  state, and disappears on its own once resolved.
- **i18n**: 3 new keys in all 4 core locales (`ar`/`en`/`fa`/`tr`).
  German/Spanish translations for the same 3 keys are written up in this
  PR's description for whoever lands the `ut-plugin-language-{de,es}`
  follow-up once this merges to `main` (lang-pack-drift is blocking on
  `main`, not on this PR — can't push the pack update before core's
  `main` carries the keys it would be checked against).
- **`web/help/{ar,en,fa,tr}/sell.md`** — a new subsection on the
  no-signer-installed state.
- Tests: `fiscal_signer_banner_test.go` (table test, 9 cases) +
  `settings_page_test.go` (render-level test, real DB, asserts the
  banner markup and its absence, plus the negative "no dismiss control"
  assertion).

## Independent review (Opus, isolated context) — 2 blockers found and fixed, re-verified

The first review pass, before I saw its output, found two real, silent
correctness gaps and 8 smaller findings:

1. **Blocker — truthy-parsing divergence.** The original
   `missingFiscalSigner` compared `sor != "true"` literally, but
   `fiscal.system_of_record` is written through the *generic* raw
   settings editor with no normalization, and `EvaluateGate` itself
   accepts `"1"`/`"on"`/mixed case/whitespace via `parseBoolSetting`. Five
   of six real truthy spellings made the gate treat a shop as
   system-of-record while the banner stayed hidden — the exact
   visibility hole this card exists to close, failing silently. **Fixed**
   by exporting `fiscal.IsSystemOfRecord` so the banner can't parse the
   setting differently than the gate ever again, by construction. Added
   test cases for `"1"`, `"on"`, `"True"`, `" true "`.
2. **Blocker — a broken signer plugin hid the banner.** `ActiveHookOwner`
   only checks `is_active`, which a wasm-load failure
   (`WasmRuntime.Sync`) deliberately leaves untouched even after flipping
   `install_state='broken'` (ut-docs#368's own scenario). A shop that
   *did* install the plugin, whose binary then broke, would see no
   banner while every sale went unsigned — the single highest-value case
   this card exists to catch. **Fixed** by also calling
   `HasBrokenActivePluginForEvent` (the same check `tax_hook.go` already
   uses for this exact problem class) and OR-ing it in. Added a test
   seeding an active-but-broken signer plugin.
3–8 (non-blockers, all applied): language-pack drift text prepared for
   the follow-up (see above); settings-read error now logged, not
   swallowed; country read via `d.CurrentState()` instead of a second
   settings lookup, matching the gate's own source; the literal
   `"fiscal.sign.ask"` string replaced with the existing
   `fiscalSignAskEvent` constant; a `helpLink "sell"` hint added per this
   repo's page-ownership convention; two screenshot PNGs that changed
   only from PNG-encoder nondeterminism (no real content change) reverted
   out of the diff; a test comment's overclaim about matching
   `001_init.sql`'s DDL corrected.

## TDD claim re-verified personally

Both blocker fixes were TDD'd: the new test cases were added and
confirmed failing against the pre-fix code first (`= false, want true`
for both), then the fix applied, then confirmed passing. I re-ran the
full targeted suite myself after the fix pass, independently of the
fixing agent's own report:

```
go test ./internal/pages/... ./internal/data/... ./internal/fiscal/... \
  -run 'FiscalSigner|EvaluateGate|FiscalSignHook|SettingsShowsFiscalSigner' -v
```

All 9 `TestMissingFiscalSigner` subtests pass (including the 4 new
truthy-variant cases and the new broken-plugin case), the render-level
`TestSettingsShowsFiscalSignerMissingBanner` passes, and all 10
`TestEvaluateGate_*` subtests pass unmodified — confirmed via
`git diff -- internal/pages/fiscal_sign_hook.go
internal/pages/fiscal_gate_test.go internal/pages/fiscal_sign_hook_test.go
internal/pages/pos_api.go` returning empty, and `git diff
internal/fiscal/fiscal.go` showing only the new additive
`IsSystemOfRecord` function with zero lines changed inside `EvaluateGate`
itself.

## Full gate (re-run personally after the fix pass, not taken from any subagent's report)

- `go build ./...`, `go vet ./...`, `gofmt -l .` — clean.
- `go test ./...` (full repo) — all packages pass.
- `scripts/ci/guard-i18n.sh`, `guard-data-access.sh`,
  `guard-compliance-claims.sh`, `guard-docs-shots.sh`,
  `guard-help-topics.sh` — all pass.

## Deferred (separate cards, not silently dropped)

- **ut-docs#1106** — move `fiscal_register_de`/the §146a AO register out
  of core into `ut-plugin-tax-de`, per ADR-0050's own boundary table.
  Needs Architect + a data-migration plan; independent of this change.
- **ut-docs#1107** — real Pi hardware verification of this banner and the
  end-to-end signing path. `blocked:env`, depends on this PR merging
  first.
- **ut-docs#1112** — the sibling `tseProvisioning` banner has the
  identical text-clipping bug this card's banner had before Tester's
  fix (`.chip`'s `white-space: nowrap` inside an `overflow: hidden`
  card). Pre-existing, untouched by this diff, filed separately.
- **`ut-plugin-language-de`/`-es`** — the 3 new i18n keys need the
  German/Spanish translations above added once this merges to `main`
  (lang-pack-drift is blocking on `main`, not on this PR).

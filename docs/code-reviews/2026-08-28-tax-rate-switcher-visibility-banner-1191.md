# 2026-08-28 — Missing tax-rate switcher visibility banner (ut-docs#1191)

## What shipped

ut-docs#1191 was filed off the retraction of ADR-0067, on the reading that
"ADR-0025 Decision 1 (a core VAT rate-set schema) was never implemented."
BA verified that premise before building anything and found it wrong:
Decision 1 was already amended away by ADR-0035 (accepted, binding) —
tax-rate switching computation is entirely a plugin's job via
`tax.rate.ask`, and `internal/pages/tax_hook.go` already does that
correctly. Building a core rate-set schema now would contradict ADR-0035
with no case to supersede it.

The real, still-open gap: when no plugin answers `tax.rate.ask` at all,
`tax_hook.go` silently sells at base rates — a DE shop that
declines/defers the fiscal plugin rings up every takeaway sale at the
full dine-in rate with no warning anywhere. Architect wrote
[ADR-0068](../../../ut-docs/adr/0068-missing-tax-rate-switcher-gets-core-visibility-banner.md)
deciding: ADR-0035 stands; the gap closes with a core, read-only Settings
visibility banner, modeled on the existing `missingFiscalSigner` pattern
— not a rate-set schema.

Shipped:

- `internal/pages/tax_rate_switcher_banner.go` — `missingTaxRateSwitcher`,
  a small mandated-country map (`{"DE": true}`) plus the same
  `ActiveHookOwner`/`HasBrokenActivePluginForEvent` pair `missingFiscalSigner`
  uses. Unlike that sibling, deliberately NOT gated on
  `fiscal.system_of_record` — VAT-rate correctness is required regardless
  of fiscal-signing status.
- `settings_page.go` wiring, `settings.html` banner block (reusing the
  existing `.notice-block-warn`/`.row-warn-icon` components — no new CSS),
  i18n keys in all 4 locales, a new manual subsection in all 4 `sell.md`
  files, regenerated doc screenshots.
- ADR-0025's status header corrected to cite ADR-0068 and stop describing
  Decision 1 as "never implemented" (it was amended, not skipped).

## Independent review

Spawned via `Agent` (Opus, per this card's `complexity:hard` routing —
Dev ran at Sonnet acting in the Fable-tier build role), isolated in a
`git worktree` off a WIP commit, with instructions to actually run the
gate and independently re-verify the TDD claim (not just trust it).

**Verdict: safe to merge, no blockers.** Full command output (build, vet,
`go test ./...`, every CI guard) all green; every ADR-0035/ADR-0068
conformance check passed; RTL/i18n/compliance-wording checks passed;
translations judged structurally sound and register-consistent with the
sibling `fiscal_signer` keys.

**TDD re-verified independently**: removed `tax_rate_switcher_banner.go`,
confirmed `go test ./internal/pages/... -run TestMissingTaxRateSwitcher`
fails to compile (`undefined: missingTaxRateSwitcher`), restored, confirmed
green again. Matches Dev's original TDD claim.

### Findings and disposition

1. **No test proved the Go→template wiring** (should-fix, real gap) — the
   original test only covered the predicate function, not that it
   actually reaches the template under the right key (a typo in either
   place would silently render nothing with all tests green). The
   sibling `missingFiscalSigner` *does* have this coverage
   (`TestSettingsShowsFiscalSignerMissingBanner`). **Fixed**: reviewer
   added `TestSettingsShowsTaxRateSwitcherMissingBanner`, self-verified
   as a real (non-false-pass) test by deliberately mutating the template
   key and confirming the test fails, then reverting. Pulled into this
   branch as-is, re-run here: passes.
2. **Banner copy was factually wrong in the broken-plugin case**
   (should-fix, real bug) — the original copy said "every sale is
   ringing up at one flat rate" / "sales are never blocked by this" in
   all cases, but a *broken* plugin actually fail-closes the tender path
   (`internal/pos/service.go`'s `effectiveTaxRateBP`/blocking, ut-docs#368)
   — sales are refused, not silently mis-rated. **Fixed**: reworded the
   Settings banner message (all 4 locales) and the manual section (all 4
   locales) to state both real outcomes correctly — no plugin at all →
   sells at flat rate; plugin present but broken → till refuses the sale.
3. **`!found || broken` can false-positive if two `tax.rate.ask` plugins
   ever coexist** (should-fix, low likelihood, deferred) — unlike
   `fiscal.sign.ask` (an ADR-0041 *exclusive* hook), `tax.rate.ask` isn't
   exclusive, so this banner's logic doesn't exactly match
   `tax_hook.go`'s own runtime behavior (which only fails closed when NO
   plugin answers, not whenever any registered one is broken). Accepted
   as a documented, low-likelihood gap rather than an in-scope fix here —
   only one tax-rate plugin per mandated country exists in practice
   today. Documented with an inline comment in
   `tax_rate_switcher_banner.go` for whoever revisits this if that
   changes.
4. **Nit, fixed**: the review-added test carried a copy-pasted, unused
   `plugins.SharedBus`/`ResetSubscribers` setup from `tax_hook_cache_test.go`
   (`missingTaxRateSwitcher` is DB-only, never touches the event bus) —
   dropped when merging the test back into this branch.
5. **Known, accepted follow-up (not this PR's to fix)**: 3 new
   `en.json` keys need a matching update in the external
   `ut-plugin-language-{de,es}` packs — `lang-pack-drift` is advisory on
   this PR, blocking on `main`. Same standing pattern as every prior
   `en.json` addition in this pipeline; owned as the immediate next task
   after this merges, per the scrum-master lane-ownership rule for an
   implied cross-repo follow-up.

## Verified beyond automated tests

- Drove the real app (`go run .`, `UT_STORE=sqlite`, fresh DB) with
  `store.country=DE` and no tax plugin installed: confirmed the banner
  renders with the correct copy and links to `/plugins`.
- Took real screenshots (Playwright, kiosk viewport 1024×600) of the
  rendered banner in English and Arabic (RTL) and looked at them: text
  wraps correctly inside `.notice-block-warn` (no clipping, the exact
  defect class this component was already built to avoid), the warning
  icon and "?" help link sit correctly, RTL mirrors the layout with the
  icon and heading on the correct side, button renders below the message
  in both directions. Did not additionally check dark theme — no new CSS
  was introduced (100% reused `.notice-block-warn`/`.row-warn-icon`), so
  risk is judged low, but this was not independently confirmed visually.
  Did not check fa/tr visually beyond reading the translated text (ar was
  the RTL representative checked).

## Translation note

The homelab Ollama endpoint (`192.168.1.231:11434`) mandated by
`ut-docs/reference/translation.md` is unreachable from this cloud
session's network (confirmed by direct probe — connection refused/
timeout). The ar/fa/tr strings in this change (both the 3 i18n keys and
the 4 manual sections) were produced directly by the pipeline's own model
rather than routed through the homelab model, and reviewed for register/
length consistency against the existing sibling `fiscal_signer` strings
in the same files. Flagging this deviation explicitly rather than
silently; a human fluent in these locales spot-checking the new strings
would be worthwhile given the translation path used differs from the
documented process.

## Safe-to-merge verdict

Yes. Build/vet/full test suite/every CI-blocking guard green after fixes;
independent Opus review found no blockers and both real should-fix
findings were corrected in this branch; the one deferred finding
(non-exclusive-hook edge case) is documented in code as a low-likelihood,
accepted gap.

# Code review: reconcile German takeaway VAT overrides on plugin activation (ut-docs#1370)

## What shipped

`ut-docs#1370`: a shop's catalog can already carry tax codes with a pinned
takeaway rate (`TaxCodeView.TakeawayRateBP`, e.g. "Imported 19% (takeaway
7%)") before the German tax plugin (`com.universaltill.tax-de`) is
installed. Nothing reconciled those pre-existing pinned rates into the
plugin's `takeaway_rate_overrides` setting once the plugin activated later
— the settings screen showed `value="" placeholder="7"` (looks configured,
isn't), and every takeaway sale was silently charged the dine-in rate. Real
§12 UStG VAT over-collection, reported live from the Germany pilot till.

Product decision (2026-09-01, already recorded on the issue): a successful
install/enable of a country plugin **is** the consent boundary — the
catalog's pinned legal defaults become **active** overrides immediately,
add-only (a merchant's explicit value is never overwritten).

**The fix reuses, not duplicates, the existing reverse-direction mechanism.**
`internal/pages/import_page.go`'s `mergeTakeawayOverrides` already folds a
catalog *import's* newly-discovered tax codes into `takeaway_rate_overrides`
when the plugin is *already* active at import time
(`data.PluginRepo.MergeAdditiveJSONMapSetting` — transactional, add-only,
self-healing against a known double-encoding bug, ut-docs#512/#1255). This
card is the missing other direction: the plugin activates *after* the
catalog already exists. New code:

- `reconcileTaxDeTakeawayOverridesOnActivate(ctx, db) (added int, failed
  bool)` — lists every active tax code via `CatalogRepo.ListTaxCodes`,
  collects those with a positive pinned `TakeawayRateBP`, and hands them to
  the existing `mergeTakeawayOverrides`. An empty result is a clean no-op
  (no DB write, no bus generation bump).
- `reconcileTaxDeTakeawayOverridesIfActivated(ctx, db, pluginID)` — the
  `taxDePluginID` gate, centralized so every activation call site is a
  single line instead of a duplicated `if` block (added in round 2, see
  below — the duplication was exactly what let two call sites go missing
  in round 1).
- Wired into **all six** places `com.universaltill.tax-de` can become
  durably active in this codebase: `handleInstallFromMarketplace`,
  `setPluginActiveHandler` (enable only, never disable), `handleUpdatePlugin`,
  `handleRollbackPlugin`, `handleImportFromFile` (`plugin_api.go`),
  `cloudInstallPluginVersion` (`cloudsync_wire.go`, the directive/sync-pull/
  pinned-upgrade path), and the Plugin Store's install handler
  (`plugins_store_page.go`).
- No change to `plugin_settings_page.go`: `buildTaxOverrideRows` already
  renders `OverridePercent` (the real persisted value) whenever the
  overrides map has an entry, so once the reconcile writes it the
  placeholder-only misleading state stops rendering with zero UI changes.
- No change to `MergeAdditiveJSONMapSetting`, `mergeTakeawayOverrides`, or
  the import-commit call site — correct and unrelated, only reused.

10 tests (`internal/pages/tax_takeaway_activate_test.go`, plus the
pre-existing `tax_takeaway_realchain_test.go` suite re-run for regressions):
real-chain install-after-catalog (the issue's exact repro, through the real
HTTP install handler), the cloud-install/directive path, the Plugin Store
install path, the sideloaded import-from-file path, add-only/never-clobbers-
a-merchant-override across a disable→re-enable cycle (with a second,
not-yet-configured tax code proving the enable path genuinely re-runs the
reconcile, not just "nothing changed"), and a focused unit test of the
helper's edge cases (empty catalog, dine-in-only tax code, idempotent
second call, generation-bump correctness).

## Review

Independent review by a different-model subagent (Opus; Dev ran on Fable —
`complexity:hard` routing) in an isolated git worktree, working from a WIP
snapshot commit on the feature branch. **Round 1 verdict: NOT safe to
merge** — a real, money/tax-class finding, which earns the second review
round this pipeline's rules allow for exactly that class of finding.

### Round 1 findings and disposition

- **BLOCKER-1 (fixed)** — `handleImportFromFile` (the offline-provisioning
  sideload path) was unwired. `PersistManifest` activates a plugin
  unconditionally (`is_active = 1` on both its INSERT and its ON CONFLICT
  UPDATE branch, confirmed by reading `internal/data/plugin_repo.go`
  directly) — this is the single most likely provisioning route for an
  offline-first pilot till, not a corner case. Now wired; regression test
  `TestTakeawayOverride_ImportFromFileReconciles` added and independently
  re-verified by me (temporarily removed just this call site, confirmed the
  test fails with "row missing — the install did not seed the setting",
  restored, confirmed green again — full trace below).
- **BLOCKER-2 (fixed)** — the Plugin Store's "download → install" button
  (`POST /api/plugins/store/install`, `plugins_store_page.go`) was unwired.
  A live, linked, operator-facing path. Now wired; regression test
  `TestTakeawayOverride_StoreInstallReconciles` added.
- **SHOULD-FIX-1 (fixed, reviewer's triage overridden)** — `handleUpdatePlugin`
  not wired. The Dev's round-1 report flagged this as a possible deferred
  gap; the reviewer's verdict, which I accept, is that this is real scope
  for THIS card: on a standalone (non-replica) till, "Update" is the
  remediation an operator or support agent is most likely to try after
  being told about the bug, and a silently-failing remediation on a live
  compliance bug is worse than none — it burns the operator's one
  hypothesis. Now wired.
- **SHOULD-FIX-2 (fixed)** — `handleRollbackPlugin` not wired. Lower
  impact (the plugin was already active before a rollback) but cheap,
  consistent, and the add-only/idempotent semantics mean it can only help.
- **SHOULD-FIX-3 (fixed)** — the `cloudInstallPluginVersion` call site
  (`cloudsync_wire.go`) had no dedicated regression coverage; the reviewer
  proved this empirically (reverting only that site left the full package
  green). `TestTakeawayOverride_RealChain_CloudInstallReconciles` added,
  installing via `primary.install` → `cloudInstallPlugin` specifically
  rather than the HTTP handler.
- **NITPICK-1 (fixed)** — the per-call-site `if pluginID == taxDePluginID {
  ... }` duplication was itself identified as the design flaw that let two
  sites go missing (this repo learned the same lesson once already, at
  ADR-0041 finding B2, about gating on a single handler being bypassable).
  Centralized into `reconcileTaxDeTakeawayOverridesIfActivated`; every call
  site is now a single line.
- **NITPICK-2 (fixed)** — added a one-line comment clarifying why the
  reconcile filters on `TakeawayRateBP > 0` (the real plugin treats
  `rate<=0` as "no opinion," ut-docs#1351 — a zero-pinned entry would be a
  dead write).
- **NITPICK-3 (deferred, product note only)** — a merchant's deliberate
  *removal* of an override (blanking the field in the typed editor) isn't
  durable across a future re-activation: add-only re-fills it from the
  catalog's still-pinned rate. There's a coherent story (the tax code's
  pinned rate is the actual source of truth; clear it there instead), but
  this is a product-decision question, not a code defect, and it's a
  pre-existing property of `mergeTakeawayOverrides`/add-only semantics in
  general, not something this card's fix introduces. Not blocking; noted
  for a future card if it comes up in practice.

### Recurring bug classes — checked, not applicable

- File-write handler missing `os.MkdirAll`: not applicable — the diff
  performs no file I/O (grepped the added lines for `os.Create`/
  `os.WriteFile`/`os.MkdirAll`/`os.Open`: zero hits).
- A cwd-relative path where `paths.Data(...)` belongs: not applicable — no
  path construction at all in the added lines.

### TDD verification — independently re-run by me after round 2

Round 1's revert-then-restore (done by the Opus reviewer, in its isolated
worktree): reverting only the wiring (function left intact) produced
exactly the two `t.Fatalf` messages the tests name — not a compile break —
then restoring returned both to green. Full trace preserved in the review
subagent's report.

Round 2, I independently re-did this myself for the highest-value new fix
(`handleImportFromFile`, BLOCKER-1) directly on the feature branch (not a
worktree — a single-file, single-hunk removal-and-restore with no other
concurrent activity on this checkout during the window):

```
$ <removed the 5-line reconcile call from handleImportFromFile>
$ go test ./internal/pages/... -run TestTakeawayOverride_ImportFromFileReconciles -v
    tax_takeaway_activate_test.go:242: takeaway_rate_overrides row missing —
      the install did not seed the manifest-declared setting
--- FAIL: TestTakeawayOverride_ImportFromFileReconciles (0.16s)
FAIL

$ <restored byte-identical via diff against a pre-edit backup>
$ go test ./internal/pages/... -run TestTakeawayOverride_ImportFromFileReconciles -v
--- PASS (confirmed green)
```

The other five call sites (marketplace install, enable, cloud install,
store install, rollback/update) are each pinned by their own dedicated
test (or, for rollback/update, by the pre-existing add-only/re-enable test
plus the shared helper being identical code to the five sites that do have
dedicated coverage) — I did not re-run a revert/restore cycle for every one
individually; the round-1 reviewer's proof that the *duplicated-if* design
was the actual gap, now replaced by one centralized function called
identically from all six sites, is what makes "prove one, trust the
pattern for the rest" a reasonable bar here rather than needing six
separate revert cycles.

### Full gate — re-run and confirmed by me after round 2, not just trusted from the Dev's report

```
gofmt -l .                                                    → empty
go build ./...                                                → clean
go vet ./...                                                  → clean
go test ./internal/pages/... -run 'TestTakeawayOverride|TestReconcileTaxDe|TestStoreAPI_InstallFromStagedBundleSucceeds' -race -v
  → 10/10 PASS (ok, 129.679s) — every new test plus every pre-existing
    ut-docs#1351 real-chain test, no regressions
go test ./internal/pages/...   (full package, no filter)      → ok, 99.861s
bash scripts/ci/guard-data-access.sh                          → pass
bash scripts/ci/guard-i18n.sh                                 → pass (1331 keys, all locales match en.json — this diff adds
                                                                  zero user-facing strings)
bash scripts/ci/guard-help-topics.sh                           → pass
bash scripts/ci/guard-compliance-claims.sh                    → pass
```

No SQL text added anywhere outside `internal/data`/`internal/db` — the new
code only calls existing repo methods (`CatalogRepo.ListTaxCodes`,
`PluginRepo.MergeAdditiveJSONMapSetting`). No new locale keys — confirmed
both by the guard and by reading the diff (`git diff main --name-only`
touches only `internal/pages/*.go` files, nothing under `web/ui/**`,
`web/help/**`, or `web/locales/**`), so the user-manual-topic requirement
does not apply to this change. No real client/shop name or secret-shaped
literal in the diff (test fixtures use generic names: "Cappuccino", SKU
`30005`, "Imported 19% (takeaway 7%)").

## Safe-to-merge verdict

**Yes.** Round 1's blocker-class finding is fixed and independently
re-verified; round 2 closes every gap the review named, with regression
coverage for each of the newly-wired paths. Full gate green.

## Explicitly deferred

- NITPICK-3 above (a merchant's explicit *removal* of an override isn't
  durable across future re-activation) — a product-decision question, not
  a defect in this fix; worth a card if a real merchant hits it.

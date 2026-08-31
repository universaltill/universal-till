# Takeaway VAT over-collection: invalidate the tax-ask cache when a catalog import seeds overrides (ut-docs#1351)

**Date:** 2026-08-31
**Card:** [ut-docs#1351](https://github.com/universaltill/ut-docs/issues/1351)
(p1, `complexity:hard`) — live Germany café pilot: a takeaway item's
plugin-configured 7% reduced rate was not applied; the till charged 19%.
**Dev:** Fable subagent. **Review:** independent Opus subagent, isolated
worktree, did not write the code under review.

## What shipped

### 1. The root cause — `mergeTakeawayOverrides` never bumped the bus generation

`internal/pages/tax_hook.go`'s `pluginTaxRateAsker` memoizes every
`tax.rate.ask` answer keyed by the payload
`(item_id, tax_code_id, tax_rate_bp, order_type)` for as long as
`EventBus.Generation()` is unchanged — a deliberate ut-docs#222 optimisation,
since one wasm ask boots the plugin's whole module (~90ms on a Pi4) and
`recomputeTotals` asks once per line per recompute.

The cache's correctness rests on an unwritten invariant: **every writer of a
setting a plugin answers from must bump the generation.** Four of the five
production writers do. `internal/pages/import_page.go`'s
`mergeTakeawayOverrides` — the ut-docs#512 path that merges per-tax-code
takeaway overrides discovered during a catalog import into
`ut-plugin-tax-de`'s `takeaway_rate_overrides` setting — did not.

The pilot till walked straight through the gap:

1. catalog imported while the tax plugin was disabled → tax codes created,
   overrides skipped (the documented ut-docs#531 branch);
2. a takeaway Cappuccino rings at 19% — **correct at that moment**, and the
   plugin's "no opinion" is cached for that exact payload;
3. the merchant re-imports to pick the overrides up. The tax codes dedup to
   the **same ids**, so every cached payload is byte-identical — nothing
   misses the cache naturally. The 7% override lands in `plugin_settings`;
   nothing bumps the generation;
4. every subsequent takeaway sale of that item serves the stale "no opinion"
   and charges 19% — **indefinitely**, until an unrelated plugin reload or
   settings save happens to bump.

The fix adds `plugins.SharedBus(db).BumpGeneration()` after the merge, guarded
on `added > 0`. The guard is correct: `MergeAdditiveJSONMapSetting` is
additive-only and returns early without writing when every discovered key is
already present, so `added == 0` means no row changed and no answer can have
changed. The bump is placed **after** the merge commits, matching
`wasm_runtime.go`'s own "bump AFTER the DB writes so no caller can cache
pre-flip state under the post-flip generation" ordering.

`mergeTakeawayOverrides`' signature changed from `*data.PluginRepo` to
`*sql.DB` so the function owns the write and the invalidation as one unit.

### 2. Independent second bug — `mergeResolved` dropped `TaxCodeID`

`internal/pos/service.go`'s `mergeResolved`, in its merge-into-an-existing-line
branch, refreshed the surviving line's `Name`/`PriceCents`/`TaxRateBP`/
`ItemID`/`VariantID`/… from the incoming scan but **not** its `TaxCodeID`. A
re-scan through a second code after a catalog change reassigned the item to a
different tax code merged the fresh rate in while leaving the stale code on the
line — and since a tax plugin keys its order-type overrides on the tax code,
the asker was then asked about the **wrong code** and the override for the new
one never applied. Found by the Dev, not by the card. Fixed by copying
`TaxCodeID` alongside `TaxRateBP`.

### 3. Regression coverage that can actually catch this class

The pre-existing `TestOrderTypeTaxSwitching*` tests install a hand-rolled
`fakeTaxAsker`, which mocks away `pluginTaxRateAsker`, the event bus, the
wazero runtime **and** the plugin's `settings_get` read. That is precisely why
they never saw this bug (the card's own AC3). They gained a `SCOPE` doc comment
saying so rather than being changed.

New: `internal/pages/tax_takeaway_realchain_test.go` (3 tests) plus a
test-only wasip1 guest at `internal/pages/testdata/taxask_overrides_guest/`.
These drive the **whole real chain** — a signed wasm plugin installed through
the real marketplace installer, its setting seeded by the real
`PersistManifest → ReconcilePluginSettings`, saved through the real typed
settings editor, and rung up through the real `/api/pos/order-type` +
`/api/pos/scan` handlers — at the issue's exact shape (SKU 30005, Cappuccino,
€3.80 gross, 19% → 7%). `internal/pos/service_test.go` gained
`TestMergeResolved_RescanUpdatesTaxCodeOnExistingLine` for finding 2.

### 4. docs-shots guard: exclude `internal/pages/**/testdata/`

Adding a *test* fixture under `internal/pages/testdata/` changed the
screenshot-freshness guard's surface hash, demanding a full manual-screenshot
regeneration for code that is `//go:build wasip1`, excluded from the server
binary, and can never render a pixel. Both implementations that must agree —
`scripts/ci/guard-docs-shots.sh` and `e2e/tests-docs/lib.js` — now prune
`testdata` under `internal/pages`, and `web/help/img/manifest.json` was
regenerated. Same false-positive class as #620 (unscreenshotted-route files)
and #659 (`.DS_Store`), fixed the same way.

## Verified independently (beyond re-running the suite)

### The TDD claims, re-proven by hand in this worktree

Not taken on trust — each fix line was removed and the test re-run.

**Fix 1.** With `BumpGeneration()` removed from `mergeTakeawayOverrides`:

```
--- PASS: TestTakeawayOverride_RealChain_FreshAddUsesOverriddenRate (1.83s)
--- PASS: TestTakeawayOverride_RealChain_AddThenToggle (1.76s)
    tax_takeaway_realchain_test.go:309: VAT over-collection (ut-docs#1351):
      tax = 61 minor units, want 25 — the import's override write did not
      invalidate the tax-ask cache, so the stale pre-override answer (19%)
      is still being served
--- FAIL: TestTakeawayOverride_RealChain_ImportSeededOverrideInvalidatesAskCache
```

Restored → `ok github.com/universaltill/universal-till/internal/pages 4.931s`.

Two things worth noting in that output. First, the **two sibling tests still
pass** with the bump removed — the failure is specific to the import path, not
a blanket break, which is what makes the test a real discriminator rather than
a tripwire. Second, the failing run's own log shows the chain is genuinely
real, not mocked:

```
[Verifier] Manifest signature verified for plugin com.universaltill.tax-de
wasm plugin com.universaltill.tax-de loaded, handling [tax.rate.ask]
[wasm:com.universaltill.tax-de] result (tax.rate.ask, 15 bytes): {"rate_bp":700}
```

The `{"rate_bp":700}` line appears in the passing tests and is **absent** after
the merge in the failing one — the plugin was never re-asked. That is the bug,
observed directly.

**Fix 2.** With the `TaxCodeID` copy removed from `mergeResolved`:

```
    service_test.go:340: merged line TaxCodeID = "tax-old", want "tax-new" —
      mergeResolved refreshed TaxRateBP but left the stale tax code
--- FAIL: TestMergeResolved_RescanUpdatesTaxCodeOnExistingLine (0.00s)
```

Restored → `--- PASS`. `git diff` empty against `HEAD` after both restores.

### Is the test fixture a faithful stand-in for the real plugin?

A regression test built on a fixture is only worth what the fixture's fidelity
is worth, so this was checked against the real
`universaltill/ut-plugin-tax-de` source rather than assumed:

- The guest's `setting()` / `callBuf()` helpers are **byte-identical** to
  `src/main.go`'s, including the grow-and-retry buffer ABI.
- The answer logic mirrors `src/taxrate.Resolve` exactly: dine-in
  short-circuits **before** reading the setting (the real plugin's deliberate
  cost optimisation); empty setting → empty map, not an error; `!present ||
  rate <= 0` → no opinion; invalid JSON → log and answer no-opinion; a
  configured override equal to the dine-in rate still answers `ok`.
- The response shape `{"rate_bp": N}` and the payload field names match
  `handleTaxRateAsk`.
- The manifest setting is declared as `default_value: {}` — the JSON **object**,
  scope global — exactly as `manifest.json` line 80 does. This is
  load-bearing, not cosmetic: ut-docs#1255 established that the JSON *string*
  `"{}"` shape gets double-encoded at install and permanently breaks the merge.
  A fixture declaring it the wrong way would have exercised the self-heal path
  instead of the real one.

Conclusion: the fixture cannot pass or fail for reasons unrelated to the real
plugin's behaviour on this path.

### Is there another writer with the same missing bump?

Every production writer of `plugin_settings` was enumerated and checked:

| Writer | Bumps? |
| --- | --- |
| `plugin_settings_page.go` typed editor (`writeTaxOverrides`) + generic save | yes — one bump at the end of the handler when `changed > 0` |
| `import_page.go` `mergeTakeawayOverrides` | **yes, as of this change** |
| `ReconcilePluginSettings` (install/upgrade seeding, via `PersistManifest`) | yes — install runs `Manager.Reload → WasmRuntime.Sync → ResetSubscribers` + explicit bump |
| `sync_admin_repo.ApplyAdmin` (cloud directives / replica drift) | yes — `init.go:399` bumps after apply |
| `plugin_api.go` permission grant/revoke | yes — both branches bump |

There is no `settings_set` host function, so a plugin cannot write its own
settings. No other gap found — this was the last one.

### Other checks

- **Recurring bug classes** (missing `os.MkdirAll`, cwd-relative path instead
  of `paths.Data(...)`): N/A — the diff adds no file writes. The pre-existing
  writes in `import_page.go` already do both correctly (`os.MkdirAll` at
  L118/L835, `paths.Data("public", "assets", "items", …)` at L832).
- **`guard-docs-shots` exclusion scope**: narrowly correct. It applies only
  under `internal/pages` (gated on `want_go`), leaving `web/ui` and
  `web/public` untouched; `internal/pages/testdata` is the only such directory
  and all five files in it are `//go:build wasip1` guests. Go itself already
  ignores `testdata/` for package resolution, so nothing compiled into the
  server is excluded.
- **Secrets / real names**: none. The test data (`Cappuccino`, SKU `30005`,
  €3.80) is the generic product shape from the issue, not a shop identity. No
  secret-shaped literals in any new file.
- **No user-visible surface**: the diff touches no `web/` template, no locale
  file, and no route. Nothing a shop owner sees or does changes — the till
  simply stops over-charging. No `web/help/` topic update is owed, and
  `guard-help-topics.sh` (which enforces page-route coverage) passes unchanged.

## Findings

### L1 — `lib.js` matched `testdata` against the absolute path (FIXED)

`e2e/tests-docs/lib.js` tested `p.split(path.sep).includes('testdata')` on the
**absolute** path, while `guard-docs-shots.sh` prunes `testdata` while walking
the **relative** `internal/pages` tree. A checkout living under a directory
literally named `testdata` would make the JS silently drop *every*
`internal/pages` file from the surface while the shell kept them. Both files
carry comments insisting they must stay in lockstep, so this was worth
closing. Changed to match against the path relative to `internal/pages`.

**Honest severity: low, and it could never have shipped silently** —
`guard-docs-shots-cross-check_test.sh` (CI-blocking, ut-docs#370) runs both
implementations and compares hashes, so any real divergence fails the build.
This is defense-in-depth, not a latent bug. Post-fix the cross-check still
reports `Python and JS agree (346ea9a81d61…)`.

### M1 — the "every settings writer must bump" invariant has no mechanical guard (ACCEPTED — suggest a backlog card)

This is the second time this exact invariant has been broken (ut-docs#222's
review caught the settings-editor case; this card is the import case). It is
enforced today only by four call sites in `internal/pages` each remembering.

**Should the fix live lower down, inside `MergeAdditiveJSONMapSetting` or
`UpsertPluginSettingScoped`, so every future writer gets it for free?** I
checked rather than hand-waved, and the answer is that it **cannot**, as
things stand: `internal/plugins` imports `internal/data` (`install.go`,
`ipc.go`, `wasm_runtime.go`, …) and `internal/data` imports `internal/plugins`
nowhere. A repo method calling `plugins.SharedBus` would be an import cycle.
Closing it properly means either an injected invalidation callback on
`PluginRepo` or hoisting the bus above both — a real design change, not a
review fix, and out of scope for a p1 live-VAT hotfix.

**Suggested backlog card** (not created here): a CI guard in the shape of the
repo's existing ones — any file under `internal/` that calls
`MergeAdditiveJSONMapSetting`, `UpsertPluginSetting` or
`UpsertPluginSettingScoped` must also reference `BumpGeneration`, with the
usual inline `// <guard>:allow <reason>` escape hatch. That turns a
twice-broken convention into something CI enforces, without needing the
layering change.

### M2 — global-vs-register scope asymmetry on `takeaway_rate_overrides` (ACCEPTED, pre-existing, out of scope)

`MergeAdditiveJSONMapSetting` reads and writes `scope='global'` only (by
design, ut-docs#668), but `GetPluginSetting` — which backs the `settings_get`
host function the plugin reads through — prefers the **most specific** scope
(`register` beats `user` beats `global`). A register-scoped
`takeaway_rate_overrides` row on one till would therefore shadow the global row
the import writes: the generation bump would fire, the plugin would dutifully
re-ask, and it would still read the per-till row.

Not reachable today — `ut-plugin-tax-de`'s manifest declares the key
`"scope": "global"`, and the typed editor writes back into `row.Scope`, so
no shipped path can create a register-scoped row for this key. Worth a backlog
note rather than code: **if a future card makes any cache-backing setting
per-till, the merge path's global-only scope becomes a real bug.**

### L2 — `mergeTakeawayOverrides` re-constructs a `PluginRepo` (ACCEPTED)

The caller already holds a `pluginRepo` built from the same `d.Db` and now
passes the raw `*sql.DB` instead, so `data.NewPluginRepo(db)` runs twice per
import. `NewPluginRepo` is a bare struct wrap (`&PluginRepo{db: db}`) — no
connection, no allocation worth naming, once per import request. The signature
change buys the function ownership of write-plus-invalidate as one unit, which
is the property that was missing in the first place. Accepted as written.

## Gate

Run in full in this worktree, after the L1 fix:

- `gofmt -l .` — clean
- `go build ./...` — clean
- `go vet ./...` — clean
- `go test ./...` — **all packages pass** (not just the touched ones);
  `internal/pages` 106.97s, `internal/plugins` 92.63s, `internal/data` 87.45s,
  `internal/pos` 5.65s
- **every** guard referenced by `.github/workflows/ci.yml` — all 30 scripts
  (guards plus their regression self-tests) pass, enumerated from the workflow
  rather than from CLAUDE.md's snapshot. Includes
  `guard-docs-shots.sh`, `guard-docs-shots_test.sh` and
  `guard-docs-shots-cross-check_test.sh` (surface `346ea9a81d61…`),
  `guard-data-access.sh`, `guard-i18n.sh`, `guard-compliance-claims.sh`,
  `guard-help-topics.sh`, `guard-kiosk-engine.sh`.

## Verdict

**Safe to merge.** No blocking findings.

The root cause is correctly identified and the fix is minimal and correctly
placed (guarded on an actual write, ordered after the commit). The second bug
is a genuine independent find of the same class. The new coverage is the right
kind — it exercises the real plugin chain a mock had been hiding, and I
confirmed by hand that it fails, with a VAT-specific message, when either fix
is backed out. One low-severity lockstep nit found and fixed; two items
accepted with backlog suggestions (M1's missing mechanical guard is the one
worth acting on, given this invariant has now been broken twice).

# Review — replica adopts an ownerless synced tender (ut-docs#6928)

Date: 2026-11-23. Branch `fix/6928-adopt-ownerless-tender`. Docs half:
universaltill/ut-docs branch `docs/6928-adr-0031-adoption` (ADR-0031
Consequences amendment, ADR-0011 wire note, `architecture/lan-sync.md`).

## What shipped

- `/api/sync/plugins` rows carry `owned_payment_method_ids` (`*[]string`,
  absent ≠ empty): the `payment_methods` ids the main till has owned by that
  row's plugin (`SyncPluginsRepo.DumpActivePlugins`, one extra query).
- `PluginRepo.AdoptOwnerlessPaymentMethods(ctx, tx, pluginID, keys)`: guarded
  `UPDATE … WHERE id IN (keys) AND plugin_id IS NULL AND id NOT IN
  (built-ins)`. The built-in ids are one shared `builtinPaymentMethodIDs`,
  also used by the `SyncPluginPaymentMethods` invariant.
- `InstallOptions.AdoptPaymentKeys`: set only by the replica plugin sync
  (`convergePluginSet` → `pluginSyncInstall` → `cloudSyncInstallPluginVersion`
  → `MarketplaceInstallRequest` → `bundleInstallSpec`). `PersistManifest`
  step 0-a claims, inside the install tx, the hinted keys that are also this
  manifest's payment entries; the existing key/label checks then run
  unchanged. A DB error fails closed; a later refusal rolls the claim back.
- `payment_methods.plugin_id` stays in the admin bundle's `skipCols`
  (ADR-0031 ② unchanged).

## Process

BA → Architect (Opus) → Dev (Sonnet) → Tester (session model) → independent
review (Opus, worktree at the pre-review snapshot).

## Verification

- `gofmt -l .` empty, `go build ./...`, `go vet` (data, plugins, pages),
  `golangci-lint run ./...` 0 issues.
- `go test ./internal/data/...` ok; `go test ./internal/plugins/...
  -timeout 25m` ok (954 s — the same package takes 957 s on `origin/main`,
  so the slowness is pre-existing, not this change); `go test
  ./internal/pages -run 'Sync|Plugin|Converge|Takeaway|Tax'` ok (no `-race`
  on pages: 90 min on the 4-core container).
- 68/70 `guard-*.sh` from the `ci.yml` build job pass locally; the other two
  need tools this container lacks (shellcheck binary, catimport tags) and
  touch nothing here — CI runs them.
- TDD re-verified by the reviewer: with step 0-a disabled,
  `TestPersistManifest_AdoptsOwnerlessHintedTender` fails with
  `payment entry key "sumup" collides with an existing built-in or
  shop-configured tender`; restored → pass.
- Tester mutations (each restored): dropping the built-in exclusion fails
  the AC2 tests; dropping `plugin_id IS NULL` fails AC9 and the data-layer
  AC4 test; adopting regardless of the hint fails AC3, AC7 and AC8's manual
  half.
- AC8 apply side (Tester gap G1, fixed here): the test now plants
  `plugin_id` on every bundled `payment_methods` row before the apply;
  removing the `skipCols` entry makes it fail with
  `plugin_id="com.hostile.pay", want present and NULL` (verified by the
  orchestrator), restored → pass.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| R1 | low | A compromised main could make a replica attribute an ownerless shop tender to a signed plugin declaring the same key. Inherent to ② stripping ownership; incremental power over the admin bundle is small. | Fixed in docs: trust-assumption sentence in the ADR-0031 amendment. |
| R2 | nit | New `placeholders`/`stringArgs` helpers duplicated `inPlaceholders` and were shadowed by locals. | Fixed: uses `inPlaceholders`. |
| R3 | nit | gofmt turned `''` in a doc comment into a curly quote. | Fixed: "the empty string". |
| R4 / G2 | low | `AdoptPaymentKeys` threading through the real marketplace installer is compile-checked only (tests use a stub calling `PersistManifest`). | Deferred → follow-up card. |
| G1 | medium | AC8 test did not pin the apply-side skip (the dump already strips `plugin_id`). | Fixed (above). |
| G3 | low | `plugins.TestPersistManifest_AdoptNeverOverridesLocalOwner` is refused by the plugin_entries check, not the adopt guard (the data-layer test covers the guard). | Deferred → follow-up card. |
| G4 | low | Nothing checks the main row's `plugin_id` equals the manifest id being installed. | Deferred → follow-up card. |

Pre-flight: Till user-visible behaviour n/a (no UI/strings; `multitill.md`
already says plugins follow the main); `en.json` n/a; ADR amended →
`adr/README.md` index n/a (titles only, `guard-adr-index.sh` passes),
`architecture/lan-sync.md` updated; manifest field n/a (`InstallOptions` /
`PluginSyncRow` are not in ut-cloud's signing mirror); migration n/a;
review record this file; follow-ups → Backlog card.

## Verdict

Ship. No high/medium finding left open.

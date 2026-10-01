# Review: ADR-0121 ABI-3 manifest fields (ut-docs#3155)

Scoped at BA to the manifest **fields**, their validation and the signing
mirror. The new permissions are #3328. The event-namespace rule and the
ut-cloud upload checks are #3329 (Admin Review: the ADR names a manifest
`key` that does not exist).

## What shipped
- `internal/plugins/manifest.go`:
  - `Manifest` gains `wasm_abi`, `limits`, `schedules`, `db`, `retention` and `views_used`.
  - `ManifestEntry` gains `view` and `slot`.
  - All are `omitempty` and appended last, so existing manifests marshal byte-identically.
- `internal/plugins/manifest_abi3.go`: `validateABI3Fields` runs from both install paths (`ParseManifest` and `ManifestVerifier.VerifyManifest`). It refuses:
  - `wasm_abi` 1, a negative value, or one above `MaxSupportedWasmABI` (2 until the ABI-3 host functions land), with "update the till";
  - negative `limits`;
  - a malformed schedule event, or one in a core namespace (`sale.*`, `fiscal.*`, `ui.*`, …);
  - `every_s` < 30 (ADR-0121 §8), or `jitter_s` outside 0..`every_s`;
  - a `db.migrations` path that is absolute, uses odd characters, or escapes the plugin;
  - negative `retention.keep_years`, or a `reason_key` that is not a locale key;
  - a malformed or duplicate `views_used` name;
  - an entry `slot` outside the six §7 content slots, or a malformed `view`.
- `EffectiveLimits(goos)` applies the defaults and the §2 platform ceilings. It never mutates the signed manifest. `EffectiveWasmABI()` applies the default of 2.
- Tests:
  - `manifest_abi3_test.go`: round-trip, unchanged canonical bytes, every refusal with its message, accepted edge cases, ceilings, and the verifier path.
  - `TestMarketplaceSignatureVerifiesABI3Fields`: a fixture signed by ut-cloud's real `Signer` that carries every new field, including zero values. The POS verifier accepts it.
- ut-cloud#(mirror PR): `CanonicalManifest`, `CanonicalEntry` and four nested types mirror the new fields. The contract test pairs table checks all of them. `TestSignManifestPreservesABI3Fields` fails without the mirror (checked).
- ut-docs: `reference/plugin-manifest.md` § "ABI-3 fields".

## Review (independent, Fable; one round)
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | A schedule event of `sale.completed` or `fiscal.sign.ask` was accepted and would be signed. Once the scheduler lands, it would tick a core event. | **Fixed.** Core event roots are refused, with the full positive rule left to #3329. Pinned by 4 refusal cases. |
| 2 | should-fix | `every_s ≥ 1` contradicts ADR-0121 §8 ("min 30 s"). | **Fixed.** Now ≥ 30, and `jitter_s` ≤ `every_s`. Doc updated. |
| 3 | should-fix | There was no byte-level cross-repo proof with the new fields populated. | **Fixed.** Added `testdata/marketplace_signed_manifest_abi3.json`, signed by the ut-cloud signer with a deterministic test seed, and verified by the POS verifier. |
| 4 | should-fix | `ut-cloud/pkg/manifest` doesn't check the new fields at upload. | **Tracked** in #3329 (already split out at BA). The doc says so. The till fails closed on both install paths. |
| 5 | nit | Refusal-test substrings were too generic. | **Fixed:** tightened. |
| 6 | nit | `db.migrations` allowed whitespace and control characters. | **Fixed:** `^[A-Za-z0-9._/-]+$`, with newline and space cases. |
| 7 | nit | The doc should separate entry `slot` from a layout's `config.slot`. | **Fixed** in the doc. |
| 8 | nit | `wasm_abi` is meaningless on `runtime: none`. | **Accepted.** Harmless. |
| 9 | nit | An empty id gave "plugin  needs …". | **Fixed:** the id is now `%q`. |

The reviewer confirmed:
- Old-manifest bytes are unchanged (`TestMarketplaceSignatureVerifies`).
- Nested zero values marshal the same way on both sides.
- No cloud path re-serialises through another struct.
- `layout-salon`'s `config.slot` is unaffected.

## Verification
- `go build ./...`, `gofmt`, `golangci-lint` on `internal/plugins` (0 issues), `guard-core-neutral`, `guard-i18n`, `guard-data-access`, and `go test ./...`.
- `guard-deadcode-baseline` reports `internal/logging` `Stderr`/`timestampWriter.Write` identically on `origin/main`: the desktop root is skipped locally for lack of GTK headers. It is not from this change.
- An unrelated full-suite flake, `TestReplicaLink_HelloCarriesItsOwnCloudDeviceID`, failed under load and then passed 5/5. Filed as #3330.
- ut-cloud `scripts/ci/verify.sh` with `POS_DIR=../universal-till`: green.

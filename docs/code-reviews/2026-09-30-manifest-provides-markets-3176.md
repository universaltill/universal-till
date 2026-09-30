# Review: ADR-0129 slice 2a — manifest `provides` / `markets` (ut-docs#3176)

- **Date:** 2026-09-30
- **Card:** universaltill/ut-docs#3176 (lane:cloud-54), rescoped at BA; split-outs #3281 (till persistence, `fiscal.*` exclusivity, real `min_pos_version`) and #3282 (ut-cloud listing `markets` + catalogue filter).
- **PRs:** universal-till (merge first) and ut-cloud (merge second: its `manifest-contract-guard` reads POS `main`), plus ut-docs `reference/plugin-manifest.md`.
- **Author:** Opus 5.5 (pipeline). **Reviewer:** Fable (independent subagent, different model), one round.

## What shipped

**universal-till**
- `plugins.Manifest` gains `Provides []string` and `Markets []string`, both `omitempty`, appended after `Permissions`. A manifest without them marshals byte-identically, so every existing signature still verifies.
- `internal/plugins/manifest_provides.go`: the ADR-0129 closed set (`fiscal.device`, `fiscal.register`, `ai`, `layout.shop_type:<ADR-0026 shop type>`), `validateProvidesAndMarkets` (unknown, duplicate, or non-`^[A-Z]{2}$` market refused; no case-folding or trimming). Called from `ParseManifest` (sideload, rollback, builtins) **and** `VerifyManifest` (marketplace install).
- The ADR-0026 shop-type list moved from `internal/pages` to `plugins` (`ShopTypes()` returns a copy; `IsKnownShopType`) so setup, Settings and manifest validation share one list.

**ut-cloud**
- `signing.CanonicalManifest` mirrors both fields (same position, tags, `omitempty`); `TestCanonicalManifestMirrorsPOS` green against the POS branch.
- `pkg/manifest`: same closed set in `Validate` (upload and scan); `layout.shop_type:*` always refused (embedded builtins only); `ValidateUploader(firstParty)` refuses `fiscal.*` from third parties.
- Ingest runs `ValidateUploader` before any bytes are stored; `reviews.Approver.ApproveRelease` re-runs it on the bundle's own `manifest.json` (the signed document) using the listing slug's first-party prefix, before signing.
- New cross-repo guard `TestProvidesClosedSetMirrorsPOS` (in the `manifest-contract-guard` job): capability constants and shop types must equal the POS's.
- `docs/manifest-validation-errors.md` lists the new messages.

## Findings (Fable review) and outcome

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | Approval signed a release without re-checking the first-party `fiscal.*` rule; a release stored before this deploy could carry a third-party `fiscal.register` to the till once the signer preserves the field. | **Fixed** — `ApproveRelease` checks the bundle manifest; `TestApproveReleaseRefusesThirdPartyFiscalProvides` (fails without the fix: approval succeeded). |
| 2 | should-fix | The till's marketplace install path uses `VerifyManifest`, not `ParseManifest`, so it skipped the new validation. | **Fixed** — `VerifyManifest` validates too; `TestVerifyManifest_RefusesUnknownProvidesAndBadMarkets` (failed before the fix). Doc wording corrected. |
| 3 | nit | `pkg/manifest.Parse` now decodes `provides`/`markets`; a stored manifest with a non-array value would fail decode. | **Accepted** — no fixture or published plugin declares the keys (the doc said not to); fails closed. |
| 4 | nit | Round-trip test claimed to pin order after `permissions` without declaring `permissions`. | **Fixed.** |
| 5 | nit | Exported mutable `ShopTypes` slice. | **Fixed** on both sides (`ShopTypes()` returns a copy). |
| 6 | nit | Merge order. | Followed: POS first, then ut-cloud. |

Reviewer confirmed: byte-compat (nil and `[]` both omitted on both sides), no other manifest mirrors, first-party check can't be bypassed via `listing_id`, core-neutral guard passes, no file writes/path issues.

## Verified beyond unit tests

- TDD re-verified by the orchestrator in a separate worktree: disabling the `ParseManifest` call makes the three refusal tests fail; restored → pass. The ut-cloud ingest and signer tests failed with the production change stashed and pass with it.
- Mutation check of the cross-repo guard: dropping a shop type on the cloud side fails `TestProvidesClosedSetMirrorsPOS` with a clear drift message; an explicit wrong `POS_DIR` fails (not skips).
- Full gates: universal-till `go test ./...`, `golangci-lint`, all 75 `build`-job guards; ut-cloud `scripts/ci/verify.sh` with `POS_DIR`.
- No UI surface, no locale keys, no migration.

## Deferred

- #3281 — till persistence, `PluginsProviding`, `fiscal.*` exclusivity at PersistManifest/`/enable`/Rollback, real `min_pos_version`.
- #3282 — listing `markets` column + catalogue filter.

**Verdict:** safe to merge, POS first.

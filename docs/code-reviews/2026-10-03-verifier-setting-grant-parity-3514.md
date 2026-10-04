# Review: VerifyManifest gains ParseManifest's setting checks (ut-docs#3514)

- **Date:** 2026-10-03
- **Branch:** `fix/3514-verifier-setting-grant-parity`
- **Card:** universaltill/ut-docs#3514 (`complexity:easy`, lane:cloud-54)
- **Author:** Dev (Sonnet). **Reviewer:** Opus 5.5 — a different model from
  the author, fresh context, in its own detached worktree.

## What shipped

`ManifestVerifier.VerifyManifest` is the only manifest check on the
marketplace-install path (`installer_marketplace.go:262`; the importer also
runs `ParseManifest`). It already mirrored `ParseManifest`'s provides/markets,
ABI-3 and validation-grant checks; it now also runs:

1. `validateSettingBoundPermissions` — a malformed `net:@setting:` /
   `tcp:@setting:` grant, or one naming an undeclared setting, is refused
   (the card's ask).
2. The ADR-0082 setting-type enum (`isValidSettingType`), same message as
   `ParseManifest`. Folded in during BA: same block, same parity class, and
   worse in effect — a typo of `secret` on a marketplace install left that
   setting's value unsealed at rest.

Both append to `result.Errors` like their neighbours. Tests:
`TestVerifyManifest_RefusesMalformedSettingBoundGrant` (7 malformed forms
refused with distinctive text; two well-formed grants accepted) and
`TestVerifyManifest_RefusesUnknownSettingType` (`secrett` refused; `secret`
and `""` accepted). CHANGELOG: one Fixed line.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | Check order differs from `ParseManifest` (type after grant); only affects joined-error order since VerifyManifest collects all errors. | Accepted. |
| 2 | nit | Tests live in `wasm_setting_grant_test.go`, not beside other VerifyManifest tests. | Accepted — they belong to the #2899 setting-grant feature. |
| 3 | nit | Bad cases iterate a map (random failure order). | Accepted — each case is asserted independently. |

## Verified beyond the tests

- TDD re-verified by the reviewer: with `manifest_verifier.go` at `HEAD~1`
  both new tests fail (all 7 malformed grants and `secrett` accepted); with
  the fix both pass.
- Callers: only `installer_marketplace.go` and `importer.go`; nothing calls
  `VerifyManifest` at boot, so already-installed plugins are never re-checked.
- In-repo manifests: the only setting-bound grant (`plugins/tax-tr/plugin.json`,
  `tcp:@setting:okc.host:okc.port`) declares both keys; no in-repo manifest
  sets a setting `type`.
- `go build ./...`, `go vet`, `gofmt`, `golangci-lint` (0 issues), and
  `go test ./internal/plugins/` all clean. `-race` on the package exceeded the
  sandbox's 10-minute limit (timeout dump in `database/sql`, unrelated
  goroutines); CI runs it.
- No UI surface touched; no help topic affected.

## Verdict

Safe to merge. Nothing deferred.

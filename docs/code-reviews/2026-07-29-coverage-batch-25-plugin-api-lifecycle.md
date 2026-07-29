# Test coverage batch 25: plugin_api.go lifecycle handlers — a real security bug found and fixed

2026-07-29

`internal/pages/plugin_api.go`'s plugin lifecycle handlers
(enable/disable, uninstall, update, rollback, check-updates,
import-from-file, export) had partial-to-zero coverage:
`setPluginActiveHandler` ~20%, `handleUpdatePlugin` ~8.7%,
`handleRollbackPlugin` ~16.7%, `handleCheckUpdates` ~8.3%,
`handleImportFromFile` ~7.4%, `handleExportPlugin` ~31.2%,
`handleUninstallPlugin` ~35.7%.

Implemented by an Opus-model agent (a different model from the one
carrying out this session, Sonnet — the standing cross-model review
requirement, satisfied by construction: Opus implemented, Sonnet
reviewed, per [[workflow-autonomy-and-review]]). Reviewed and committed
here after independent verification below.

## Bug: plugin signature verification was silently disabled on manual import

`handleImportFromFile` (`internal/pages/plugin_api.go:936`, pre-fix)
constructed its verifier as `plugins.NewManifestVerifier("")` — a literal
empty string, with a `// TODO: Add public key path from config` comment
— instead of the till's actually-configured marketplace Ed25519 public
key (`d.Cfg.Marketplace.PublicKey`, set via `UT_MARKETPLACE_PUBLIC_KEY`).
An empty-key verifier has nothing to check a signature against, so
**every manually imported plugin bundle skipped signature verification
entirely**, on every till, regardless of whether an operator had
configured a signing key. This directly violates the non-negotiable
CLAUDE.md rule: "Installed plugins are Ed25519-verified before they
run... Never run an unverified plugin." A bundle with a forged or
tampered signature — or no signature at all — imported and ran exactly
like a legitimately signed one.

**Fix**: pass `d.Cfg.Marketplace.PublicKey` into `NewManifestVerifier`
instead of `""`. When no key is configured (the offline/dev default),
behavior is unchanged (unsigned bundles still import — offline
provisioning without a marketplace still works); when a key IS
configured, an invalid or missing signature is now rejected.

## Independent verification (sonnet, different model from the Opus implementer)

- Re-read the one-line diff plus its 4-line comment: matches the fix
  description exactly, minimal and in-scope.
- **Manually re-ran the TDD claim from scratch**, not just trusted it:
  temporarily reverted the fix (`NewManifestVerifier(d.Cfg.Marketplace.PublicKey)`
  → `NewManifestVerifier("")`) and re-ran
  `TestHandleImportFromFile_RejectsInvalidSignatureWhenPublicKeyConfigured`
  — confirmed it fails exactly as claimed: a bundle with an
  invalid-but-well-formed signature imports with `200 ... "imported
  successfully"` against the pre-fix code. Restored the fix and confirmed
  the test passes (`400`, plugin not in `Pm.Installed`).
- Read the full new `internal/pages/plugin_api_test.go` (589 lines): the
  signature-boundary is covered from three angles — invalid signature
  rejected when a key is configured, valid signature accepted when a key
  is configured, and unsigned bundles still import when no key is
  configured (guards the fix from regressing offline provisioning). No
  false-pass patterns found — assertions check `rec.Code` plus
  `Pm.Installed` state, not just a 200 with an unrelated body check.
- `go build ./...`, a full `go clean -testcache && go test ./...` (all
  packages, not just `internal/pages`), and both CI guards
  (`guard-data-access.sh`, `guard-i18n.sh`) — all pass.
- Coverage confirmed matching the implementer's report:
  `setPluginActiveHandler` 20→75%, `handleUninstallPlugin` 35.7→75%,
  `handleUpdatePlugin` 8.7→43.5%, `handleRollbackPlugin` 16.7→83.3%,
  `handleCheckUpdates` 8.3→83.3%, `handleImportFromFile` 7.4→74.1%,
  `handleExportPlugin` 31.2→78.1%.

## Coverage added (by handler)

- `setPluginActiveHandler`: empty-id 400; disable then re-enable toggles
  `Pm.Installed` correctly.
- `handleUninstallPlugin`: empty-id 400; removes an installed plugin from
  the active set.
- `handleUpdatePlugin`: empty-id 400; not-installed 404; installed but no
  marketplace listing recorded → 404 with an explanatory message (a
  manually imported plugin can't be blindly "updated" from a marketplace
  it was never listed on).
- `handleRollbackPlugin`: empty-id / invalid JSON body / missing version
  → 400; unknown target version (not stored on disk) → 500.
- `handleCheckUpdates`: no catalog configured → 503; catalog configured
  with no installed plugins → 200 with an empty result.
- `handleImportFromFile`: missing file field → 400; the signature
  boundary above; a correctly signed bundle imports successfully.
- `handleExportPlugin`: full import→export round trip — a signed bundle
  imported through `import-from-file` lands on disk in the exact layout
  the exporter reads, streamed back as a `.tar.gz` with the
  `X-Bundle-SHA256` checksum header set.

## Noted, not fixed (out of scope for this batch)

- `internal/plugins/rollback.go`'s `RollbackManager.StoreVersion` is a
  pre-existing `// For now` stub that creates the `versions/<v>`
  directory but never copies plugin files into it — so a genuine
  update-then-rollback can never find `manifest.json` at rollback time.
  Different package, pre-existing, not touched by this diff.
- `ManifestVerifier.VerifyManifest` rejects an invalid signature but does
  NOT require a signature to be *present* when a key is configured — an
  unsigned manifest still passes verification even with a key set. This
  narrows this batch's fix: it closes the "signature present but wrong"
  gap, not "signature entirely absent." Worth a follow-up ticket, not
  addressed here to keep this batch's diff minimal and reviewable.

## Verification

`go build ./...`, `go clean -testcache && go test ./...` (whole repo),
`scripts/ci/guard-data-access.sh`, `scripts/ci/guard-i18n.sh` — all pass.

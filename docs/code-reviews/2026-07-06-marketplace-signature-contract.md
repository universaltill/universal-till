# Code Review — Marketplace signature contract test

Date: 2026-07-06
Reviewer: Claude (autonomous review before commit)
Scope: new `internal/plugins/marketplace_signature_crossrepo_test.go` +
`internal/plugins/testdata/marketplace_signed_manifest.json`.

## What the change delivers

A cross-repo contract test proving the POS `ManifestVerifier` accepts Ed25519
signatures produced by the marketplace signer (`ut-market-place/internal/signing`).
The fixture manifest was signed by the marketplace with a fixed test key; the
test configures the real verifier with that key's public half and asserts
`SignatureVerified`, plus a negative case under a wrong key.

## Why it matters

The signing contract is a brittle canonical-JSON agreement: the signature is over
`json.Marshal` of this package's `Manifest` struct with an empty signature field.
If the marketplace `CanonicalManifest` struct and this `Manifest` struct drift
(field order, tags, omitempty), installs silently break. This test fails loudly
on drift.

## Notes

- Test-only; no production code changed.
- If regenerating the fixture, re-sign with the marketplace signer and update the
  hardcoded public key.

## Verification

- `go test ./internal/plugins/` passes (both the positive and wrong-key cases).

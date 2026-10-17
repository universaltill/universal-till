# 2026-10-17 — Verify a signed content digest of every plugin file (ut-docs#4546, ADR-0165)

## What shipped
- `plugins.Manifest` gains `content_digest` / `content_signature`
  (omitempty, right after `artifact_hash`; mirrored by ut-cloud's
  `signing.CanonicalManifest`).
- `ComputeContentDigest(dir)` (ADR-0165 §2): sorted `<sha256>  <path>`
  listing of every regular file except the root `manifest.json`; refuses
  symlinks, other non-regular entries and unsafe paths.
- `VerifyManifest`: the legacy `signature` is checked over the manifest
  with all three fields emptied (byte-identical to before); with a key
  configured it also requires both content fields, a valid
  `content_signature` and an on-disk digest match. Both install paths
  (marketplace `installBundleFile`, manual `Importer.Import`) verify the
  freshly extracted directory, so manual import can no longer pair a signed
  manifest with arbitrary files. A missing content field wraps
  `ErrManifestUnsigned`, so the import screen shows the actionable
  "install it from the Plugin Store" message.
- Help (`web/help/{en,de,tr,ar,fa}/plugins.md` step 6): the signature covers
  every file; an export of a pre-check install is refused — reinstall and
  export again. `docs/data-model.md` bullet.

## Review
Independent review on a different model from the author (author Opus 5.5,
reviewer Fable) in a throwaway worktree. Verdict: safe to merge once M1
was addressed.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| M1 | major | Missing content fields fell through to the generic "Import failed" message | Fixed: wrapped in `ErrManifestUnsigned`; test-first (`TestVerifyManifest_MissingContentFieldsRefused` red, then green) |
| m1 | minor | No positive cross-repo fixture signed by the real ut-cloud signer | Accepted: parity proven by the shared golden vector and the ut-cloud reviewer's signer→extractor→digest harness (26 shapes) |
| m2 | minor | Help says "installed before this check" (the exact condition is "before its release was re-signed") | Accepted: conservative wording, the remedy is the same |
| m3 | minor | A bundle staged before the backfill fails install as `invalid_package` | Accepted: re-download fixes it; noted for support |
| — | nits | Symlink-root message, case/NFD collisions fail closed, TOCTOU same window as before, digest computed after a failed content signature | Accepted |

Bypass audit (reviewer): every path that puts plugin bytes on disk goes
through `installBundleFile` or `Import`; Rollback and builtin layouts use
`ParseManifest` on already-installed or binary-embedded content
(pre-existing, consistent with ADR-0165 §5). LAN sync never copies plugin
directories. Export→import of a re-signed install round-trips (runtime
writes go to `paths.Data("plugin-data")`).

## Verified beyond the unit tests
- Golden vector computed with coreutils `sha256sum`, identical to ut-cloud's.
- The pre-ADR-0165 marketplace fixtures (signed by the real ut-cloud
  signer) still verify their legacy `signature` and are refused only for
  the missing content fields — real-world proof the legacy bytes are unchanged.
- TDD re-verified by the reviewer: disabling the content check turns 11
  tests red (both install paths, verifier, cross-repo, demo mode); restored, green.
- Mutation by the author: same, 9 tests red.
- Gates: gofmt, build, vet, golangci-lint 0 issues, `go test ./...`
  (plugins 1075s, pages 406s, 88 other packages ok), devplugins step,
  guard-help-drift/help-topics/compliance-claims and the other `ci.yml`
  guards (deadcode/shellcheck-version not runnable here: tool versions).

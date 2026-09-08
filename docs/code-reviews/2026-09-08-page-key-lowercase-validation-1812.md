# Page-entry manifest keys: enforce lowercase at install (ut-docs#1812)

**Date:** 2026-09-08
**Card:** ut-docs#1812 (`complexity:easy`)
**PR:** universal-till#928 (mirrors the already-merged sibling fix, PR #926 / ut-docs#1811)

## What shipped

`validatePageEntryKeys` (`internal/plugins/manifest.go`) had the identical
theoretical gap ut-docs#1811 fixed for payment-entry keys: `FindPageKeyConflicts`
(`internal/data/plugin_repo.go`) compares candidate page-entry keys against
existing ones with SQLite's default case-sensitive `TEXT` collation, so a
mixed-case key (e.g. `"MixedCase"`) could install alongside an existing
differently-cased key without being caught as a conflict.

Fix: `validatePageEntryKeys` now rejects a page-entry key that isn't already
its own lowercase form — `e.Key != strings.ToLower(e.Key)` — placed in the
identical relative position as the payment-key precedent: after the `':'`
format check, before the next gate (`DocsEntryKey` exemption `continue`
here, vs. `reservedTenderSentinelKeys` on the payment side). `DocsEntryKey`
(`"docs"`) is already lowercase, so it passes the new check cleanly before
reaching its own exemption.

## Why low severity today (mirrors #1811's own reasoning)

Not exploitable by an unprivileged request — plugins are Ed25519-verified
before they run (`internal/plugins/manifest_verifier.go`) — and no shipped
manifest currently uses a mixed-case page key (verified below). This is a
guard against a future mis-cased manifest, not a live data bug.

## Changes

- `internal/plugins/manifest.go` — one new format check in `validatePageEntryKeys`.
- `internal/plugins/page_key_validation_test.go` — extends
  `TestPersistManifest_PageKeyFormatValidation` with a mixed-case-key case,
  asserting the error names the actual key (not just `err != nil`),
  consistent with the function's other three format assertions.

## Testing

- TDD: test written first, confirmed failing (`got: <nil>`), then the fix
  added and confirmed passing.
- Independently re-verified by a separate reviewer subagent (fresh-context
  Sonnet, per this card's `complexity:easy` routing) in an isolated
  worktree: reverted just the fix (kept the new test), reran — **RED**
  (`mixed-case page key must be rejected... got: <nil>`) — then restored
  the fix, reran — **GREEN**. Full output reproduced in the review.
- Full gate, run by both Dev and the reviewer independently: `gofmt -l`,
  `go build ./...`, `go vet ./internal/plugins/...`,
  `go test ./internal/plugins/...` (full package — `plugins`, `marketplace`,
  `oauth` subpackages, all `ok`), `bash scripts/ci/guard-data-access.sh`,
  `bash scripts/ci/guard-i18n.sh` — all clean. Confirmed no regression in
  the pre-existing `TestPersistManifest_DocsKeyExemptAcrossPlugins`,
  `TestPersistManifest_PageKeySelfUpgradeNotAConflict`,
  `TestRollback_RejectsCollidingPageKeys`,
  `TestPersistManifest_RejectsPageKeyOwnedByAnotherPlugin`.
- AC3 (no shipped plugin manifest uses a non-lowercase page key today):
  confirmed. The only in-repo manifest with a `type:"page"` entry
  (`internal/plugins/testdata/marketplace_signed_manifest.json`, key
  `"faq-page"`) is already lowercase; the repo's one real shipped plugin
  manifest under `plugins/tax-tr/` has a single `type:"payment"` entry, no
  page entries. All other `Type: "page"`/`CanonicalType: "page"` literals in
  the repo are Go test fixtures, already lowercase.
- Backend-only change (no template/UI/locale files touched) — the visual-
  check attestation in `tester`'s skill does not apply.

## Independent review verdict

**SAFE TO MERGE.** No findings of any severity. Diff scope confirmed exactly
two files (`manifest.go` +9, `page_key_validation_test.go` +9), no secrets or
real client/shop names, no file-I/O (so the `os.MkdirAll`/`paths.Data(...)`
bug classes this pipeline watches for are not applicable to this diff).

## Deferred / out of scope

None — this card's acceptance criteria are fully satisfied. Same non-goal as
#1811: no backfill/migration of already-installed `plugin_entries` rows
(none exist with non-lowercase page keys today).

# Code review: name unsigned-manifest import failures explicitly (ut-docs#2132)

**Date:** 2026-09-12
**Card:** universaltill/ut-docs#2132 (split c/3 of universaltill/ut-docs#2115)
**Branch:** `fix/2132-unsigned-import-error-message`
**Complexity:** easy (Dev: Sonnet inline, Review: fresh-context Sonnet subagent)

## What shipped

`manifest_verifier.VerifyManifest` returned `"manifest validation failed: N
errors"` on any failure — no caller could tell WHICH validation failed, and
the operator-facing message built from it (`http.Error(w, fmt.Sprintf("Import
failed: %v", err), ...)`) was equally uninformative: a raw, untranslated Go
error with no next step. Reproduced live on the pilot tablet importing the
official `com.universaltill.language-de` GitHub Release artifact — it is
never signed (signing happens at marketplace *approve* time), so
`internal/plugins/manifest_verifier.go`'s fail-closed check correctly rejects
it, but told the operator nothing actionable.

Fix, scoped to the till's own side of the defect (see "Deferred" below):

- `manifest_verifier.go`: the returned error now names the actual
  validation failure(s) instead of a bare count, and wraps a new exported
  `ErrManifestUnsigned` sentinel specifically for the "public key
  configured, manifest has no signature" case, so a caller can
  `errors.Is()` it without re-deriving the reason from string matching.
- `plugin_api.go`'s `handleImportFromFile`: on import failure, shows a
  distinct, translated, operator-actionable message for the unsigned case
  (`plugins.error.import_unsigned`, naming both supported routes — the
  Plugin Store, or Export from another till) vs. a generic translated
  message (`plugins.error.import_failed`) for anything else. Uses the
  existing `common.LogAndLocalizedError` pattern already used elsewhere in
  this same handler file (e.g. the `manager_or_admin_required` checks) —
  the real Go error still reaches the server log via that helper, just not
  the operator's screen.
- `web/help/{en,ar,fa,tr,de}/plugins.md`: documents that **Import from
  file** expects a marketplace-signed bundle (from the Plugin Store, or
  **Export** on a till that already has the plugin), and that a `.tar.gz`
  attached directly to a plugin's GitHub Release page is unsigned and will
  be refused — closing the exact gap the pilot-tablet report hit.
- New locale keys in `en`/`ar`/`fa`/`tr` (alphabetically placed among the
  existing `plugins.error.*` keys); `de`/`es` plugin-pack translations are
  a separate `lang-pack-drift` follow-up in the external
  `ut-plugin-language-{de,es}` repos (needs the self-hosted NAS
  translation model, unreachable from this cloud session — same standing
  convention as ut-docs#1918/#982/universal-till#1076).

## Tests

- `internal/plugins/verifier_hardening_test.go`:
  `TestVerifyManifestUnsignedErrorIsDetectableAndNamesTheReason` (positive:
  `errors.Is` finds `ErrManifestUnsigned`, and the error text names the
  actual reason) and `TestVerifyManifestOtherFailureIsNotErrManifestUnsigned`
  (negative: a missing-required-field failure must NOT be misreported as
  the unsigned case).
- `internal/pages/plugin_api_test.go`:
  `TestHandleImportFromFile_UnsignedBundleWithKeyConfiguredShowsLocalizedReason`
  — asserts the HTTP response body is exactly the translated
  `plugins.error.import_unsigned` string, that it does **not** contain the
  raw `"manifest validation failed"` Go error text, and that the plugin is
  not installed.
- TDD verified for real: reverted `manifest_verifier.go` + `plugin_api.go`
  only (`git stash` those two paths) — the plugins-package test then fails
  to **build** (`undefined: ErrManifestUnsigned`), and the handler test
  fails asserting the exact old raw message
  (`"Import failed: manifest verification failed: manifest validation
  failed: 1 errors\n"`). Restored and re-ran green.

## Independent review (fresh-context Sonnet subagent, shared checkout)

Re-verified the `errors.Is` wrapping chain independently rather than
trusting the diff's own claim — checked out the pre-fix versions of both
Go files and confirmed the build failure and the literal old error text,
then restored. Ran `guard-i18n.sh`, `guard-help-drift.sh`,
`guard-help-topics.sh`, `guard-docs-shots.sh`, `guard-data-access.sh`,
`guard-kiosk-engine.sh`, `guard-compliance-claims.sh` (all green),
`gofmt -l .` / `go build ./...` / `go vet ./...` (clean),
`go test ./internal/plugins/... ./internal/pages/...` (twice, consistent
pass), and `golangci-lint run ./internal/plugins/... ./internal/pages/...`
(0 issues). Read the new locale strings and all five `plugins.md` manual
translations for consistency and numbering gaps — none found.

**No findings.** Verdict: safe to merge.

## Scoping decisions (deliberate, not oversights)

- **Not fixing the ~16 `ut-plugin-*` repos' own `release.yml` wording**
  ("Create GitHub Release (direct-download link for self-hosters)" / "…
  always points at whichever release is newest") that advertises the
  artifact as installable. Confirmed each plugin repo carries its own
  independent, non-templated release workflow (inspected
  `ut-plugin-language-de`'s `release.yml` directly) — there is no shared
  workflow a single edit could fix. Properly fixing this is ~16 separate
  repo PRs, disproportionate to this card and to `complexity:easy`.
  Tracked as a new Backlog follow-up (see close-out comment on the issue).
- **`de`/`es` plugin-pack JSON translations** for the two new keys are a
  `lang-pack-drift` follow-up in the external pack repos, per this repo's
  own standing convention — not blocked here since `en.json` is the base
  and `ar`/`fa`/`tr` (this repo's own shipped locales) are complete.
- **`scripts/ci/i18n-baseline/help-drift-baseline.json`'s `-update` flag
  was tried and rejected**: it recomputes and rewrites the *entire*
  1391-line file (73 entries), producing a ~543-line unrelated diff
  (reordering/refreshing topics this card never touched). Reverted that
  and hand-edited only the 3 pre-existing `ar`/`fa`/`tr` "plugins" topic
  entries instead, using the guard's own dry-run output to get the exact
  post-change signature numbers right.

## Verification beyond automated tests

- `docs-shots` regenerated for real via the pre-installed Chromium
  (`make docs-shots`, 124 screenshots) — required because `plugins.md`'s
  rendered content changed. A handful of unrelated topics (`catalog`,
  `sell`, `till-designer`) picked up tiny multi-byte screenshot deltas
  from the same run (e.g. `catalog.png | Bin 130162 -> 130119 bytes`) —
  consistent with the known guard-docs-shots re-render noise already
  tracked as ut-docs#2102, not a content regression from this change.
- Backend-only change to the API response text plus documentation; no UI
  markup/CSS touched, so no separate UX/driven-browser pass beyond the
  docs-shots Chromium run above.

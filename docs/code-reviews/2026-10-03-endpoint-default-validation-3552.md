# Review: ParseManifest/VerifyManifest validate an endpoint setting's default_value (ut-docs#3552)

- **Date:** 2026-10-03
- **Branch:** `fix/3552-endpoint-default-validation`
- **Card:** universaltill/ut-docs#3552 (`complexity:easy`, lane:cloud-41b)
- **Author:** Dev (Sonnet). **Reviewer:** Opus 5.5 — a different model from
  the author, fresh context, in its own detached worktree.

## What shipped

`ValidEndpointURL` (ADR-0121 §2) was previously enforced only when an
operator saves a value via `POST /api/plugins/{id}/settings`. `PersistManifest`
writes a manifest's declared `default_value` straight into `plugin_settings`
at install/update time with no check, so an `"endpoint"`-typed setting could
ship `default_value: "javascript:alert(1)"` (or any non-URL string) and have
it persisted verbatim.

1. New `validateSettingDefaults(m *Manifest) error`
   (`internal/plugins/secret_settings.go`): for every `endpoint`-typed
   setting, a **non-empty** `default_value` must pass `ValidEndpointURL`; a
   non-string default is refused too. Absent and empty-string (`""`)
   defaults are left alone — matching the existing no-default-yet convention
   already used for `secret`-typed settings (the Stripe/SumUp fixtures in
   `manifest_secret_setting_test.go`).
2. Wired into **both** manifest-validation entry points, each failing the
   whole manifest with a clear error, same as every sibling check:
   `ParseManifest` (`manifest.go`, the importer/builtinlayouts path) and
   `ManifestVerifier.VerifyManifest` (`manifest_verifier.go`, the
   marketplace-install path, which never calls `ParseManifest` — same split
   already fixed for setting-bound grants in ut-docs#3514).
3. `docs/plugin_guidelines.md`'s endpoint-setting paragraph updated to say
   so.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | blocker | Review round 1: the fix only landed in `ParseManifest`; `VerifyManifest` (the actual marketplace-install path) duplicates its own copy of every sibling check and was missed, so the exact attack described in the card was still live via a marketplace bundle. Reproduced live (`err=nil` on a malicious default) before the fix. | Fixed — `validateSettingDefaults` call added to `VerifyManifest`, plus `TestVerifyManifest_RefusesInvalidEndpointDefault`. |
| 2 | should-fix | `SettingTypeEndpoint`'s doc comment said default_value is "not validated at parse time" — now false. | Fixed, comment updated. |
| 3 | should-fix | `docs/plugin_guidelines.md` didn't mention the new rule. | Fixed. |
| 4 | nit | Pre-existing (not from this change): `VerifyManifest`'s setting-type-enum error message lists only `secret` as an allowed type, omitting `endpoint` (`ParseManifest`'s equivalent lists both). | Fixed while in that block for #1. |
| 5 | info | ut-docs `reference/plugin-manifest.md` has no endpoint/default_value section at all — pre-existing gap, already flagged in the #3328 review, not introduced here. | Deferred, not blocking — out of this card's scope (core repo). |

## Verified beyond the tests

- TDD re-verified by the reviewer for round 1 (`ParseManifest` path): reverted
  the call site, confirmed `TestParseManifest_EndpointDefaultValue` fails with
  a real assertion error, restored, confirmed it passes.
- TDD re-verified by Dev for round 2 (`VerifyManifest` path, finding #1's fix):
  removed the new call site, confirmed `TestVerifyManifest_RefusesInvalidEndpointDefault`
  fails (not a compile error), restored, confirmed it passes.
- No existing in-repo manifest fixture declares `type:"endpoint"` with a
  `default_value` (grepped the whole repo) — nothing newly rejected.
- Scope check: no SQL outside `internal/data`, no new user-facing/i18n string
  (these are installer-side Go error strings, same as every sibling
  `ParseManifest`/`VerifyManifest` check — none of those are localized), no
  UI touched, no money/tax/migration/concurrency angle.
- `go build ./...`, `go vet ./...`, `gofmt -l` (0 files) all clean.
  `go test ./internal/plugins/...` and `./internal/pages/...` clean; full
  `go test ./...` clean (run twice: once after round 1, once on the final
  tree after round 2).
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh`
  both clean.

## Verdict

Safe to merge. Nothing deferred that blocks this card (item #5 is a
pre-existing, separately-tracked gap in a different repo).

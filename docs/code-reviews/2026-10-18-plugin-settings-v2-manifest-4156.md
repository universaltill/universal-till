# Code review — plugin settings v2 manifest schema (ut-docs#4156)

Date: 2026-10-18 · Lane: `lane:cloud-41` · Card: universaltill/ut-docs#4156
(ADR-0121 amendment 2026-10-12, S1–S8; first build card of #4023)

## What shipped
- **universal-till** `internal/plugins`: the S1–S7 manifest fields
  (`settings_tabs`, `settings_tests`, `settings_setup`; per-setting `tab`,
  `required`, `min`/`max`, `max_len`, `placeholder`, `fields`/`title_field`/
  `min_items`/`max_items`, `map_over`/`value`, `options_ask`/`depends_on`,
  `guide`/`guide_url`/`paste`/`notice`) and the types `text`, `bool`,
  `number`, `percent`, `money`, `list`, `map`. All `omitempty`, appended at
  the end of their structs, new nested types in `manifest.go`.
  `validateSettingsV2` (new `settings_v2.go`) plus the refactored
  `validateSettingForm` run at both install paths (`ParseManifest`,
  `VerifyManifest`). No settings-page, `PersistManifest` or i18n change.
- **ut-cloud**: `internal/signing` mirrors every new field and the five new
  types (contract test `pairs` extended); `pkg/manifest` ports the till's
  rules to upload and adds the S8 rule: a manifest using any v2 field needs
  `min_pos_version` ≥ `SettingsV2MinPOSVersion`.
- **ut-docs**: `reference/plugin-manifest.md` "Settings form v2 fields",
  `architecture/plugin-architecture.md` "Settings form metadata".

## Review
Independent review by **Sonnet** — MODEL-ROUTING's Fable reviewer was refused
(out of usage credits), so the documented fallback was used. The author model
was Opus 5.5. The reviewer ran vet + the affected tests in both repos, a
35-manifest differential harness (till `ParseManifest` vs ut-cloud
`Parse`+`Validate`+`canonicalBytes`) and mutation tests in throwaway
worktrees.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | Upload refused a top-level `secret` with a non-string `default_value` that the till (and origin/main upload) accept — legacy manifests could fail ingest/rescan. | **Fixed**: only a record-field secret must be a string. Legacy test extended (failed first, then passed); the refused case now targets a record field. |
| 2 | should-fix | `SettingsV2MinPOSVersion` was a guess (`0.34.12`). | **Fixed at merge**: the till PR merges first; the constant is then set to the latest till tag + 1 patch, which is guaranteed to contain it (any later tag includes that `main`). |
| 3 | nit | Upload refused a whitespace-only record-field key the till accepts. | **Fixed** (parity: empty key only). |
| 4 | nit | Till mutation survived: dropping the credential-named check on `title_field`. | **Fixed**: added the case; verified it fails with the mutation and passes restored. |
| 5 | accepted | New typed names (`min`, `value`, `tab`…) could break a legacy manifest carrying a stray key of that name. | Accepted: none of the local manifests do; the names are the ADR's. |
| 6 | accepted | The S8 cap of 64 top-level settings also applies to legacy manifests. | Accepted (spec'd; nothing near it). |
| 7 | nit | Docs `placeholder` row was vaguer than the code. | **Fixed**. |

Checked fine: legacy manifest bytes unchanged (pinned test, both sides);
field order/tags/omitempty identical; `*int64` 0 kept on both sides; integer
defaults survive float64; every upload path (`ingest.go`, `scanner.go`) runs
`ValidateForRelease`; no case the upload accepts that the till refuses.

## Deliberate deviations from the design
- Top-level untyped (`""`) and `secret` settings keep any default shape
  (tax-de ships object defaults on untyped keys; a till test pins a numeric
  secret default). Record fields are strict.
- Added: `show_when`/`depends_on` never name a list/map setting; `title_field`
  never credential-named; record endpoint defaults must be valid URLs.

## Verified beyond automated tests
Backend-only, no UI surface. Differential harness and mutation runs as above.

## Verdict
Safe to merge, till first, then ut-cloud with the finalised version constant.

## Deferred
Settings page v2, batch save, list sealing, map editor, UI asks and setup flow
are #4023's next build cards (not part of #4156). Page v2 must also decide
whether the v2 upload floor moves to the release that renders the fields.

# Country settings: refuse a bypassed edit-mode code swap server-side (ut-docs#2405)

**Date:** 2026-09-18
**Card:** ut-docs#2405 (split from ut-docs#2187's independent review)
**Complexity:** easy — Sonnet dev, fresh-context Sonnet review
**Lane:** `lane:cloud-24`

## What shipped

`POST /api/country-settings` is the single save endpoint for both create
and edit of a `country_settings` row, keyed by the `code` form field
(unlike Locations/Registers, `code` is not a URL path segment). The
dialog's `code` input is locked `readonly` in edit mode by a page-local
JS listener, but that guard is client-side only — nothing server-side
previously told "editing DE" apart from "creating FR". If the readonly
lock were ever bypassed (devtools, a stale page, a future
`record-dialog.js` regression), a save with a different code would
silently upsert a **new** row instead of updating the one being edited —
not data loss (the original row survives untouched), but silent-wrong
behavior.

- `web/ui/pages/country_settings.html`: each row now carries
  `data-field-original_code="{{ .Code }}"`; the dialog form gained a
  hidden `original_code` input (blank template default). Both ride
  `record-dialog.js`'s existing generic `data-field-*` prefill/reset
  mechanism — no new JS.
- `internal/pages/country_settings_page.go`: the save handler rejects
  (via the existing `renderCountrySettingsDialogError` path) a submission
  where `original_code` is non-blank and differs from the submitted
  `code`, before any DB write.
- New i18n key `countrysettings.error.code_changed`, all 4 core locales
  (en/ar/fa/tr).
- `web/help/img/manifest.json`: `surface_sha256` refreshed via
  `scripts/ci/update-docs-shots-surface-hash.sh` (no PNG regen) — the
  change adds no rendered pixel (a hidden input, and an error message no
  screenshot state reaches). Verified, not assumed: `make docs-shots`
  was started for real first, then aborted mid-run (unrelated screenshots
  were mid-flight and it would have taken several more minutes); the
  escape hatch was used instead since the only touched surfaces
  (hidden input, dead-until-bypass Go branch) are provably non-rendering,
  same precedent as ut-docs#1566's `internal/pages` slice. Independently
  re-confirmed by the reviewer via `guard-docs-shots.sh`.
- Tests: `TestCountrySettingsPageSave_OriginalCodeMismatchRejected`,
  `TestCountrySettingsPageSave_OriginalCodeMatchingCodeSucceeds`,
  `TestCountrySettingsPageCreate_BlankOriginalCodeSucceeds`,
  `TestCountrySettingsPage_HtmxOriginalCodeMismatchRendersInDialogMessage`.

## TDD evidence

Both refusal tests written first against a DE→ZZ mismatch (an unseeded
code, not the seeded FR row, so the assertion isn't confused by seed
data). Re-verified independently twice: once by the orchestrator
(`git stash` the handler change, confirm both fail with the exact
pre-fix symptom — a plain redirect with no `code_changed` error, and a
new ZZ row actually created — then restore and confirm green), and
again by the reviewer in an isolated worktree with the same
revert-then-restore method.

## Independent review (fresh-context Sonnet, isolated worktree)

**Verdict: safe to merge. No blocking or should-fix findings.**

- Verified the load-bearing mechanism directly rather than trusting the
  commit message: read `record-dialog.js`'s
  `rememberHiddenDefaults`/`resetHiddenDefaults`/`setField` to confirm
  the hidden `original_code` field is genuinely blank on every
  create-mode open and genuinely populated from the row's own code on
  every edit-mode open.
- Independently re-ran the full gate: build, vet, gofmt, the
  `CountrySettings` test suite, `golangci-lint`, `guard-i18n.sh`,
  `guard-data-access.sh`, `guard-docs-shots.sh` — all clean.
- Independently re-verified the TDD claim (see above).
- Checked the ar/fa/tr translations are coherent, correctly-scripted, and
  match the register of their sibling keys — not placeholder text.
- Adversarial edge-case check: a case-only `code`/`original_code`
  mismatch can't happen on any legitimate edit (both are rendered from
  the same already-uppercased `{{ .Code }}`, and `country_settings_repo.go`
  normalizes every stored code to uppercase on write) — flagged as a nit
  only, not a reachable false-positive on real usage, not worth a fix.
- A stale dialog against a since-deleted row (`original_code` names a row
  that no longer exists) falls through to pre-existing behavior
  unchanged by this diff — confirmed not a regression.

## Verified beyond automated tests

- `guard-data-access.sh`: confirms the diff adds zero SQL outside
  `internal/data` (it only touches the handler and templates).
- `guard-i18n.sh`: confirms all 4 core locales carry the new key, no
  hardcoded strings introduced.
- Manual read of `web/help/en/country-settings.md`: already documents
  "once you're editing an existing row the code is locked, since it's
  what identifies which row your changes are saved to" — the operator-
  facing behavior this fix hardens was already accurately described, so
  no help-topic update was needed (nothing a shop owner sees or does
  changed; the new error only surfaces on a bypass no normal user path
  reaches).

## Deferred / out of scope

None — the card's own body scoped this as a single, self-contained
hardening fix.

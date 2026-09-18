# Code review — Country settings admin: record_dialog/list_header conversion (ut-docs#2187)

- **Date:** 2026-09-18
- **Ticket:** ut-docs#2187 (`complexity:medium`, `bug`, `p2`, `source:user`, `ux`)
- **Branch:** `feat/2187-country-settings-record-dialog`
- **Reviewer:** independent pass, Opus subagent (per this card's
  `complexity:medium` routing, `MODEL-ROUTING.md` — Dev at Sonnet, review
  at Opus), isolated in its own git worktree.
- **Verdict: SAFE TO MERGE.** No blocking findings; two real, non-blocking
  findings filed as separate Backlog follow-ups (ut-docs#2404, ut-docs#2405);
  several cosmetic/pre-existing observations recorded here, not filed.

## What shipped

Converted `/country-settings` from its pre-#2010 always-editable
inline-rows-plus-side-create-panel layout to the shared `record_dialog`/
`list_header` pattern (`ut-docs/reference/list-and-dialog-pattern.md`),
mirroring Locations (#2124) and Registers (#2185). This was flagged as
the "needs judgement pass" screen of the four (alongside Fiscal register,
in progress under a separate lane) because it is compliance-facing
(tax/jurisdiction config) — the judgement was done in this cycle's
Architect/BA pass, grounded in ADR-0035 (tax-rate *switching* logic lives
in a plugin hook, core's `country_settings.tax_rate_bp` is read only at
setup-wizard time, never live) and ADR-0040 (the `archive_min_days`
3650-day floor is enforced at the repository layer, independent of any
UI), which together established that a dismissible dialog carries no
data-integrity risk for any of this screen's fields.

Two structural differences from the Locations/Registers precedent, both
deliberate:

1. `code` (the primary key) is a **form field**, not a URL path segment —
   `POST /api/country-settings` stays the single save endpoint for both
   create and edit. The dialog's `code` input is free-text+required on
   create, `readonly` once editing an existing row (a page-local
   `record-dialog:open` listener on `document.body`, since neither
   Locations nor Registers has an analogous immutable-identifier case to
   copy from).
2. The destructive action is a three-way branch (`data-field-is_builtin`
   → `data-record-when`), not Locations' plain activate/deactivate pair:
   a builtin country's row offers "Restore defaults" (never removes the
   row — `country_settings_repo.go`'s `Delete` resets it to
   `builtinCountryDefaults`), a custom country's row offers "Delete"
   (genuinely removed).

Files changed: `internal/pages/country_settings_page.go` (new
`countrySettingsRedirectTarget`/`redirectCountrySettings`/
`renderCountrySettingsDialogError`, mirroring `locations_page.go`'s
pair), `web/ui/pages/country_settings.html` (full template conversion),
`internal/httpx/icons.go` (new `rotate-ccw` icon — "trash-2"/never-erase
reads wrong for a restore-to-shipped-defaults action), 7 new/updated Go
tests, a new e2e spec (`country-settings-record-dialog-2187.spec.ts`), a
selector fix in `osk-decimal-admin-fields-1275.spec.ts` (the OSK-decimal
regression test that targeted the old `.users-form` markup), 5 new i18n
keys translated into all four core locales (en/ar/fa/tr — this repo ships
no core `de.json`, German is external-pack-only), and
`web/help/{en,de,ar,fa,tr}/country-settings.md` + regenerated screenshots
via `make docs-shots`.

## What the independent review found

Ran the full gate live from an isolated worktree at the pre-review WIP
commit: `go build ./...`, `gofmt -l .`, `go test ./...` (full suite, 0
failures), `golangci-lint run ./...` (0 issues), `guard-i18n.sh`,
`guard-help-topics.sh`, `guard-help-drift.sh`, `guard-docs-shots.sh`,
`guard-compliance-claims.sh`, `guard-data-access.sh`,
`guard-page-http-error.sh`, `guard-htmx-loaded.sh`,
`guard-autofill-suppression.sh`, `guard-e2e-fixtures-import.sh` — all
green. E2e: `country-settings-record-dialog-2187`,
`osk-decimal-admin-fields-1275`, `admin-country-scope-filter-2167`,
`locations-record-dialog-2124` — 25 passed, 0 failed, against a real
Chromium.

**TDD claim independently re-verified for the `archive_min_days` floor**:
reverted the floor check in `country_settings_repo.go`'s
`validateCountrySetting`, re-ran the three relevant tests — all failed
with the expected errors (`TestCountrySettingsPage_HtmxFloorRefusalRendersInDialogMessage`
went from a 400 in-dialog fragment to a silent 200;
`TestCountrySettingsPageSaveAndFloorRefusal` and
`TestCountrySettingsRetentionFloorEnforcedInRepo` failed on the expected
assertions) — restored, all green again, `git diff` clean.

**Both structural design calls verified correct, not just plausible**:
the `code` readonly-lock was traced end-to-end (dispatch ordering
relative to prefill/discard-snapshot/dialog-paint/focus, the on-screen
keyboard's own `readOnly` guard, `FormData` still carrying the field on
submit) with no bypass found; the three-way destructive branch was
confirmed to fail closed (`data-record-when`'s exact-match hiding means
a row with no `is_builtin` value shows *neither* control, never both) and
`is_builtin` itself confirmed not caller-controlled (derived server-side
from `builtinCountryDefault(code)` in `Upsert`).

**`NameKey`/`DefaultLocale` preservation** (the form doesn't carry either
field) confirmed genuinely preserved from the existing row on every
update, and the regression test confirmed to assert real survival (not a
vacuous status-code check — it first asserts DE's seed data isn't already
empty, so it can't pass by accident).

**`?all=1` scope carry-through** confirmed correct on both the htmx
(`HX-Redirect`) and non-htmx (303) paths, including the dialog's
`createAction` and every row's destructive action, not just the plain
save path.

**i18n**: all 5 new keys present in all 4 locale files with real,
idiomatic translations (spot-checked, not machine-garbled); help-doc
translations structurally parallel to English. `en.json` changed, so the
advisory `lang-pack-drift` warning for `ut-plugin-language-{de,es}` will
fire on this PR — expected, non-blocking by design (ut-docs#1934).

**2 real, non-blocking findings — filed as separate Backlog cards**,
deliberately not folded into this PR (both are hardening/polish, not
things this ticket's acceptance criteria required):

- ut-docs#2404: edit-mode auto-focus lands on the now-`readonly` `code`
  field (the shared `record-dialog.js`'s `firstField()` doesn't exclude
  `readonly`), so the on-screen keyboard doesn't open until the operator
  taps a second field — a small touchscreen-UX regression versus
  Locations/Categories, whose first focusable field is editable. Fix
  belongs in the shared partial, affecting every adopting page, so it's
  wider than this ticket's own scope.
- ut-docs#2405: the code-swap-during-edit guard is client-side only
  (`readonly`) — no server-side check that a submitted `code` matches the
  row the dialog was opened for. Not exploitable through the normal UI
  (the dialog never opens without JS) and the failure mode is an extra
  row, not data loss, but worth a defense-in-depth hardening pass.

**Recorded but not filed** (cosmetic/pre-existing, reviewer's own
judgement that a card isn't warranted): `countrysettings.save` is now an
orphaned locale key (the dialog uses the shared `common.save` instead) —
`guard-i18n.sh` doesn't check for orphans so this is silent, not a CI
gap introduced by this change; `countryRow.AtFloor` was already dead
before this change (covered by the existing ut-docs#1566 deadcode
sweep, not new); one table cell (`tax_rate_pct%` + inclusive-tax note)
wraps translated prose inside a `dir="ltr"` cell rather than scoping the
`dir` to just the numeric run — cosmetic, doesn't visibly break in the
regenerated `ar` screenshot; `country_settings.html` has no
`{{ helpLink }}` in its head, matching the pre-existing state of
`locations.html` (this conversion's own reference precedent), not
something #2187 introduced; no test drives edit→Close→New to confirm the
code field clears and unlocks (the mechanism was traced and is sound,
just not e2e-pinned).

**Cleared, checked explicitly by the reviewer**: `rotate-ccw` icon SVG
path is well-formed Lucide, stylistically consistent with the shared
icon wrapper; no hardcoded `left`/`right` CSS, only logical properties;
no `inputmode="none"` misuse; screenshots for en/ar spot-checked visually
(search box + green + button, read-only table, pencil action; ar mirrors
correctly for RTL); no real client/shop name in any test fixture (`ZZ`/
`DE`/`FR`/generated `Z<base36>` codes only); no raw SQL added outside
`internal/data`.

## Explicitly deferred

- ut-docs#2404, ut-docs#2405 (above).
- A German (`de`) help topic screenshot doesn't exist because this repo
  ships no core `de.json`/`web/help/de/` binding for this topic's images
  the way it does for en/ar/fa/tr — pre-existing repo structure, not
  something this ticket changes.
